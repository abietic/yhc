package permission

import (
	"fmt"
	"regexp"
	"strings"
)

// SecurityAnalysis represents the result of analyzing a shell command for
// security-relevant constructs. It identifies pipes, redirections, background
// operations, heredocs, and other risk factors that may require user confirmation.
type SecurityAnalysis struct {
	// HasRedirection is true if the command contains output redirections (>, >>).
	HasRedirection bool
	// RedirectionTargets contains the file paths targeted by output redirections.
	RedirectionTargets []string
	// HasPipes is true if the command contains pipe operators (|).
	HasPipes bool
	// HasBackgroundOps is true if the command contains background operators (&).
	HasBackgroundOps bool
	// HasHeredoc is true if the command contains heredoc constructs (<<).
	HasHeredoc bool
	// SubCommands contains the individual commands split by operators.
	SubCommands []string
	// RiskFactors describes detected security concerns in human-readable form.
	RiskFactors []string
}

// sensitivePaths contains path prefixes and patterns considered sensitive for
// write operations via redirections.
var sensitivePaths = []string{
	"/etc/",
	"/usr/",
	"/bin/",
	"/sbin/",
	"/boot/",
	"/sys/",
	"/proc/",
	"/dev/",
	"/var/log/",
	"/root/",
	"/home/",
}

// sensitiveFilePatterns matches individual sensitive files.
var sensitiveFilePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?:^|/)\.bashrc$`),
	regexp.MustCompile(`(?:^|/)\.bash_profile$`),
	regexp.MustCompile(`(?:^|/)\.profile$`),
	regexp.MustCompile(`(?:^|/)\.zshrc$`),
	regexp.MustCompile(`(?:^|/)\.ssh/`),
	regexp.MustCompile(`(?:^|/)\.env$`),
	regexp.MustCompile(`(?:^|/)\.gitconfig$`),
	regexp.MustCompile(`(?:^|/)passwd$`),
	regexp.MustCompile(`(?:^|/)shadow$`),
	regexp.MustCompile(`(?:^|/)sudoers$`),
	regexp.MustCompile(`(?:^|/)crontab$`),
	regexp.MustCompile(`(?:^|/)authorized_keys$`),
	regexp.MustCompile(`(?:^|/)id_rsa`),
	regexp.MustCompile(`(?:^|/)id_ed25519`),
}

// SplitCommands splits a shell command string into individual sub-commands,
// separating by &&, ||, ;, and | operators. It respects quoted strings and
// nested constructs (parentheses, backticks, $()) so operators inside them
// are not treated as separators.
//
// This is a security-oriented split: the goal is to enumerate all commands
// that will actually execute, so each can be checked independently.
func SplitCommands(command string) []string {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}

	var commands []string
	var current strings.Builder
	i := 0
	n := len(command)

	for i < n {
		ch := command[i]

		// Handle single-quoted strings (no escape inside)
		if ch == '\'' {
			current.WriteByte(ch)
			i++
			for i < n && command[i] != '\'' {
				current.WriteByte(command[i])
				i++
			}
			if i < n {
				current.WriteByte(command[i])
				i++
			}
			continue
		}

		// Handle double-quoted strings (backslash escape)
		if ch == '"' {
			current.WriteByte(ch)
			i++
			for i < n && command[i] != '"' {
				if command[i] == '\\' && i+1 < n {
					current.WriteByte(command[i])
					i++
					current.WriteByte(command[i])
					i++
					continue
				}
				current.WriteByte(command[i])
				i++
			}
			if i < n {
				current.WriteByte(command[i])
				i++
			}
			continue
		}

		// Handle backslash escape outside quotes
		if ch == '\\' && i+1 < n {
			current.WriteByte(ch)
			i++
			current.WriteByte(command[i])
			i++
			continue
		}

		// Handle $(...) command substitution - don't split inside
		if ch == '$' && i+1 < n && command[i+1] == '(' {
			depth := 0
			current.WriteByte(ch)
			i++
			current.WriteByte(command[i])
			i++
			depth++
			for i < n && depth > 0 {
				switch command[i] {
				case '(':
					depth++
				case ')':
					depth--
				case '\'':
					current.WriteByte(command[i])
					i++
					for i < n && command[i] != '\'' {
						current.WriteByte(command[i])
						i++
					}
				case '"':
					current.WriteByte(command[i])
					i++
					for i < n && command[i] != '"' {
						if command[i] == '\\' && i+1 < n {
							current.WriteByte(command[i])
							i++
						}
						current.WriteByte(command[i])
						i++
					}
				}
				if i < n {
					current.WriteByte(command[i])
					i++
				}
			}
			continue
		}

		// Handle backtick command substitution - don't split inside
		if ch == '`' {
			current.WriteByte(ch)
			i++
			for i < n && command[i] != '`' {
				if command[i] == '\\' && i+1 < n {
					current.WriteByte(command[i])
					i++
				}
				current.WriteByte(command[i])
				i++
			}
			if i < n {
				current.WriteByte(command[i])
				i++
			}
			continue
		}

		// Handle parentheses (subshells) - don't split inside
		if ch == '(' {
			depth := 1
			current.WriteByte(ch)
			i++
			for i < n && depth > 0 {
				switch command[i] {
				case '(':
					depth++
				case ')':
					depth--
				case '\'':
					current.WriteByte(command[i])
					i++
					for i < n && command[i] != '\'' {
						current.WriteByte(command[i])
						i++
					}
				case '"':
					current.WriteByte(command[i])
					i++
					for i < n && command[i] != '"' {
						if command[i] == '\\' && i+1 < n {
							current.WriteByte(command[i])
							i++
						}
						current.WriteByte(command[i])
						i++
					}
				}
				if i < n {
					current.WriteByte(command[i])
					i++
				}
			}
			continue
		}

		// Check for && operator
		if ch == '&' && i+1 < n && command[i+1] == '&' {
			if s := strings.TrimSpace(current.String()); s != "" {
				commands = append(commands, s)
			}
			current.Reset()
			i += 2
			continue
		}

		// Check for || operator
		if ch == '|' && i+1 < n && command[i+1] == '|' {
			if s := strings.TrimSpace(current.String()); s != "" {
				commands = append(commands, s)
			}
			current.Reset()
			i += 2
			continue
		}

		// Check for | pipe operator (single)
		if ch == '|' {
			if s := strings.TrimSpace(current.String()); s != "" {
				commands = append(commands, s)
			}
			current.Reset()
			i++
			continue
		}

		// Check for ; separator
		if ch == ';' {
			if s := strings.TrimSpace(current.String()); s != "" {
				commands = append(commands, s)
			}
			current.Reset()
			i++
			continue
		}

		// Check for background & (standalone, not &&)
		if ch == '&' {
			// Already handled && above, so this is standalone &
			if s := strings.TrimSpace(current.String()); s != "" {
				commands = append(commands, s)
			}
			current.Reset()
			i++
			continue
		}

		current.WriteByte(ch)
		i++
	}

	if s := strings.TrimSpace(current.String()); s != "" {
		commands = append(commands, s)
	}

	return commands
}

