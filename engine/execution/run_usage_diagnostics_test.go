package execution

import (
	"sync"
	"testing"

	"github.com/abietic/yhc/engine/internal/providerdiag"
	"github.com/cloudwego/eino/schema"
)

func TestRunUsageDiagnosticsConcurrentObservationAndSettlement(t *testing.T) {
	r, _ := NewRunUsage(RunUsageLimits{})
	call, _ := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{})
	o := providerdiag.FromContext(withProviderUsageResponseObserver(t.Context(), call))
	var observers sync.WaitGroup
	observers.Go(func() {
		for range 1000 {
			o.Body(1)
		}
	})
	observers.Go(func() {
		for range 1000 {
			snapshot := r.Snapshot()
			if diagnostic := snapshot.CallLedger.Records[0].Diagnostics; diagnostic != nil {
				diagnostic.BodyBytes = 999999 // detached snapshots cannot change the sink.
			}
		}
	})
	observers.Wait()
	o.Event(42)
	o.Terminal(providerdiag.HashIdentifier("receipt"), true)
	o.Terminate("completed")
	if err := call.CompleteProviderUsage(&schema.TokenUsage{}); err != nil {
		t.Fatal(err)
	}
	observers.Go(func() { o.Event(100) })
	observers.Go(func() { o.Body(1000) })
	observers.Wait()
	s := r.Snapshot().CallLedger.Records[0]
	if s.Diagnostics.BodyBytes != 1000 || *s.Diagnostics.LastValidatedSequence != 42 || s.State != "known" {
		t.Fatalf("settled diagnostics changed: %+v", s)
	}
}

func TestRunUsageDiagnosticsSettleOnceAndSnapshotIsolation(t *testing.T) {
	r, _ := NewRunUsage(RunUsageLimits{})
	call, _ := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{})
	ctx := withProviderUsageResponseObserver(t.Context(), call)
	o := providerdiag.FromContext(ctx)
	o.Headers(429, providerdiag.HashIdentifier("request-private"))
	o.Event(4)
	o.Terminal(providerdiag.HashIdentifier("response-private"), true)
	o.Terminate("completed")
	if err := call.CompleteProviderUsage(&schema.TokenUsage{}); err != nil {
		t.Fatal(err)
	}
	o.Event(5) // late producer observation must not alter the settled record.
	record := r.Snapshot().CallLedger.Records[0]
	if record.Diagnostics == nil || record.Diagnostics.HTTPStatus != 429 || record.Diagnostics.LastValidatedSequence == nil || *record.Diagnostics.LastValidatedSequence != 4 {
		t.Fatalf("record=%+v", record)
	}
	*record.Diagnostics.LastValidatedSequence = 99
	if got := *r.Snapshot().CallLedger.Records[0].Diagnostics.LastValidatedSequence; got != 4 {
		t.Fatalf("diagnostic aliases snapshot: %d", got)
	}
}

func TestRunUsageDiagnosticsCombinedAndUnsupported(t *testing.T) {
	r, _ := NewRunUsage(RunUsageLimits{})
	strict := &p242bUsageReporter{}
	call, err := CombineProviderUsage(r, strict).AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{})
	if err != nil {
		t.Fatal(err)
	}
	if observer := providerdiag.FromContext(withProviderUsageResponseObserver(t.Context(), call)); observer == nil {
		t.Fatal("combined owner did not forward diagnostics")
	} else {
		observer.Headers(502, providerdiag.HashIdentifier("request"))
	}
	if err := call.CompleteProviderUsage(&schema.TokenUsage{}); err != nil {
		t.Fatal(err)
	}
	if got := r.Snapshot().CallLedger.Records[0].Diagnostics; got == nil || got.HTTPStatus != 502 {
		t.Fatalf("combined diagnostic=%+v", got)
	}
	unsupported := countingProviderUsageCall{}
	parent := providerdiag.WithObserver(t.Context(), providerdiag.New(nil))
	if got := providerdiag.FromContext(withProviderUsageResponseObserver(parent, unsupported)); got != nil {
		t.Fatal("unsupported provider fabricated diagnostics observer")
	}
	if got := providerdiag.FromContext(withProviderUsageResponseObserver(parent, nil)); got != nil {
		t.Fatal("untracked helper inherited an enclosing call diagnostic")
	}
	unused, _ := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{})
	_ = withProviderUsageResponseObserver(t.Context(), unused)
	_ = unused.CompleteProviderUsage(&schema.TokenUsage{})
	if r.Snapshot().CallLedger.Records[1].Diagnostics != nil {
		t.Fatal("unbridged provider fabricated an empty diagnostic")
	}
}
