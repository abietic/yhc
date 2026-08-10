package engine

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Pure P24.1 Goal input normalization helpers. They are deterministic and
// carry no QueryEngine state, persistence, event, tool, command,
// continuation, transport, or UI behavior. Error labels are bounded and
// never echo the rejected input.

const (
	goalObjectiveMaxScalars = 4000
	goalReasonMaxScalars    = 1024
	goalBlockerKeyMaxLen    = 128
)

var (
	errGoalObjectiveInvalidUTF8 = errors.New("goal objective: invalid utf-8")
	errGoalObjectiveEmpty       = errors.New("goal objective: empty after normalization")
	errGoalObjectiveNUL         = errors.New("goal objective: contains NUL")
	errGoalObjectiveTooLong     = errors.New("goal objective: exceeds 4000 scalar values")

	errGoalReasonInvalidUTF8 = errors.New("goal reason: invalid utf-8")
	errGoalReasonEmpty       = errors.New("goal reason: empty after normalization")
	errGoalReasonNUL         = errors.New("goal reason: contains NUL")
	errGoalReasonTooLong     = errors.New("goal reason: exceeds 1024 scalar values")

	errGoalBlockerKeyInvalid = errors.New("goal blocker key: must match [a-z0-9][a-z0-9._:/-]{0,127}")
)

// normalizeGoalObjective validates and normalizes a Goal objective: the input
// must be valid UTF-8, CRLF sequences are converted to LF, outer Unicode
// whitespace is trimmed, and the result must be non-empty, contain no NUL,
// and hold at most 4000 Unicode scalar values.
func normalizeGoalObjective(input string) (string, error) {
	return normalizeGoalText(input, goalObjectiveMaxScalars,
		errGoalObjectiveInvalidUTF8, errGoalObjectiveEmpty,
		errGoalObjectiveNUL, errGoalObjectiveTooLong)
}

// normalizeGoalReason applies the same normalization and rejection rules as
// normalizeGoalObjective with a 1024-scalar limit.
func normalizeGoalReason(input string) (string, error) {
	return normalizeGoalText(input, goalReasonMaxScalars,
		errGoalReasonInvalidUTF8, errGoalReasonEmpty,
		errGoalReasonNUL, errGoalReasonTooLong)
}

// normalizeGoalBlockerKey trims outer whitespace, ASCII-lowercases the key,
// and requires [a-z0-9][a-z0-9._:/-]{0,127}. Empty, non-ASCII, and every
// other byte outside the allowed set is rejected.
func normalizeGoalBlockerKey(input string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(input))
	if len(key) == 0 || len(key) > goalBlockerKeyMaxLen {
		return "", errGoalBlockerKeyInvalid
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		if i > 0 && (c == '.' || c == '_' || c == ':' || c == '/' || c == '-') {
			continue
		}
		return "", errGoalBlockerKeyInvalid
	}
	return key, nil
}

// normalizeGoalText implements the shared objective/reason contract: valid
// UTF-8, CRLF to LF before validation, trim outer Unicode whitespace, reject
// empty and NUL, and enforce a Unicode scalar value limit.
func normalizeGoalText(input string, maxScalars int, errUTF8, errEmpty, errNUL, errTooLong error) (string, error) {
	if !utf8.ValidString(input) {
		return "", errUTF8
	}
	normalized := strings.TrimSpace(strings.ReplaceAll(input, "\r\n", "\n"))
	if normalized == "" {
		return "", errEmpty
	}
	if strings.IndexByte(normalized, 0) >= 0 {
		return "", errNUL
	}
	if utf8.RuneCountInString(normalized) > maxScalars {
		return "", errTooLong
	}
	return normalized, nil
}
