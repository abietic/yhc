package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/scripts/internal/ownedprocess"
)

const (
	manifestLimit = 64 << 10
	fixtureLimit  = 256 << 10
)

var packageBinary struct {
	sync.Once
	path string
	root string
	err  error
}

type scenario struct {
	SchemaVersion  int      `json:"schema_version"`
	ID             string   `json:"id"`
	Prompt         string   `json:"prompt"`
	Tools          []string `json:"tools"`
	PermissionMode string   `json:"permission_mode"`
	MaxTurns       int      `json:"max_turns"`
	TimeoutMillis  int      `json:"timeout_milliseconds"`
	Expected       expected `json:"expected"`
}
type expected struct {
	ExitCode       int               `json:"exit_code"`
	Status         string            `json:"status"`
	TerminalReason string            `json:"terminal_reason,omitempty"`
	FileSHA256     map[string]string `json:"file_sha256"`
	Absent         []string          `json:"absent"`
	GitStatus      []string          `json:"git_status"`
}
type providerStep struct {
	Kind        string            `json:"kind"`
	CallID      string            `json:"call_id,omitempty"`
	Tool        string            `json:"tool,omitempty"`
	Arguments   json.RawMessage   `json:"arguments,omitempty"`
	Text        string            `json:"text,omitempty"`
	ExpectModel string            `json:"expect_model,omitempty"`
	ExpectPrior []priorToolResult `json:"expect_prior,omitempty"`
}
type priorToolResult struct {
	CallID   string `json:"call_id"`
	Contains string `json:"contains,omitempty"`
	Exact    string `json:"exact,omitempty"`
}
type providerScript struct {
	SchemaVersion int            `json:"schema_version"`
	Steps         []providerStep `json:"steps"`
	Parent        *providerLane  `json:"parent,omitempty"`
	Child         *providerLane  `json:"child,omitempty"`
}
type providerLane struct {
	Name        string         `json:"name"`
	ExpectTools []string       `json:"expect_tools"`
	Steps       []providerStep `json:"steps"`
}
type envelope struct {
	SchemaVersion  int    `json:"schema_version"`
	Status         string `json:"status"`
	Output         string `json:"output,omitempty"`
	SessionID      string `json:"session_id"`
	TerminalReason string `json:"terminal_reason"`
	ExitCode       int    `json:"exit_code"`
}

func TestYHCModuleCommandAndArtifactIdentity(t *testing.T) {
	t.Setenv("EINO_E2E_BINARY", "")
	path := e2eBinary(t)
	wantName := "yhc"
	if runtime.GOOS == "windows" {
		wantName += ".exe"
	}
	if got := filepath.Base(path); got != wantName {
		t.Errorf("E2E binary name = %q, want %q", got, wantName)
	}
	if got := filepath.Base(packageBinary.root); !strings.HasPrefix(got, "yhc-e2e-binary-") {
		t.Errorf("E2E temporary binary root = %q", got)
	}
}

func strictJSON(data []byte, dst any) error {
	if len(data) > manifestLimit {
		return errors.New("manifest exceeds limit")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("manifest has trailing document")
	}
	return nil
}

func loadScenario(data []byte) (scenario, error) {
	var s scenario
	if err := strictJSON(data, &s); err != nil {
		return s, err
	}
	return s, validateScenario(s)
}

func validateScenario(s scenario) error {
	if s.SchemaVersion != 1 || s.ID == "" || strings.ContainsAny(s.ID, "/\\\x00\r\n") ||
		s.Prompt == "" || len(s.Prompt) > 8192 || s.MaxTurns <= 0 || s.MaxTurns > 100 ||
		s.TimeoutMillis <= 0 || s.TimeoutMillis > 120000 ||
		!map[string]bool{"bypass": true, "plan": true}[s.PermissionMode] {
		return errors.New("invalid scenario")
	}
	switch s.Expected.Status {
	case "completed":
		if s.Expected.ExitCode != 0 || s.Expected.TerminalReason != "completed" {
			return errors.New("invalid completed expectation")
		}
	case "cancelled":
		if s.Expected.ExitCode != 130 ||
			!map[string]bool{"": true, "aborted_streaming": true, "aborted_tools": true}[s.Expected.TerminalReason] {
			return errors.New("invalid cancellation expectation")
		}
	default:
		return errors.New("invalid expected status")
	}
	seen := map[string]bool{}
	for _, v := range s.Tools {
		if seen[v] || !map[string]bool{"Agent": true, "Bash": true, "Glob": true, "Grep": true, "Read": true, "Edit": true, "Write": true}[v] {
			return errors.New("invalid tools")
		}
		seen[v] = true
	}
	for name, digest := range s.Expected.FileSHA256 {
		if !safeFixturePath(name) || !validSHA256(digest) {
			return errors.New("invalid expected file")
		}
	}
	absent := make(map[string]struct{}, len(s.Expected.Absent))
	for _, name := range s.Expected.Absent {
		if !safeFixturePath(name) {
			return errors.New("invalid absent path")
		}
		if _, duplicate := absent[name]; duplicate {
			return errors.New("duplicate absent path")
		}
		if _, conflicts := s.Expected.FileSHA256[name]; conflicts {
			return errors.New("conflicting file expectation")
		}
		absent[name] = struct{}{}
	}
	return nil
}

func loadScript(data []byte) (providerScript, error) {
	var p providerScript
	if err := strictJSON(data, &p); err != nil {
		return p, err
	}
	return p, validateScript(p)
}