// DetectHeredoc returns true if the command contains a heredoc construct
// (<<WORD, <<'WORD', <<"WORD", <<-WORD). It distinguishes heredocs from
// herestrings (<<<) and simple redirections.
func DetectHeredoc(command string) bool {
	if !strings.Contains(command, "<<") {
		return false
	}
	// Exclude herestrings (<<<) — we need << that is NOT followed by <
	// Use a more precise check: find << that is not <<< and not inside quotes
	i := 0
	n := len(command)
	for i < n {
		ch := command[i]
		// Skip single-quoted strings
		if ch == '\'' {
			i++
			for i < n && command[i] != '\'' {
				i++
			}
			if i < n {
				i++
			}
			continue
		}
		// Skip double-quoted strings
		if ch == '"' {
			i++
			for i < n && command[i] != '"' {
				if command[i] == '\\' && i+1 < n {
					i++
				}
				i++
			}
			if i < n {
				i++
			}
			continue
		}
		// Skip comments
		if ch == '#' {
			for i < n && command[i] != '\n' {
				i++
			}
			continue
		}
		// Check for <<
		if ch == '<' && i+1 < n && command[i+1] == '<' {
			// Ensure it's not <<<
			if i+2 < n && command[i+2] == '<' {
				i += 3
				continue
			}
			// This is << (possibly <<-), which is a heredoc
			return true
		}
		i++
	}
	return false
}

// DetectOutputRedirection checks if the command contains output redirections
// (> or >>). It returns whether a redirection was found and the first target
// path. It respects quoting so that > inside quoted strings is not a false
// positive.
func DetectOutputRedirection(command string) (bool, string) {
	i := 0
	n := len(command)
	for i < n {
		ch := command[i]
		// Skip single-quoted strings
		if ch == '\'' {
			i++
			for i < n && command[i] != '\'' {
				i++
			}
			if i < n {
				i++
			}
			continue
		}
		// Skip double-quoted strings
		if ch == '"' {
			i++
			for i < n && command[i] != '"' {
				if command[i] == '\\' && i+1 < n {
					i++
				}
				i++
			}
			if i < n {
				i++
			}
			continue
		}
		// Skip backslash escapes
		if ch == '\\' && i+1 < n {
			i += 2
			continue
		}
		// Skip comments
		if ch == '#' {
			for i < n && command[i] != '\n' {
				i++
			}
			continue
		}
		// Check for > or >> (output redirection)
		if ch == '>' {
			// Make sure it's not inside a heredoc operator like <<
			if i > 0 && command[i-1] == '<' {
				i++
				continue
			}
			// Skip >> (advance past second >)
			j := i + 1
			if j < n && command[j] == '>' {
				j++
			}
			// Skip whitespace after operator
			for j < n && (command[j] == ' ' || command[j] == '\t') {
				j++
			}
			// Extract target path
			target := extractRedirectTarget(command, j)
			if target != "" {
				return true, target
			}
			i = j
			continue
		}
		i++
	}
	return false, ""
}

