package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type recordingHookStore struct {
	mu     sync.Mutex
	states map[string]HookSessionState
}

func (store *recordingHookStore) Update(
	sessionID string,
	transition func(HookSessionState, bool) (HookSessionState, error),
) (HookSessionState, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.states == nil {
		store.states = make(map[string]HookSessionState)
	}
	current, exists := store.states[sessionID]
	next, err := transition(current, exists)
	if err != nil {
		return HookSessionState{}, err
	}
	store.states[sessionID] = next
	return next, nil
}

func (store *recordingHookStore) state(sessionID string) HookSessionState {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.states[sessionID]
}

func testHookSnapshot(digest string, changed bool) HookSnapshot {
	plan := Plan{
		SchemaVersion: 1,
		BaseRef:       "origin/master",
		Base:          strings.Repeat("0", 40),
		Head:          strings.Repeat("1", 40),
		DiffDigest:    digest,
	}
	if changed {
		plan.Changed = []ChangedPath{{Path: "scripts/iteration/hook.go", Status: "M", Owner: "tooling", Kind: PathProduction}}
		plan.RequiredTargets = []string{"fmt", "lint", "test", "build"}
	}
	return HookSnapshot{
		Plan:     plan,
		Evidence: emptyStoreEvidence(plan),
		Branch:   "codex/feat/hook",
	}
}

func runHookFixture(
	t *testing.T,
	store HookStateStore,
	event HookEventName,
	root string,
	sessionID string,
	snapshot HookSnapshot,
	extra string,
) (HookSessionState, string, error) {
	t.Helper()
	body := fmt.Sprintf(
		`{"session_id":%q,"cwd":%q,"hook_event_name":%q%s}`,
		sessionID,
		root,
		event,
		extra,
	)
	var stdout bytes.Buffer
	err := runHook(event, strings.NewReader(body), &stdout, root, snapshot, store)
	state := HookSessionState{}
	if recording, ok := store.(*recordingHookStore); ok {
		state = recording.state(sessionID)
	}
	return state, stdout.String(), err
}

func TestHookLifecycleTransitions(t *testing.T) {
	root := t.TempDir()
	store := &recordingHookStore{}
	sessionID := "session-1"
	initialDigest := strings.Repeat("a", 64)
	changedDigest := strings.Repeat("b", 64)

	state, output, err := runHookFixture(t, store, HookSessionStart, root, sessionID, testHookSnapshot(initialDigest, false), "")
	if err != nil {
		t.Fatal(err)
	}
	if output != "" || !state.Open || state.CreatedTrackedChange || state.InitialDigest != initialDigest {
		t.Fatalf("clean SessionStart state = %#v, output = %q", state, output)
	}

	state, output, err = runHookFixture(t, store, HookPostToolUse, root, sessionID, testHookSnapshot(initialDigest, false), "")
	if err != nil || output != "" || state.CreatedTrackedChange {
		t.Fatalf("unchanged PostToolUse state = %#v, output = %q, err = %v", state, output, err)
	}

	state, output, err = runHookFixture(t, store, HookPostToolUse, root, sessionID, testHookSnapshot(changedDigest, true), "")
	if err != nil || output != "" || !state.CreatedTrackedChange || state.CurrentDigest != changedDigest {
		t.Fatalf("changed PostToolUse state = %#v, output = %q, err = %v", state, output, err)
	}

	state, output, err = runHookFixture(t, store, HookSubagentStart, root, sessionID, testHookSnapshot(changedDigest, true), `,"agent_id":"child-1","agent_type":"review"`)
	if err != nil || output != "" || state.Children["child-1"] != "running" {
		t.Fatalf("SubagentStart state = %#v, output = %q, err = %v", state, output, err)
	}
	state, output, err = runHookFixture(t, store, HookSubagentStop, root, sessionID, testHookSnapshot(changedDigest, true), `,"agent_id":"child-1","agent_type":"review"`)
	if err != nil || output != "" || state.Children["child-1"] != "stopped" {
		t.Fatalf("SubagentStop state = %#v, output = %q, err = %v", state, output, err)
	}

	state, output, err = runHookFixture(t, store, HookStop, root, sessionID, testHookSnapshot(changedDigest, true), "")
	if err != nil || !state.StopContinued || !strings.Contains(output, `"decision": "block"`) {
		t.Fatalf("first Stop state = %#v, output = %q, err = %v", state, output, err)
	}
	state, output, err = runHookFixture(t, store, HookStop, root, sessionID, testHookSnapshot(changedDigest, true), "")
	if err != nil || output != "" || !state.StopContinued {
		t.Fatalf("second Stop state = %#v, output = %q, err = %v", state, output, err)
	}

	state, output, err = runHookFixture(t, store, HookSessionEnd, root, sessionID, testHookSnapshot(changedDigest, true), "")
	if err != nil || output != "" || state.Open || !state.Incomplete {
		t.Fatalf("SessionEnd state = %#v, output = %q, err = %v", state, output, err)
	}
}

