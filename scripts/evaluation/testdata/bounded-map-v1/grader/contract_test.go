package fixture

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

func signalled(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestBatchConcurrentProgressAndOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release, first, second, queued, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
		var got []int
		var err error
		go func() {
			got, err = Map(context.Background(), []int{0, 1, 2}, 2, func(_ context.Context, value int) (int, error) {
				switch value {
				case 0:
					close(first)
					<-release
				case 1:
					close(second)
				case 2:
					close(queued)
				}
				return value + 10, nil
			})
			close(done)
		}()
		synctest.Wait()
		if !signalled(first) || !signalled(second) || !signalled(queued) {
			t.Error("independent and queued callbacks must progress while first is blocked")
		}
		if signalled(done) {
			t.Error("success returned before all callbacks finished")
		}
		close(release)
		synctest.Wait()
		if !signalled(done) || err != nil || !slices.Equal(got, []int{10, 11, 12}) {
			t.Errorf("ordered result = %v, %v; done=%v", got, err, signalled(done))
		}
	})
}

func TestBatchLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release, done := make(chan struct{}), make(chan struct{})
		var active, called atomic.Int32
		var visits [6]atomic.Int32
		go func() {
			_, err := Map(context.Background(), []int{0, 1, 2, 3, 4, 5}, 2, func(_ context.Context, value int) (int, error) {
				active.Add(1)
				called.Add(1)
				visits[value].Add(1)
				<-release
				active.Add(-1)
				return value, nil
			})
			if err != nil {
				t.Error(err)
			}
			close(done)
		}()
		synctest.Wait()
		if active.Load() != 2 {
			t.Errorf("blocked active callbacks=%d, want exactly 2", active.Load())
		}
		close(release)
		synctest.Wait()
		if !signalled(done) || called.Load() != 6 || active.Load() != 0 {
			t.Error("return did not join every input exactly once")
		}
		for i := range visits {
			if visits[i].Load() != 1 {
				t.Errorf("input %d invoked %d times", i, visits[i].Load())
			}
		}
	})
}

func TestBatchErrorCancelsAndJoins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		defer cancel()
		entered, cancelled, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
		want := errors.New("callback failure")
		var got error
		go func() {
			_, got = Map(parent, []int{0, 1}, 2, func(ctx context.Context, value int) (int, error) {
				if value == 0 {
					select {
					case <-entered:
					case <-ctx.Done():
					}
					return 0, want
				}
				close(entered)
				<-ctx.Done()
				close(cancelled)
				<-release
				return 0, ctx.Err()
			})
			close(done)
		}()
		synctest.Wait()
		if !signalled(cancelled) {
			t.Error("first callback error did not cancel other started callbacks")
		}
		if signalled(done) {
			t.Error("error return failed to join started callbacks")
		}
		// Cleanup also unblocks a no-cancel or sequential mutant without crediting
		// it for cancellation. Assertions above precede caller cancellation.
		cancel()
		close(release)
		synctest.Wait()
		if !signalled(done) || !errors.Is(got, want) {
			t.Errorf("error return = %v, done=%v", got, signalled(done))
		}
	})
}

func TestBatchPreCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var called atomic.Int32
	_, err := Map(ctx, []int{1, 2}, 2, func(context.Context, int) (int, error) {
		called.Add(1)
		return 0, nil
	})
	if !errors.Is(err, context.Canceled) || called.Load() != 0 {
		t.Fatalf("pre-cancelled call: callbacks=%d err=%v", called.Load(), err)
	}
}

func TestBatchEmptyAndInvalidLimit(t *testing.T) {
	work := func(context.Context, int) (int, error) { t.Error("unexpected callback"); return 0, nil }
	if got, err := Map(context.Background(), nil, 2, work); err != nil || len(got) != 0 {
		t.Errorf("empty call = %v, %v", got, err)
	}
	if _, err := Map(context.Background(), []int{1}, 0, work); err == nil {
		t.Error("non-positive limit succeeded")
	}
}

func TestBatchInputPreserved(t *testing.T) {
	input := []int{3, 1, 2}
	_, err := Map(context.Background(), input, 2, func(_ context.Context, value int) (int, error) {
		return value * 2, nil
	})
	if err != nil || !slices.Equal(input, []int{3, 1, 2}) {
		t.Fatalf("caller input changed: %v, err=%v", input, err)
	}
}
