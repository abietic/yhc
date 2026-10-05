package commands

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/tools"
)

// ArgumentCompletion describes suggestions only; execution validation remains
// owned by the command handler. Sources are built-in, never shell programs.
type ArgumentCompletion struct {
	Choices  []string
	Default  string
	Source   string
	Suggest  bool
	Rest     bool
	Branches map[string][]ArgumentCompletion
}

type CompletionRequest struct {
	Input  string
	Cursor int // rune offset
}

type ArgumentCandidate struct {
	Value       string
	Description string
	Source      string
	Continue    bool // directory navigation, without a trailing separator
}

type CompletionResult struct {
	Command         string
	Start, End      int // half-open rune range, including existing quotes
	Prefix          string
	ArgumentIndex   int
	Candidates      []ArgumentCandidate
	AllowSuggestion bool
	Rest            bool
}

type completionToken struct {
	start, end int
	value      string
}

type completionDirectoryCache struct {
	expires time.Time
	entries []os.DirEntry
}

func (r *Registry) completionDirectory(ctx context.Context, path string) ([]os.DirEntry, error) {
	now := time.Now()
	r.mu.RLock()
	cached, ok := r.completionDirectories[path]
	r.mu.RUnlock()
	if ok && now.Before(cached.expires) {
		return cached.entries, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil || ctx.Err() != nil {
		return nil, err
	}
	// Keep short-lived, bounded directory snapshots; large directories are still
	// queried asynchronously but are not retained in the registry.
	if len(entries) <= 4096 {
		r.mu.Lock()
		if len(r.completionDirectories) >= 8 {
			r.completionDirectories = nil
		}
		if r.completionDirectories == nil {
			r.completionDirectories = map[string]completionDirectoryCache{}
		}
		r.completionDirectories[path] = completionDirectoryCache{expires: now.Add(time.Second), entries: entries}
		r.mu.Unlock()
	}
	return entries, nil
}

// completionTokens tolerates an unfinished quote while preserving the lexical
// rules of dispatch. It deliberately does not relax dispatch's strict parser.
func completionTokens(input []rune) []completionToken {
	var tokens []completionToken
	for i := 0; i < len(input); {
		if input[i] == ' ' {
			i++
			continue
		}
		start := i
		var quote rune
		var value strings.Builder
		for i < len(input) {
			ch := input[i]
			if ch == ' ' && quote == 0 {
				break
			}
			if ch == '\\' && quote == '"' && i+1 < len(input) {
				i++
				value.WriteRune(input[i])
				i++
				continue
			}
			if (ch == '\'' || ch == '"') && (quote == 0 || quote == ch) {
				if quote == 0 {
					quote = ch
				} else {
					quote = 0
				}
				i++
				continue
			}
			value.WriteRune(ch)
			i++
		}
		tokens = append(tokens, completionToken{start, i, value.String()})
	}
	return tokens
}

func cloneArgumentCompletions(specs []ArgumentCompletion) []ArgumentCompletion {
	if specs == nil {
		return nil
	}
	out := append([]ArgumentCompletion(nil), specs...)
	for i := range out {
		out[i].Choices = append([]string(nil), out[i].Choices...)
		if out[i].Branches != nil {
			out[i].Branches = make(map[string][]ArgumentCompletion, len(specs[i].Branches))
			for name, branch := range specs[i].Branches {
				out[i].Branches[name] = cloneArgumentCompletions(branch)
			}
		}
	}
	return out
}

// PrepareArgumentCompletion resolves the command and editable argument without
// enumerating runtime or filesystem values. It is safe on the editor loop.
func (r *Registry) PrepareArgumentCompletion(ctx context.Context, request CompletionRequest, commandContext *CommandContext) (CompletionResult, ArgumentCompletion, bool) {
	text := []rune(request.Input)
	if !utf8.ValidString(request.Input) || request.Cursor < 0 || request.Cursor > len(text) || strings.ContainsAny(request.Input, "\n\r\t") {
		return CompletionResult{}, ArgumentCompletion{}, false
	}
	tokens := completionTokens(text)
	if len(tokens) == 0 || !strings.HasPrefix(tokens[0].value, "/") || request.Cursor <= tokens[0].end {
		return CompletionResult{}, ArgumentCompletion{}, false
	}
	cmd := r.GetForContext(ctx, EntrypointTUI, commandContext, strings.TrimPrefix(tokens[0].value, "/"))
	if cmd == nil {
		return CompletionResult{}, ArgumentCompletion{}, false
	}
	result := CompletionResult{Command: cmd.Name, Start: request.Cursor, End: request.Cursor}
	args := make([]string, 0, len(tokens)-1)
	for _, token := range tokens[1:] {
		args = append(args, token.value)
	}
	index := len(args)
	for i, token := range tokens[1:] {
		if request.Cursor >= token.start && request.Cursor <= token.end {
			index = i
			result.Start, result.End = token.start, token.end
			partial := completionTokens(text[token.start:request.Cursor])
			if len(partial) > 0 {
				result.Prefix = partial[0].value
			}
			break
		}
		if request.Cursor < token.start {
			index = i
			break
		}
	}
	result.ArgumentIndex = index
	specs, offset := cmd.Completion, 0
	for len(specs) > 0 && specs[0].Branches != nil && index > offset {
		if offset >= len(args) {
			return result, ArgumentCompletion{}, false
		}
		specs = specs[0].Branches[strings.ToLower(args[offset])]
		offset++
	}
	position := index - offset
	if position < 0 {
		return result, ArgumentCompletion{}, false
	}
	if position >= len(specs) {
		if len(specs) == 0 || !specs[len(specs)-1].Rest {
			return result, ArgumentCompletion{}, false
		}
		position = len(specs) - 1
	}
	spec := specs[position]
	if spec.Rest {
		result.Rest = true
		first := offset + position + 1
		if first < len(tokens) && request.Cursor >= tokens[first].start {
			result.Start = tokens[first].start
			result.End = len(text)
			var parts []string
			for _, token := range completionTokens(text[result.Start:request.Cursor]) {
				parts = append(parts, token.value)
			}
			result.Prefix = strings.Join(parts, " ")
		}
	}
	result.AllowSuggestion = spec.Suggest && len(spec.Choices) == 0 && spec.Source == ""
	return result, spec, true
}

func (r *Registry) CompleteArguments(ctx context.Context, request CompletionRequest, commandContext *CommandContext) CompletionResult {
	result, spec, ok := r.PrepareArgumentCompletion(ctx, request, commandContext)
	if !ok || ctx.Err() != nil {
		return result
	}
	values := make([]ArgumentCandidate, 0, len(spec.Choices)+1)
	for _, choice := range spec.Choices {
		values = append(values, ArgumentCandidate{Value: choice, Source: "option"})
	}
	if spec.Default != "" {
		values = append([]ArgumentCandidate{{Value: spec.Default, Description: "default", Source: "default"}}, values...)
	}
	dynamic := r.argumentSourceValues(ctx, commandContext, spec.Source, result.Prefix)
	if spec.Source == "models" {
		values = append(dynamic, values...)
	} else {
		values = append(values, dynamic...)
	}
	seen := map[string]bool{}
	for _, value := range values {
		if ctx.Err() != nil {
			break
		}
		if value.Value == "" || !utf8.ValidString(value.Value) || strings.IndexFunc(value.Value, unicode.IsControl) >= 0 || seen[value.Value] || !strings.HasPrefix(strings.ToLower(value.Value), strings.ToLower(result.Prefix)) {
			continue
		}
		seen[value.Value] = true
		if value.Value == result.Prefix && !value.Continue {
			continue
		}
		result.Candidates = append(result.Candidates, value)
		if len(result.Candidates) == 50 {
			break
		}
	}
	return result
}

func (r *Registry) argumentSourceValues(ctx context.Context, commandContext *CommandContext, source, prefix string) []ArgumentCandidate {
	if commandContext == nil {
		return nil
	}
	var out []ArgumentCandidate
	switch source {
	case "models":
		for _, entry := range commandInventory(commandContext).Entries {
			out = append(out, ArgumentCandidate{Value: entry.Selector, Description: entry.DisplayName, Source: "model"})
		}
	case "efforts":
		if values, err := availableEffortLevels(commandContext); err == nil {
			for _, value := range values {
				out = append(out, ArgumentCandidate{Value: value, Source: "model capability"})
			}
		}
	case "mcp":
		if source, ok := commandContext.Engine.(interface {
			MCPInventorySnapshot() tools.MCPInventorySnapshot
		}); ok {
			for _, server := range source.MCPInventorySnapshot().Servers {
				out = append(out, ArgumentCandidate{Value: server.Name, Source: "MCP"})
			}
		}
	case "sessions":
		if source, ok := commandContext.Engine.(interface {
			ListResumableSessions(int) ([]session.SessionInfo, error)
		}); ok {
			if sessions, err := source.ListResumableSessions(50); err == nil {
				for _, entry := range sessions {
					out = append(out, ArgumentCandidate{Value: entry.SessionID, Description: entry.CustomTitle, Source: "session"})
				}
			}
		}
	case "files", "directories":
		if commandContext.CWD == "" {
			return nil
		}
		dir, base := filepath.Split(prefix)
		root := dir
		if !filepath.IsAbs(root) {
			root = filepath.Join(commandContext.CWD, root)
		}
		entries, err := r.completionDirectory(ctx, root)
		if err != nil {
			return nil
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				break
			}
			if source == "directories" && !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") && !strings.HasPrefix(base, ".") {
				continue
			}
			value := dir + entry.Name()
			if entry.IsDir() {
				value += string(filepath.Separator)
			}
			out = append(out, ArgumentCandidate{Value: value, Source: source, Continue: entry.IsDir()})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	}
	return out
}

