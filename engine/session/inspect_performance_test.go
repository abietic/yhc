package session

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/transcript"
)

func BenchmarkInspectRecent10KMessages(b *testing.B) {
	info := performanceSessionInfo(b, 10_000)
	b.ReportMetric(10_000, "messages")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := InspectRecent(info, 4); err != nil {
			b.Fatal(err)
		}
	}
}

func TestInspectRecent10KPerformanceBudget(t *testing.T) {
	info := performanceSessionInfo(t, 10_000)
	durations := make([]time.Duration, 20)
	for index := range durations {
		started := time.Now()
		result, err := InspectRecent(info, 4)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Messages) != 4 {
			t.Fatalf("recent messages = %d, want 4", len(result.Messages))
		}
		durations[index] = time.Since(started)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95 := durations[(len(durations)*95-1)/100]
	if p95 > 500*time.Millisecond {
		t.Fatalf("10K disk-backed recent transcript p95 = %s, budget 500ms", p95)
	}
	t.Logf("10K disk-backed recent transcript p95=%s budget=500ms", p95)
}

type sessionPerformanceTB interface {
	Helper()
	TempDir() string
	Fatal(args ...any)
}

func performanceSessionInfo(tb sessionPerformanceTB, messages int) SessionInfo {
	tb.Helper()
	directory := tb.TempDir()
	recorder := transcript.NewRecorder("performance-session", directory)
	rows := make([]*schema.Message, messages)
	for index := range rows {
		role := schema.User
		if index%2 == 1 {
			role = schema.Assistant
		}
		rows[index] = &schema.Message{Role: role, Content: fmt.Sprintf("transcript message %05d", index)}
	}
	if err := recorder.Record(rows, false); err != nil {
		tb.Fatal(err)
	}
	if err := recorder.Flush(); err != nil {
		tb.Fatal(err)
	}
	return SessionInfo{
		SessionID: "performance-session", TranscriptDir: directory, TranscriptPath: recorder.Path(),
	}
}
