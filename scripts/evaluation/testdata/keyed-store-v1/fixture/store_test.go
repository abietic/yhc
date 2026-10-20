package fixture

import (
	"bytes"
	"testing"
)

func TestStoredValue(t *testing.T) {
	store := NewStore()
	called := false
	got := store.Update("one", func(previous []byte) []byte {
		called = true
		if previous != nil {
			t.Errorf("initial input = %v, want nil", previous)
		}
		return []byte("saved")
	})
	if !called || !bytes.Equal(got, []byte("saved")) || !bytes.Equal(store.Get("one"), []byte("saved")) {
		t.Fatal("update did not finish and save its result")
	}
	if store.Get("missing") != nil {
		t.Fatal("missing key should return nil")
	}
}
