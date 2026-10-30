package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

func TestColdToolDispatchClaimHasOneWinnerAndCannotBeReopened(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("private claim persistence requires Linux or Darwin")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proof := ColdToolStateProof{Version: 1, SourceSegmentID: uuid.NewString(), NewSegmentID: uuid.NewString()}
	var wins atomic.Int32
	var wait sync.WaitGroup
	for range 8 {
		wait.Go(func() {
			if claimColdToolDispatch(root, proof) == nil {
				wins.Add(1)
			}
		})
	}
	wait.Wait()
	if wins.Load() != 1 {
		t.Fatalf("claim winners=%d", wins.Load())
	}
	path := filepath.Join(root, ".yhc", "cold-dispatch-claims", proof.SourceSegmentID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	proof.NewSegmentID = uuid.NewString()
	if claimColdToolDispatch(root, proof) == nil {
		t.Fatal("different new ledger reopened source claim")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("consumed claim changed")
	}
}