func validateScript(p providerScript) error {
	switch p.SchemaVersion {
	case 1:
		if len(p.Steps) == 0 || p.Parent != nil || p.Child != nil {
			return errors.New("invalid v1 script")
		}
		// A v1 script can span an initial invocation and one or more resumed
		// invocations, so a terminal response in one invocation is not terminal
		// for the whole fixture.
		return validateSteps(p.Steps, false)
	case 2:
		if len(p.Steps) != 0 || p.Parent == nil || p.Child == nil {
			return errors.New("invalid v2 script")
		}
		if err := validateLane(*p.Parent, "parent"); err != nil {
			return err
		}
		if err := validateLane(*p.Child, "child"); err != nil {
			return err
		}
		if sameTools(p.Parent.ExpectTools, p.Child.ExpectTools) {
			return errors.New("ambiguous v2 lanes")
		}
		return nil
	default:
		return errors.New("invalid script version")
	}
}

func validateLane(lane providerLane, wantName string) error {
	if lane.Name != wantName || len(lane.ExpectTools) == 0 || !sortedUniqueTools(lane.ExpectTools) {
		return errors.New("invalid lane")
	}
	return validateSteps(lane.Steps, false)
}

func sortedUniqueTools(tools []string) bool {
	for i, tool := range tools {
		if !map[string]bool{"Agent": true, "Bash": true, "Glob": true, "Grep": true, "Read": true}[tool] || (i > 0 && tools[i-1] >= tool) {
			return false
		}
	}
	return true
}

func sameTools(left, right []string) bool { return strings.Join(left, ",") == strings.Join(right, ",") }

func validateSteps(steps []providerStep, terminalMustBeLast bool) error {
	if len(steps) == 0 {
		return errors.New("empty lane")
	}
	calls := map[string]bool{}
	terminalSeen := false
	for _, s := range steps {
		if terminalMustBeLast && terminalSeen {
			return errors.New("step after terminal")
		}
		if !map[string]bool{"tool_call": true, "final": true, "overload": true, "block_until_cancel": true, "assert_prior_tool_result": true}[s.Kind] {
			return errors.New("invalid step kind")
		}
		if len(s.Text) > 4096 || len(s.Arguments) > 8192 || len(s.ExpectModel) > 128 {
			return errors.New("step bound")
		}
		for _, x := range s.ExpectPrior {
			if x.CallID == "" || !calls[x.CallID] || (x.Contains == "" && x.Exact == "") || (x.Contains != "" && x.Exact != "") {
				return errors.New("invalid prior")
			}
		}
		if s.Kind == "tool_call" {
			if s.CallID == "" || s.Tool == "" || !jsonObject(s.Arguments) || calls[s.CallID] {
				return errors.New("invalid tool step")
			}
			calls[s.CallID] = true
		}
		if (s.Kind == "final" || s.Kind == "assert_prior_tool_result") && s.Text == "" {
			return errors.New("invalid final step")
		}
		if s.Kind != "tool_call" && s.Kind != "block_until_cancel" && (s.CallID != "" || s.Tool != "" || len(s.Arguments) > 0) {
			return errors.New("invalid non-tool step")
		}
		if s.Kind == "block_until_cancel" && (s.CallID == "" || s.Tool == "" || !jsonObject(s.Arguments)) {
			return errors.New("invalid blocking tool step")
		}
		if (s.Kind == "overload" || s.Kind == "block_until_cancel") && s.Text != "" {
			return errors.New("invalid overload")
		}
		if s.Kind == "final" {
			terminalSeen = true
		}
	}
	return nil
}

func jsonObject(v []byte) bool {
	var x map[string]any
	return len(v) > 0 && json.Unmarshal(v, &x) == nil && x != nil
}

