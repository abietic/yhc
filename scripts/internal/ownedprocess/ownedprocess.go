// Package ownedprocess runs a command while owning its complete process tree.
package ownedprocess

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

const (
	codeTreeUnavailable = "process_tree_unavailable"
	codeStartFailed     = "process_start_failed"
	codeProcessFailed   = "process_failed"
	codeTreeCloseFailed = "process_tree_close_failed"
	codeTimeout         = "process_timeout"
	codeCanceled        = "process_canceled"
)

// Error classifies a process-tree lifecycle failure without exposing command
// details. Callers should branch on Code rather than parsing Error strings.
type Error struct {
	code string
	err  error
}

func (err *Error) Error() string {
	if err.err == nil {
		return err.code
	}
	return err.code + ": " + err.err.Error()
}

func (err *Error) Unwrap() error { return err.err }

func fail(code string, cause error) error { return &Error{code: code, err: cause} }

// Code returns an owned-process failure code, or an empty string for nil and
// errors not returned by Run.
func Code(err error) string {
	var processErr *Error
	if errors.As(err, &processErr) {
		return processErr.code
	}
	return ""
}

type tree interface {
	terminate() error
	kill() error
	close() error
}

// Run executes command with ownership of its complete process tree.
func Run(ctx context.Context, command *exec.Cmd) error {
	if err := configure(command); err != nil {
		return fail(codeTreeUnavailable, err)
	}
	if err := command.Start(); err != nil {
		return fail(codeStartFailed, err)
	}
	tree, err := attach(command)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return fail(codeTreeUnavailable, err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	select {
	case waitErr := <-waited:
		closeErr := tree.close()
		if waitErr != nil {
			return fail(codeProcessFailed, waitErr)
		}
		if closeErr != nil {
			return fail(codeTreeCloseFailed, closeErr)
		}
		return nil
	case <-ctx.Done():
		_ = tree.terminate()
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-waited:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			_ = tree.kill()
			<-waited
		}
		_ = tree.close()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fail(codeTimeout, ctx.Err())
		}
		return fail(codeCanceled, ctx.Err())
	}
}