// extractRedirectTarget extracts the redirect target starting at position pos.
// It handles quoted targets and stops at shell metacharacters.
func extractRedirectTarget(command string, pos int) string {
	n := len(command)
	if pos >= n {
		return ""
	}

	var target strings.Builder

	// Handle quoted target
	if command[pos] == '\'' {
		pos++
		for pos < n && command[pos] != '\'' {
			target.WriteByte(command[pos])
			pos++
		}
		return target.String()
	}
	if command[pos] == '"' {
		pos++
		for pos < n && command[pos] != '"' {
			if command[pos] == '\\' && pos+1 < n {
				pos++
			}
			target.WriteByte(command[pos])
			pos++
		}
		return target.String()
	}

	// Unquoted target: stop at whitespace or shell metacharacters
	for pos < n {
		ch := command[pos]
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == ';' ||
			ch == '&' || ch == '|' || ch == '<' || ch == '>' ||
			ch == '(' || ch == ')' {
			break
		}
		if ch == '\\' && pos+1 < n {
			pos++
			target.WriteByte(command[pos])
			pos++
			continue
		}
		target.WriteByte(ch)
		pos++
	}
	return target.String()
}

// AnalyzeCommandSecurity performs a comprehensive security analysis of a shell
// command string, identifying pipes, redirections, background operations,
// heredocs, sub-commands, and risk factors.
func AnalyzeCommandSecurity(command string) *SecurityAnalysis {
	analysis := &SecurityAnalysis{
		RedirectionTargets: []string{},
		SubCommands:        []string{},
		RiskFactors:        []string{},
	}

	command = strings.TrimSpace(command)
	if command == "" {
		return analysis
	}

	// Split into sub-commands
	analysis.SubCommands = SplitCommands(command)

	// Detect heredoc
	analysis.HasHeredoc = DetectHeredoc(command)
	if analysis.HasHeredoc {
		analysis.RiskFactors = append(analysis.RiskFactors,
			"command contains heredoc construct which may obscure content")
	}

	// Detect pipes (check using quote-aware scanning)
	analysis.HasPipes = detectPipesQuoteAware(command)
	if analysis.HasPipes {
		analysis.RiskFactors = append(analysis.RiskFactors,
			"command uses pipes which may chain operations")
	}

	// Detect background operations
	analysis.HasBackgroundOps = detectBackgroundOpsQuoteAware(command)
	if analysis.HasBackgroundOps {
		analysis.RiskFactors = append(analysis.RiskFactors,
			"command runs operations in background")
	}

	// Detect output redirections and collect targets
	analysis.RedirectionTargets = collectAllRedirectTargets(command)
	analysis.HasRedirection = len(analysis.RedirectionTargets) > 0
	if analysis.HasRedirection {
		analysis.RiskFactors = append(analysis.RiskFactors,
			"command writes output to file via redirection")
		// Check for sensitive path targets
		for _, target := range analysis.RedirectionTargets {
			if IsWriteToSensitivePath(target) {
				analysis.RiskFactors = append(analysis.RiskFactors,
					"redirection targets sensitive system path: "+target)
			}
		}
	}

	// Check for multiple sub-commands (compound command risk)
	if len(analysis.SubCommands) > 1 {
		analysis.RiskFactors = append(analysis.RiskFactors,
			"compound command with multiple sub-commands")
	}

	return analysis
}

// detectPipesQuoteAware returns true if the command contains a pipe operator
// (|) that is not inside quotes and is not part of || .
func detectPipesQuoteAware(command string) bool {
	i := 0
	n := len(command)
	for i < n {
		ch := command[i]
		if ch == '\'' {
			i++
			for i < n && command[i] != '\'' {
				i++
			}
			if i < n {
				i++
			}
			continue
		}
		if ch == '"' {
			i++
			for i < n && command[i] != '"' {
				if command[i] == '\\' && i+1 < n {
					i++
				}
				i++
			}
			if i < n {
				i++
			}
			continue
		}
		if ch == '\\' && i+1 < n {
			i += 2
			continue
		}
		if ch == '|' {
			// Check it's not ||
			if i+1 < n && command[i+1] == '|' {
				i += 2
				continue
			}
			// Check it's not |&
			if i+1 < n && command[i+1] == '&' {
				// pipe with stderr detected
				// Still a pipe (with stderr), so return true
				return true
			}
			return true
		}
		i++
	}
	return false
}

// detectBackgroundOpsQuoteAware returns true if the command contains a
// background operator (&) that is not inside quotes and is not part of && .
func detectBackgroundOpsQuoteAware(command string) bool {
	i := 0
	n := len(command)
	for i < n {
		ch := command[i]
		if ch == '\'' {
			i++
			for i < n && command[i] != '\'' {
				i++
			}
			if i < n {
				i++
			}
			continue
		}
		if ch == '"' {
			i++
			for i < n && command[i] != '"' {
				if command[i] == '\\' && i+1 < n {
					i++
				}
				i++
			}
			if i < n {
				i++
			}
			continue
		}
		if ch == '\\' && i+1 < n {
			i += 2
			continue
		}
		if ch == '&' {
			// Check it's not &&
			if i+1 < n && command[i+1] == '&' {
				i += 2
				continue
			}
			// Check it's not &> or &>> (redirection)
			if i+1 < n && command[i+1] == '>' {
				i += 2
				continue
			}
			return true
		}
		i++
	}
	return false
}

