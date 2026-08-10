package cron

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/abietic/yhc/internal/identity"
)

const schedulerLockName = "scheduler.lock"

// LockInfo contains information about who holds the scheduler lock.
type LockInfo struct {
	PID       int
	Timestamp int64
}

// TryAcquireSchedulerLock attempts to acquire the scheduler lock.
// Returns true if the lock was acquired, false if another process holds it.
func TryAcquireSchedulerLock(projectDir string) (bool, error) {
	store, _, err := openCanonicalCronStore(projectDir, true)
	if err != nil {
		return false, err
	}
	defer store.Close() //nolint:errcheck

	for attempts := 0; attempts < 16; attempts++ {
		data, existing, exists, readErr := readStoreRegular(store, schedulerLockName, 128)
		if readErr != nil {
			return false, errors.New("scheduler lock is unsafe")
		}
		if exists {
			info, parseErr := parseLock(data)
			if parseErr != nil {
				return false, errors.New("scheduler lock is unsafe")
			}
			if info.PID == os.Getpid() {
				return true, nil
			}
			if isProcessAlive(info.PID) {
				return false, nil
			}
			store.RemoveRegularIfSame(schedulerLockName, existing)
		}

		content := []byte(fmt.Sprintf("%d %d", os.Getpid(), time.Now().UnixMilli()))
		temporary, expected, stageErr := stageCanonicalRegular(store, ".scheduler-lock-", content)
		if stageErr != nil {
			return false, stageErr
		}
		collision, promoteErr := store.PromoteRegularFromNoReplace(
			store,
			temporary,
			schedulerLockName,
			expected,
		)
		if promoteErr != nil {
			store.RemoveRegularIfSame(temporary, expected)
			return false, errors.New("scheduler lock promotion failed")
		}
		if collision {
			store.RemoveRegularIfSame(temporary, expected)
			continue
		}
		return true, nil
	}
	return false, errors.New("scheduler lock acquisition did not converge")
}

// ReleaseSchedulerLock releases the scheduler lock.
func ReleaseSchedulerLock(projectDir string) {
	store, exists, err := openCanonicalCronStore(projectDir, false)
	if err != nil || !exists {
		return
	}
	defer store.Close() //nolint:errcheck
	data, opened, exists, err := readStoreRegular(store, schedulerLockName, 128)
	if err != nil || !exists {
		return
	}
	info, err := parseLock(data)
	if err == nil && info.PID == os.Getpid() {
		store.RemoveRegularIfSame(schedulerLockName, opened)
	}
}

// GetSchedulerLockPath returns the canonical scheduler lock path.
func GetSchedulerLockPath(projectDir string) string {
	return filepath.Join(projectDir, identity.ProjectDirName, schedulerLockName)
}

func parseLock(data []byte) (*LockInfo, error) {
	parts := strings.Fields(strings.TrimSpace(string(data)))
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid lock format")
	}

	pid, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid PID in lock: %w", err)
	}
	if pid <= 0 {
		return nil, fmt.Errorf("invalid PID in lock")
	}

	ts, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp in lock: %w", err)
	}
	if ts < 0 {
		return nil, fmt.Errorf("invalid timestamp in lock")
	}

	return &LockInfo{PID: pid, Timestamp: ts}, nil
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return true
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	err = process.Signal(os.Signal(nil))
	if err == nil || errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM) {
		return true
	}
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return false
	}
	// An unsupported or permission-obscured check cannot prove quiescence.
	return true
}
