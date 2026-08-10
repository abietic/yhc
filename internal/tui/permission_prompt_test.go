package tui

import (
	"strings"
	"testing"
	"time"
)

func TestAssessRiskLevel(t *testing.T) {
	tests := []struct {
		name   string
		tool   string
		params map[string]any
		want   RiskLevel
	}{
		{
			name: "read is low risk",
			tool: "Read",
			want: RiskLow,
		},
		{
			name: "grep is low risk",
			tool: "Grep",
			want: RiskLow,
		},
		{
			name: "write is medium risk",
			tool: "Write",
			want: RiskMedium,
		},
		{
			name: "edit is medium risk",
			tool: "Edit",
			want: RiskMedium,
		},
		{
			name:   "rm -rf is high risk",
			tool:   "Bash",
			params: map[string]any{"command": "rm -rf /tmp/stuff"},
			want:   RiskHigh,
		},
		{
			name:   "sudo is high risk",
			tool:   "Bash",
			params: map[string]any{"command": "sudo apt install foo"},
			want:   RiskHigh,
		},
		{
			name:   "git push --force is high risk",
			tool:   "Bash",
			params: map[string]any{"command": "git push --force origin main"},
			want:   RiskHigh,
		},
		{
			name:   "ls is low risk",
			tool:   "Bash",
			params: map[string]any{"command": "ls -la"},
			want:   RiskLow,
		},
		{
			name:   "git status is low risk",
			tool:   "Bash",
			params: map[string]any{"command": "git status"},
			want:   RiskLow,
		},
		{
			name:   "npm install is medium risk",
			tool:   "Bash",
			params: map[string]any{"command": "npm install express"},
			want:   RiskMedium,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := assessRiskLevel(tt.tool, tt.params)
			if got != tt.want {
				t.Errorf("assessRiskLevel(%s) = %v, want %v", tt.tool, got, tt.want)
			}
		})
	}
}

func TestPermissionPrompt_Show(t *testing.T) {
	styles := defaultStyles()
	p := NewPermissionPrompt(styles)

	ch := make(chan PermissionResponse, 1)
	p.Show("Bash", `{"command":"ls -la"}`, "this command", ch)

	if !p.IsVisible() {
		t.Fatal("prompt should be visible after Show")
	}
	if p.riskLevel != RiskLow {
		t.Fatalf("expected RiskLow for ls, got %v", p.riskLevel)
	}
	if len(p.options) == 0 {
		t.Fatal("expected options to be populated")
	}
}

func TestPermissionPrompt_ShowHighRisk(t *testing.T) {
	styles := defaultStyles()
	p := NewPermissionPrompt(styles)

	ch := make(chan PermissionResponse, 1)
	p.Show("Bash", `{"command":"rm -rf /"}`, "this command", ch)

	if p.riskLevel != RiskHigh {
		t.Fatalf("expected RiskHigh, got %v", p.riskLevel)
	}
	if !p.timeoutEnabled {
		t.Fatal("expected timeout to be enabled for high risk")
	}
}

func TestPermissionPrompt_Timeout(t *testing.T) {
	styles := defaultStyles()
	p := NewPermissionPrompt(styles)

	ch := make(chan PermissionResponse, 1)
	p.ShowWithTimeout("Bash", `{"command":"rm -rf /tmp"}`, "", 100*time.Millisecond, ch)

	// Initially not expired
	expired := p.Tick()
	if expired {
		t.Fatal("should not be expired immediately")
	}

	// Wait for timeout
	time.Sleep(150 * time.Millisecond)
	expired = p.Tick()
	if !expired {
		t.Fatal("should be expired after timeout")
	}

	// Should have sent deny response
	resp := <-ch
	if resp != PermissionDeny {
		t.Fatalf("expected PermissionDeny on timeout, got %v", resp)
	}
}

func TestPermissionPrompt_Render(t *testing.T) {
	styles := defaultStyles()
	p := NewPermissionPrompt(styles)

	ch := make(chan PermissionResponse, 1)
	p.Show("Bash", `{"command":"echo hello"}`, "this command", ch)

	rendered := p.Render(80, 30)
	if rendered == "" {
		t.Fatal("expected non-empty render")
	}

	// Should contain tool name and options
	if !strings.Contains(rendered, "Shell Command") {
		t.Fatal("expected 'Shell Command' in render")
	}
	if !strings.Contains(rendered, "Allow Once") {
		t.Fatal("expected 'Allow Once' option in render")
	}
	if !strings.Contains(rendered, "Deny") {
		t.Fatal("expected 'Deny' option in render")
	}

	// Clean up
	p.ForceClose()
}

func TestPermissionPrompt_ForceClose(t *testing.T) {
	styles := defaultStyles()
	p := NewPermissionPrompt(styles)

	ch := make(chan PermissionResponse, 1)
	p.Show("Read", `{"file_path":"/tmp/test"}`, "", ch)

	p.ForceClose()
	if p.IsVisible() {
		t.Fatal("should not be visible after ForceClose")
	}

	resp := <-ch
	if resp != PermissionDeny {
		t.Fatalf("expected PermissionDeny on ForceClose, got %v", resp)
	}
}

func TestGenerateToolContext(t *testing.T) {
	tests := []struct {
		tool   string
		params map[string]any
		want   string
	}{
		{
			tool:   "Bash",
			params: map[string]any{"command": "ls"},
			want:   "Execute: ls",
		},
		{
			tool:   "Write",
			params: map[string]any{"file_path": "/tmp/test.go"},
			want:   "Create or overwrite file:",
		},
		{
			tool:   "Agent",
			params: map[string]any{"description": "search code"},
			want:   "Spawn sub-agent: search code",
		},
	}

	for _, tt := range tests {
		ctx := generateToolContext(tt.tool, tt.params)
		if !strings.Contains(ctx, tt.want) {
			t.Errorf("generateToolContext(%s) = %q, want to contain %q", tt.tool, ctx, tt.want)
		}
	}
}

func TestBuildPromptOptions(t *testing.T) {
	opts := buildPromptOptions("Bash", "this command")
	if len(opts) < 3 {
		t.Fatalf("expected at least 3 options, got %d", len(opts))
	}

	// First should be Allow
	if opts[0].Response != PermissionAllow {
		t.Fatal("first option should be Allow")
	}
	// Last should be Deny
	if opts[len(opts)-1].Response != PermissionDeny {
		t.Fatal("last option should be Deny")
	}
}

func TestRiskLevel_String(t *testing.T) {
	if RiskLow.String() != "Low" {
		t.Fatal("RiskLow.String() should be 'Low'")
	}
	if RiskMedium.String() != "Medium" {
		t.Fatal("RiskMedium.String() should be 'Medium'")
	}
	if RiskHigh.String() != "High" {
		t.Fatal("RiskHigh.String() should be 'High'")
	}
}
