package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abietic/yhc/engine/internal/mediastore"
	"github.com/abietic/yhc/engine/transcript"
)

// SessionMediaCollection reports one active-owner manual collection.
type SessionMediaCollection struct {
	ManifestEntriesRemoved int
	BlobsRemoved           int
	BytesRemoved           int64
}

func (e *QueryEngine) beginMediaLifecycleWrite() error {
	if e == nil {
		return errors.New("session media lifecycle is unavailable")
	}
	e.mediaLifecycleMu.RLock()
	if e.mediaLifecycleClosed {
		e.mediaLifecycleMu.RUnlock()
		return errors.New("session media lifecycle is closed")
	}
	return nil
}

func (e *QueryEngine) endMediaLifecycleWrite() {
	if e != nil {
		e.mediaLifecycleMu.RUnlock()
	}
}

// HasPrivateSessionMedia reports whether the active saved Session has any
// ref-backed transcript or runtime-input reachability. It waits for an
// in-flight durable prompt publication to commit or roll back before taking
// the projection.
func (e *QueryEngine) HasPrivateSessionMedia() (bool, error) {
	if e == nil {
		return false, errors.New("session media inspection is unavailable")
	}
	e.mediaLifecycleMu.Lock()
	defer e.mediaLifecycleMu.Unlock()
	if e.mediaLifecycleClosed || e.administrationOnly {
		return false, errors.New(
			"session media inspection requires an active owner",
		)
	}

	e.mu.Lock()
	recorder := e.transcript
	e.mu.Unlock()
	if recorder != nil && recorder.Path() != "" {
		projection, err := recorder.LoadRefProjection()
		if err != nil {
			return false, fmt.Errorf("inspect transcript media: %w", err)
		}
		if projection.HasMediaRefs {
			return true, nil
		}
	}

	coordinator, _, err := e.runtimeInputOwner()
	if err != nil {
		return false, err
	}
	if coordinator == nil || !coordinator.Durable() {
		return false, nil
	}
	snapshot, err := coordinator.mediaReachabilitySnapshot()
	if err != nil {
		return false, err
	}
	return len(snapshot.refs) > 0, nil
}

