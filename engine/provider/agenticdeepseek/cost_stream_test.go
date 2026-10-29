package agenticdeepseek

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/abietic/yhc/engine/internal/providercost"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestCostPreparedStreamFreezesWireAndDisablesRedirectReplay(t *testing.T) {
	var calls, redirected atomic.Int32
	next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); w.WriteHeader(200) }))
	defer next.Close()
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Location", next.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	m, e := New(t.Context(), &Config{APIKey: "fixture-key", BaseURL: server.URL, Model: FlashModel})
	if e != nil {
		t.Fatal(e)
	}
	msg := schema.UserAgenticMessage("original")
	s, e := m.PrepareBudgetStream(t.Context(), []*schema.AgenticMessage{msg}, 8, model.WithMaxTokens(100))
	if e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 0 || s.CostBound().InputTokens != 1048576 || s.CostBound().OutputTokens != 8 {
		t.Fatalf("bound=%+v calls=%d", s.CostBound(), calls.Load())
	}
	*msg = *schema.UserAgenticMessage("mutated")
	m.httpClient.Transport = opaqueBudgetTransport{}
	if _, e = s.Stream(t.Context()); e == nil {
		t.Fatal("redirect unexpectedly succeeded")
	}
	if captured["max_output_tokens"] != float64(8) || captured["model"] != FlashModel {
		t.Fatalf("wire controls=%v", captured)
	}
	input := captured["input"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if input["text"] != "original" || redirected.Load() != 0 || calls.Load() != 1 {
		t.Fatalf("input=%v calls=%d redirected=%d", input, calls.Load(), redirected.Load())
	}
	if _, e = s.Stream(t.Context()); e == nil || calls.Load() != 1 {
		t.Fatal("prepared stream dispatched twice")
	}
}

type opaqueBudgetTransport struct{}

func (opaqueBudgetTransport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("opaque transport must not dispatch")
}

func TestCostPreparationRejectsUnknownModelsAndOpaqueRetries(t *testing.T) {
	for _, config := range []*Config{{APIKey: "fixture-key", Model: "acme/deepseek-flash"}, {APIKey: "fixture-key", Model: FlashModel, HTTPClient: &http.Client{Transport: opaqueBudgetTransport{}}}} {
		m, e := New(t.Context(), config)
		if e != nil {
			t.Fatal(e)
		}
		_, e = m.PrepareBudgetStream(t.Context(), []*schema.AgenticMessage{schema.UserAgenticMessage("fixture")}, 8)
		if !errors.Is(e, providercost.ErrUnsupported) {
			t.Fatalf("unsupported=%v", e)
		}
	}
}