// collectAllRedirectTargets scans the command for all output redirection targets
// (> and >>), returning them as a slice. It respects quoting context.
func collectAllRedirectTargets(command string) []string {
	var targets []string
	i := 0
	n := len(command)

	for i < n {
		ch := command[i]
		// Skip single-quoted strings
		if ch == '\'' {
			i++
			for i < n && command[i] != '\'' {
				i++
			}
			if i < n {
				i++
			}
			continue
		}
		// Skip double-quoted strings
		if ch == '"' {
			i++
			for i < n && command[i] != '"' {
				if command[i] == '\\' && i+1 < n {
					i++
				}
				i++
			}
			if i < n {
				i++
			}
			continue
		}
		// Skip backslash escapes
		if ch == '\\' && i+1 < n {
			i += 2
			continue
		}
		// Skip comments
		if ch == '#' {
			for i < n && command[i] != '\n' {
				i++
			}
			continue
		}
		// Check for output redirection
		if ch == '>' {
			// Skip if preceded by < (part of heredoc <<)
			if i > 0 && command[i-1] == '<' {
				i++
				continue
			}
			// Advance past > or >>
			j := i + 1
			if j < n && command[j] == '>' {
				j++
			}
			// Skip &> style (already past >)
			// Skip whitespace
			for j < n && (command[j] == ' ' || command[j] == '\t') {
				j++
			}
			target := extractRedirectTarget(command, j)
			if target != "" {
				targets = append(targets, target)
			}
			i = j + len(target)
			if i <= j {
				i = j + 1
			}
			continue
		}
		i++
	}
	return targets
}

// CommandSubstitutionResult holds details about detected command substitutions.
type CommandSubstitutionResult struct {
	HasDollarParen   bool     // $(...)
	HasBacktick      bool     // `...`
	HasProcessSubIn  bool     // <(...)
	HasProcessSubOut bool     // >(...)
	Substitutions    []string // extracted substitution contents
}

