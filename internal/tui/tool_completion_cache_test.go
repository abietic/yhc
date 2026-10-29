package tui

import (
	"strings"
	"testing"
)

func TestToolCompletionRefreshesAnimatedHistory(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "success"
		if failed {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			chat := NewChatView(defaultStyles())
			chat.SetSize(100, 28)
			chat.AppendToolStart("write-1", "Write", `{"file_path":"fixture.txt","content":"completed bytes\n"}`)
			// Render the running item at frame one before its result arrives.
			// Completion advances the item version by one; the animation
			// counter must not make that different state share a cache key.
			chat.SetSpinnerCount(1)
			before := stripANSIForTest(chat.Render(100, 28))
			if !strings.Contains(before, "Write running") {
				t.Fatalf("running tool missing before result: %q", before)
			}
			if failed {
				chat.UpdateToolError("write-1", "Write", "failed fixture write")
			} else {
				chat.UpdateToolResult("write-1", "Write", "wrote fixture.txt")
			}
			after := stripANSIForTest(chat.Render(100, 28))
			want := "Write +"
			if failed {
				want = "Write failed"
			}
			if strings.Contains(after, "Write running") || !strings.Contains(after, want) {
				t.Fatalf("result did not replace animated running history: %q", after)
			}
			if got := stripANSIForTest(chat.Render(100, 28)); got != after {
				t.Fatalf("completed frame changed without another event: %q", got)
			}
		})
	}
}
