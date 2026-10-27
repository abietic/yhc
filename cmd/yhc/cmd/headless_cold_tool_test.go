package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/session"
	"github.com/google/uuid"
)

func TestExecColdToolHandoffRestoresGuardBeforeEditAndRejectsUnverifiedState(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("private cold handoff requires Linux or Darwin")
	}
	repo := prepareHeadlessJSONLProviderTest(t)
	artifact := filepath.Join(repo, "cold-progress.txt")
	price := writeHeadlessPriceCard(t, repo)
	raw, err := os.ReadFile(price)
	if err != nil {
		t.Fatal(err)
	}
	var card map[string]any
	if err := json.Unmarshal(raw, &card); err != nil {
		t.Fatal(err)
	}
	card["window"] = "upper_bound"
	card["routes"].([]any)[0].(map[string]any)["api_model"] = "deepseek-v4-flash"
	raw, err = json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(price, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var request struct {
			Input json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		item := p430Function("cold-source-write", artifact, "before\n")
		if call == 2 {
			args, _ := json.Marshal(map[string]string{"file_path": artifact, "old_string": "before", "new_string": "after"})
			item = fmt.Sprintf(`{"type":"function_call","id":"cold-edit","call_id":"cold-next-edit","name":"Edit","status":"completed","arguments":%q}`, string(args))
		} else if call == 3 {
			output, found, decodeErr := p430FunctionOutput(request.Input, "cold-next-edit")
			if decodeErr != nil || !found || output != "Replaced 1 occurrence in "+artifact {
				t.Errorf("restored guard did not admit Edit: %q %v %v", output, found, decodeErr)
			}
			item = `{"type":"message","id":"cold-done","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done","annotations":[]}]}`
		} else if call != 1 {
			t.Errorf("unexpected replay/call %d", call)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":0,\"output_index\":0,\"item\":%s}\n\n", item)
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"cold-response\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"deepseek-v4-flash\",\"output\":[%s],\"usage\":{\"input_tokens\":10,\"output_tokens\":4,\"total_tokens\":14,\"input_tokens_details\":{\"cached_tokens\":6},\"output_tokens_details\":{\"reasoning_tokens\":2}}}}\n\n", item)
	}))
	defer server.Close()
	parent := filepath.Join(repo, ".yhc", "usage-checkpoints")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(directory string, extra ...string) (headlessEnvelope, error) {
		var out, stderr bytes.Buffer
		command := newRootCommand()
		command.SetOut(&out)
		command.SetErr(&stderr)
		command.SetIn(bytes.NewReader(nil))
		args := []string{"exec", "continue the saved file task", "--output-format", "json", "--provider", "deepseek", "--model", "deepseek-v4-flash", "--base-url", server.URL, "--api-key", p430FakeKey, "--permission-mode", "acceptEdits", "--tools", "Read,Write,Edit", "--max-turns", "3", "--timeout", "20s", "--price-card", price, "--reserve-cost-before-dispatch", "--max-output-tokens-per-call", "16", "--max-estimated-cost", "5", "--usage-checkpoint-dir", directory, "--capture-read-guards"}
		command.SetArgs(append(args, extra...))
		ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
		defer cancel()
		runErr := command.ExecuteContext(ctx)
		var result headlessEnvelope
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("decode %v, stdout=%s stderr=%s", err, out.String(), stderr.String())
		}
		return result, runErr
	}
	sourceDir := filepath.Join(parent, "source")
	source, err := run(sourceDir, "--max-provider-calls", "1")
	if err == nil || source.Error == nil || source.Error.Code != "run_budget_exceeded" || source.ReadGuards == nil || !source.ReadGuards.Complete || source.ReadGuards.Snapshot == nil {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	checkpoint, err := execution.ReadRunUsageCheckpointDir(sourceDir, source.Usage.CallLedger.SegmentID)
	if err != nil {
		t.Fatal(err)
	}
	transcriptDir := filepath.Join(repo, ".yhc", "transcripts")
	admin := engine.NewSessionAdministrationEngine(engine.SessionAdministrationConfig{CWD: repo, TranscriptDir: transcriptDir})
	_, native, err := admin.SessionService().ResumeInfoWithRecoveryProof(t.Context(), session.SessionInfo{SessionID: source.SessionID, CWD: repo, TranscriptDir: transcriptDir})
	admin.Close()
	if err != nil {
		t.Fatal(err)
	}
	handoff := engine.ColdToolHandoff{Version: 1, Scope: "cold-file-read-guard-handoff-v1", NativeProof: native, CheckpointPath: ".yhc/usage-checkpoints/source", Checkpoint: checkpoint, ReadGuards: *source.ReadGuards.Snapshot}
	private, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(private, 0o700); err != nil {
		t.Fatal(err)
	}
	invoke := func(value engine.ColdToolHandoff, name string) (headlessEnvelope, error) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(private, name+".json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		return run(filepath.Join(parent, name), "--max-provider-calls", "2", "--resume", source.SessionID, "--cold-tool-handoff", path, "--cold-tool-handoff-sha256", hex.EncodeToString(hash[:]))
	}
	if native.WorkspaceHistory == nil {
		t.Fatal("source lacks real history proof")
	}
	unverified, unverifiedErr := invoke(handoff, "unimported_history")
	if unverifiedErr == nil || unverified.Error == nil || calls.Load() != 1 {
		t.Fatal("history without migration provenance was admitted")
	}
	proofRaw, err := json.Marshal(native.WorkspaceHistory)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(foreign)
	wrongRoot := newRootCommand()
	wrongRoot.SetIn(bytes.NewReader(proofRaw))
	var wrongOut, wrongErr bytes.Buffer
	wrongRoot.SetOut(&wrongOut)
	wrongRoot.SetErr(&wrongErr)
	wrongRoot.SetArgs([]string{"sessions", "recover-history", source.SessionID, "--source-sha256", native.WorkspaceHistory.SHA256, "--migration-id", uuid.NewString(), "--binding-sha256", strings.Repeat("a", 64), "--output-format", "json"})
	rootErr := wrongRoot.ExecuteContext(t.Context())
	t.Chdir(repo)
	if rootErr == nil || calls.Load() != 1 {
		t.Fatal("logical root remapping admitted or dispatched a provider")
	}
	if entries, err := os.ReadDir(foreign); err != nil || len(entries) != 0 {
		t.Fatal("invalid root import mutated the target before admission", err)
	}
	var historyOut, historyErr bytes.Buffer
	importCommand := newRootCommand()
	importCommand.SetIn(bytes.NewReader(proofRaw))
	importCommand.SetOut(&historyOut)
	importCommand.SetErr(&historyErr)
	importCommand.SetArgs([]string{"sessions", "recover-history", source.SessionID, "--source-sha256", native.WorkspaceHistory.SHA256, "--migration-id", uuid.NewString(), "--binding-sha256", strings.Repeat("a", 64), "--output-format", "json"})
	if err := importCommand.ExecuteContext(t.Context()); err != nil || calls.Load() != 1 {
		t.Fatalf("provider-free history import: err=%v stderr=%s", err, historyErr.String())
	}
	admin = engine.NewSessionAdministrationEngine(engine.SessionAdministrationConfig{CWD: repo, TranscriptDir: transcriptDir})
	_, native, err = admin.SessionService().ResumeInfoWithRecoveryProof(t.Context(), session.SessionInfo{SessionID: source.SessionID, CWD: repo, TranscriptDir: transcriptDir})
	admin.Close()
	if err != nil || native.WorkspaceHistoryOrigin == nil {
		t.Fatal("imported history lacks fresh native provenance", err)
	}
	handoff.NativeProof = native
	for _, fault := range []string{"native", "sequence", "open_accounting", "file_hash", "external_path"} {
		data, _ := json.Marshal(handoff)
		var bad engine.ColdToolHandoff
		if err := json.Unmarshal(data, &bad); err != nil {
			t.Fatal(err)
		}
		switch fault {
		case "native":
			bad.NativeProof.MessagesSHA256 = "wrong"
		case "sequence":
			bad.Checkpoint.Sequence++
		case "open_accounting":
			bad.Checkpoint.AdmissionClosed = false
		case "file_hash":
			bad.ReadGuards.Entries[0].SHA256 = "wrong"
		case "external_path":
			bad.ReadGuards.Entries[0].Path = "../outside"
		}
		result, err := invoke(bad, fault)
		if err == nil || result.Error == nil || result.Error.Code != "cold_tool_handoff_invalid" || calls.Load() != 1 {
			t.Fatalf("%s: result=%+v calls=%d err=%v", fault, result, calls.Load(), err)
		}
	}
	before, err := os.ReadFile(filepath.Join(sourceDir, "checkpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := invoke(handoff, "next")
	if err != nil || result.Status != "completed" || result.ColdRecovery == nil || !result.ColdRecovery.ReadGuardsRestored || !result.ColdRecovery.DispatchClaimed || !result.ColdRecovery.HistoryRestored {
		t.Fatalf("cold result=%+v error=%v", result, err)
	}
	if result.Usage.ProviderCalls != 2 || result.Usage.CallLedger.SegmentID == source.Usage.CallLedger.SegmentID || calls.Load() != 3 {
		t.Fatalf("usage=%+v calls=%d", result.Usage, calls.Load())
	}
	data, err := os.ReadFile(artifact)
	if err != nil || string(data) != "after\n" {
		t.Fatalf("artifact=%q err=%v", data, err)
	}
	after, err := os.ReadFile(filepath.Join(sourceDir, "checkpoint.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("historical accounting changed")
	}
	repeated, err := invoke(handoff, "repeated")
	if err == nil || repeated.Error == nil || calls.Load() != 3 {
		t.Fatalf("repeated=%+v calls=%d err=%v", repeated, calls.Load(), err)
	}
}