// CollectSessionMedia removes only media proven unreachable while this exact
// live saved-Session owner is quiesced. It never runs automatically.
func (e *QueryEngine) CollectSessionMedia(
	ctx context.Context,
) (SessionMediaCollection, error) {
	if e == nil {
		return SessionMediaCollection{}, errors.New(
			"session media collection is unavailable",
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.mediaLifecycleMu.Lock()
	defer e.mediaLifecycleMu.Unlock()
	if e.mediaLifecycleClosed || e.administrationOnly {
		return SessionMediaCollection{}, errors.New(
			"session media collection requires an active owner",
		)
	}

	e.mu.Lock()
	recorder := e.transcript
	e.mu.Unlock()
	if recorder == nil || recorder.Path() == "" {
		return SessionMediaCollection{}, errors.New(
			"session media collection requires a saved transcript",
		)
	}
	coordinator, _, err := e.runtimeInputOwner()
	if err != nil {
		return SessionMediaCollection{}, err
	}
	if coordinator == nil || !coordinator.Durable() ||
		coordinator.mediaStore == nil {
		return SessionMediaCollection{}, errors.New(
			"session media collection requires a durable input owner",
		)
	}
	mediaRoot := recorder.Path() + ".media"
	if filepath.Clean(coordinator.mediaStore.Root()) != filepath.Clean(mediaRoot) {
		return SessionMediaCollection{}, errors.New(
			"session media collection owner mismatch",
		)
	}

	transcriptInfo, projection, err := mediaTranscriptSnapshot(recorder)
	if err != nil {
		return SessionMediaCollection{}, err
	}
	runtimeSnapshot, err := coordinator.mediaReachabilitySnapshot()
	if err != nil {
		return SessionMediaCollection{}, err
	}
	liveRefs, err := mediaRefsForCollection(
		projection.AllPromptRecords,
		runtimeSnapshot.refs,
	)
	if err != nil {
		return SessionMediaCollection{}, err
	}

	if _, err := os.Lstat(mediaRoot); errors.Is(err, os.ErrNotExist) {
		if len(liveRefs) == 0 {
			return SessionMediaCollection{}, nil
		}
		return SessionMediaCollection{}, errors.New(
			"session media store is missing",
		)
	} else if err != nil {
		return SessionMediaCollection{}, errors.New(
			"session media store is unavailable",
		)
	}

	collected, err := coordinator.mediaStore.Collect(
		ctx,
		liveRefs,
		func() error {
			currentInfo, current, snapshotErr := mediaTranscriptSnapshot(recorder)
			if snapshotErr != nil {
				return snapshotErr
			}
			if !os.SameFile(transcriptInfo, currentInfo) ||
				current.Revision != projection.Revision {
				return errors.New("transcript revision changed")
			}
			return coordinator.revalidateMediaReachability(
				runtimeSnapshot.revision,
			)
		},
	)
	if err != nil {
		return SessionMediaCollection{}, err
	}
	return SessionMediaCollection{
		ManifestEntriesRemoved: collected.ManifestEntriesRemoved,
		BlobsRemoved:           collected.BlobsRemoved,
		BytesRemoved:           collected.BytesRemoved,
	}, nil
}

func mediaTranscriptSnapshot(
	recorder *transcript.Recorder,
) (os.FileInfo, *transcript.LoadResult, error) {
	if recorder == nil || recorder.Path() == "" {
		return nil, nil, errors.New("session transcript is unavailable")
	}
	before, err := os.Lstat(recorder.Path())
	if err != nil ||
		before.Mode()&os.ModeSymlink != 0 ||
		!before.Mode().IsRegular() {
		return nil, nil, errors.New(
			"session transcript is not a regular non-symlink file",
		)
	}
	projection, err := recorder.LoadRefProjection()
	if err != nil {
		return nil, nil, fmt.Errorf("load media reachability: %w", err)
	}
	after, err := os.Lstat(recorder.Path())
	if err != nil ||
		after.Mode()&os.ModeSymlink != 0 ||
		!after.Mode().IsRegular() ||
		!os.SameFile(before, after) {
		return nil, nil, errors.New(
			"session transcript changed while reading reachability",
		)
	}
	return after, projection, nil
}

func mediaRefsForCollection(
	transcriptRecords []transcript.PromptRecordBinding,
	runtimeRefs []mediastore.Ref,
) ([]mediastore.Ref, error) {
	refs := append([]mediastore.Ref(nil), runtimeRefs...)
	for _, binding := range transcriptRecords {
		recordRefs, err := binding.Record.MediaRefs()
		if err != nil {
			return nil, err
		}
		refs = append(refs, recordRefs...)
	}
	return refs, nil
}

type runtimeMediaReachabilitySnapshot struct {
	revision uint64
	refs     []mediastore.Ref
}

func (c *RuntimeInputCoordinator) mediaReachabilitySnapshot() (
	runtimeMediaReachabilitySnapshot,
	error,
) {
	if c == nil {
		return runtimeMediaReachabilitySnapshot{}, errors.New(
			"runtime media reachability is unavailable",
		)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot := runtimeMediaReachabilitySnapshot{
		revision: c.revision,
		refs:     make([]mediastore.Ref, 0),
	}
	for _, item := range c.items {
		if item.UserPrompt == nil || item.UserPrompt.durablePrompt == nil {
			continue
		}
		refs, err := item.UserPrompt.durablePrompt.MediaRefs()
		if err != nil {
			return runtimeMediaReachabilitySnapshot{}, err
		}
		snapshot.refs = append(snapshot.refs, refs...)
	}
	return snapshot, nil
}

func (c *RuntimeInputCoordinator) revalidateMediaReachability(
	revision uint64,
) error {
	if c == nil {
		return errors.New("runtime media reachability is unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.revision != revision {
		return errors.New("runtime input revision changed")
	}
	return nil
}