func safeFixturePath(name string) bool {
	return name != "" && filepath.IsLocal(name) && filepath.ToSlash(filepath.Clean(name)) == name && !strings.Contains(name, "\\")
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func fixtureReadPath(name string) string { return "fixture/" + name }

type fixtureManifest struct {
	SchemaVersion int      `json:"schema_version"`
	Files         []string `json:"files"`
}

func loadManifest(root, name string, dst any) error {
	data, err := readConfinedRegular(root, name, manifestLimit)
	if err != nil {
		return err
	}
	if credentialShaped(data) {
		return errors.New("manifest contains credential-shaped bytes")
	}
	return strictJSON(data, dst)
}

func readConfinedRegular(root, name string, limit int64) ([]byte, error) {
	return readConfinedRegularBeforeOpen(root, name, limit, nil)
}

func readConfinedRegularBeforeOpen(root, name string, limit int64, beforeOpen func() error) ([]byte, error) {
	if !safeFixturePath(name) {
		return nil, errors.New("fixture path is invalid")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, errors.New("fixture root is unsafe")
	}
	confined, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer confined.Close()
	rootFile, err := confined.Open(".")
	if err != nil {
		return nil, err
	}
	openedRootInfo, statErr := rootFile.Stat()
	_ = rootFile.Close()
	if statErr != nil || !os.SameFile(rootInfo, openedRootInfo) {
		return nil, errors.New("fixture root changed while opening")
	}

	prefixes := make([]string, 0, strings.Count(name, "/")+1)
	prefixInfo := make([]os.FileInfo, 0, cap(prefixes))
	prefix := ""
	for _, part := range strings.Split(name, "/") {
		if prefix == "" {
			prefix = part
		} else {
			prefix += "/" + part
		}
		info, statErr := confined.Lstat(prefix)
		if statErr != nil {
			return nil, statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("fixture path contains symlink")
		}
		prefixes = append(prefixes, prefix)
		prefixInfo = append(prefixInfo, info)
	}
	if beforeOpen != nil {
		if err := beforeOpen(); err != nil {
			return nil, err
		}
	}
	file, err := confined.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(prefixInfo[len(prefixInfo)-1], info) {
		return nil, errors.New("fixture is not regular")
	}
	if info.Size() > limit {
		return nil, errors.New("fixture exceeds limit")
	}
	for i, checkedPrefix := range prefixes {
		currentInfo, statErr := confined.Lstat(checkedPrefix)
		if statErr != nil || currentInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(prefixInfo[i], currentInfo) {
			return nil, errors.New("fixture path changed while opening")
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("fixture exceeds limit")
	}
	return data, nil
}

func credentialShaped(data []byte) bool {
	lower := bytes.ToLower(data)
	return bytes.Contains(lower, []byte("authorization")) ||
		bytes.Contains(lower, []byte("api_key")) ||
		bytes.Contains(lower, []byte("bearer ")) ||
		bytes.Contains(lower, []byte("sk-"))
}

type journal struct {
	Request int
	Lane    string
	Model   string
	Tools   []string
	Prior   []string
	Kind    string
}

type priorObservation struct {
	digest     [sha256.Size]byte
	count      int
	consistent bool
}

type provider struct {
	t         *testing.T
	mu        sync.Mutex
	script    providerScript
	wantTools []string
	n         int
	laneN     map[string]int
	journal   []journal
	prior     map[string]priorObservation
	server    *httptest.Server
	started   chan struct{}
	startOnce sync.Once
}

func newProvider(t *testing.T, p providerScript, tools []string) *provider {
	x := &provider{t: t, script: p, wantTools: append([]string(nil), tools...), laneN: make(map[string]int), prior: make(map[string]priorObservation), started: make(chan struct{})}
	sort.Strings(x.wantTools)
	x.server = httptest.NewServer(http.HandlerFunc(x.serve))
	return x
}

func (p *provider) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.URL.Path != "/responses" {
		http.Error(w, "contract", 400)
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, manifestLimit+1))
	if err != nil || len(b) > manifestLimit {
		http.Error(w, "bound", 400)
		return
	}
	var q struct {
		Model string          `json:"model"`
		Input json.RawMessage `json:"input"`
		Tools []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tools"`
	}
	if json.Unmarshal(b, &q) != nil {
		http.Error(w, "contract", 400)
		return
	}
	got := make([]string, 0, len(q.Tools))
	for _, x := range q.Tools {
		if x.Type != "function" {
			http.Error(w, "contract", 400)
			return
		}
		got = append(got, x.Name)
	}
	sort.Strings(got)
	lane, step, request, ok := p.takeStep(got)
	if !ok {
		http.Error(w, "contract", 400)
		return
	}
	prior, consistent := p.observePriorOutputs(q.Input)
	if !consistent {
		http.Error(w, "prior", http.StatusBadRequest)
		return
	}
	for _, e := range step.ExpectPrior {
		v, ok := prior[e.CallID]
		if !ok || (e.Exact != "" && v != e.Exact) || (e.Contains != "" && !strings.Contains(v, e.Contains)) {
			http.Error(w, "prior", 400)
			return
		}
	}
	keys := make([]string, 0, len(prior))
	for k := range prior {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// The journal is an oracle for routing, not a transcript cache. Keep only
	// bounded structural metadata; model input and tool-result bodies can carry
	// fixture content and must never be retained here.
	p.appendJournal(journal{Request: request, Lane: lane, Model: q.Model, Tools: got, Prior: keys, Kind: step.Kind})
	if step.ExpectModel != "" && q.Model != step.ExpectModel {
		http.Error(w, "model", 400)
		return
	}
	if step.Kind == "overload" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(529)
		_, _ = w.Write([]byte(`{"error":{"type":"overloaded_error","message":"overloaded"}}`))
		return
	}
	if step.Kind == "block_until_cancel" {
		w.Header().Set("Content-Type", "text/event-stream")
		item := map[string]any{"type": "function_call", "id": "item-" + step.CallID, "call_id": step.CallID, "name": step.Tool, "arguments": string(step.Arguments), "status": "completed"}
		p.event(w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
		p.event(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		p.startOnce.Do(func() { close(p.started) })
		<-r.Context().Done()
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	id := p.requestCount()
	if step.Kind == "tool_call" {
		item := map[string]any{"type": "function_call", "id": "item-" + step.CallID, "call_id": step.CallID, "name": step.Tool, "arguments": string(step.Arguments), "status": "completed"}
		p.event(w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
	} else {
		p.event(w, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": fmt.Sprintf("msg-%d", id), "output_index": 0, "content_index": 0, "delta": step.Text})
		item := map[string]any{"type": "message", "id": fmt.Sprintf("msg-%d", id), "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": step.Text, "annotations": []any{}}}}
		p.event(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
	}
	p.event(w, "response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("resp-%d", id), "object": "response", "status": "completed", "model": q.Model, "output": []any{}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}})
}

func (p *provider) takeStep(got []string) (string, providerStep, int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	lane := "v1"
	steps := p.script.Steps
	if p.script.SchemaVersion == 1 && !sameTools(got, p.wantTools) {
		return "", providerStep{}, 0, false
	}
	if p.script.SchemaVersion == 2 {
		matches := make([]providerLane, 0, 2)
		for _, candidate := range []providerLane{*p.script.Parent, *p.script.Child} {
			if sameTools(got, candidate.ExpectTools) {
				matches = append(matches, candidate)
			}
		}
		if len(matches) != 1 {
			return "", providerStep{}, 0, false
		}
		lane, steps = matches[0].Name, matches[0].Steps
	}
	index := p.laneN[lane]
	if index >= len(steps) {
		return "", providerStep{}, 0, false
	}
	step := steps[index]
	p.laneN[lane]++
	p.n++
	step.Arguments = append(json.RawMessage(nil), step.Arguments...)
	step.ExpectPrior = append([]priorToolResult(nil), step.ExpectPrior...)
	return lane, step, p.n, true
}

func (p *provider) appendJournal(entry journal) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.journal = append(p.journal, entry)
}

func (p *provider) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

func (p *provider) journalSnapshot() []journal {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]journal, len(p.journal))
	for index, entry := range p.journal {
		result[index] = entry
		result[index].Tools = append([]string(nil), entry.Tools...)
		result[index].Prior = append([]string(nil), entry.Prior...)
	}
	return result
}

func (p *provider) observePriorOutputs(raw json.RawMessage) (map[string]string, bool) {
	prior, unique := priorOutputs(raw)
	if !unique {
		return prior, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for callID, value := range prior {
		digest := sha256.Sum256([]byte(value))
		observation, seen := p.prior[callID]
		if !seen {
			p.prior[callID] = priorObservation{digest: digest, count: 1, consistent: true}
			continue
		}
		observation.count++
		if observation.digest != digest {
			observation.consistent = false
			p.prior[callID] = observation
			return prior, false
		}
		p.prior[callID] = observation
	}
	return prior, true
}

func (p *provider) priorObservationSnapshot(callID string) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	observation, ok := p.prior[callID]
	return observation.count, ok && observation.consistent
}

func (p *provider) event(w http.ResponseWriter, n string, v any) {
	b, _ := json.Marshal(v)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", n, b)
}

func priorOutputs(raw json.RawMessage) (map[string]string, bool) {
	var a []struct {
		Type   string          `json:"type"`
		CallID string          `json:"call_id"`
		Output json.RawMessage `json:"output"`
	}
	_ = json.Unmarshal(raw, &a)
	out := map[string]string{}
	for _, x := range a {
		if x.Type != "function_call_output" {
			continue
		}
		if _, duplicate := out[x.CallID]; duplicate {
			return out, false
		}
		var s string
		if json.Unmarshal(x.Output, &s) == nil {
			out[x.CallID] = s
			continue
		}
		var ps []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(x.Output, &ps) == nil {
			for _, z := range ps {
				if z.Type == "input_text" {
					out[x.CallID] += z.Text
				}
			}
		}
	}
	return out, true
}

func TestStrictManifests(t *testing.T) {
	if _, err := loadScenario([]byte(`{"schema_version":1,"id":"x","prompt":"p","tools":["Read","Read"],"permission_mode":"plan","max_turns":1,"timeout_milliseconds":1,"expected":{"exit_code":0,"status":"completed","terminal_reason":"completed"}}`)); err == nil {
		t.Fatal("duplicate tools accepted")
	}
	if _, err := loadScenario([]byte(`{"schema_version":1,"id":"x","prompt":"p","tools":["LS"],"permission_mode":"plan","max_turns":1,"timeout_milliseconds":1,"expected":{"exit_code":0,"status":"completed","terminal_reason":"completed"}}`)); err == nil {
		t.Fatal("scenario LS accepted")
	}
	if _, err := loadScript([]byte(`{"schema_version":1,"steps":[{"kind":"tool_call","call_id":"x","tool":"Read","arguments":{}},{"kind":"final","text":"ok"}]} {}`)); err == nil {
		t.Fatal("second document accepted")
	}
	if _, unique := priorOutputs(json.RawMessage(`[{"type":"function_call_output","call_id":"x","output":"one"},{"type":"function_call_output","call_id":"x","output":"two"}]`)); unique {
		t.Fatal("duplicate prior call ID accepted")
	}
	for name, raw := range map[string]string{
		"missing_child":   `{"schema_version":2,"parent":{"name":"parent","expect_tools":["Agent","Bash"],"steps":[{"kind":"final","text":"ok"}]}}`,
		"duplicate_tools": `{"schema_version":2,"parent":{"name":"parent","expect_tools":["Agent","Agent"],"steps":[{"kind":"final","text":"ok"}]},"child":{"name":"child","expect_tools":["Bash"],"steps":[{"kind":"final","text":"ok"}]}}`,
		"unknown_tool":    `{"schema_version":2,"parent":{"name":"parent","expect_tools":["Agent","Nope"],"steps":[{"kind":"final","text":"ok"}]},"child":{"name":"child","expect_tools":["Bash"],"steps":[{"kind":"final","text":"ok"}]}}`,
		"invalid_name":    `{"schema_version":2,"parent":{"name":"root","expect_tools":["Agent"],"steps":[{"kind":"final","text":"ok"}]},"child":{"name":"child","expect_tools":["Bash"],"steps":[{"kind":"final","text":"ok"}]}}`,
		"ambiguous":       `{"schema_version":2,"parent":{"name":"parent","expect_tools":["Agent"],"steps":[{"kind":"final","text":"ok"}]},"child":{"name":"child","expect_tools":["Agent"],"steps":[{"kind":"final","text":"ok"}]}}`,
		"mixed":           `{"schema_version":2,"steps":[{"kind":"final","text":"ok"}],"parent":{"name":"parent","expect_tools":["Agent"],"steps":[{"kind":"final","text":"ok"}]},"child":{"name":"child","expect_tools":["Bash"],"steps":[{"kind":"final","text":"ok"}]}}`,
		"out_of_order":    `{"schema_version":2,"parent":{"name":"parent","expect_tools":["Agent","Bash"],"steps":[{"kind":"final","text":"ok","expect_prior":[{"call_id":"late","contains":"x"}]},{"kind":"tool_call","call_id":"late","tool":"Agent","arguments":{}}]},"child":{"name":"child","expect_tools":["Bash"],"steps":[{"kind":"final","text":"ok"}]}}`,
		"phantom_ls":      `{"schema_version":2,"parent":{"name":"parent","expect_tools":["Agent","LS"],"steps":[{"kind":"final","text":"ok"}]},"child":{"name":"child","expect_tools":["Bash"],"steps":[{"kind":"final","text":"ok"}]}}`,
	} {
		if _, err := loadScript([]byte(raw)); err == nil {
			t.Fatalf("accepted v2 %s", name)
		}
	}
}

func TestManifestLoaderRejectsUnsafeAndInvalidInput(t *testing.T) {
	root := t.TempDir()
	valid := []byte(`{"schema_version":1,"id":"x","prompt":"p","tools":["Read"],"permission_mode":"plan","max_turns":1,"timeout_milliseconds":1,"expected":{}}`)
	if err := os.WriteFile(filepath.Join(root, "valid.json"), valid, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../valid.json", "/tmp/valid.json"} {
		var s scenario
		if err := loadManifest(root, name, &s); err == nil {
			t.Fatalf("accepted unsafe path %q", name)
		}
	}
	var s scenario
	if err := os.Symlink(filepath.Join(root, "valid.json"), filepath.Join(root, "link.json")); err == nil {
		if err := loadManifest(root, "link.json", &s); err == nil {
			t.Fatal("accepted symlink")
		}
	} else {
		t.Logf("manifest symlink check unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.json"), bytes.Repeat([]byte("x"), manifestLimit+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadManifest(root, "large.json", &s); err == nil {
		t.Fatal("accepted oversized manifest")
	}
	for name, data := range map[string]string{
		"unknown.json":    `{"schema_version":1,"id":"x","prompt":"p","tools":["Read"],"permission_mode":"plan","max_turns":1,"timeout_milliseconds":1,"expected":{},"unknown":true}`,
		"trailing.json":   string(valid) + ` {}`,
		"credential.json": `{"schema_version":1,"id":"x","prompt":"sk-test","tools":["Read"],"permission_mode":"plan","max_turns":1,"timeout_milliseconds":1,"expected":{}}`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := loadManifest(root, name, &s); err == nil {
			t.Fatalf("accepted invalid manifest %q", name)
		}
	}
	fixtureRoot := filepath.Join(root, "fixture")
	if err := os.Mkdir(fixtureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixtureRoot, "safe.txt"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFixtureFiles(root, fixtureManifest{SchemaVersion: 1, Files: []string{"safe.txt", "safe.txt"}}); err == nil {
		t.Fatal("accepted duplicate fixture path")
	}
	if err := os.Symlink(filepath.Join(fixtureRoot, "safe.txt"), filepath.Join(fixtureRoot, "link.txt")); err == nil {
		if _, err := loadFixtureFiles(root, fixtureManifest{SchemaVersion: 1, Files: []string{"link.txt"}}); err == nil {
			t.Fatal("accepted fixture symlink")
		}
	} else {
		t.Logf("fixture symlink check unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixtureRoot, "oversized.txt"), bytes.Repeat([]byte("x"), fixtureLimit+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFixtureFiles(root, fixtureManifest{SchemaVersion: 1, Files: []string{"oversized.txt"}}); err == nil {
		t.Fatal("accepted oversized fixtures")
	}
}

func TestConfinedReaderRejectsSymlinkSwapBetweenValidationAndOpen(t *testing.T) {
	probeRoot := t.TempDir()
	probeTarget := filepath.Join(probeRoot, "target")
	if err := os.WriteFile(probeTarget, []byte("probe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(probeTarget, filepath.Join(probeRoot, "link")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	for _, swap := range []string{"file", "parent"} {
		t.Run(swap, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			insideDir := filepath.Join(root, "nested")
			if err := os.Mkdir(insideDir, 0o700); err != nil {
				t.Fatal(err)
			}
			insideFile := filepath.Join(insideDir, "fixture.json")
			if err := os.WriteFile(insideFile, []byte("inside"), 0o600); err != nil {
				t.Fatal(err)
			}
			outsideFile := filepath.Join(outside, "fixture.json")
			if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := readConfinedRegularBeforeOpen(root, "nested/fixture.json", fixtureLimit, func() error {
				switch swap {
				case "file":
					if err := os.Rename(insideFile, insideFile+".original"); err != nil {
						return err
					}
					return os.Symlink(outsideFile, insideFile)
				case "parent":
					if err := os.Rename(insideDir, insideDir+".original"); err != nil {
						return err
					}
					return os.Symlink(outside, insideDir)
				default:
					return errors.New("unknown swap")
				}
			})
			if err == nil {
				t.Fatalf("accepted %s symlink swap", swap)
			}
		})
	}
}

func TestFixtureReadPathRemainsPortableAndConfined(t *testing.T) {
	got := fixtureReadPath("calc/calc.go")
	if got != "fixture/calc/calc.go" || !safeFixturePath(got) {
		t.Fatalf("fixture read path = %q", got)
	}
}

func TestPermissionRejectedNoWrite(t *testing.T) {
	runFixtureScenario(t, "permission-rejection")
}

func TestReadEditTest(t *testing.T) {
	runFixtureScenario(t, "read-edit-test")
}

func TestMalformedToolInput(t *testing.T) {
	runFixtureScenario(t, "malformed-input")
}

func TestCancellationTerminatesOwnedTreeWithoutWrite(t *testing.T) {
	s, p, files := loadFixture(t, "cancellation")
	root, repo := scenarioRepo(t, files)
	provider := newProvider(t, p, s.Tools)
	defer provider.server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	binary := e2eBinary(t)
	result := make(chan error, 1)
	go func() {
		_, err := runInvocation(ctx, t, binary, root, repo, s, provider.server.URL)
		result <- err
	}()
	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider request did not arrive")
	}
	started := time.Now()
	cancel()
	if err := <-result; ownedprocess.Code(err) != "process_canceled" {
		t.Fatalf("cancellation error code = %q", ownedprocess.Code(err))
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("process tree termination took %s", elapsed)
	}
	for _, name := range s.Expected.Absent {
		if _, err := os.Lstat(filepath.Join(repo, filepath.FromSlash(name))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cancelled invocation wrote %q", name)
		}
	}
	if count := provider.requestCount(); count != 1 {
		t.Fatalf("cancelled invocation made %d provider requests", count)
	}
	if got := workingTreeStatus(t, repo); strings.Join(got, "\n") != strings.Join(s.Expected.GitStatus, "\n") {
		t.Fatal("cancelled invocation dirtied repository")
	}
}

func TestSessionResumePreservesExactToolResult(t *testing.T) {
	s, p, files := loadFixture(t, "session-resume")
	root, repo := scenarioRepo(t, files)
	provider := newProvider(t, p, s.Tools)
	defer provider.server.Close()
	first, err := runInvocation(context.Background(), t, e2eBinary(t), root, repo, s, provider.server.URL)
	if err != nil || first.Status != s.Expected.Status || first.TerminalReason != s.Expected.TerminalReason || first.SessionID == "" {
		t.Fatal("first invocation failed")
	}
	journal := provider.journalSnapshot()
	if len(journal) != 2 {
		t.Fatal("first invocation did not retain one exact write result")
	}
	marker := filepath.Join(repo, "marker.txt")
	before := hash(t, marker)
	content, err := os.ReadFile(marker)
	if err != nil || strings.Count(string(content), "marker\n") != 1 {
		t.Fatal("first invocation did not write one marker")
	}
	if before != s.Expected.FileSHA256["marker.txt"] || strings.Join(workingTreeStatus(t, repo), "\n") != strings.Join(s.Expected.GitStatus, "\n") {
		t.Fatal("first invocation marker oracle failed")
	}
	second, err := runInvocation(context.Background(), t, e2eBinary(t), root, repo, s, provider.server.URL, "--resume", first.SessionID)
	if err != nil || second.Status != s.Expected.Status || second.TerminalReason != s.Expected.TerminalReason || second.Output != "resumed" {
		t.Fatalf("resumed invocation failed")
	}
	journal = provider.journalSnapshot()
	if provider.requestCount() != len(p.Steps) || len(journal) != len(p.Steps) || len(journal[2].Prior) != 1 || journal[2].Prior[0] != "write" {
		t.Fatal("resume did not preserve prior tool result")
	}
	if count, consistent := provider.priorObservationSnapshot("write"); count != 2 || !consistent {
		t.Fatalf("exact write result observations = count:%d consistent:%v, want count:2 consistent:true", count, consistent)
	}
	if hash(t, marker) != before {
		t.Fatal("resume rewrote marker")
	}
	if strings.Join(workingTreeStatus(t, repo), "\n") != strings.Join(s.Expected.GitStatus, "\n") {
		t.Fatal("resume changed repository state")
	}
}

func TestFailoverDisposition(t *testing.T) {
	s, p, files := loadFixture(t, "failover")
	root, repo := scenarioRepo(t, files)
	provider := newProvider(t, p, s.Tools)
	defer provider.server.Close()
	out, err := runInvocation(context.Background(), t, e2eBinary(t), root, repo, s, provider.server.URL, "--fallback-model", "gpt-4o-mini")
	if err != nil || out.Status != s.Expected.Status || out.TerminalReason != s.Expected.TerminalReason || out.Output != "recovered" {
		t.Fatal("fallback invocation failed")
	}
	journal := provider.journalSnapshot()
	if len(journal) != 4 || journal[0].Model != "gpt-4o" || journal[1].Model != "gpt-4o" || journal[2].Model != "gpt-4o" || journal[3].Model != "gpt-4o-mini" {
		t.Fatalf("failover order = %#v", journal)
	}
	if got := workingTreeStatus(t, repo); strings.Join(got, "\n") != strings.Join(s.Expected.GitStatus, "\n") {
		t.Fatal("failover changed repository state")
	}
}

func TestAgentCompletionRealBinaryDeliversOnceAcrossRestart(t *testing.T) {
	s, p, files := loadFixture(t, "agent-completion")
	root, repo := scenarioRepo(t, files)
	provider := newProvider(t, p, s.Tools)
	defer provider.server.Close()
	first, err := runInvocation(context.Background(), t, e2eBinary(t), root, repo, s, provider.server.URL)
	if err != nil || first.Status != "completed" || first.SessionID == "" || first.Output != "parent-done" {
		t.Fatalf("foreground Explore completion did not reach parent: err=%v journal=%#v", err, provider.journalSnapshot())
	}
	transcriptDir := durableTranscriptDir(t, root, first.SessionID)
	assertDurableForegroundAgentCompletion(t, first.SessionID, transcriptDir)
	second, err := runInvocation(context.Background(), t, e2eBinary(t), root, repo, s, provider.server.URL, "--resume", first.SessionID)
	if err != nil || second.Status != "completed" || second.Output != "resumed" {
		t.Fatal("first restart did not consume completion")
	}
	third, err := runInvocation(context.Background(), t, e2eBinary(t), root, repo, s, provider.server.URL, "--resume", first.SessionID)
	if err != nil || third.Status != "completed" || third.Output != "no-redelivery" {
		t.Fatal("second restart redelivered completion")
	}
	journal := provider.journalSnapshot()
	if len(journal) != 5 {
		t.Fatalf("journal request count = %d", len(journal))
	}
	for index, want := range []string{"parent", "child", "parent", "parent", "parent"} {
		if journal[index].Lane != want || journal[index].Request != index+1 {
			t.Fatalf("journal transition %d = %#v", index, journal[index])
		}
	}
	for _, index := range []int{2, 3, 4} {
		if len(journal[index].Prior) != 1 || journal[index].Prior[0] != "agent" {
			t.Fatalf("parent request %d historical Agent result IDs = %q, want exactly [agent]", index+1, journal[index].Prior)
		}
	}
	assertDurableForegroundAgentCompletion(t, first.SessionID, transcriptDir)
	if strings.Join(journal[0].Tools, ",") != "Agent,Bash,Glob,Grep,Read" || strings.Join(journal[1].Tools, ",") != "Bash,Glob,Grep,Read" || len(journal[2].Prior) != 1 || journal[2].Prior[0] != "agent" {
		t.Fatal("lane tool-set or one-time parent consumption mismatch")
	}
}

func assertDurableForegroundAgentCompletion(t *testing.T, sessionID, transcriptDir string) {
	t.Helper()
	loaded, err := transcript.NewRecorder(sessionID, transcriptDir).LoadFull()
	if err != nil {
		t.Fatalf("load durable parent transcript: %v", err)
	}
	if len(loaded.AgentCompletionReceipts) != 1 {
		t.Fatalf("foreground Agent completion receipts = %d, want 1", len(loaded.AgentCompletionReceipts))
	}
	parentVisible := 0
	for _, message := range loaded.Messages {
		if message != nil && strings.Contains(message.Content, "Sub-agent completed task: explore") {
			parentVisible++
		}
	}
	if parentVisible != 1 {
		t.Fatalf("durable parent-visible foreground Agent result count = %d, want 1", parentVisible)
	}
}

func runScenarioData(t *testing.T, s scenario, p providerScript, files map[string]string) {
	t.Helper()
	rawScenario, _ := json.Marshal(s)
	rawScript, _ := json.Marshal(p)
	runScenario(t, string(rawScenario), string(rawScript), files)
}

func runFixtureScenario(t *testing.T, id string) {
	t.Helper()
	s, p, files := loadFixture(t, id)
	runScenarioData(t, s, p, files)
}

func loadFixture(t *testing.T, id string) (scenario, providerScript, map[string]string) {
	t.Helper()
	root := filepath.Join("testdata", id)
	var s scenario
	if err := loadManifest(root, "scenario.json", &s); err != nil || validateScenario(s) != nil {
		t.Fatal("invalid scenario fixture")
	}
	var p providerScript
	if err := loadManifest(root, "provider/steps.json", &p); err != nil {
		t.Fatalf("load provider fixture: %v", err)
	} else if err := validateScript(p); err != nil {
		t.Fatalf("validate provider fixture: %v", err)
	}
	var fixtures fixtureManifest
	if err := loadManifest(root, "fixtures.json", &fixtures); err != nil || fixtures.SchemaVersion != 1 {
		t.Fatal("invalid fixture manifest")
	}
	files, err := loadFixtureFiles(root, fixtures)
	if err != nil {
		t.Fatal("invalid fixture file")
	}
	return s, p, files
}

func loadFixtureFiles(root string, manifest fixtureManifest) (map[string]string, error) {
	if manifest.SchemaVersion != 1 || len(manifest.Files) == 0 {
		return nil, errors.New("invalid fixture manifest")
	}
	files := make(map[string]string, len(manifest.Files))
	var total int64
	for _, name := range manifest.Files {
		if !safeFixturePath(name) {
			return nil, errors.New("invalid fixture path")
		}
		if _, exists := files[name]; exists {
			return nil, errors.New("duplicate fixture path")
		}
		data, err := readConfinedRegular(root, fixtureReadPath(name), fixtureLimit-total)
		if err != nil {
			return nil, err
		}
		if credentialShaped(data) {
			return nil, errors.New("fixture contains credential-shaped bytes")
		}
		total += int64(len(data))
		if total > fixtureLimit {
			return nil, errors.New("fixtures exceed limit")
		}
		files[name] = string(data)
	}
	return files, nil
}

func runScenario(t *testing.T, rawScenario, rawScript string, files map[string]string) {
	t.Helper()
	s, err := loadScenario([]byte(rawScenario))
	if err != nil {
		t.Fatalf("%s: scenario", s.ID)
	}
	p, err := loadScript([]byte(rawScript))
	if err != nil {
		t.Fatalf("%s: script", s.ID)
	}
	root, repo := scenarioRepo(t, files)
	initialReadEditHash := ""
	if s.ID == "read-edit-test" {
		initialReadEditHash = hash(t, filepath.Join(repo, "calc", "calc.go"))
	}
	provider := newProvider(t, p, s.Tools)
	defer provider.server.Close()
	binary := e2eBinary(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.TimeoutMillis)*time.Millisecond)
	defer cancel()
	out, err := runInvocation(ctx, t, binary, root, repo, s, provider.server.URL)
	if err != nil {
		t.Fatalf("%s: binary failed", s.ID)
	}
	if out.ExitCode != s.Expected.ExitCode || out.Status != s.Expected.Status || out.TerminalReason != s.Expected.TerminalReason {
		t.Fatalf("%s: envelope mismatch", s.ID)
	}
	if provider.requestCount() != len(p.Steps) {
		t.Fatalf("%s: provider journal mismatch", s.ID)
	}
	for name, want := range s.Expected.FileSHA256 {
		if got := hash(t, filepath.Join(repo, filepath.FromSlash(name))); got != want {
			t.Fatalf("%s: hash(%s) = %q, want %q", s.ID, name, got, want)
		}
	}
	for _, name := range s.Expected.Absent {
		if _, err := os.Lstat(filepath.Join(repo, filepath.FromSlash(name))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: expected %q to remain absent, got %v", s.ID, name, err)
		}
	}
	if s.Expected.GitStatus != nil {
		got := workingTreeStatus(t, repo)
		if strings.Join(got, "\n") != strings.Join(s.Expected.GitStatus, "\n") {
			t.Fatalf("%s: git status = %q, want %q", s.ID, got, s.Expected.GitStatus)
		}
	}
	if s.ID == "read-edit-test" {
		git(t, repo, "diff", "--check")
		test := exec.Command("go", "test", "./...")
		test.Dir = repo
		if b, e := test.CombinedOutput(); e != nil {
			t.Fatalf("%s: independent test failed: %d", s.ID, len(b))
		}
		if hash(t, filepath.Join(repo, "calc/calc.go")) == initialReadEditHash {
			t.Fatalf("%s: no edit", s.ID)
		}
	}
}

func scenarioRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	for _, dir := range []string{"home", "cfg", "cache", "tmp", "transcripts"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var total int64
	for n, v := range files {
		if !safeFixturePath(n) {
			t.Fatal("invalid fixture path")
		}
		total += int64(len(v))
		if total > fixtureLimit {
			t.Fatal("fixtures exceed limit")
		}
		path := filepath.Join(repo, n)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "init")
	git(t, repo, "config", "user.email", "e2e@example.invalid")
	git(t, repo, "config", "user.name", "e2e")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "fixture")
	return root, repo
}

func durableTranscriptDir(t *testing.T, root, sessionID string) string {
	t.Helper()
	var matches []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && entry.Name() == sessionID+".jsonl" {
			matches = append(matches, filepath.Dir(path))
		}
		return nil
	})
	if err != nil || len(matches) != 1 {
		t.Fatalf("durable parent transcript paths = %q, err=%v", matches, err)
	}
	return matches[0]
}

func runInvocation(ctx context.Context, t *testing.T, binary, root, repo string, s scenario, providerURL string, extra ...string) (envelope, error) {
	t.Helper()
	args := []string{"exec", s.Prompt, "--output-format", "json", "--provider", "openai", "--model", "gpt-4o", "--base-url", providerURL, "--api-key", "e2e-key", "--max-turns", fmt.Sprint(s.MaxTurns), "--tools", strings.Join(s.Tools, ","), "--sandbox", "danger-full-access"}
	if s.PermissionMode == "bypass" {
		args = append(args, "-y")
	}
	args = append(args, extra...)
	cmd := exec.Command(binary, args...)
	cmd.Dir = repo
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(root, "home"), "XDG_CONFIG_HOME=" + filepath.Join(root, "cfg"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "TMPDIR=" + filepath.Join(root, "tmp"), "CLAUDE_TRANSCRIPT_DIR=" + filepath.Join(root, "transcripts"), "NO_PROXY=127.0.0.1,localhost", "GOTOOLCHAIN=local"}
	var stdout, stderr limitedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := ownedprocess.Run(ctx, cmd)
	if err != nil {
		return envelope{}, err
	}
	var out envelope
	if err := strictJSON(stdout.Bytes(), &out); err != nil {
		return envelope{}, err
	}
	return out, nil
}

func workingTreeStatus(t *testing.T, repo string) []string {
	t.Helper()
	status := strings.TrimSuffix(gitOutput(t, repo, "status", "--short", "--untracked-files=all"), "\n")
	if status == "" {
		return []string{}
	}
	lines := strings.Split(status, "\n")
	kept := lines[:0]
	canonicalStatePrefix := identity.ProjectDirName + "/"
	for _, line := range lines {
		if len(line) >= 4 && strings.HasPrefix(filepath.ToSlash(line[3:]), canonicalStatePrefix) {
			continue
		}
		kept = append(kept, line)
	}
	return kept
}

type limitedBuffer struct{ b bytes.Buffer }

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.b.Len()+len(p) > 64<<10 {
		return 0, errors.New("output limit")
	}
	return l.b.Write(p)
}
func (l *limitedBuffer) Bytes() []byte { return l.b.Bytes() }