// CompletionInsertion quotes values using dispatch's double-quote rules.
func CompletionInsertion(input string, result CompletionResult, candidate ArgumentCandidate) string {
	value := candidate.Value
	text := []rune(input)
	quoted := result.Start < len(text) && (text[result.Start] == '\'' || text[result.Start] == '"')
	if result.Rest && !quoted && !strings.ContainsAny(value, "'\"") {
		return value
	}
	if quoted || strings.ContainsAny(value, "'\"") || strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		value = "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(value) + "\""
	}
	return value
}

func ApplyCompletion(input string, result CompletionResult, candidate ArgumentCandidate) string {
	text := []rune(input)
	if result.Start < 0 || result.End < result.Start || result.End > len(text) {
		return input
	}
	value := CompletionInsertion(input, result, candidate)
	if !candidate.Continue && result.End == len(text) {
		value += " "
	}
	return string(text[:result.Start]) + value + string(text[result.End:])
}

func builtinArgumentCompletion(name string) []ArgumentCompletion {
	options := func(values ...string) []ArgumentCompletion { return []ArgumentCompletion{{Choices: values}} }
	switch name {
	case "diff":
		return options("full", "staged", "stat")
	case "theme":
		return options("polar-night", "daybreak", "dark-ansi", "light-ansi", "snowy", "aubergine", "dark", "light")
	case "model":
		return []ArgumentCompletion{{Choices: []string{"list"}, Source: "models"}}
	case "effort":
		return []ArgumentCompletion{{Source: "efforts"}}
	case "mcp":
		return []ArgumentCompletion{{Source: "mcp"}}
	case "add-dir":
		return []ArgumentCompletion{{Source: "directories"}}
	case "compact":
		return []ArgumentCompletion{{Suggest: true, Rest: true}}
	case "permissions":
		var modes []string
		for _, mode := range permission.ValidModes() {
			if mode != permission.ModeBypassPermissions {
				modes = append(modes, string(mode))
			}
		}
		return []ArgumentCompletion{{Choices: []string{"mode", "bypass", "rules"}, Branches: map[string][]ArgumentCompletion{"mode": options(modes...), "bypass": options("confirm"), "rules": options("list", "add", "remove")}}}
	case "sessions":
		return []ArgumentCompletion{{Choices: []string{"list", "search", "resume", "rename", "export"}, Branches: map[string][]ArgumentCompletion{"resume": {{Source: "sessions"}}, "rename": {{Source: "sessions"}}, "export": {{Source: "sessions"}, {Source: "files"}}}}}
	default:
		return nil
	}
}
