package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

func checkpointDirectory(t *testing.T) string {
	t.Helper()
	if !runUsageCheckpointStoreSupported() {
		t.Skip("private checkpoint durability requires Linux or Darwin")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "invocation")
}

func readCheckpoint(t *testing.T, directory string, r *RunUsage) RunUsageCheckpoint {
	t.Helper()
	value, err := ReadRunUsageCheckpointDir(directory, r.Snapshot().CallLedger.SegmentID)
	if err != nil {
		t.Fatal(err)
	}
	if value.CanResumeAgent || value.NewSpendingAuthorized {
		t.Fatal("accounting inspection granted execution or spending")
	}
	return value
}

func TestRunUsageCheckpointAdmissionTerminalAndClose(t *testing.T) {
	directory := checkpointDirectory(t)
	r, d := reservationFixture(t, "1")
	if r.Snapshot().Checkpoint != nil {
		t.Fatal("persistence enabled by default")
	}
	if err := r.EnableCheckpointDir(directory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	call, err := r.AdmitProviderUsage(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	pending := readCheckpoint(t, directory, r)
	if pending.Sequence != 2 || pending.Phase != "admitted" || pending.Snapshot.InFlight != 1 ||
		pending.Snapshot.Cost.Reservation.OutstandingAmount != "0.000080000000" || pending.AccountingComplete {
		t.Fatalf("missing durable pre-dispatch reservation: %+v", pending)
	}
	if err := call.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 5, CompletionTokens: 5}); err != nil {
		t.Fatal(err)
	}
	settled := readCheckpoint(t, directory, r)
	if settled.Sequence != 3 || !settled.TokenUsageComplete || !settled.CostComplete || settled.AccountingComplete ||
		settled.Snapshot.TotalTokens != 10 || settled.Snapshot.Cost.KnownEstimatedAmount != "0.000030000000" {
		t.Fatalf("wrong durable terminal: %+v", settled)
	}
	_ = call.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 99})
	if readCheckpoint(t, directory, r).Sequence != settled.Sequence {
		t.Fatal("duplicate terminal receipt was persisted again")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	closed := readCheckpoint(t, directory, r)
	if !closed.AdmissionClosed || !closed.AccountingComplete || closed.Snapshot.Cost.PriceCard.Currency != "CNY" {
		t.Fatalf("closed accounting incomplete: %+v", closed)
	}
	other, _ := NewRunUsage(RunUsageLimits{})
	if err := other.EnableCheckpointDir(directory); err == nil {
		t.Fatal("old invocation was reopened for writing")
	}
	if _, err := other.AdmitProviderUsage(t.Context(), d); !errors.Is(err, ErrRunUsageUnknown) {
		t.Fatalf("failed checkpoint setup silently fell back to memory: %v", err)
	}
}

type failingCheckpointStore struct {
	inner runUsageCheckpointStore
	fail  bool
}

func (s *failingCheckpointStore) WriteCheckpoint(value []byte) error {
	if s.fail {
		return errors.New("injected checkpoint failure")
	}
	return s.inner.WriteCheckpoint(value)
}

func (s *failingCheckpointStore) Close() error { return s.inner.Close() }

func TestRunUsageCheckpointFailureFencesDispatchAndPreservesFacts(t *testing.T) {
	for _, phase := range []string{"admission", "terminal", "seal"} {
		t.Run(phase, func(t *testing.T) {
			directory := checkpointDirectory(t)
			r, d := reservationFixture(t, "1")
			if err := r.EnableCheckpointDir(directory); err != nil {
				t.Fatal(err)
			}
			store := &failingCheckpointStore{inner: r.checkpoint.store, fail: phase == "admission"}
			r.checkpoint.store = store
			t.Cleanup(func() { _ = r.Close() })
			call, err := r.AdmitProviderUsage(t.Context(), d)
			if phase == "admission" {
				if call != nil || !errors.Is(err, ErrRunUsageUnknown) {
					t.Fatalf("unpersisted admission escaped: %v", err)
				}
				if s := r.Snapshot(); s.ProviderCalls != 0 || s.ReleasedCalls != 1 || s.Cost.Reservation.OutstandingAmount != "0.000000000000" {
					t.Fatalf("non-dispatched admission was counted or retained: %+v", s)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				store.fail = phase == "terminal"
				err = call.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 5, CompletionTokens: 5})
				if phase == "terminal" && !errors.Is(err, ErrRunUsageUnknown) {
					t.Fatalf("terminal persistence failure ignored: %v", err)
				}
				if s := r.Snapshot(); s.KnownCalls != 1 || s.TotalTokens != 10 || s.Cost.KnownEstimatedAmount != "0.000030000000" {
					t.Fatalf("known vendor facts discarded: %+v", s)
				}
				if phase == "seal" {
					store.fail = true
					r.Seal()
				}
			}
			s := r.Snapshot()
			if !s.Checkpoint.Failed || s.StopReason != "run_usage_unknown" {
				t.Fatalf("persistence failure not observable: %+v", s)
			}
			if _, err := r.AdmitProviderUsage(t.Context(), d); !errors.Is(err, ErrRunUsageUnknown) {
				t.Fatalf("persistence failure permitted another call: %v", err)
			}
			retained := readCheckpoint(t, directory, r)
			if retained.AccountingComplete {
				t.Fatal("unclosed old checkpoint claimed complete accounting")
			}
			if phase == "terminal" && (retained.Snapshot.InFlight != 1 || retained.Snapshot.Cost.Reservation.OutstandingAmount != "0.000080000000") {
				t.Fatal("durable unresolved reservation lost")
			}
		})
	}
}

