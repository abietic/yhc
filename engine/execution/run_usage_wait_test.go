package execution

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestRunCostReservationWaitsForKnownSettlement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, d := reservationFixture(t, "0.00012")
		first, err := r.AdmitProviderUsage(t.Context(), d)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan ProviderUsageCall, 1)
		go func() {
			next, err := r.AdmitProviderUsage(t.Context(), d)
			if err != nil {
				t.Error(err)
			}
			done <- next
		}()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("temporary reservation contention terminated admission")
		default:
		}
		if s := r.Snapshot(); s.ProviderCalls != 1 || s.DeniedCalls != 0 || s.StopReason != "" {
			t.Fatalf("waiting call changed accounting: %+v", s)
		}
		usage := &schema.TokenUsage{PromptTokens: 5, CompletionTokens: 5}
		if err := first.CompleteProviderUsage(usage); err != nil {
			t.Fatal(err)
		}
		next := <-done
		if next == nil {
			t.Fatal("settlement did not admit waiting call")
		}
		if err := next.CompleteProviderUsage(usage); err != nil {
			t.Fatal(err)
		}
		if s := r.Snapshot(); s.KnownCalls != 2 || s.DeniedCalls != 0 || s.Cost.Reservation.PeakExposureAmount != "0.000110000000" || s.Cost.KnownEstimatedAmount != "0.000060000000" {
			t.Fatalf("serialized calls exceeded or lost allowance: %+v", s)
		}
		if _, err := r.AdmitProviderUsage(t.Context(), d); !errors.Is(err, ErrRunBudgetExceeded) {
			t.Fatalf("exhausted allowance = %v", err)
		}
	})
}

func TestRunCostReservationWaiterStopsOrRechecks(t *testing.T) {
	for _, mode := range []string{"release", "exhaust", "cancel", "seal", "unknown", "untracked", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r, d := reservationFixture(t, "0.00010")
				first, err := r.AdmitProviderUsage(t.Context(), d)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				if mode == "deadline" {
					cancel()
					ctx, cancel = context.WithTimeout(t.Context(), time.Second)
				}
				defer cancel()
				done := make(chan error, 1)
				go func() {
					c, err := r.AdmitProviderUsage(ctx, d)
					if c != nil {
						err = c.ReleaseProviderUsageBeforeDispatch()
					}
					done <- err
				}()
				synctest.Wait()
				select {
				case <-done:
					t.Fatal("admission returned before settlement or cancellation")
				default:
				}
				var want error
				switch mode {
				case "release":
					err = first.ReleaseProviderUsageBeforeDispatch()
				case "exhaust":
					err = first.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 5, CompletionTokens: 5})
					want = ErrRunBudgetExceeded
				case "cancel":
					cancel()
					want = context.Canceled
				case "seal":
					r.Seal()
					want = context.Canceled
				case "unknown":
					err = first.MarkProviderUsageAmbiguous(errors.New("receipt lost"))
					if !errors.Is(err, ErrRunUsageUnknown) {
						t.Fatal(err)
					}
					err = nil
					want = ErrRunUsageUnknown
				case "deadline":
					time.Sleep(time.Second)
					want = context.DeadlineExceeded
				case "untracked":
					r.RecordUntrackedCall()
					want = ErrRunUsageUnknown
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := <-done; !errors.Is(err, want) {
					t.Fatalf("waiting admission = %v, want %v", err, want)
				}
				if mode == "unknown" && r.Snapshot().Cost.Reservation.RetainedAmount != "0.000080000000" {
					t.Fatal("unknown reservation was released")
				}
				_ = first.ReleaseProviderUsageBeforeDispatch()
			})
		})
	}
}

func TestRunCostReservationRechecksAfterEverySettlement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, d := reservationFixture(t, "0.00016")
		first, err := r.AdmitProviderUsage(t.Context(), d)
		if err != nil {
			t.Fatal(err)
		}
		second, err := r.AdmitProviderUsage(t.Context(), d)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan ProviderUsageCall, 1)
		go func() {
			third, err := r.AdmitProviderUsage(t.Context(), d)
			if err != nil {
				t.Error(err)
			}
			done <- third
		}()
		synctest.Wait()
		if err := first.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 5, CompletionTokens: 5}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("admitted before enough headroom was released")
		default:
		}
		if err := second.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 5, CompletionTokens: 5}); err != nil {
			t.Fatal(err)
		}
		third := <-done
		if third == nil {
			t.Fatal("second settlement did not wake admission")
		}
		if err := third.ReleaseProviderUsageBeforeDispatch(); err != nil {
			t.Fatal(err)
		}
		s := r.Snapshot()
		if s.ProviderCalls != 2 || s.DeniedCalls != 0 || s.Cost.Reservation.PeakExposureAmount != "0.000160000000" || s.Cost.KnownEstimatedAmount != "0.000060000000" {
			t.Fatalf("allowance changed: %+v", s)
		}
	})
}
