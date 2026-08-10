package engine

import (
	"strings"
	"testing"
)

func TestGoalObjectiveNormalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{name: "plain", input: "ship the feature", want: "ship the feature"},
		{name: "trim ascii whitespace", input: "  ship it\n\t", want: "ship it"},
		{name: "trim unicode whitespace", input: " 修复 bug ", want: "修复 bug"},
		{name: "trim mixed unicode whitespace", input: "\u2003goal\u00a0", want: "goal"},
		{name: "crlf converted to lf", input: "line one\r\nline two", want: "line one\nline two"},
		{name: "crlf around trim", input: "\r\nobjective\r\n", want: "objective"},
		{name: "lone cr preserved", input: "a\rb", want: "a\rb"},
		{name: "interior whitespace preserved", input: "a  b\tc", want: "a  b\tc"},
		{name: "multibyte scalars", input: "目标：交付 P24.1", want: "目标：交付 P24.1"},
		{name: "empty", input: "", wantErr: errGoalObjectiveEmpty},
		{name: "whitespace only", input: "  \t\n ", wantErr: errGoalObjectiveEmpty},
		{name: "unicode whitespace only", input: "  ", wantErr: errGoalObjectiveEmpty},
		{name: "invalid utf8", input: "bad\xffinput", wantErr: errGoalObjectiveInvalidUTF8},
		{name: "invalid utf8 only", input: "\xc3\x28", wantErr: errGoalObjectiveInvalidUTF8},
		{name: "nul interior", input: "a\x00b", wantErr: errGoalObjectiveNUL},
		{name: "nul after crlf", input: "a\r\n\x00", wantErr: errGoalObjectiveNUL},
		{
			name:  "exactly 4000 ascii scalars",
			input: strings.Repeat("a", 4000),
			want:  strings.Repeat("a", 4000),
		},
		{
			name:    "4001 ascii scalars",
			input:   strings.Repeat("a", 4001),
			wantErr: errGoalObjectiveTooLong,
		},
		{
			name:  "exactly 4000 multibyte scalars",
			input: strings.Repeat("é", 4000),
			want:  strings.Repeat("é", 4000),
		},
		{
			name:    "4001 multibyte scalars",
			input:   strings.Repeat("é", 4001),
			wantErr: errGoalObjectiveTooLong,
		},
		{
			name:  "crlf conversion counted before limit",
			input: strings.Repeat("a", 3999) + "\r\n",
			want:  strings.Repeat("a", 3999),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeGoalObjective(tc.input)
			if tc.wantErr != nil {
				if err != tc.wantErr {
					t.Fatalf("normalizeGoalObjective() error = %v, want %v", err, tc.wantErr)
				}
				if got != "" {
					t.Fatalf("normalizeGoalObjective() = %q on error, want empty", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeGoalObjective() unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("normalizeGoalObjective() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGoalReasonNormalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{name: "plain", input: "quota exhausted", want: "quota exhausted"},
		{name: "trim unicode whitespace", input: "\u3000network down\u3000", want: "network down"},
		{name: "crlf converted to lf", input: "first\r\nsecond", want: "first\nsecond"},
		{name: "empty", input: "", wantErr: errGoalReasonEmpty},
		{name: "whitespace only", input: " \r\n\t", wantErr: errGoalReasonEmpty},
		{name: "invalid utf8", input: "\xff", wantErr: errGoalReasonInvalidUTF8},
		{name: "nul", input: "blocked\x00now", wantErr: errGoalReasonNUL},
		{
			name:  "exactly 1024 ascii scalars",
			input: strings.Repeat("r", 1024),
			want:  strings.Repeat("r", 1024),
		},
		{
			name:    "1025 ascii scalars",
			input:   strings.Repeat("r", 1025),
			wantErr: errGoalReasonTooLong,
		},
		{
			name:  "exactly 1024 multibyte scalars",
			input: strings.Repeat("界", 1024),
			want:  strings.Repeat("界", 1024),
		},
		{
			name:    "1025 multibyte scalars",
			input:   strings.Repeat("界", 1025),
			wantErr: errGoalReasonTooLong,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeGoalReason(tc.input)
			if tc.wantErr != nil {
				if err != tc.wantErr {
					t.Fatalf("normalizeGoalReason() error = %v, want %v", err, tc.wantErr)
				}
				if got != "" {
					t.Fatalf("normalizeGoalReason() = %q on error, want empty", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeGoalReason() unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("normalizeGoalReason() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGoalBlockerKeyNormalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{name: "simple", input: "quota", want: "quota"},
		{name: "full alphabet", input: "a0._:/-z9", want: "a0._:/-z9"},
		{name: "uppercase lowercased", input: "Quota.Exceeded", want: "quota.exceeded"},
		{name: "trim whitespace", input: "  network/down  ", want: "network/down"},
		{name: "digits first", input: "0abc", want: "0abc"},
		{
			name:  "exactly 128 chars",
			input: "a" + strings.Repeat("b", 127),
			want:  "a" + strings.Repeat("b", 127),
		},
		{name: "empty", input: "", wantErr: errGoalBlockerKeyInvalid},
		{name: "whitespace only", input: "   ", wantErr: errGoalBlockerKeyInvalid},
		{name: "leading separator", input: ".abc", wantErr: errGoalBlockerKeyInvalid},
		{name: "leading dash", input: "-abc", wantErr: errGoalBlockerKeyInvalid},
		{name: "interior space", input: "quota exceeded", wantErr: errGoalBlockerKeyInvalid},
		{name: "non-ascii", input: "quöta", wantErr: errGoalBlockerKeyInvalid},
		{name: "non-ascii cjk", input: "a配额", wantErr: errGoalBlockerKeyInvalid},
		{name: "invalid utf8", input: "a\xffb", wantErr: errGoalBlockerKeyInvalid},
		{name: "nul", input: "a\x00b", wantErr: errGoalBlockerKeyInvalid},
		{name: "at sign", input: "a@b", wantErr: errGoalBlockerKeyInvalid},
		{name: "uppercase extended stays invalid", input: "aÉ", wantErr: errGoalBlockerKeyInvalid},
		{
			name:    "129 chars",
			input:   "a" + strings.Repeat("b", 128),
			wantErr: errGoalBlockerKeyInvalid,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeGoalBlockerKey(tc.input)
			if tc.wantErr != nil {
				if err != tc.wantErr {
					t.Fatalf("normalizeGoalBlockerKey() error = %v, want %v", err, tc.wantErr)
				}
				if got != "" {
					t.Fatalf("normalizeGoalBlockerKey() = %q on error, want empty", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeGoalBlockerKey() unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("normalizeGoalBlockerKey() = %q, want %q", got, tc.want)
			}
		})
	}
}