func TestHookPreExistingDiffIsNotAttributedAndActiveStopDoesNotContinue(t *testing.T) {
	root := t.TempDir()
	digest := strings.Repeat("c", 64)
	store := &recordingHookStore{}
	state, output, err := runHookFixture(t, store, HookSessionStart, root, "preexisting", testHookSnapshot(digest, true), "")
	if err != nil || state.CreatedTrackedChange || output == "" {
		t.Fatalf("pre-existing SessionStart state = %#v, output = %q, err = %v", state, output, err)
	}
	if !strings.Contains(output, `"hookEventName": "SessionStart"`) || !strings.Contains(output, "branch=codex/feat/hook base=origin/master state=changed") {
		t.Fatalf("SessionStart output = %q", output)
	}

	changed := testHookSnapshot(strings.Repeat("d", 64), true)
	state, _, err = runHookFixture(t, store, HookPostToolUse, root, "preexisting", changed, "")
	if err != nil || !state.CreatedTrackedChange {
		t.Fatalf("PostToolUse state = %#v, err = %v", state, err)
	}
	state, output, err = runHookFixture(t, store, HookStop, root, "preexisting", changed, `,"stop_hook_active":true`)
	if err != nil || output != "" || state.StopContinued {
		t.Fatalf("active Stop state = %#v, output = %q, err = %v", state, output, err)
	}
}

func TestHookResumePreservesAttributedChangeAndOneShotStop(t *testing.T) {
	root := t.TempDir()
	store := &recordingHookStore{}
	clean := testHookSnapshot(strings.Repeat("5", 64), false)
	changed := testHookSnapshot(strings.Repeat("6", 64), true)
	if _, _, err := runHookFixture(t, store, HookSessionStart, root, "resume-session", clean, `,"source":"startup"`); err != nil {
		t.Fatal(err)
	}
	state, _, err := runHookFixture(t, store, HookPostToolUse, root, "resume-session", changed, "")
	if err != nil || !state.CreatedTrackedChange {
		t.Fatalf("tracked change was not attributed before resume: %#v, err=%v", state, err)
	}
	state, output, err := runHookFixture(t, store, HookSessionStart, root, "resume-session", changed, `,"source":"resume"`)
	if err != nil || !state.CreatedTrackedChange || state.StopContinued || output == "" {
		t.Fatalf("resume lost session attribution: %#v, output=%q, err=%v", state, output, err)
	}
	state, output, err = runHookFixture(t, store, HookStop, root, "resume-session", changed, "")
	if err != nil || !state.StopContinued || !strings.Contains(output, `"decision": "block"`) {
		t.Fatalf("resumed Stop did not continue once: %#v, output=%q, err=%v", state, output, err)
	}
	_, output, err = runHookFixture(t, store, HookStop, root, "resume-session", changed, "")
	if err != nil || output != "" {
		t.Fatalf("resumed Stop repeated decision: output=%q, err=%v", output, err)
	}
}

func TestHookPrivacyMarkersNeverEscapeAnyEvent(t *testing.T) {
	markers := []string{
		"TRANSCRIPT_SECRET_MARKER",
		"PROMPT_SECRET_MARKER",
		"ARGV_SECRET_MARKER",
		"COMMAND_OUTPUT_SECRET_MARKER",
		"SOURCE_SECRET_MARKER",
		"CREDENTIAL_SECRET_MARKER",
	}
	unknown := `,"transcript_path":"TRANSCRIPT_SECRET_MARKER","prompt":"PROMPT_SECRET_MARKER","tool_input":{"command":"ARGV_SECRET_MARKER"},"tool_response":"COMMAND_OUTPUT_SECRET_MARKER","last_assistant_message":"SOURCE_SECRET_MARKER","credential":"CREDENTIAL_SECRET_MARKER"`
	for _, event := range []HookEventName{HookSessionStart, HookPostToolUse, HookSubagentStart, HookSubagentStop, HookStop, HookSessionEnd} {
		t.Run(string(event), func(t *testing.T) {
			root := t.TempDir()
			store := newFileHookStateStore(root)
			extra := unknown
			if event == HookSubagentStart || event == HookSubagentStop {
				extra = `,"agent_id":"child","agent_type":"worker"` + extra
			}
			_, output, err := runHookFixture(t, store, event, root, "privacy-"+string(event), testHookSnapshot(strings.Repeat("e", 64), true), extra)
			if err != nil {
				t.Fatal(err)
			}
			var persisted strings.Builder
			walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return err
				}
				data, readErr := os.ReadFile(path)
				if readErr != nil {
					return readErr
				}
				persisted.Write(data)
				return nil
			})
			if walkErr != nil {
				t.Fatal(walkErr)
			}
			for _, marker := range markers {
				if strings.Contains(output, marker) || strings.Contains(persisted.String(), marker) {
					t.Fatalf("marker %q escaped: stdout=%q persisted=%q", marker, output, persisted.String())
				}
			}
		})
	}
}

