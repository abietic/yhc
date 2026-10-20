package fixture

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

func waitFor(t *testing.T, done <-chan struct{}, observation string) bool {
	t.Helper()
	select {
	case <-done:
		return true
	case <-time.After(time.Second):
		t.Error(observation)
		return false
	}
}

func TestDifferentKeyProgress(t *testing.T) {
	store := NewStore()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	go func() {
		store.Update("held", func([]byte) []byte { close(entered); <-release; return []byte("held") })
		close(finished)
	}()
	if !waitFor(t, entered, "first transform did not start") {
		return
	}
	other := make(chan struct{})
	go func() {
		store.Update("other", func([]byte) []byte { return []byte("other") })
		if !bytes.Equal(store.Get("other"), []byte("other")) {
			t.Error("different key did not retain its update")
		}
		close(other)
	}()
	waitFor(t, other, "different key cannot update and read while first key is blocked")
	unblock()
	waitFor(t, finished, "first key did not finish after release")
	waitFor(t, other, "different key did not finish after release")
}

func TestSameKeyExclusionAndQueue(t *testing.T) {
	store := NewStore()
	entered, release, firstDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	go func() {
		store.Update("same", func([]byte) []byte { close(entered); <-release; return []byte("first") })
		close(firstDone)
	}()
	if !waitFor(t, entered, "first transform did not start") {
		return
	}
	secondAttempted, secondEntered, secondDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		close(secondAttempted)
		store.Update("same", func(previous []byte) []byte {
			close(secondEntered)
			if !bytes.Equal(previous, []byte("first")) {
				t.Error("queued transform did not observe previous commit")
			}
			return []byte("second")
		})
		close(secondDone)
	}()
	if !waitFor(t, secondAttempted, "queued update was not scheduled") {
		return
	}
	readAttempted, readDone := make(chan struct{}), make(chan struct{})
	go func() {
		close(readAttempted)
		store.Get("same")
		close(readDone)
	}()
	if !waitFor(t, readAttempted, "concurrent read was not scheduled") {
		return
	}
	select {
	case <-secondEntered:
		t.Error("same-key transforms overlapped")
	case <-time.After(200 * time.Millisecond):
	}
	otherDone := make(chan struct{})
	go func() {
		store.Update("other", func([]byte) []byte { return []byte("other") })
		if !bytes.Equal(store.Get("other"), []byte("other")) {
			t.Error("independent key did not retain its update")
		}
		close(otherDone)
	}()
	waitFor(t, otherDone, "same-key waiters blocked an independent key")
	unblock()
	waitFor(t, firstDone, "first transform did not finish")
	waitFor(t, secondDone, "queued transform did not finish after release")
	waitFor(t, readDone, "read did not finish after release")
	waitFor(t, otherDone, "independent key did not finish after release")
}

func TestTransformResultDetached(t *testing.T) {
	store := NewStore()
	input := []byte("value")
	store.Update("key", func([]byte) []byte { return input })
	input[0] = 'X'
	if !bytes.Equal(store.Get("key"), []byte("value")) {
		t.Fatal("store borrowed the transform result")
	}
}

func TestUpdateReturnDetached(t *testing.T) {
	store := NewStore()
	result := store.Update("key", func([]byte) []byte { return []byte("value") })
	result[0] = 'X'
	if !bytes.Equal(store.Get("key"), []byte("value")) {
		t.Fatal("update return aliases stored data")
	}
}

func TestGetReturnDetached(t *testing.T) {
	store := NewStore()
	store.Update("key", func([]byte) []byte { return []byte("value") })
	result := store.Get("key")
	result[0] = 'X'
	if !bytes.Equal(store.Get("key"), []byte("value")) {
		t.Fatal("get return aliases stored data")
	}
}

func TestPanicRollbackAndRelease(t *testing.T) {
	store := NewStore()
	store.Update("key", func([]byte) []byte { return []byte("before") })
	sentinel := new(int)
	func() {
		defer func() {
			if recover() != sentinel {
				t.Error("transform panic was not propagated")
			}
		}()
		store.Update("key", func(previous []byte) []byte { previous[0] = 'X'; panic(sentinel) })
	}()
	finished := make(chan struct{})
	go func() {
		if !bytes.Equal(store.Get("key"), []byte("before")) {
			t.Error("panicking transform changed stored value")
		}
		store.Update("key", func([]byte) []byte { return []byte("after") })
		if !bytes.Equal(store.Get("key"), []byte("after")) {
			t.Error("subsequent update was not committed")
		}
		close(finished)
	}()
	waitFor(t, finished, "panic did not release the key for subsequent operations")
}
