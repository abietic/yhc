package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type executionCWDContextKey struct{}

// WithExecutionCWD binds filesystem and shell tool execution to one engine-owned
// working directory without mutating the process-wide current directory.
func WithExecutionCWD(ctx context.Context, cwd string) context.Context {
	return context.WithValue(ctx, executionCWDContextKey{}, strings.TrimSpace(cwd))
}

// ExecutionCWDFromCtx returns the engine-owned working directory, if one was
// supplied at the canonical tool execution boundary.
func ExecutionCWDFromCtx(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	cwd, _ := ctx.Value(executionCWDContextKey{}).(string)
	return strings.TrimSpace(cwd)
}

func effectiveExecutionCWD(ctx context.Context) (string, error) {
	cwd := ExecutionCWDFromCtx(ctx)
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", err)
		}
	}
	if !filepath.IsAbs(cwd) {
		absolute, err := filepath.Abs(cwd)
		if err != nil {
			return "", fmt.Errorf("resolve working directory %q: %w", cwd, err)
		}
		cwd = absolute
	}
	return filepath.Clean(cwd), nil
}

func resolveExecutionPath(ctx context.Context, path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	cwd, err := effectiveExecutionCWD(ctx)
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.Join(cwd, path)), nil
}

func rewriteExecutionPathInput(ctx context.Context, input, field string, defaultToCWD bool) (string, error) {
	var params map[string]any
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return input, nil
	}
	path, _ := params[field].(string)
	if strings.TrimSpace(path) == "" {
		if !defaultToCWD {
			return input, nil
		}
		cwd, err := effectiveExecutionCWD(ctx)
		if err != nil {
			return "", err
		}
		params[field] = cwd
	} else {
		resolved, err := resolveExecutionPath(ctx, path)
		if err != nil {
			return "", err
		}
		params[field] = resolved
	}
	rewritten, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("encode execution path: %w", err)
	}
	return string(rewritten), nil
}
