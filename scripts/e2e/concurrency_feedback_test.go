package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"runtime"
	"testing"
)

func TestConcurrentProgressFailureRepairFeedback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash process fixture requires Unix")
	}
	const before = `func (d *Dispatcher) Run(key string, job func()) {
	<-d.mu
	defer func() { d.mu <- struct{}{} }()
	guard := d.keys[key]
	if guard == nil {
		guard = make(chan struct{}, 1)
		guard <- struct{}{}
		d.keys[key] = guard
	}
	<-guard
	defer func() { guard <- struct{}{} }()
	job()
}
`
	const after = `func (d *Dispatcher) Run(key string, job func()) {
	<-d.mu
	guard := d.keys[key]
	if guard == nil {
		guard = make(chan struct{}, 1)
		guard <- struct{}{}
		d.keys[key] = guard
	}
	d.mu <- struct{}{}
	<-guard
	defer func() { guard <- struct{}{} }()
	job()
}
`
	const header = `package fixture

type Dispatcher struct {
	mu chan struct{}
	keys map[string]chan struct{}
}

func NewDispatcher() *Dispatcher {
	d := &Dispatcher{mu: make(chan struct{}, 1), keys: make(map[string]chan struct{})}
	d.mu <- struct{}{}
	return d
}

`
	// The oracle checks independent progress and same-key exclusion while the
	// first job is blocked. Final-state checks alone accept the coarse lock.
	// This scripted fixture keeps channel guards, whose waits synctest can
	// observe. A live implementation using sync.Mutex needs a different oracle.
	const checks = `package fixture

import (
	"testing"
	"testing/synctest"
)

func TestKeyIsolation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := NewDispatcher()
		entered := make(chan struct{})
		release := make(chan struct{})
		sameDone := make(chan struct{})
		otherDone := make(chan struct{})
		go d.Run("blocked", func() {
			close(entered)
			<-release
		})
		<-entered
		defer func() {
			close(release)
			synctest.Wait()
			select {
			case <-sameDone:
			default:
				t.Error("queued same-key job did not finish after release")
			}
		}()
		go d.Run("blocked", func() { close(sameDone) })
		go d.Run("independent", func() { close(otherDone) })
		synctest.Wait()
		select {
		case <-otherDone:
		default:
			t.Error("independent key did not progress")
		}
		select {
		case <-sameDone:
			t.Error("same key bypassed exclusion")
		default:
		}
	})
}
`
	files := map[string]string{
		"go.mod":           "module fixture\n\ngo 1.27.0\n",
		"dispatch.go":      header + before,
		"dispatch_test.go": checks,
	}
	args := func(value map[string]string) json.RawMessage {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	for _, native := range []bool{false, true} {
		name := "responses"
		model := ""
		var extra []string
		if native {
			name = "native-deepseek"
			model = "deepseek-flash"
			extra = []string{"--provider", "agenticdeepseek", "--model", model}
		}
		t.Run(name, func(t *testing.T) {
			digests := make(map[string]string, len(files))
			for path, content := range files {
				if path == "dispatch.go" {
					content = header + after
				}
				digest := sha256.Sum256([]byte(content))
				digests[path] = hex.EncodeToString(digest[:])
			}
			s := scenario{
				SchemaVersion: 1, ID: "concurrent-progress-feedback",
				Prompt: "Repair the dispatcher so independent keys can progress while preserving same-key exclusion. Keep the existing checks unchanged and verify the saved implementation.",
				Tools:  []string{"Bash", "Read", "Edit"}, PermissionMode: "bypass", MaxTurns: 8, TimeoutMillis: 30000,
				Expected: expected{
					Status: "completed", TerminalReason: "completed",
					FileSHA256: digests, GitStatus: []string{" M dispatch.go"},
				},
			}
			command := args(map[string]string{"command": "go test -count=1 -v ./..."})
			p := providerScript{SchemaVersion: 1, Steps: []providerStep{
				{Kind: "tool_call", CallID: "before", Tool: "Bash", Arguments: command, ExpectModel: model},
				{
					Kind: "tool_call", CallID: "read", Tool: "Read", Arguments: args(map[string]string{"file_path": "dispatch.go"}), ExpectModel: model,
					ExpectPrior: []priorToolResult{{CallID: "before", Contains: "independent key did not progress"}, {CallID: "before", Contains: "[exit code: 1]"}},
				},
				{
					Kind: "tool_call", CallID: "repair", Tool: "Edit", Arguments: args(map[string]string{"file_path": "dispatch.go", "old_string": before, "new_string": after}), ExpectModel: model,
					ExpectPrior: []priorToolResult{{CallID: "read", Contains: "defer func() { d.mu <- struct{}{} }()"}},
				},
				{
					Kind: "tool_call", CallID: "after", Tool: "Bash", Arguments: command, ExpectModel: model,
					ExpectPrior: []priorToolResult{{CallID: "repair", Contains: "Replaced"}},
				},
				{
					Kind: "final", Text: "The saved dispatcher passes both concurrency checks.", ExpectModel: model,
					ExpectPrior: []priorToolResult{{CallID: "after", Contains: "--- PASS: TestKeyIsolation"}},
				},
			}}
			runScenarioData(t, s, p, files, extra...)
		})
	}
}