// DetectCommandSubstitution scans a shell command for command substitution
// constructs: $(...), backticks, and process substitution <(...) / >(...).
// These are security-relevant because they execute arbitrary commands whose
// output is embedded in the parent command.
func DetectCommandSubstitution(command string) *CommandSubstitutionResult {
	result := &CommandSubstitutionResult{}
	i := 0
	n := len(command)

	for i < n {
		ch := command[i]

		// Skip single-quoted strings (no substitution inside)
		if ch == '\'' {
			i++
			for i < n && command[i] != '\'' {
				i++
			}
			if i < n {
				i++
			}
			continue
		}

		// Skip escaped characters
		if ch == '\\' && i+1 < n {
			i += 2
			continue
		}

		// Double-quoted strings: $() and backticks ARE active inside
		if ch == '"' {
			i++
			for i < n && command[i] != '"' {
				if command[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if command[i] == '$' && i+1 < n && command[i+1] == '(' {
					result.HasDollarParen = true
					sub := extractBalancedParen(command, i+1)
					if sub != "" {
						result.Substitutions = append(result.Substitutions, sub)
					}
				}
				if command[i] == '`' {
					result.HasBacktick = true
					sub := extractBacktickContent(command, i)
					if sub != "" {
						result.Substitutions = append(result.Substitutions, sub)
					}
					// skip past the backtick content
					i++
					for i < n && command[i] != '`' {
						if command[i] == '\\' && i+1 < n {
							i++
						}
						i++
					}
					if i < n {
						i++
					}
					continue
				}
				i++
			}
			if i < n {
				i++
			}
			continue
		}

		// $(...) command substitution
		if ch == '$' && i+1 < n && command[i+1] == '(' {
			result.HasDollarParen = true
			sub := extractBalancedParen(command, i+1)
			if sub != "" {
				result.Substitutions = append(result.Substitutions, sub)
			}
			// Skip past the substitution
			i += 2
			depth := 1
			for i < n && depth > 0 {
				if command[i] == '(' { //nolint:staticcheck
					depth++
				} else if command[i] == ')' {
					depth--
				} else if command[i] == '\'' {
					i++
					for i < n && command[i] != '\'' {
						i++
					}
				} else if command[i] == '"' {
					i++
					for i < n && command[i] != '"' {
						if command[i] == '\\' && i+1 < n {
							i++
						}
						i++
					}
				}
				if i < n {
					i++
				}
			}
			continue
		}

		// Backtick command substitution
		if ch == '`' {
			result.HasBacktick = true
			sub := extractBacktickContent(command, i)
			if sub != "" {
				result.Substitutions = append(result.Substitutions, sub)
			}
			i++
			for i < n && command[i] != '`' {
				if command[i] == '\\' && i+1 < n {
					i++
				}
				i++
			}
			if i < n {
				i++
			}
			continue
		}

		// Process substitution <(...)
		if ch == '<' && i+1 < n && command[i+1] == '(' {
			result.HasProcessSubIn = true
			sub := extractBalancedParen(command, i+1)
			if sub != "" {
				result.Substitutions = append(result.Substitutions, sub)
			}
			i += 2
			depth := 1
			for i < n && depth > 0 {
				if command[i] == '(' { //nolint:staticcheck
					depth++
				} else if command[i] == ')' {
					depth--
				}
				i++
			}
			continue
		}

		// Process substitution >(...)
		if ch == '>' && i+1 < n && command[i+1] == '(' {
			result.HasProcessSubOut = true
			sub := extractBalancedParen(command, i+1)
			if sub != "" {
				result.Substitutions = append(result.Substitutions, sub)
			}
			i += 2
			depth := 1
			for i < n && depth > 0 {
				if command[i] == '(' { //nolint:staticcheck
					depth++
				} else if command[i] == ')' {
					depth--
				}
				i++
			}
			continue
		}

		i++
	}

	return result
}

// HasAnySubstitution returns true if any command substitution form was detected.
func (r *CommandSubstitutionResult) HasAnySubstitution() bool {
	return r.HasDollarParen || r.HasBacktick || r.HasProcessSubIn || r.HasProcessSubOut
}

// extractBalancedParen extracts the content between the opening ( at pos and its
// matching ). Returns empty string if not found.
func extractBalancedParen(command string, pos int) string {
	if pos >= len(command) || command[pos] != '(' {
		return ""
	}
	depth := 0
	start := pos + 1
	i := pos
	for i < len(command) {
		if command[i] == '(' { //nolint:staticcheck
			depth++
		} else if command[i] == ')' {
			depth--
			if depth == 0 {
				return command[start:i]
			}
		}
		i++
	}
	return command[start:]
}

// extractBacktickContent extracts content between backticks starting at pos.
func extractBacktickContent(command string, pos int) string {
	if pos >= len(command) || command[pos] != '`' {
		return ""
	}
	i := pos + 1
	var content strings.Builder
	for i < len(command) && command[i] != '`' {
		if command[i] == '\\' && i+1 < len(command) {
			i++
		}
		content.WriteByte(command[i])
		i++
	}
	return content.String()
}

// DangerousPatternResult holds details about detected dangerous shell patterns.
type DangerousPatternResult struct {
	HasEval        bool // eval with dynamic argument
	HasSource      bool // source/. with dynamic argument
	HasExec        bool // exec with dynamic argument
	HasPipeToShell bool // | sh, | bash, | zsh, etc.
	HasCurlPipe    bool // curl ... | sh (or wget)
	HasChmod       bool // chmod +x or chmod 777
	HasSudo        bool // sudo usage
	HasDDWrite     bool // dd writing to device/file
	Reasons        []string
}

// dangerousExecPatterns matches dangerous command patterns.
var dangerousExecPatterns = []*regexp.Regexp{
	// eval with any argument
	regexp.MustCompile(`\beval\s+`),
	// source or dot command
	regexp.MustCompile(`\b(?:source|\.)\s+`),
	// exec with arguments
	regexp.MustCompile(`\bexec\s+`),
}

// pipeToShellPattern matches piping into a shell interpreter.
var pipeToShellPattern = regexp.MustCompile(`\|\s*(?:sh|bash|zsh|ksh|dash|fish|csh|tcsh|python[23]?|perl|ruby|node|php)\b`)

// curlPipePattern matches curl/wget output piped to shell.
var curlPipePattern = regexp.MustCompile(`\b(?:curl|wget)\b[^|;]*\|\s*(?:sh|bash|zsh|sudo\s+(?:sh|bash)|python[23]?|perl|ruby|node)`)

// chmodDangerousPattern matches chmod with executable or overly permissive modes.
var chmodDangerousPattern = regexp.MustCompile(`\bchmod\s+(?:\+[xXsS]|[0-7]*[7][0-7]*|a\+)`)

// sudoPattern matches sudo usage.
var sudoPattern = regexp.MustCompile(`\bsudo\s+`)

// ddWritePattern matches dd writing to a device or file.
var ddWritePattern = regexp.MustCompile(`\bdd\b[^;|&]*\bof=`)

// DetectDangerousPatterns scans a shell command for dangerous execution patterns
// that warrant heightened security review: eval, source, exec with dynamic args,
// pipe-to-shell idioms, curl|sh, and other risky constructs.
func DetectDangerousPatterns(command string) *DangerousPatternResult {
	result := &DangerousPatternResult{}

	// Work on subcommands individually to avoid false matches inside quotes
	subCmds := SplitCommands(command)
	fullCmd := command

	for _, sub := range subCmds {
		trimmed := strings.TrimSpace(sub)

		// Check eval
		if dangerousExecPatterns[0].MatchString(trimmed) {
			result.HasEval = true
			result.Reasons = append(result.Reasons, "uses 'eval' which executes dynamically constructed commands")
		}

		// Check source/.
		if dangerousExecPatterns[1].MatchString(trimmed) {
			result.HasSource = true
			result.Reasons = append(result.Reasons, "uses 'source' or '.' which executes a file in the current shell context")
		}

		// Check exec
		if dangerousExecPatterns[2].MatchString(trimmed) {
			result.HasExec = true
			result.Reasons = append(result.Reasons, "uses 'exec' which replaces the current process")
		}

		// Check chmod
		if chmodDangerousPattern.MatchString(trimmed) {
			result.HasChmod = true
			result.Reasons = append(result.Reasons, "changes file permissions to executable or overly permissive")
		}

		// Check sudo
		if sudoPattern.MatchString(trimmed) {
			result.HasSudo = true
			result.Reasons = append(result.Reasons, "uses 'sudo' for elevated privileges")
		}

		// Check dd
		if ddWritePattern.MatchString(trimmed) {
			result.HasDDWrite = true
			result.Reasons = append(result.Reasons, "uses 'dd' with output file which can overwrite devices/files")
		}
	}

	// Check pipe-to-shell (needs full command with pipes)
	if pipeToShellPattern.MatchString(fullCmd) {
		result.HasPipeToShell = true
		result.Reasons = append(result.Reasons, "pipes output to a shell interpreter")
	}

	// Check curl/wget pipe patterns
	if curlPipePattern.MatchString(fullCmd) {
		result.HasCurlPipe = true
		result.Reasons = append(result.Reasons, "downloads and executes remote code (curl/wget piped to shell)")
	}

	return result
}

// HasAnyDanger returns true if any dangerous pattern was detected.
func (r *DangerousPatternResult) HasAnyDanger() bool {
	return r.HasEval || r.HasSource || r.HasExec || r.HasPipeToShell ||
		r.HasCurlPipe || r.HasChmod || r.HasSudo || r.HasDDWrite
}

// SedWriteResult holds details about sed write operations detected.
type SedWriteResult struct {
	HasInPlace  bool   // sed -i (in-place edit)
	HasWriteCmd bool   // sed w command (write to file)
	TargetFile  string // the file being written (if detectable)
	Hint        string // suggestion for the user
}

// sedInPlacePattern matches sed -i or sed --in-place flags.
var sedInPlacePattern = regexp.MustCompile(`\bsed\b[^;|&]*\s(?:-i|--in-place)`)

// sedWriteCmdPattern matches sed w command that writes to a file.
var sedWriteCmdPattern = regexp.MustCompile(`\bsed\b[^;|&]*['"]?[^'"]*\bw\s+\S+`)

// DetectSedWrite checks if a command uses sed in a way that writes to files.
// This includes -i (in-place editing) and the w command. Returns details about
// the sed write and a hint suggesting the Edit tool instead.
func DetectSedWrite(command string) *SedWriteResult {
	result := &SedWriteResult{}

	if !strings.Contains(command, "sed") {
		return result
	}

	subCmds := SplitCommands(command)
	for _, sub := range subCmds {
		trimmed := strings.TrimSpace(sub)
		if !strings.Contains(trimmed, "sed") {
			continue
		}

		// Check -i / --in-place
		if sedInPlacePattern.MatchString(trimmed) {
			result.HasInPlace = true
			// Try to extract the target file
			result.TargetFile = extractSedTarget(trimmed)
			result.Hint = "sed -i modifies files in-place; consider using the Edit tool for tracked, reversible file modifications"
		}

		// Check w command in sed script
		if sedWriteCmdPattern.MatchString(trimmed) {
			result.HasWriteCmd = true
			if result.Hint == "" {
				result.Hint = "sed 'w' command writes to a file; consider using the Write tool instead"
			}
		}
	}

	return result
}

// HasSedWrite returns true if any sed write operation was detected.
func (r *SedWriteResult) HasSedWrite() bool {
	return r.HasInPlace || r.HasWriteCmd
}

// extractSedTarget attempts to extract the target file from a sed -i command.
func extractSedTarget(cmd string) string {
	// Pattern: sed -i[suffix] 'script' file
	// or: sed --in-place[=suffix] 'script' file
	parts := strings.Fields(cmd)
	if len(parts) < 3 {
		return ""
	}
	// The last non-flag argument is typically the file
	lastArg := parts[len(parts)-1]
	// Skip if it looks like a sed expression
	if strings.HasPrefix(lastArg, "'") || strings.HasPrefix(lastArg, "\"") ||
		strings.HasPrefix(lastArg, "s/") || strings.HasPrefix(lastArg, "/") {
		return ""
	}
	return lastArg
}

// readOnlyCommands is the allowlist of commands considered safe in read-only (plan) mode.
// These commands only read data, never modify files or system state.
var readOnlyCommands = map[string]bool{
	// File listing/info
	"ls": true, "dir": true, "stat": true, "file": true, "wc": true,
	"du": true, "df": true, "find": true, "locate": true, "which": true,
	"whereis": true, "type": true, "readlink": true, "realpath": true,
	// File reading
	"cat": true, "head": true, "tail": true, "less": true, "more": true,
	"bat": true, "hexdump": true, "xxd": true, "strings": true, "od": true,
	// Text search/processing (read-only)
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true,
	"ack": true, "diff": true, "cmp": true, "comm": true, "sort": true,
	"uniq": true, "tr": true, "cut": true, "paste": true, "join": true,
	"fmt": true, "fold": true, "column": true, "expand": true, "unexpand": true,
	"nl": true, "pr": true, "rev": true,
	// Git read-only
	"git status": true, "git log": true, "git diff": true, "git show": true,
	"git branch": true, "git tag": true, "git remote": true, "git blame": true,
	"git shortlog": true, "git describe": true, "git rev-parse": true,
	"git ls-files": true, "git ls-tree": true, "git cat-file": true,
	"git config --get": true, "git config --list": true,
	// System info
	"uname": true, "hostname": true, "whoami": true, "id": true,
	"date": true, "uptime": true, "env": true, "printenv": true,
	"pwd": true, "echo": true, "printf": true, "test": true,
	// Go tooling (read-only)
	"go version": true, "go env": true, "go list": true, "go doc": true,
	"go vet": true,
	// Node/Python info
	"node --version": true, "npm --version": true, "python --version": true,
	"python3 --version": true, "pip list": true, "pip3 list": true,
	// Process info
	"ps": true, "top": true, "htop": true, "pgrep": true, "lsof": true,
	// Network info (read-only)
	"ifconfig": true, "ip addr": true, "netstat": true, "ss": true,
	"dig": true, "nslookup": true, "host": true, "ping": true,
	// Archive listing
	"tar -tf": true, "tar --list": true, "unzip -l": true, "zipinfo": true,
	// JSON/YAML processing (read-only)
	"jq": true, "yq": true,
	// tree
	"tree": true,
}

// readOnlyCommandPrefixes are command prefixes (first word) that are always safe.
var readOnlyCommandPrefixes = []string{
	"ls", "dir", "stat", "file", "wc", "du", "df", "find", "locate", "which",
	"whereis", "type", "readlink", "realpath",
	"cat", "head", "tail", "less", "more", "bat", "hexdump", "xxd", "strings", "od",
	"grep", "egrep", "fgrep", "rg", "ag", "ack", "diff", "cmp", "comm",
	"sort", "uniq", "tr", "cut", "paste", "join", "fmt", "fold", "column",
	"expand", "unexpand", "nl", "pr", "rev",
	"uname", "hostname", "whoami", "id", "date", "uptime", "env", "printenv",
	"pwd", "echo", "printf", "test",
	"ps", "top", "htop", "pgrep", "lsof",
	"ifconfig", "netstat", "ss", "dig", "nslookup", "host", "ping",
	"jq", "yq", "tree",
}

// writeCommands is a denylist of commands that always mutate state.
var writeCommands = map[string]bool{
	"rm": true, "rmdir": true, "mv": true, "cp": true, "mkdir": true,
	"touch": true, "chmod": true, "chown": true, "chgrp": true,
	"ln": true, "install": true, "shred": true, "truncate": true,
	"dd": true, "mkfs": true, "mount": true, "umount": true,
	"kill": true, "killall": true, "pkill": true, "reboot": true,
	"shutdown": true, "halt": true, "poweroff": true,
	"useradd": true, "userdel": true, "usermod": true, "groupadd": true,
	"passwd": true, "chpasswd": true,
	"apt": true, "apt-get": true, "dpkg": true, "yum": true, "dnf": true,
	"pacman": true, "brew": true, "snap": true,
	"pip": true, "pip3": true, "npm": true, "yarn": true, "pnpm": true,
	"cargo": true, "go install": true, "go get": true,
	"docker": true, "podman": true, "kubectl": true,
	"systemctl": true, "service": true,
	"crontab":  true,
	"git push": true, "git commit": true, "git merge": true,
	"git rebase": true, "git reset": true, "git checkout": true,
	"git stash": true, "git cherry-pick": true, "git revert": true,
	"git pull": true, "git fetch": true, "git clone": true,
	"git add": true, "git rm": true, "git mv": true,
	"git init": true, "git clean": true,
}

// IsReadOnlyCommand checks if a command is safe to execute in read-only (plan) mode.
// Returns true if all subcommands are in the read-only allowlist.
func IsReadOnlyCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return true
	}

	subCmds := SplitCommands(command)
	for _, sub := range subCmds {
		if !isSubCommandReadOnly(strings.TrimSpace(sub)) {
			return false
		}
	}
	return true
}

