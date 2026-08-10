package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/abietic/yhc/engine/session"
)

// ResumeSessionInfo resumes a row selected by the paginated picker. The M6.3
// restore contract applies the selected execution context before continuation.
func (e *QueryEngine) ResumeSessionInfo(ctx context.Context, info session.SessionInfo) (*session.ResumedSession, error) {
	if e == nil {
		return nil, fmt.Errorf("query engine is nil")
	}
	return e.SessionService().ResumeInfo(ctx, info)
}

// ForkSessionInfo branches the complete selected transcript and resumes the
// new branch. It does not mutate the selected source session.
func (e *QueryEngine) ForkSessionInfo(ctx context.Context, info session.SessionInfo) (*session.ResumedSession, *session.BranchResult, error) {
	now := time.Now()
	if e != nil {
		e.mu.Lock()
		if e.config.Clock != nil {
			now = e.config.Clock()
		}
		e.mu.Unlock()
	}
	branchName := "fork-" + now.UTC().Format("20060102-150405")
	resumed, created, err := e.SessionService().ForkInfo(ctx, info, branchName)
	if err != nil {
		if created == nil {
			return nil, nil, err
		}
		return nil, created.Branch, err
	}
	return resumed, created.Branch, nil
}

func canonicalSessionDirectory(value string) string {
	abs, err := filepath.Abs(value)
	if err != nil {
		return filepath.Clean(value)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(abs)
}

func firstSessionValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
