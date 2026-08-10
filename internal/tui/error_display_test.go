package tui

import (
	"strings"
	"testing"
	"time"
)

func TestClassifyError_RateLimit(t *testing.T) {
	entry := ClassifyError("Rate limit exceeded: 429 Too Many Requests")

	if entry.Severity != SeverityInfo {
		t.Fatalf("expected SeverityInfo, got %v", entry.Severity)
	}
	if entry.Category != CategoryRateLimit {
		t.Fatalf("expected CategoryRateLimit, got %v", entry.Category)
	}
	if !entry.Retryable {
		t.Fatal("rate limit errors should be retryable")
	}
}

func TestClassifyError_Auth(t *testing.T) {
	entry := ClassifyError("Unauthorized: invalid api key provided")

	if entry.Severity != SeverityError {
		t.Fatalf("expected SeverityError, got %v", entry.Severity)
	}
	if entry.Category != CategoryAuth {
		t.Fatalf("expected CategoryAuth, got %v", entry.Category)
	}
	if entry.Retryable {
		t.Fatal("auth errors should not be retryable")
	}
}

func TestClassifyError_Network(t *testing.T) {
	entry := ClassifyError("connection refused: dial tcp 127.0.0.1:8080")

	if entry.Severity != SeverityWarning {
		t.Fatalf("expected SeverityWarning, got %v", entry.Severity)
	}
	if entry.Category != CategoryNetwork {
		t.Fatalf("expected CategoryNetwork, got %v", entry.Category)
	}
	if !entry.Retryable {
		t.Fatal("network errors should be retryable")
	}
}

func TestClassifyError_Model(t *testing.T) {
	entry := ClassifyError("context length exceeded: maximum context 128000 tokens")

	if entry.Category != CategoryModel {
		t.Fatalf("expected CategoryModel, got %v", entry.Category)
	}
	if !entry.Retryable {
		t.Fatal("model errors should be retryable")
	}
	if len(entry.Suggestions) == 0 {
		t.Fatal("model errors should have suggestions")
	}
}

func TestClassifyError_Config(t *testing.T) {
	entry := ClassifyError("missing config: no API key configured")

	if entry.Category != CategoryConfig {
		t.Fatalf("expected CategoryConfig, got %v", entry.Category)
	}
	if entry.Retryable {
		t.Fatal("config errors should not be retryable")
	}
}

func TestClassifyError_General(t *testing.T) {
	entry := ClassifyError("something unexpected happened")

	if entry.Category != CategoryGeneral {
		t.Fatalf("expected CategoryGeneral, got %v", entry.Category)
	}
}

func TestNewRateLimitError(t *testing.T) {
	entry := NewRateLimitError(30*time.Second, "rate limited")

	if entry.RetryAfter != 30*time.Second {
		t.Fatalf("expected 30s retry after, got %v", entry.RetryAfter)
	}
	if entry.RetryAt.IsZero() {
		t.Fatal("RetryAt should be set")
	}
	if len(entry.Suggestions) == 0 {
		t.Fatal("should have suggestions")
	}
}

func TestNewToolError(t *testing.T) {
	entry := NewToolError("Bash", "command failed", "exit code 1\nstderr: file not found")

	if entry.Category != CategoryTool {
		t.Fatalf("expected CategoryTool, got %v", entry.Category)
	}
	if entry.Context != "Bash" {
		t.Fatalf("expected context 'Bash', got '%s'", entry.Context)
	}
	if entry.Details == "" {
		t.Fatal("expected non-empty details")
	}
	if !strings.Contains(entry.Title, "Bash") {
		t.Fatal("title should contain tool name")
	}
}

func TestErrorDisplay_AddAndRetrieve(t *testing.T) {
	styles := defaultStyles()
	ed := NewErrorDisplay(styles)

	if ed.Count() != 0 {
		t.Fatal("expected 0 errors initially")
	}

	ed.AddError(ErrorEntry{
		Severity: SeverityWarning,
		Category: CategoryGeneral,
		Title:    "Test Error",
		Message:  "Something went wrong",
	})

	if ed.Count() != 1 {
		t.Fatalf("expected 1 error, got %d", ed.Count())
	}

	last := ed.LastError()
	if last == nil {
		t.Fatal("expected non-nil last error")
		return
	}
	if last.Title != "Test Error" {
		t.Fatalf("expected title 'Test Error', got '%s'", last.Title)
	}
}

func TestErrorDisplay_Clear(t *testing.T) {
	styles := defaultStyles()
	ed := NewErrorDisplay(styles)

	ed.AddError(ErrorEntry{Title: "Error 1"})
	ed.AddError(ErrorEntry{Title: "Error 2"})

	if ed.Count() != 2 {
		t.Fatal("expected 2 errors")
	}

	ed.Clear()
	if ed.Count() != 0 {
		t.Fatal("expected 0 errors after Clear")
	}
}

