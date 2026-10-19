package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestBashFailureFeedbackReachesNextModelRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash process fixture requires Unix")
	}
	// The expected diagnostic exists only in the fixture's output, not in the
	// echoed command. Seeing the command cannot satisfy the provider oracle.
	const check = "printf 'check started\\n'\nprintf '%12000s' ' '\nprintf '\\nFINAL_ASSERTION_FAILED\\n'\nexit 7\n"
	digest := sha256.Sum256([]byte(check))
	for _, test := range []struct {
		name    string
		command string
		model   string
		extra   []string
		masked  bool
	}{
		{name: "large output", command: "bash check.sh"},
		{name: "command preview and large output", command: "bash check.sh\n# " + strings.Repeat("padding ", 750)},
		{
			name:    "native DeepSeek command preview and large output",
			command: "bash check.sh\n# " + strings.Repeat("padding ", 750),
			model:   "deepseek-flash",
			extra:   []string{"--provider", "agenticdeepseek", "--model", "deepseek-flash"},
		},
		{
			name:    "native DeepSeek failure before successful followup",
			command: "bash check.sh\ntrue",
			model:   "deepseek-flash",
			extra:   []string{"--provider", "agenticdeepseek", "--model", "deepseek-flash"},
			masked:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			args, err := json.Marshal(map[string]string{"command": test.command})
			if err != nil {
				t.Fatal(err)
			}
			s := scenario{
				SchemaVersion: 1, ID: "bash-failure-feedback", Prompt: "Run the fixture check and report its result.",
				Tools: []string{"Bash"}, PermissionMode: "bypass", MaxTurns: 3, TimeoutMillis: 30000,
				Expected: expected{
					Status: "completed", TerminalReason: "completed",
					FileSHA256: map[string]string{"check.sh": hex.EncodeToString(digest[:])}, GitStatus: []string{},
				},
			}
			prior := []priorToolResult{{CallID: "fixture-check", Contains: "FINAL_ASSERTION_FAILED"}}
			if !test.masked {
				prior = append(prior, priorToolResult{CallID: "fixture-check", Contains: "[exit code: 7]"})
			}
			// A compound command reports its final command's status. Its earlier
			// failed assertion must still reach the model as execution evidence.
			p := providerScript{SchemaVersion: 1, Steps: []providerStep{
				{Kind: "tool_call", CallID: "fixture-check", Tool: "Bash", Arguments: args, ExpectModel: test.model},
				{Kind: "final", Text: "The fixture check failed.", ExpectModel: test.model, ExpectPrior: prior},
			}}
			runScenarioData(t, s, p, map[string]string{"check.sh": check}, test.extra...)
		})
	}
}