func TestRunUsageCheckpointUnknownStrictWithoutBudgetAndNoContent(t *testing.T) {
	directory := checkpointDirectory(t)
	r, _ := NewRunUsage(RunUsageLimits{})
	if err := r.EnableCheckpointDir(directory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	call, err := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{Model: "sensitive label with spaces", Provider: "agenticdeepseek", QuerySource: "sensitive-source"})
	if err != nil {
		t.Fatal(err)
	}
	if err := call.MarkProviderUsageAmbiguous(errors.New("private provider response")); !errors.Is(err, ErrRunUsageUnknown) {
		t.Fatalf("checkpoint mode accepted unknown usage: %v", err)
	}
	if _, err := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{}); !errors.Is(err, ErrRunUsageUnknown) {
		t.Fatal("unknown usage allowed another admission without a budget")
	}
	_ = r.Close()
	retained := readCheckpoint(t, directory, r)
	if retained.TokenUsageComplete || retained.AccountingComplete {
		t.Fatal("unknown usage became complete")
	}
	data, _ := os.ReadFile(filepath.Join(directory, "checkpoint.json"))
	if bytes.Contains(data, []byte("sensitive")) || bytes.Contains(data, []byte("private provider")) {
		t.Fatal("checkpoint included arbitrary labels or provider text")
	}
}

func TestRunUsageCheckpointConcurrentSettlement(t *testing.T) {
	directory := checkpointDirectory(t)
	r, d := reservationFixture(t, "1")
	if err := r.EnableCheckpointDir(directory); err != nil {
		t.Fatal(err)
	}
	var calls []ProviderUsageCall
	for range 4 {
		call, err := r.AdmitProviderUsage(t.Context(), d)
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	var wg sync.WaitGroup
	for _, call := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := call.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 5, CompletionTokens: 5}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	value := readCheckpoint(t, directory, r)
	if !value.AccountingComplete || value.Snapshot.ProviderCalls != 4 || value.Snapshot.TotalTokens != 40 || value.Sequence != 10 {
		t.Fatalf("concurrent receipts lost or repeated: %+v", value)
	}
}

func TestRunUsageCheckpointCloseWithInFlightCannotBecomeComplete(t *testing.T) {
	for range 4 {
		directory := checkpointDirectory(t)
		r, d := reservationFixture(t, "1")
		if err := r.EnableCheckpointDir(directory); err != nil {
			t.Fatal(err)
		}
		call, err := r.AdmitProviderUsage(t.Context(), d)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _ = r.Close() }()
		go func() {
			defer wg.Done()
			<-start
			_ = call.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 5, CompletionTokens: 5})
		}()
		close(start)
		wg.Wait()
		value := readCheckpoint(t, directory, r)
		if value.AccountingComplete {
			if value.Snapshot.TotalTokens != 10 || value.Snapshot.InFlight != 0 {
				t.Fatal("complete checkpoint lost settled facts")
			}
		} else if value.Snapshot.InFlight != 1 || value.Snapshot.Cost.Reservation.OutstandingAmount != "0.000080000000" {
			t.Fatal("closed with pending call but lost reservation")
		}
		if _, err := r.AdmitProviderUsage(t.Context(), d); err == nil {
			t.Fatal("closed collector admitted spending")
		}
	}
}

