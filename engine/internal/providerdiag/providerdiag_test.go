package providerdiag

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSnapshotIsBoundedAndDetached(t *testing.T) {
	o := New(nil)
	o.Headers(503, HashIdentifier("private-request-id"))
	o.Transport("body")
	o.Event(7)
	o.Body(12)
	o.Terminal(HashIdentifier("private-response-id"), true)
	o.Terminate("completed")
	s := o.Snapshot()
	if s.HTTPStatus != 503 || s.BodyBytes != 12 || s.StreamEventCount != 1 || s.LastValidatedSequence == nil || *s.LastValidatedSequence != 7 || !s.ValidatedTerminalEvent || !s.AuthoritativeUsage || s.RequestIDHash == "private-request-id" || s.ResponseIDHash == "private-response-id" {
		t.Fatalf("snapshot=%+v", s)
	}
	*s.LastValidatedSequence = 99
	if got := *o.Snapshot().LastValidatedSequence; got != 7 {
		t.Fatalf("snapshot aliases observer: %d", got)
	}
	if FromContext(WithObserver(context.Background(), o)) != o {
		t.Fatal("context lost observer")
	}
	forged := New(nil)
	forged.Headers(200, "private-unhashed-request")
	forged.ResponseID("private-unhashed-response")
	forged.Terminal("private-unhashed-terminal", false)
	raw, _ := json.Marshal(forged.Snapshot())
	if strings.Contains(string(raw), "private-unhashed") || forged.Snapshot().RequestIDHash != "" || forged.Snapshot().ResponseIDHash != "" {
		t.Fatal("unvalidated hash retained")
	}
}
