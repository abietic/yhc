package permission

import (
	"path"
	"strings"
)

// BashCriticalPathDecision reports whether a supported literal Bash command
// targets a path that must receive a fresh permission decision.
type BashCriticalPathDecision struct {
	Match      bool
	ReasonCode string
}

type criticalPathWord struct {
	value         string
	unquotedTilde bool
	unquotedStars []int
}

// ClassifyBashCriticalPath recognizes only the deliberately small literal
// rm/rmdir subset owned by the proof-bound Auto Bash contract. Unsupported
// shell syntax is an unknown non-match.
func ClassifyBashCriticalPath(command, cwd, home string) BashCriticalPathDecision {
	if !path.IsAbs(cwd) || !path.IsAbs(home) {
		return BashCriticalPathDecision{}
	}

	segments, ok := tokenizeCriticalPathCommand(command)
	if !ok {
		return BashCriticalPathDecision{}
	}
	cwd = path.Clean(cwd)
	home = path.Clean(home)
	for _, segment := range segments {
		if decision := classifyCriticalPathSegment(segment, cwd, home); decision.Match {
			return decision
		}
	}
	return BashCriticalPathDecision{}
}

func tokenizeCriticalPathCommand(command string) ([][]criticalPathWord, bool) {
	var (
		segments     [][]criticalPathWord
		segment      []criticalPathWord
		word         criticalPathWord
		value        strings.Builder
		wordStarted  bool
		requiresNext bool
	)

	flushWord := func() {
		if !wordStarted {
			return
		}
		word.value = value.String()
		segment = append(segment, word)
		requiresNext = false
		word = criticalPathWord{}
		value.Reset()
		wordStarted = false
	}
	flushSegment := func(requireFollowing bool) bool {
		flushWord()
		if len(segment) == 0 {
			return false
		}
		segments = append(segments, segment)
		segment = nil
		requiresNext = requireFollowing
		return true
	}

	for i := 0; i < len(command); {
		switch command[i] {
		case ' ', '\t', '\r':
			flushWord()
			i++
		case '\n':
			flushWord()
			if len(segment) > 0 {
				if !flushSegment(false) {
					return nil, false
				}
			}
			i++
		case ';':
			if !flushSegment(false) {
				return nil, false
			}
			i++
		case '&':
			if i+1 >= len(command) || command[i+1] != '&' || !flushSegment(true) {
				return nil, false
			}
			i += 2
		case '|':
			if i+1 >= len(command) || command[i+1] != '|' || !flushSegment(true) {
				return nil, false
			}
			i += 2
		case '<', '>', '(', ')', '`', '$':
			return nil, false
		case '\\':
			if i+1 >= len(command) || command[i+1] == '\n' {
				return nil, false
			}
			wordStarted = true
			value.WriteByte(command[i+1])
			i += 2
		case '\'':
			wordStarted = true
			i++
			for i < len(command) && command[i] != '\'' {
				if command[i] == '$' || command[i] == '`' {
					return nil, false
				}
				value.WriteByte(command[i])
				i++
			}
			if i >= len(command) {
				return nil, false
			}
			i++
		case '"':
			wordStarted = true
			i++
			for i < len(command) && command[i] != '"' {
				if command[i] == '$' || command[i] == '`' {
					return nil, false
				}
				if command[i] == '\\' {
					if i+1 >= len(command) || command[i+1] == '\n' {
						return nil, false
					}
					value.WriteByte(command[i+1])
					i += 2
					continue
				}
				value.WriteByte(command[i])
				i++
			}
			if i >= len(command) {
				return nil, false
			}
			i++
		default:
			wordStarted = true
			if command[i] == '~' && value.Len() == 0 {
				word.unquotedTilde = true
			}
			if command[i] == '*' {
				word.unquotedStars = append(word.unquotedStars, value.Len())
			}
			if command[i] == '?' || command[i] == '[' || command[i] == ']' {
				return nil, false
			}
			value.WriteByte(command[i])
			i++
		}
	}

	flushWord()
	if requiresNext {
		return nil, false
	}
	if len(segment) > 0 {
		segments = append(segments, segment)
	}
	if len(segments) == 0 {
		return nil, false
	}
	for _, words := range segments {
		for _, candidate := range words {
			if !supportedCriticalPathGlob(candidate) {
				return nil, false
			}
		}
	}
	return segments, true
}

func supportedCriticalPathGlob(word criticalPathWord) bool {
	if len(word.unquotedStars) == 0 {
		return true
	}
	if len(word.unquotedStars) != 1 || word.unquotedStars[0] != len(word.value)-1 {
		return false
	}
	return word.value == "*" || strings.HasSuffix(word.value, "/*")
}

func classifyCriticalPathSegment(words []criticalPathWord, cwd, home string) BashCriticalPathDecision {
	if len(words) == 0 {
		return BashCriticalPathDecision{}
	}
	commandIndex := 0
	if words[0].value == "command" || words[0].value == "builtin" {
		commandIndex++
	}
	if commandIndex >= len(words) || (words[commandIndex].value != "rm" && words[commandIndex].value != "rmdir") {
		return BashCriticalPathDecision{}
	}

	options := true
	for _, target := range words[commandIndex+1:] {
		if options {
			switch {
			case target.value == "--":
				options = false
				continue
			case strings.HasPrefix(target.value, "-") && target.value != "-":
				continue
			}
			options = false
		}
		if decision := classifyCriticalPathTarget(target, cwd, home); decision.Match {
			return decision
		}
	}
	return BashCriticalPathDecision{}
}

func classifyCriticalPathTarget(target criticalPathWord, cwd, home string) BashCriticalPathDecision {
	if len(target.unquotedStars) == 1 {
		return BashCriticalPathDecision{Match: true, ReasonCode: "all_entries"}
	}
	if target.value == "~" && target.unquotedTilde {
		return BashCriticalPathDecision{Match: true, ReasonCode: "home"}
	}

	resolved := target.value
	if !path.IsAbs(resolved) {
		resolved = path.Join(cwd, resolved)
	}
	resolved = path.Clean(resolved)
	if resolved == "/" {
		return BashCriticalPathDecision{Match: true, ReasonCode: "root"}
	}
	if resolved == home {
		return BashCriticalPathDecision{Match: true, ReasonCode: "home"}
	}
	if reason := classifyCriticalVolumePath(resolved); reason != "" {
		return BashCriticalPathDecision{Match: true, ReasonCode: reason}
	}
	if path.Dir(resolved) == "/" {
		return BashCriticalPathDecision{Match: true, ReasonCode: "root_child"}
	}
	return BashCriticalPathDecision{}
}

func classifyCriticalVolumePath(targetPath string) string {
	const volumesRoot = "/Volumes"
	if !strings.HasPrefix(targetPath, volumesRoot+"/") {
		return ""
	}
	relative := strings.TrimPrefix(targetPath, volumesRoot+"/")
	parts := strings.Split(relative, "/")
	switch len(parts) {
	case 1:
		return "volume_root"
	case 2:
		return "volume_child"
	default:
		return ""
	}
}
