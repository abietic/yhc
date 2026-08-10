package commands

import (
	"fmt"
	"strings"
)

// ParseCommandInput splits a slash command string into the command name
// (without the leading slash) and a slice of arguments. It handles quoted
// arguments so that `/cmd "arg with spaces" arg2` parses correctly.
func ParseCommandInput(input string) (name string, args []string) {
	name, args, _ = parseCommandInputStrict(input)
	return name, args
}

func parseCommandInputStrict(input string) (name string, args []string, err error) {
	input = strings.TrimSpace(input)
	if len(input) == 0 {
		return "", nil, nil
	}

	if input[0] == '/' {
		input = input[1:]
	}

	tokens, err := tokenize(input)
	if err != nil {
		return "", nil, err
	}
	if len(tokens) == 0 {
		return "", nil, nil
	}

	name = strings.ToLower(tokens[0])
	if len(tokens) > 1 {
		args = tokens[1:]
	}
	return name, args, nil
}

// tokenize splits a string into tokens, respecting double-quoted and
// single-quoted strings. Quotes are stripped from the resulting tokens.
func tokenize(input string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

	for i := 0; i < len(input); i++ {
		ch := input[i]

		if escaped {
			current.WriteByte(ch)
			escaped = false
			continue
		}

		if ch == '\\' && inDouble {
			escaped = true
			continue
		}

		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}

		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}

		if ch == ' ' && !inSingle && !inDouble {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
			continue
		}

		current.WriteByte(ch)
	}

	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}

	switch {
	case escaped:
		return nil, fmt.Errorf("unterminated escape in double-quoted argument")
	case inSingle:
		return nil, fmt.Errorf("unterminated single-quoted argument")
	case inDouble:
		return nil, fmt.Errorf("unterminated double-quoted argument")
	default:
		return tokens, nil
	}
}
