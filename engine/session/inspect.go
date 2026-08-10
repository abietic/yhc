package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/abietic/yhc/engine/transcript"
)

// InspectRecent returns a bounded recent-message projection without mutating
// engine state.
func InspectRecent(info SessionInfo, limit int) (*transcript.RecentResult, error) {
	path, err := resolveSessionInfoPath(info)
	if err != nil {
		return nil, err
	}
	return transcript.LoadRecent(path, limit, 0)
}

// InspectFull loads a selected transcript for read-only overlay rendering.
func InspectFull(info SessionInfo) (*transcript.LoadResult, error) {
	path, err := resolveSessionInfoPath(info)
	if err != nil {
		return nil, err
	}
	recorder := transcript.NewRecorder(info.SessionID, filepath.Dir(path))
	return recorder.LoadFull()
}

func resolveSessionInfoPath(info SessionInfo) (string, error) {
	if strings.TrimSpace(info.SessionID) == "" {
		return "", errors.New("session ID is required")
	}
	path := info.TranscriptPath
	if path == "" && info.TranscriptDir != "" {
		path = transcript.NewRecorder(info.SessionID, info.TranscriptDir).Path()
	}
	if path == "" {
		return "", errors.New("session transcript source is required")
	}
	path = filepath.Clean(path)
	if strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) != info.SessionID || filepath.Ext(path) != ".jsonl" {
		return "", fmt.Errorf("session transcript path does not match %s", info.SessionID)
	}
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}