func TestErrorDisplay_ToggleDetails(t *testing.T) {
	styles := defaultStyles()
	ed := NewErrorDisplay(styles)

	ed.AddError(ErrorEntry{
		Title:   "Test Error",
		Details: "some details here",
	})

	// Initially not expanded
	if ed.expandedIdx[0] {
		t.Fatal("should not be expanded initially")
	}

	ed.ToggleDetails(0)
	if !ed.expandedIdx[0] {
		t.Fatal("should be expanded after toggle")
	}

	ed.ToggleDetails(0)
	if ed.expandedIdx[0] {
		t.Fatal("should be collapsed after second toggle")
	}
}

func TestErrorMessage_Render(t *testing.T) {
	styles := defaultStyles()
	entry := ErrorEntry{
		Severity: SeverityError,
		Category: CategoryAuth,
		Title:    "Auth Failed",
		Message:  "Invalid API key",
		Suggestions: []SuggestedAction{
			{Label: "Check API key", Command: "/config"},
		},
		Timestamp: time.Now(),
	}

	msg := NewErrorMessage(entry, styles)
	rendered := msg.Render(80, styles)

	if rendered == "" {
		t.Fatal("expected non-empty render")
	}
	if !strings.Contains(rendered, "Auth Failed") {
		t.Fatal("render should contain title")
	}
	if !strings.Contains(rendered, "Invalid API key") {
		t.Fatal("render should contain message")
	}
	if !strings.Contains(rendered, "Check API key") {
		t.Fatal("render should contain suggestion")
	}
}

func TestErrorMessage_ChatItemInterface(t *testing.T) {
	styles := defaultStyles()
	entry := ErrorEntry{
		Title:   "Test",
		Message: "msg",
	}

	msg := NewErrorMessage(entry, styles)

	// Should always be finished (errors are static)
	if !msg.Finished() {
		t.Fatal("ErrorMessage should always be Finished")
	}

	// Version should start at 1
	if msg.Version() < 1 {
		t.Fatal("Version should be >= 1")
	}

	// Toggle expand should increment version
	v := msg.Version()
	msg.ToggleExpand()
	if msg.Version() <= v {
		t.Fatal("Version should increment after ToggleExpand")
	}
}

func TestErrorDisplay_Panel(t *testing.T) {
	styles := defaultStyles()
	ed := NewErrorDisplay(styles)

	// Empty panel
	ed.ShowPanel(20)
	rendered := ed.RenderPanel(80, 20)
	if !strings.Contains(rendered, "No errors") {
		t.Fatal("empty panel should show 'No errors' message")
	}

	// Add errors and re-render
	ed.HidePanel()
	ed.AddError(ErrorEntry{
		Severity:  SeverityWarning,
		Category:  CategoryNetwork,
		Title:     "Connection Lost",
		Message:   "Failed to connect to API",
		Timestamp: time.Now(),
	})
	ed.AddError(ErrorEntry{
		Severity:  SeverityError,
		Category:  CategoryAuth,
		Title:     "Auth Failed",
		Message:   "API key expired",
		Timestamp: time.Now(),
	})

	ed.ShowPanel(30)
	rendered = ed.RenderPanel(80, 30)
	if !strings.Contains(rendered, "Error History") {
		t.Fatal("panel should contain 'Error History' header")
	}
	if !strings.Contains(rendered, "2 errors") {
		t.Fatal("panel should show error count")
	}
}

func TestErrorSeverity_String(t *testing.T) {
	if SeverityInfo.String() != "Info" {
		t.Fatal("SeverityInfo.String() should be 'Info'")
	}
	if SeverityWarning.String() != "Warning" {
		t.Fatal("SeverityWarning.String() should be 'Warning'")
	}
	if SeverityError.String() != "Error" {
		t.Fatal("SeverityError.String() should be 'Error'")
	}
}

func TestErrorCategory_String(t *testing.T) {
	categories := []struct {
		cat  ErrorCategory
		want string
	}{
		{CategoryGeneral, "General"},
		{CategoryRateLimit, "Rate Limit"},
		{CategoryAuth, "Authentication"},
		{CategoryNetwork, "Network"},
		{CategoryModel, "Model"},
		{CategoryTool, "Tool"},
		{CategoryConfig, "Configuration"},
		{CategoryPermission, "Permission"},
	}

	for _, tc := range categories {
		if tc.cat.String() != tc.want {
			t.Errorf("%v.String() = %q, want %q", tc.cat, tc.cat.String(), tc.want)
		}
	}
}

func TestFormatRetryDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "now"},
		{-1 * time.Second, "now"},
		{5 * time.Second, "5s"},
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m 30s"},
		{3600 * time.Second, "1h 0m"},
	}

	for _, tt := range tests {
		got := formatRetryDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatRetryDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestWrapErrorText(t *testing.T) {
	text := "This is a longer error message that should be wrapped when it exceeds the available width"
	lines := wrapErrorText(text, 40)

	if len(lines) < 2 {
		t.Fatal("expected text to be wrapped into multiple lines")
	}
	for _, line := range lines {
		if len(line) > 40 {
			t.Fatalf("line exceeds width: '%s' (%d chars)", line, len(line))
		}
	}
}