func TestRunUsageCheckpointRejectsRehashedCounterAndPriceCorruption(t *testing.T) {
	for _, field := range []string{"tokens", "price", "ids", "metadata", "version", "unknown-field"} {
		t.Run(field, func(t *testing.T) {
			directory := checkpointDirectory(t)
			r, d := reservationFixture(t, "1")
			if err := r.EnableCheckpointDir(directory); err != nil {
				t.Fatal(err)
			}
			call, _ := r.AdmitProviderUsage(t.Context(), d)
			if err := call.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 5, CompletionTokens: 5}); err != nil {
				t.Fatal(err)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "checkpoint.json")
			raw, _ := os.ReadFile(path)
			var envelope runUsageCheckpointEnvelope
			_ = json.Unmarshal(raw, &envelope)
			var payload runUsageCheckpointPayload
			_ = json.Unmarshal(envelope.Data, &payload)
			switch field {
			case "tokens":
				payload.Snapshot.TotalTokens++
			case "price":
				amount := "0.000020000000"
				payload.Snapshot.Cost.KnownEstimatedAmount = amount
				payload.Snapshot.CallLedger.Records[0].Cost.EstimatedAmount = &amount
			case "ids":
				payload.Snapshot.CallLedger.Records[0].CallID = "invalid"
			case "metadata":
				payload.Snapshot.Limits.MaxEstimatedCost = "2"
			case "version":
				envelope.Version = 2
			}
			envelope.Data, _ = json.Marshal(payload)
			if field == "unknown-field" {
				envelope.Data = append(envelope.Data[:len(envelope.Data)-1], []byte(`,"extra":true}`)...)
			}
			envelope.SHA256 = checkpointSHA(envelope.Data)
			raw, _ = json.Marshal(envelope)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadRunUsageCheckpointDir(directory, r.Snapshot().CallLedger.SegmentID); err == nil {
				t.Fatal("corrupt accounting accepted")
			}
		})
	}
}

func TestRunUsageCheckpointSurvivesProcessExit(t *testing.T) {
	for _, state := range []string{"admitted", "settled"} {
		t.Run(state, func(t *testing.T) {
			directory := checkpointDirectory(t)
			command := exec.Command(os.Args[0], "-test.run=^TestRunUsageCheckpointProcessHelper$")
			command.Env = append(os.Environ(), "YHC_CHECKPOINT_TEST_STATE="+state, "YHC_CHECKPOINT_TEST_DIRECTORY="+directory)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("fixture process: %v %s", err, output)
			}
			segment := strings.TrimSpace(string(output))
			value, err := ReadRunUsageCheckpointDir(directory, segment)
			if err != nil {
				t.Fatal(err)
			}
			if value.AdmissionClosed || value.AccountingComplete || value.CanResumeAgent || value.NewSpendingAuthorized {
				t.Fatal("process exit granted continuation")
			}
			if state == "admitted" && (value.Snapshot.InFlight != 1 || value.Snapshot.Cost.Reservation.OutstandingAmount != "0.000080000000") {
				t.Fatal("pending state lost on process exit")
			}
			if state == "settled" && (value.Snapshot.TotalTokens != 10 || value.Snapshot.Cost.KnownEstimatedAmount != "0.000030000000") {
				t.Fatal("known terminal lost on process exit")
			}
		})
	}
}

func TestRunUsageCheckpointProcessHelper(t *testing.T) {
	state := os.Getenv("YHC_CHECKPOINT_TEST_STATE")
	if state == "" {
		return
	}
	r, d := reservationFixture(t, "1")
	if err := r.EnableCheckpointDir(os.Getenv("YHC_CHECKPOINT_TEST_DIRECTORY")); err != nil {
		t.Fatal(err)
	}
	call, err := r.AdmitProviderUsage(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if state == "settled" {
		if err := call.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 5, CompletionTokens: 5}); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println(r.Snapshot().CallLedger.SegmentID)
	os.Exit(0) // Deliberately omit Seal/Close, modelling abrupt process loss.
}

func TestRunUsageCheckpointReaderRequiresExpectedIdentity(t *testing.T) {
	directory := checkpointDirectory(t)
	r, _ := NewRunUsage(RunUsageLimits{})
	if err := r.EnableCheckpointDir(directory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if _, err := ReadRunUsageCheckpointDir(directory, uuid.NewString()); err == nil {
		t.Fatal("wrong invocation identity accepted")
	}
}
