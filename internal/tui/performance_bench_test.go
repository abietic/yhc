package tui

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
)

func BenchmarkChatRender10KMessages(b *testing.B) {
	chat := performanceLongChat(10_000)
	chat.Render(120, 40)
	b.ReportMetric(10_000, "messages")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		chat.viewDirty = true
		chat.Render(120, 40)
	}
}

func BenchmarkAgentThreadCatalog20(b *testing.B) {
	app, _ := performanceAgentApp(20)
	b.ReportMetric(20, "agents")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		entries := app.threadNavigationEntries()
		app.agentPicker.Show(entries, app.activeThreadViewID(), app.leaderThreadViewID())
		app.agentPicker.Overlay("", 100, 30)
		app.agentPicker.Close()
	}
}

func BenchmarkStreamBatch64(b *testing.B) {
	app := performanceStreamApp()
	events := performanceStreamEvents(64)
	b.ReportMetric(64, "events/batch")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		app.chat.Reset()
		app.queryID = 1
		app.running = true
		app.Update(engineBatchMsg{events: events, queryID: 1, done: true})
		app.renderView()
	}
}

func BenchmarkThreadSwitch20Agents(b *testing.B) {
	app, threadIDs := performanceAgentApp(20)
	b.ReportMetric(20, "agents")
	b.ReportAllocs()
	b.ResetTimer()
	for index := range b.N {
		threadID := threadIDs[index%len(threadIDs)]
		if err := app.switchThreadView(threadID, engine.ThreadModeLiveAttach); err != nil {
			b.Fatal(err)
		}
		app.renderView()
	}
}

func TestTUIHotPathPerformanceBudgets(t *testing.T) {
	requireUninstrumentedPerformanceBudget(t)

	if streamBatchWindow < time.Second/30 {
		t.Fatalf("stream batch window %s exceeds the 30fps redraw ceiling", streamBatchWindow)
	}

	chat := performanceLongChat(10_000)
	chat.Render(120, 40)
	assertPerformanceP95(t, "10K visible render", 50*time.Millisecond, 80, func() {
		chat.viewDirty = true
		chat.Render(120, 40)
	})

	agentApp, threadIDs := performanceAgentApp(20)
	switchIndex := 0
	assertPerformanceP95(t, "20-Agent in-memory thread switch", 100*time.Millisecond, 80, func() {
		threadID := threadIDs[switchIndex%len(threadIDs)]
		switchIndex++
		if err := agentApp.switchThreadView(threadID, engine.ThreadModeLiveAttach); err != nil {
			t.Fatal(err)
		}
		agentApp.renderView()
	})

	workflowApp, _ := performanceAgentAppWithMessages(20, 256)
	const attentionThreadID = "thread-17"
	assertPerformanceP95(t, "20-Agent attention locate", 50*time.Millisecond, 80, func() {
		workflowApp.agentPicker.Show(
			workflowApp.threadNavigationEntries(),
			workflowApp.activeThreadViewID(),
			workflowApp.leaderThreadViewID(),
		)
		for _, value := range "agent-17" {
			workflowApp.agentPicker.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{value})})
		}
		overlay := stripANSIForTest(workflowApp.agentPicker.Overlay("", 100, 30))
		if !strings.Contains(overlay, "agent-17") || !strings.Contains(overlay, "!1") {
			t.Fatalf("attention target missing from picker:\n%s", overlay)
		}
		workflowApp.agentPicker.Close()
	})
	assertPerformanceP95(t, "256-message first usable Agent transcript", 100*time.Millisecond, 80, func() {
		if err := workflowApp.activateThreadByID("leader-thread"); err != nil {
			t.Fatal(err)
		}
		if view := workflowApp.threadViews.views[attentionThreadID]; view != nil {
			view.projectedRevision = 0
			view.projectedAgentID = ""
			view.Chat.Reset()
		}
		if err := workflowApp.activateThreadByID(attentionThreadID); err != nil {
			t.Fatal(err)
		}
		if rendered := stripANSIForTest(workflowApp.renderView()); !strings.Contains(rendered, "usable transcript agent-17") {
			t.Fatalf("first usable transcript did not reach the visible frame:\n%s", rendered)
		}
	})

	streamApp := performanceStreamApp()
	streamEvents := performanceStreamEvents(64)
	assertPerformanceP95(t, "64-event stream batch to frame", 500*time.Millisecond, 40, func() {
		streamApp.chat.Reset()
		streamApp.queryID = 1
		streamApp.running = true
		streamApp.Update(engineBatchMsg{events: streamEvents, queryID: 1, done: true})
		streamApp.renderView()
	})

	progressApp := newTestApp(120, 40)
	progressApp.running = true
	progressEvent := engine.QueryEvent{Type: engine.EventTaskProgress, TaskProgress: &engine.TaskProgressEvent{
		TaskID: "latency-agent", Description: "Visible progress budget", LastToolName: "Read",
		Usage: engine.TaskProgressUsage{ToolUses: 1, TotalTokens: 100},
	}}
	assertPerformanceP95(t, "background event to visible progress", 500*time.Millisecond, 40, func() {
		if err := progressApp.handleEngineEvent(progressEvent); err != nil {
			t.Fatal(err)
		}
		progressApp.updateLayout()
		progressApp.renderView()
	})
	if rendered := stripANSIForTest(progressApp.renderView()); !strings.Contains(rendered, "Visible progress budget") {
		t.Fatalf("background progress did not reach the visible frame:\n%s", rendered)
	}

	keyApp := newTestApp(120, 40)
	assertPerformanceP95(t, "ordinary key to frame", 50*time.Millisecond, 80, func() {
		keyApp.textarea.Reset()
		keyApp.Update(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'x'})})
		keyApp.renderView()
	})
}

