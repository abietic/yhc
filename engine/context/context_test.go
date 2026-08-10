package promptctx

import (
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestComposeSystemPromptIncludesContexts(t *testing.T) {
	user := BuildUserContext("/tmp/project", time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC))
	system := map[string]string{"gitStatus": "clean"}
	prompt := ComposeSystemPrompt("Base prompt.", "Appendix.", user, system)

	for _, want := range []string{"Base prompt.", "User context:", "System context:", "currentDate", "gitStatus", "Appendix."} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got %q", want, prompt)
		}
	}
}

func TestComposeBaseSystemPromptOmitsRuntimeContext(t *testing.T) {
	prompt := ComposeBaseSystemPrompt("Base prompt.", "Appendix.")
	for _, want := range []string{"Base prompt.", "Appendix."} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got %q", want, prompt)
		}
	}
	for _, unwanted := range []string{"User context:", "System context:", "currentDate", "gitStatus"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("did not expect prompt to contain %q, got %q", unwanted, prompt)
		}
	}
}

func TestAppendSystemContextAppendsInlineContext(t *testing.T) {
	msg := AppendSystemContext(&schema.Message{Role: schema.System, Content: "Base prompt."}, map[string]string{"gitStatus": "clean", "shell": "bash"})
	if msg == nil {
		t.Fatal("expected system prompt")
		return
	}
	for _, want := range []string{"Base prompt.", "gitStatus: clean", "shell: bash"} {
		if !strings.Contains(msg.Content, want) {
			t.Fatalf("expected appended system prompt to contain %q, got %q", want, msg.Content)
		}
	}
}

func TestPrependUserContextPrependsMetaReminderOutsideTestEnv(t *testing.T) {
	t.Setenv("NODE_ENV", "production")
	messages := []*schema.Message{{Role: schema.User, Content: "hello"}}
	withContext := PrependUserContext(messages, map[string]string{"cwd": "/tmp/project"})
	if len(withContext) != 2 {
		t.Fatalf("expected prepended meta reminder plus original message, got %#v", withContext)
	}
	if withContext[0].Role != schema.User {
		t.Fatalf("expected prepended reminder to be user-role, got %#v", withContext[0])
	}
	if withContext[0].Extra == nil || withContext[0].Extra["is_meta"] != true {
		t.Fatalf("expected prepended reminder to be meta, got %#v", withContext[0].Extra)
		return
	}
	for _, want := range []string{"<system-reminder>", "# cwd", "/tmp/project"} {
		if !strings.Contains(withContext[0].Content, want) {
			t.Fatalf("expected prepended reminder to contain %q, got %q", want, withContext[0].Content)
		}
	}
	if withContext[1].Content != "hello" {
		t.Fatalf("expected original message to remain last, got %#v", withContext[1])
	}
}

func TestPrependUserContextSkipsReminderInTestEnv(t *testing.T) {
	t.Setenv("NODE_ENV", "test")
	messages := []*schema.Message{{Role: schema.User, Content: "hello"}}
	withContext := PrependUserContext(messages, map[string]string{"cwd": "/tmp/project"})
	if len(withContext) != 1 || withContext[0].Content != "hello" {
		t.Fatalf("expected test env to skip prepended reminder, got %#v", withContext)
	}
}