// ValidateReadOnlyMode checks if a command is allowed in plan/read-only mode.
// Returns an error message if the command is not allowed, or empty string if OK.
func ValidateReadOnlyMode(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}

	subCmds := SplitCommands(command)
	for _, sub := range subCmds {
		trimmed := strings.TrimSpace(sub)
		if !isSubCommandReadOnly(trimmed) {
			firstWord := extractFirstWord(trimmed)
			return fmt.Sprintf("command %q is not allowed in read-only (plan) mode; only read-only commands are permitted", firstWord)
		}
	}
	return ""
}

// isSubCommandReadOnly checks if a single subcommand (no pipes/operators) is read-only.
func isSubCommandReadOnly(cmd string) bool {
	if cmd == "" {
		return true
	}

	// Check if it has output redirection (always a write operation)
	if hasRedir, _ := DetectOutputRedirection(cmd); hasRedir {
		return false
	}

	// Check command substitution — the outer command matters, not the substitution
	firstWord := extractFirstWord(cmd)
	if firstWord == "" {
		return true
	}

	// Check against write denylist (both single word and two-word commands)
	if writeCommands[firstWord] {
		return false
	}
	// Check two-word write commands (e.g., "git push")
	twoWord := extractFirstTwoWords(cmd)
	if twoWord != "" && writeCommands[twoWord] {
		return false
	}

	// Check if first word is in the read-only allowlist prefixes
	for _, prefix := range readOnlyCommandPrefixes {
		if firstWord == prefix {
			return true
		}
	}

	// Check two-word read-only commands
	if twoWord != "" && readOnlyCommands[twoWord] {
		return true
	}

	// Special cases for git — allow git with read-only subcommands
	if firstWord == "git" {
		gitSub := extractGitSubcommand(cmd)
		readOnlyGitSubs := map[string]bool{
			"status": true, "log": true, "diff": true, "show": true,
			"branch": true, "tag": true, "remote": true, "blame": true,
			"shortlog": true, "describe": true, "rev-parse": true,
			"ls-files": true, "ls-tree": true, "cat-file": true,
			"stash list": true, "reflog": true, "bisect": true,
		}
		if readOnlyGitSubs[gitSub] {
			return true
		}
		// If git subcommand is in write list, deny
		gitTwoWord := "git " + gitSub
		if writeCommands[gitTwoWord] {
			return false
		}
	}

	// Special case for go — allow go with read-only subcommands
	if firstWord == "go" {
		goSub := extractSecondWord(cmd)
		readOnlyGoSubs := map[string]bool{
			"version": true, "env": true, "list": true, "doc": true,
			"vet": true, "build": true, "test": true, "mod": true,
		}
		if readOnlyGoSubs[goSub] {
			return true
		}
	}

	// Special case for make — allow make with no dangerous targets
	if firstWord == "make" {
		return true // make itself is typically safe for building
	}

	// Unknown command — not in either list. Default to deny in strict mode.
	// However, we're lenient for common dev tools.
	return false
}

