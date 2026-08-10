package engine

import (
	"fmt"
	"testing"
)

func BenchmarkRuntimeSnapshot20Agents(b *testing.B) {
	store := NewRuntimeStateStore()
	for index := 0; index < 20; index++ {
		event := runtimeTestEvent(1, fmt.Sprintf("agent-launch:agent-%02d:1", index), EventAgentLifecycle, func(evt *QueryEvent) {
			evt.SessionID = "benchmark-session"
			evt.ThreadID = fmt.Sprintf("thread-%02d", index)
			evt.AgentID = fmt.Sprintf("agent-%02d", index)
			evt.ParentSessionID = "benchmark-session"
			evt.ParentThreadID = "leader-thread"
			evt.ParentAgentID = "leader-agent"
			evt.ParentToolUseID = fmt.Sprintf("spawn-%02d", index)
			evt.AgentLifecycle = &AgentLifecycleEvent{
				Phase: "launched", Name: evt.AgentID, Task: "benchmark runtime snapshot",
				Status: "running", Generation: 1, StartedAt: evt.Timestamp,
			}
		})
		if err := store.Apply(event); err != nil {
			b.Fatal(err)
		}
	}

	b.Run("task_agent_projection", func(b *testing.B) {
		b.ReportMetric(20, "agents")
		b.ReportAllocs()
		for range b.N {
			_ = store.TaskExplorerSnapshot("leader-thread").Runtime
		}
	})
	b.Run("full_snapshot", func(b *testing.B) {
		b.ReportMetric(20, "agents")
		b.ReportAllocs()
		for range b.N {
			store.Snapshot("leader-thread")
		}
	})
}