func requireUninstrumentedPerformanceBudget(t *testing.T) {
	t.Helper()
	if mode := testing.CoverMode(); mode != "" {
		t.Skipf("portable performance budgets require an uninstrumented test binary; coverage mode %q is diagnostic only", mode)
	}
}

func performanceLongChat(messages int) *ChatView {
	chat := NewChatView(defaultStyles())
	chat.SetSize(120, 40)
	for index := 0; index < messages; index++ {
		chat.AppendSystem(fmt.Sprintf("bounded transcript message %05d", index))
	}
	return chat
}

func performanceAgentApp(agentCount int) (*App, []string) {
	return performanceAgentAppWithMessages(agentCount, 1)
}

func performanceAgentAppWithMessages(agentCount, messageCount int) (*App, []string) {
	app := newTestApp(120, 40)
	app.rebindLeaderThreadView("leader-thread")
	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	catalog := engine.RuntimeThreadCatalogSnapshot{
		Revision: 1,
		Threads: []engine.RuntimeThreadCatalogEntry{{
			ThreadID: "leader-thread", Status: engine.RuntimeThreadRunning,
			Mode: engine.ThreadModeLiveAttach, StartedAt: base,
		}},
	}
	details := make(map[string]engine.AgentDetailSnapshot, agentCount)
	threadIDs := make([]string, 0, agentCount+1)
	threadIDs = append(threadIDs, "leader-thread")
	for index := 0; index < agentCount; index++ {
		agentID := fmt.Sprintf("agent-%02d", index)
		threadID := fmt.Sprintf("thread-%02d", index)
		entry := engine.RuntimeThreadCatalogEntry{
			ThreadID: threadID, AgentID: agentID, Name: agentID,
			Status: engine.RuntimeThreadRunning, Mode: engine.ThreadModeLiveAttach,
			StartedAt: base.Add(time.Duration(index+1) * time.Second),
		}
		if index == 17 {
			entry.Status = engine.RuntimeThreadWaitingInput
			entry.PermissionCount = 1
		}
		catalog.Threads = append(catalog.Threads, entry)
		messages := make([]engine.AgentDetailMessage, 0, max(1, messageCount))
		boundedMessageCount := max(1, messageCount)
		usableMessageIndex := max(0, boundedMessageCount-2)
		for messageIndex := 0; messageIndex < boundedMessageCount; messageIndex++ {
			content := fmt.Sprintf("bounded transcript %03d", messageIndex)
			if index == 17 && messageIndex == usableMessageIndex {
				content = "usable transcript agent-17"
			}
			messages = append(messages, engine.AgentDetailMessage{
				ID: fmt.Sprintf("message-%03d", messageIndex), Role: "assistant", Content: content, Completed: true,
			})
		}
		details[agentID] = engine.AgentDetailSnapshot{
			Revision: 1,
			Agent:    engine.RuntimeAgentSnapshot{AgentID: agentID, ThreadID: threadID, Name: agentID, Status: "running"},
			Thread:   engine.RuntimeThreadSnapshot{ThreadID: threadID, AgentID: agentID, Status: engine.RuntimeThreadRunning},
			Messages: messages,
		}
		threadIDs = append(threadIDs, threadID)
	}
	app.threadCatalogProvider = func() engine.RuntimeThreadCatalogSnapshot { return catalog }
	app.agentDetailProvider = func(agentID string) (engine.AgentDetailSnapshot, bool) {
		detail, ok := details[agentID]
		return detail, ok
	}
	for _, threadID := range threadIDs[1:] {
		if err := app.activateThreadByID(threadID); err != nil {
			panic(err)
		}
	}
	if err := app.activateThreadByID("leader-thread"); err != nil {
		panic(err)
	}
	return app, threadIDs
}

func performanceStreamApp() *App {
	app := newTestApp(120, 40)
	app.reducedMotion = true
	app.queryID = 1
	return app
}

func performanceStreamEvents(count int) []engine.QueryEvent {
	events := make([]engine.QueryEvent, count)
	for index := range events {
		events[index] = engine.QueryEvent{
			Type: engine.EventStream,
			StreamEvent: &schema.Message{
				Role: schema.Assistant, Content: fmt.Sprintf("stream delta %02d ", index),
			},
		}
	}
	return events
}

func assertPerformanceP95(t *testing.T, name string, budget time.Duration, samples int, operation func()) {
	t.Helper()
	durations := make([]time.Duration, samples)
	for index := range durations {
		started := time.Now()
		operation()
		durations[index] = time.Since(started)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95 := durations[(len(durations)*95-1)/100]
	if p95 > budget {
		t.Fatalf("%s p95 = %s, budget %s", name, p95, budget)
	}
	t.Logf("%s p95=%s budget=%s", name, p95, budget)
}
