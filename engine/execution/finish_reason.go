package execution

import (
	"errors"
	"fmt"
	"strings"
)

type streamTerminalDisposition uint8

const (
	streamTerminalCommit streamTerminalDisposition = iota
	streamTerminalRejectTruncated
	streamTerminalCancel
	streamTerminalModelError
)

type streamTerminalDecision struct {
	Disposition  streamTerminalDisposition
	FinishReason string
	Err          error
}

func classifyStreamTerminal(
	rawFinishReason string,
	hasWithheld bool,
	withheldReason string,
	cleanEOF bool,
	streamErr error,
	contextErr error,
) streamTerminalDecision {
	if contextErr != nil {
		return streamTerminalDecision{
			Disposition: streamTerminalCancel,
			Err:         contextErr,
		}
	}
	if streamErr != nil {
		return streamTerminalDecision{
			Disposition: streamTerminalModelError,
			Err:         streamErr,
		}
	}
	if hasWithheld {
		normalizedWithheld := strings.ToLower(strings.TrimSpace(withheldReason))
		if normalizedWithheld == "" {
			normalizedWithheld = "api_error"
		}
		switch normalizedWithheld {
		case "length", "max_tokens", "max_output_tokens":
			return streamTerminalDecision{
				Disposition:  streamTerminalRejectTruncated,
				FinishReason: normalizedWithheld,
			}
		default:
			return streamTerminalDecision{
				Disposition:  streamTerminalModelError,
				FinishReason: normalizedWithheld,
				Err:          fmt.Errorf("model withheld response (%s)", normalizedWithheld),
			}
		}
	}
	if !cleanEOF {
		return streamTerminalDecision{
			Disposition: streamTerminalModelError,
			Err:         errors.New("model stream ended without a clean terminal boundary"),
		}
	}

	normalized := strings.ToLower(strings.TrimSpace(rawFinishReason))
	switch normalized {
	case "", "stop", "stop_sequence", "end_turn", "tool_calls", "tool_use":
		return streamTerminalDecision{
			Disposition:  streamTerminalCommit,
			FinishReason: normalized,
		}
	case "length", "max_tokens", "max_output_tokens":
		return streamTerminalDecision{
			Disposition:  streamTerminalRejectTruncated,
			FinishReason: normalized,
		}
	default:
		return streamTerminalDecision{
			Disposition:  streamTerminalModelError,
			FinishReason: normalized,
			Err:          fmt.Errorf("unsupported model finish reason %q", rawFinishReason),
		}
	}
}

func streamTerminalToolError(decision streamTerminalDecision) string {
	switch decision.Disposition {
	case streamTerminalRejectTruncated:
		return "Tool call rejected: model response was truncated before the assistant turn committed"
	case streamTerminalCancel:
		return "Interrupted by user"
	case streamTerminalModelError:
		if decision.Err != nil {
			return "Tool call rejected: " + decision.Err.Error()
		}
		return "Tool call rejected: model stream did not commit"
	default:
		return ""
	}
}
