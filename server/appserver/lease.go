package appserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type sessionLease struct {
	file *os.File
	path string
}

// acquireSessionLease locks a server-admitted transcript directory. It never
// derives a state root from renderer-provided CWD, avoiding a second authority
// for the live-session lock path.
func acquireSessionLease(transcriptDir, sessionID, serverID string) (*sessionLease, error) {
	transcriptDir = strings.TrimSpace(transcriptDir)
	sessionID = strings.TrimSpace(sessionID)
	if transcriptDir == "" || sessionID == "" || filepath.Base(sessionID) != sessionID {
		return nil, fmt.Errorf("session lease identity is invalid")
	}
	lockDir := filepath.Join(transcriptDir, sessionID)
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, fmt.Errorf("create session lease directory: %w", err)
	}
	path := filepath.Join(lockDir, ".app-server.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open session lease: %w", err)
	}
	if err := lockSessionFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("session %s already has an active app-server owner: %w", sessionID, err)
	}

	record, err := json.Marshal(struct {
		PID        int       `json:"pid"`
		ServerID   string    `json:"server_id"`
		AcquiredAt time.Time `json:"acquired_at"`
	}{
		PID:        os.Getpid(),
		ServerID:   serverID,
		AcquiredAt: time.Now().UTC(),
	})
	if err != nil {
		_ = unlockSessionFile(file)
		_ = file.Close()
		return nil, fmt.Errorf("encode session lease: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		_ = unlockSessionFile(file)
		_ = file.Close()
		return nil, fmt.Errorf("truncate session lease: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		_ = unlockSessionFile(file)
		_ = file.Close()
		return nil, fmt.Errorf("rewind session lease: %w", err)
	}
	if _, err := file.Write(append(record, '\n')); err != nil {
		_ = unlockSessionFile(file)
		_ = file.Close()
		return nil, fmt.Errorf("write session lease: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = unlockSessionFile(file)
		_ = file.Close()
		return nil, fmt.Errorf("sync session lease: %w", err)
	}
	return &sessionLease{file: file, path: path}, nil
}

func (l *sessionLease) close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unlockSessionFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
