package commands

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxCompletionRunes = 2048

// InputCompletion contains advisory edits only. Dispatch still validates the
// selected command against live capabilities when the user submits it.
type InputCompletion struct {
	Start      int              `json:"start"`
	End        int              `json:"end"`
	Command    string           `json:"command,omitempty"`
	Hint       string           `json:"hint,omitempty"`
	Candidates []InputCandidate `json:"candidates"`
}

type InputCandidate struct {
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"`
	Replacement string `json:"replacement"`
}

// CompleteInputFor shares command admission, positional metadata, and strict
// dispatch quoting with the TUI. It never invokes a command or an AI provider.
func (r *Registry) CompleteInputFor(ctx context.Context, entrypoint Entrypoint, request CompletionRequest, commandContext *CommandContext) InputCompletion {
	out := InputCompletion{Candidates: []InputCandidate{}}
	if ctx == nil {
		ctx = context.Background()
	}
	text := []rune(request.Input)
	if r == nil || ctx.Err() != nil || !utf8.ValidString(request.Input) || len(text) > MaxCompletionRunes || request.Cursor < 1 || request.Cursor > len(text) || !strings.HasPrefix(request.Input, "/") || strings.ContainsAny(request.Input, "\n\r\t") {
		return out
	}
	tokens := completionTokens(text)
	if len(tokens) == 0 || tokens[0].start != 0 || tokens[0].value != string(text[:tokens[0].end]) {
		return out
	}
	if request.Cursor <= tokens[0].end {
		prefix := strings.ToLower(string(text[1:request.Cursor]))
		out.End = tokens[0].end
		for _, cmd := range r.ListForContext(ctx, entrypoint, commandContext) {
			match := strings.HasPrefix(strings.ToLower(cmd.Name), prefix)
			for _, alias := range cmd.Aliases {
				match = match || strings.HasPrefix(strings.ToLower(alias), prefix)
			}
			if !match || ctx.Err() != nil {
				continue
			}
			value := "/" + cmd.Name
			replacement := value
			if out.End == len(text) {
				replacement += " "
			}
			if utf8.RuneCountInString(value) > 512 || utf8.RuneCountInString(replacement) > MaxCompletionRunes {
				continue
			}
			out.Candidates = append(out.Candidates, InputCandidate{Value: value, Description: completionLabel(cmd.Description), Source: "command", Replacement: replacement})
			if len(out.Candidates) == 50 {
				break
			}
		}
		return out
	}
	result := r.completeArgumentsFor(ctx, entrypoint, request, commandContext)
	if result.Command == "" {
		return out
	}
	out.Start, out.End, out.Command = result.Start, result.End, result.Command
	if cmd := r.GetForContext(ctx, entrypoint, commandContext, result.Command); cmd != nil {
		out.Hint = completionLabel(cmd.ArgumentHint())
	}
	for _, candidate := range result.Candidates {
		if utf8.RuneCountInString(candidate.Value) > 512 {
			continue
		}
		replacement := CompletionInsertion(request.Input, result, candidate)
		if !candidate.Continue && result.End == len(text) {
			replacement += " "
		}
		if utf8.RuneCountInString(replacement) > MaxCompletionRunes {
			continue
		}
		out.Candidates = append(out.Candidates, InputCandidate{Value: candidate.Value, Description: completionLabel(candidate.Description), Source: candidate.Source, Replacement: replacement})
	}
	return out
}

func completionLabel(value string) string {
	value = strings.Map(func(ch rune) rune {
		if unicode.IsControl(ch) {
			return ' '
		}
		return ch
	}, value)
	text := []rune(value)
	if len(text) > 512 {
		return string(text[:512])
	}
	return value
}
