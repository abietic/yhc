package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type pageSummaryRecorder struct {
	webFetchGoalUsageModel
	inputs []string
}

func (m *pageSummaryRecorder) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.inputs = append(m.inputs, input[len(input)-1].Content)
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "summary"}}), nil
}

func TestWebFetchContinueExtractedPage(t *testing.T) {
	// Put a multibyte character across the summary boundary and the answer beyond the raw boundary.
	text := strings.Repeat("a", 49999) + "界" + strings.Repeat("b", 52401) + "section answer"
	var reads atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		fmt.Fprint(w, "<article>", text, "</article>")
	}))
	defer server.Close()
	transport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = transport })
	m := &pageSummaryRecorder{}
	ctx := WithWebFetchModel(t.Context(), m)
	tool := WebFetchTool()
	fetch := func(raw bool, offset int) (string, error) {
		input, err := json.Marshal(map[string]any{"url": server.URL, "prompt": "find the answer", "raw_mode": raw, "offset_bytes": offset})
		if err != nil {
			t.Fatal(err)
		}
		return tool.ExecuteCtx(ctx, string(input))
	}
	result, err := fetch(false, 0)
	if err != nil || !strings.Contains(result, "offset_bytes=49999") || !strings.Contains(result, "summary") || len(m.inputs) != 1 || strings.Contains(m.inputs[0], "界") {
		t.Fatalf("first summary: result=%q calls=%d err=%v", result, len(m.inputs), err)
	}
	result, err = fetch(false, 49999)
	if err != nil || !strings.Contains(result, "offset_bytes=99999") || len(m.inputs) != 2 || !strings.Contains(m.inputs[1], "界") {
		t.Fatalf("second summary: result=%q calls=%d err=%v", result, len(m.inputs), err)
	}
	result, err = fetch(true, 0)
	if err != nil || !strings.Contains(result, "offset_bytes=102400") || strings.Contains(result, "section answer") {
		t.Fatalf("raw page: bytes=%d err=%v", len(result), err)
	}
	result, err = fetch(true, 102400)
	if err != nil || result != text[102400:] {
		t.Fatalf("raw continuation: result=%q err=%v", result, err)
	}
	result, err = fetch(false, 102400)
	if err != nil || result != "summary" || len(m.inputs) != 3 || !strings.Contains(m.inputs[2], "section answer") {
		t.Fatalf("last summary: result=%q calls=%d err=%v", result, len(m.inputs), err)
	}
	for _, offset := range []int{-1, 50000, len(text) + 1} {
		if _, err := fetch(false, offset); err == nil {
			t.Fatalf("accepted invalid offset %d", offset)
		}
	}
	if len(m.inputs) != 3 || reads.Load() != 7 {
		t.Fatalf("model calls=%d page reads=%d", len(m.inputs), reads.Load())
	}
}