// extractFirstWord returns the first whitespace-delimited word from a command.
func extractFirstWord(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	// Skip leading env vars (VAR=val cmd)
	for {
		if idx := strings.IndexByte(cmd, '='); idx > 0 {
			prefix := cmd[:idx]
			if !strings.ContainsAny(prefix, " \t") && isValidEnvVarName(prefix) {
				// Skip past the value
				rest := cmd[idx+1:]
				// Skip the value (may be quoted)
				rest = skipValue(rest)
				cmd = strings.TrimSpace(rest)
				continue
			}
		}
		break
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// extractFirstTwoWords returns the first two words joined by a space.
func extractFirstTwoWords(cmd string) string {
	first := extractFirstWord(cmd)
	if first == "" {
		return ""
	}
	second := extractSecondWord(cmd)
	if second == "" {
		return ""
	}
	return first + " " + second
}

// extractSecondWord returns the second word from a command.
func extractSecondWord(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	fields := strings.Fields(cmd)
	if len(fields) < 2 {
		return ""
	}
	// Skip env vars
	idx := 0
	for idx < len(fields) {
		if strings.Contains(fields[idx], "=") && !strings.HasPrefix(fields[idx], "-") {
			idx++
			continue
		}
		break
	}
	if idx+1 < len(fields) {
		return fields[idx+1]
	}
	return ""
}

// extractGitSubcommand returns the git subcommand (first arg after 'git' and flags).
func extractGitSubcommand(cmd string) string {
	fields := strings.Fields(cmd)
	for i := 1; i < len(fields); i++ {
		if strings.HasPrefix(fields[i], "-") {
			continue
		}
		return fields[i]
	}
	return ""
}

// isValidEnvVarName checks if a string is a valid environment variable name.
func isValidEnvVarName(s string) bool {
	if s == "" {
		return false
	}
	for i, ch := range s {
		if i == 0 {
			if !((ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '_') { //nolint:staticcheck
				return false
			}
		} else {
			if !((ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_') { //nolint:staticcheck
				return false
			}
		}
	}
	return true
}

// skipValue advances past an env var value (possibly quoted).
func skipValue(s string) string {
	if len(s) == 0 {
		return s
	}
	if s[0] == '\'' {
		end := strings.IndexByte(s[1:], '\'')
		if end >= 0 {
			return s[end+2:]
		}
		return ""
	}
	if s[0] == '"' {
		i := 1
		for i < len(s) {
			if s[i] == '\\' && i+1 < len(s) {
				i += 2
				continue
			}
			if s[i] == '"' {
				return s[i+1:]
			}
			i++
		}
		return ""
	}
	// Unquoted: advance to next space
	end := strings.IndexAny(s, " \t")
	if end >= 0 {
		return s[end:]
	}
	return ""
}

// IsWriteToSensitivePath returns true if the given path is a sensitive system
// location that should not be written to via shell redirections without explicit
// user approval.
func IsWriteToSensitivePath(path string) bool {
	if path == "" {
		return false
	}

	// Normalize the path for comparison
	normalized := strings.TrimSpace(path)

	// Check absolute paths against sensitive prefixes
	if strings.HasPrefix(normalized, "/") {
		for _, prefix := range sensitivePaths {
			if strings.HasPrefix(normalized, prefix) {
				return true
			}
		}
		// Also catch writes directly to /etc, /usr, etc. (without trailing /)
		directSensitive := []string{"/etc", "/usr", "/bin", "/sbin", "/boot", "/sys", "/proc", "/dev"}
		for _, p := range directSensitive {
			if normalized == p {
				return true
			}
		}
	}

	// Check against sensitive file patterns regardless of path type
	for _, pattern := range sensitiveFilePatterns {
		if pattern.MatchString(normalized) {
			return true
		}
	}

	return false
}
