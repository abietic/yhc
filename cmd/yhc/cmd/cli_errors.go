package cmd

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	engineerrors "github.com/abietic/yhc/engine/errors"
)

const (
	ExitSuccess   = 0
	ExitFailure   = 1
	ExitUsage     = 2
	ExitCancelled = 130
)

type exitError struct {
	code   int
	silent bool
	err    error
}

func (e *exitError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *exitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func usageErrorf(format string, args ...any) error {
	return &exitError{code: ExitUsage, err: fmt.Errorf(format, args...)}
}

func renderedExitError(code int, err error) error {
	if err == nil {
		err = errors.New("command failed")
	}
	return &exitError{code: code, silent: true, err: err}
}

// ExitCode returns the stable process exit code for a CLI error.
func ExitCode(err error) int {
	if err == nil {
		return ExitSuccess
	}
	var cliErr *exitError
	if errors.As(err, &cliErr) {
		return cliErr.code
	}
	if errors.Is(err, context.Canceled) || engineerrors.IsAbort(err) {
		return ExitCancelled
	}
	return ExitFailure
}

// IsSilentError reports whether the command already rendered its complete
// machine/text failure output.
func IsSilentError(err error) bool {
	var cliErr *exitError
	return errors.As(err, &cliErr) && cliErr.silent
}

func maximumNArgs(maximum int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) > maximum {
			return usageErrorf("accepts at most %d argument(s), received %d", maximum, len(args))
		}
		return nil
	}
}

func exactArgs(count int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != count {
			return usageErrorf("requires exactly %d argument(s), received %d", count, len(args))
		}
		return nil
	}
}

func minimumArgs(minimum int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) < minimum {
			return usageErrorf("requires at least %d argument(s), received %d", minimum, len(args))
		}
		return nil
	}
}

func rangeArgs(minimum, maximum int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) < minimum || len(args) > maximum {
			return usageErrorf(
				"requires between %d and %d argument(s), received %d",
				minimum,
				maximum,
				len(args),
			)
		}
		return nil
	}
}

func noArgs(_ *cobra.Command, args []string) error {
	if len(args) != 0 {
		return usageErrorf("accepts no arguments, received %d", len(args))
	}
	return nil
}

func installFlagErrorHandlers(command *cobra.Command) {
	command.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageErrorf("%v", err)
	})
	for _, child := range command.Commands() {
		installFlagErrorHandlers(child)
	}
}

var (
	credentialAssignmentPattern = regexp.MustCompile(`(?i)(\b(?:api[_-]?key|authorization|credential|password|secret|access[_-]?token|refresh[_-]?token|auth[_-]?token|cookie)\b"?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\r\n,;}\]]+)`)
	bearerPattern               = regexp.MustCompile(`(?i)\bBearer\s+[^\s,;]+`)
	urlPattern                  = regexp.MustCompile(`https?://[^\s\]\[()]+`)
)

func redactSensitiveText(value string, exactSecrets ...string) string {
	redacted := value
	for _, secret := range exactSecrets {
		if strings.TrimSpace(secret) != "" {
			redacted = strings.ReplaceAll(redacted, secret, "<redacted>")
		}
	}
	redacted = credentialAssignmentPattern.ReplaceAllString(redacted, "$1<redacted>")
	redacted = bearerPattern.ReplaceAllString(redacted, "Bearer <redacted>")
	return urlPattern.ReplaceAllStringFunc(redacted, redactURL)
}

func redactURL(raw string) string {
	separator := strings.Index(raw, "://")
	if separator < 0 {
		return "<redacted-url>"
	}
	remainder := raw[separator+3:]
	if at := strings.LastIndex(remainder, "@"); at >= 0 {
		remainder = remainder[at+1:]
	}
	if end := strings.IndexAny(remainder, "/?#"); end >= 0 {
		remainder = remainder[:end]
	}
	if remainder == "" {
		return "<redacted-url>"
	}
	return raw[:separator+3] + remainder
}
