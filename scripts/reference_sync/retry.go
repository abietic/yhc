package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Attempts includes the first call. Unknown errors fail closed, not retry forever.
type RetryPolicy struct {
	Attempts       int `yaml:"attempts"`
	BackoffSeconds int `yaml:"backoff_seconds"`
}

func (p RetryPolicy) normalized() RetryPolicy {
	if p.Attempts == 0 {
		p.Attempts = 2
	}
	if p.BackoffSeconds == 0 {
		p.BackoffSeconds = 3
	}
	return p
}

func (p RetryPolicy) validate(name string) error {
	if p.Attempts < 0 || p.Attempts > 3 || p.BackoffSeconds < 0 || p.BackoffSeconds > 30 {
		return fmt.Errorf("%s.retry requires attempts 0..3 and backoff_seconds 0..30 (0 uses defaults)", name)
	}
	return nil
}

func retryTransient[T any](ctx context.Context, policy RetryPolicy, call func() (T, error)) (T, int, error) {
	policy = policy.normalized()
	var value T
	for attempt := 1; attempt <= policy.Attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return value, attempt - 1, err
		}
		var err error
		value, err = call()
		if canceled := ctx.Err(); canceled != nil {
			return value, attempt, canceled
		}
		if err == nil || !isTransientFailure(err) || attempt == policy.Attempts {
			return value, attempt, err
		}
		delay := time.Duration(policy.BackoffSeconds) * time.Second * time.Duration(1<<(attempt-1))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return value, attempt, ctx.Err()
		case <-timer.C:
		}
	}
	return value, 0, errors.New("invalid retry budget")
}

func isTransientFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	text := strings.ToLower(err.Error())
	// Never amplify authentication, quota, configuration or integrity failures.
	for _, permanent := range []string{"permission denied", "authentication", "unauthorized", "forbidden", "quota", "usage limit", "rate limit", "insufficient", "invalid api key", "model not found", "not supported", "repository not found", "origin mismatch", "blocked_"} {
		if strings.Contains(text, permanent) {
			return false
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	for _, transient := range []string{"connection reset", "connection timed out", "connection timeout", "network is unreachable", "could not resolve host", "could not resolve hostname", "temporary failure", "tls handshake timeout", "unexpected eof", "http/2", "http2", "curl 92", "remote end hung up", "stream disconnected", "service unavailable", "bad gateway", "gateway timeout", "status 502", "status 503", "status 504"} {
		if strings.Contains(text, transient) {
			return true
		}
	}
	return false
}

func generateWithRetry(ctx context.Context, cfg RetryPolicy, timeout time.Duration, generator modelSummaryGenerator, prompt string) (string, int, error) {
	return retryTransient(ctx, cfg, func() (string, error) {
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return generator.Generate(callCtx, prompt)
	})
}

type FetchPolicy struct {
	Retry          RetryPolicy `yaml:"retry"`
	TimeoutSeconds int         `yaml:"timeout_seconds"`
}

type fetchGuardError struct{ status, detail string }

func (e *fetchGuardError) Error() string { return e.status + ": " + e.detail }

func (g gitClient) fetch(remote, before, upstream string, policy FetchPolicy) (int, error) {
	ctx := g.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if policy.TimeoutSeconds > 0 {
		g.timeout = time.Duration(policy.TimeoutSeconds) * time.Second
	}
	var lastErr error
	_, attempts, err := retryTransient(ctx, policy.Retry, func() (string, error) {
		// Another actor may change the checkout during a timeout/backoff.
		if err := validateRemote(g, remote); err != nil {
			return "", err
		}
		head, err := g.run("rev-parse", "HEAD")
		if err != nil {
			return "", err
		}
		if head != before {
			return "", &fetchGuardError{"blocked_changed", "HEAD changed during fetch retry"}
		}
		status, err := g.run("status", "--porcelain", "--untracked-files=all")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(status) != "" {
			return "", &fetchGuardError{"blocked_dirty", summarizeLines(status)}
		}
		branch := strings.TrimPrefix(upstream, "origin/")
		if branch == "" || branch == upstream {
			return "", errors.New("fetch upstream must name an origin branch")
		}
		// Sync only the configured branch, not every unrelated upstream topic.
		refspec := "+refs/heads/" + branch + ":refs/remotes/origin/" + branch
		args := []string{"fetch", "--prune", "--no-tags", "origin", refspec}
		if lastErr != nil {
			actual, err := g.run("remote", "get-url", "origin")
			if err != nil {
				return "", err
			}
			// These options live only in this command; never rewrite origin/config.
			options := []string{"-c", "http.version=HTTP/1.1"}
			for _, prefix := range []string{"git@github.com:", "ssh://git@github.com/"} {
				if strings.HasPrefix(actual, prefix) {
					options = append(options, "-c", "url.https://github.com/.insteadOf="+prefix)
					break
				}
			}
			args = append(options, args...)
		}
		text, err := g.run(args...)
		lastErr = err
		return text, err
	})
	return attempts, err
}