func e2eBinary(t *testing.T) string {
	t.Helper()
	if b := os.Getenv("EINO_E2E_BINARY"); b != "" {
		return b
	}
	packageBinary.Do(func() {
		packageBinary.root, packageBinary.err = os.MkdirTemp("", "yhc-e2e-binary-")
		if packageBinary.err != nil {
			return
		}
		binaryName := "yhc"
		if runtime.GOOS == "windows" {
			binaryName += ".exe"
		}
		packageBinary.path = filepath.Join(packageBinary.root, binaryName)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(context.WithoutCancel(ctx), "go", "build", "-o", packageBinary.path, "./cmd/yhc")
		cmd.Dir = filepath.Clean(filepath.Join("..", ".."))
		var output limitedBuffer
		cmd.Stdout, cmd.Stderr = &output, &output
		packageBinary.err = ownedprocess.Run(ctx, cmd)
	})
	if packageBinary.err != nil {
		t.Fatalf("build binary failed: %s", ownedprocess.Code(packageBinary.err))
	}
	return packageBinary.path
}

func TestMain(m *testing.M) {
	code := m.Run()
	if packageBinary.root != "" {
		_ = os.RemoveAll(packageBinary.root)
	}
	os.Exit(code)
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = gitOutput(t, dir, args...)
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git: %d", len(b))
	}
	return string(b)
}

func hash(t *testing.T, p string) string {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal("hash")
	}
	return hashBytes(string(b))
}
func hashBytes(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