func TestHookRejectsMultipleAndOversizedJSONWithoutReflection(t *testing.T) {
	root := t.TempDir()
	snapshot := testHookSnapshot(strings.Repeat("f", 64), false)
	valid := fmt.Sprintf(`{"session_id":"session","cwd":%q,"hook_event_name":"SessionStart"}`, root)
	for _, body := range []string{
		valid + `{"prompt":"TRAILING_SECRET_MARKER"}`,
		valid[:len(valid)-1] + `,"padding":"` + strings.Repeat("x", maxHookInputBytes) + `OVERSIZED_SECRET_MARKER"}`,
	} {
		var stdout bytes.Buffer
		err := runHook(HookSessionStart, strings.NewReader(body), &stdout, root, snapshot, &recordingHookStore{})
		if err == nil || stdout.Len() != 0 {
			t.Fatalf("runHook accepted %d-byte input, stdout=%q", len(body), stdout.String())
		}
		if strings.Contains(err.Error(), "SECRET_MARKER") {
			t.Fatalf("error reflected input: %v", err)
		}
	}
}

func TestSessionStartBudgetPreservesIdentityAndUTF8(t *testing.T) {
	root := t.TempDir()
	digest := strings.Repeat("1", 64)
	snapshot := testHookSnapshot(digest, true)
	for index := range 80 {
		snapshot.Plan.RequiredTargets = append(snapshot.Plan.RequiredTargets,
			fmt.Sprintf("risk-%03d-%s", index, strings.Repeat("你", 60)))
	}
	snapshot.Evidence = emptyStoreEvidence(snapshot.Plan)
	_, output, err := runHookFixture(t, &recordingHookStore{}, HookSessionStart, root, "budget", snapshot, "")
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		HookSpecificOutput struct {
			HookEventName     HookEventName `json:"hookEventName"`
			AdditionalContext string        `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatal(err)
	}
	context := decoded.HookSpecificOutput.AdditionalContext
	if decoded.HookSpecificOutput.HookEventName != HookSessionStart || len([]byte(context)) > maxHookContextBytes || !utf8.ValidString(context) {
		t.Fatalf("SessionStart context bytes=%d valid=%v event=%q", len([]byte(context)), utf8.ValidString(context), decoded.HookSpecificOutput.HookEventName)
	}
	if !strings.HasPrefix(context, "Iteration: branch=codex/feat/hook base=origin/master state=changed pending=") {
		t.Fatalf("SessionStart context lost identity prefix: %q", context)
	}
	if strings.Contains(context, "risk-079") {
		t.Fatalf("SessionStart context did not truncate pending detail: %q", context)
	}
}

func TestHookCWDAllowsRepositorySubdirectoryAndRejectsEscape(t *testing.T) {
	root := t.TempDir()
	subdirectory := filepath.Join(root, "subdirectory")
	if err := os.Mkdir(subdirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	snapshot := testHookSnapshot(strings.Repeat("2", 64), false)
	runWithCWD := func(cwd, sessionID string) error {
		body := fmt.Sprintf(`{"session_id":%q,"cwd":%q,"hook_event_name":"SessionStart"}`, sessionID, cwd)
		return runHook(HookSessionStart, strings.NewReader(body), io.Discard, root, snapshot, &recordingHookStore{})
	}
	if err := runWithCWD(subdirectory, "subdirectory"); err != nil {
		t.Fatalf("repository subdirectory rejected: %v", err)
	}

	outside := t.TempDir()
	if err := runWithCWD(outside, "outside"); err == nil {
		t.Fatal("outside cwd accepted")
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symlink unavailable:", err)
	}
	if err := runWithCWD(link, "symlink"); err == nil {
		t.Fatal("symlink cwd escape accepted")
	}
}

func TestHookFileStateIsHashedPrivateAndRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	sessionID := "private-session-id"
	store := newFileHookStateStore(root)
	snapshot := testHookSnapshot(strings.Repeat("3", 64), false)
	if _, _, err := runHookFixture(t, store, HookSessionStart, root, sessionID, snapshot, ""); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "build", "iteration", "hooks")
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("hook directory mode = %o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(sessionID))
	wantName := hex.EncodeToString(digest[:]) + ".json"
	wantLock := wantName + ".lock"
	entryByName := make(map[string]os.DirEntry, len(entries))
	for _, entry := range entries {
		entryByName[entry.Name()] = entry
	}
	if len(entries) != 2 || entryByName[wantName] == nil || entryByName[wantLock] == nil {
		t.Fatalf("hook state entries = %v, want %q and %q", entries, wantName, wantLock)
	}
	stateInfo, err := entryByName[wantName].Info()
	if err != nil {
		t.Fatal(err)
	}
	if stateInfo.Mode().Perm() != 0o600 {
		t.Fatalf("hook state mode = %o", stateInfo.Mode().Perm())
	}
	lockInfo, err := entryByName[wantLock].Info()
	if err != nil {
		t.Fatal(err)
	}
	if lockInfo.Mode().Perm() != 0o600 {
		t.Fatalf("hook lock mode = %o", lockInfo.Mode().Perm())
	}
	data, err := os.ReadFile(filepath.Join(directory, wantName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), sessionID) {
		t.Fatalf("raw session id persisted: %s", data)
	}

	attackRoot := t.TempDir()
	attackDirectory := filepath.Join(attackRoot, "build", "iteration", "hooks")
	if err := os.MkdirAll(attackDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(attackDirectory, wantName)); err != nil {
		t.Skip("symlink unavailable:", err)
	}
	if _, _, err := runHookFixture(t, newFileHookStateStore(attackRoot), HookSessionStart, attackRoot, sessionID, snapshot, ""); err == nil {
		t.Fatal("symlink state file accepted")
	}
	outsideData, err := os.ReadFile(outside)
	if err != nil || string(outsideData) != "unchanged" {
		t.Fatalf("outside state changed: %q, err=%v", outsideData, err)
	}
}

func TestHookFileStoreSerializesConcurrentTransitions(t *testing.T) {
	root := t.TempDir()
	store := newFileHookStateStore(root)
	snapshot := testHookSnapshot(strings.Repeat("4", 64), false)
	if _, _, err := runHookFixture(t, store, HookSessionStart, root, "concurrent", snapshot, ""); err != nil {
		t.Fatal(err)
	}
	const children = 32
	start := make(chan struct{})
	errorsByChild := make(chan error, children)
	var ready sync.WaitGroup
	ready.Add(children)
	for index := range children {
		go func(index int) {
			ready.Done()
			<-start
			body := fmt.Sprintf(`{"session_id":"concurrent","cwd":%q,"hook_event_name":"SubagentStart","agent_id":"child-%02d","agent_type":"worker"}`, root, index)
			errorsByChild <- runHook(HookSubagentStart, strings.NewReader(body), io.Discard, root, snapshot, store)
		}(index)
	}
	ready.Wait()
	close(start)
	for range children {
		if err := <-errorsByChild; err != nil {
			t.Fatal(err)
		}
	}
	state, err := store.Update("concurrent", func(current HookSessionState, exists bool) (HookSessionState, error) {
		if !exists {
			t.Fatal("concurrent state disappeared")
		}
		return current, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Children) != children {
		t.Fatalf("children retained = %d, want %d", len(state.Children), children)
	}
}

func TestHookAdvisoryLockNeverBreaksAStillHeldOwner(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "hooks")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(directory, "session.lock")
	first, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	locked, err := tryLockHookFile(first)
	if err != nil || !locked {
		t.Fatalf("first lock = %v, err=%v", locked, err)
	}
	defer func() { _ = unlockHookFile(first) }()
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}

	second, err := os.OpenFile(lockPath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	locked, err = tryLockHookFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if locked {
		_ = unlockHookFile(second)
		t.Fatal("second writer acquired an old but still-held lock")
	}
	if err := unlockHookFile(first); err != nil {
		t.Fatal(err)
	}
	locked, err = tryLockHookFile(second)
	if err != nil || !locked {
		t.Fatalf("second lock after release = %v, err=%v", locked, err)
	}
	if err := unlockHookFile(second); err != nil {
		t.Fatal(err)
	}
}
