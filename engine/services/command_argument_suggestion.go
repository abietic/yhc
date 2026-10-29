package services

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const commandArgumentSuggestionPrompt = `[COMMAND ARGUMENT SUGGESTION MODE]

Complete the current slash-command argument using only the JSON context supplied.
Do not inspect files, skills, conversation history, or execute anything. Return exactly
one JSON object with exactly this shape: {"argument":"..."}. The argument must extend
the supplied prefix exactly, on one line, and contain no explanation or Markdown.`

const (
	maxCommandArgumentHelpRunes   = 1024
	maxCommandArgumentHintRunes   = 256
	maxCommandArgumentPrefixRunes = 256
	maxCommandArgumentRunes       = 256
)

// CommandArgumentSuggestionContext is the bounded, metadata-only input for an
// explicitly requested command argument suggestion.
type CommandArgumentSuggestionContext struct {
	Command       string `json:"command"`
	Help          string `json:"help"`
	ArgumentHint  string `json:"argument_hint"`
	Prefix        string `json:"prefix"`
	ArgumentIndex int    `json:"argument_index"`
}

// GetCommandArgumentSuggestionPrompt returns the dedicated auxiliary prompt.
func GetCommandArgumentSuggestionPrompt() string {
	return commandArgumentSuggestionPrompt
}

// BuildCommandArgumentSuggestionContext serializes only bounded command
// metadata. It deliberately accepts no workspace, skill, or transcript input.
func BuildCommandArgumentSuggestionContext(
	command string,
	help string,
	argumentHint string,
	prefix string,
	argumentIndex int,
) string {
	value := CommandArgumentSuggestionContext{
		Command:       trimRunes(command, maxCommandArgumentHintRunes),
		Help:          trimRunes(help, maxCommandArgumentHelpRunes),
		ArgumentHint:  trimRunes(argumentHint, maxCommandArgumentHintRunes),
		Prefix:        trimRunes(prefix, maxCommandArgumentPrefixRunes),
		ArgumentIndex: argumentIndex,
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// ParseCommandArgumentSuggestion accepts exactly one strict JSON value and
// returns a valid extension of prefix. All failures fail closed.
func ParseCommandArgumentSuggestion(output string, prefix string) string {
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.DisallowUnknownFields()
	var value struct {
		Argument string `json:"argument"`
	}
	if err := decoder.Decode(&value); err != nil {
		return ""
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ""
	}
	if !validCommandArgument(value.Argument, prefix) {
		return ""
	}
	return value.Argument
}

func validCommandArgument(argument string, prefix string) bool {
	if strings.TrimSpace(argument) == "" || strings.TrimSpace(argument) == strings.TrimSpace(prefix) || !utf8.ValidString(argument) || utf8.RuneCountInString(argument) > maxCommandArgumentRunes ||
		!strings.HasPrefix(argument, prefix) || utf8.RuneCountInString(argument) <= utf8.RuneCountInString(prefix) {
		return false
	}
	for _, char := range argument {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func trimRunes(value string, maximum int) string {
	if maximum <= 0 || utf8.RuneCountInString(value) <= maximum {
		return value
	}
	var bounded bytes.Buffer
	for _, char := range value {
		if maximum == 0 {
			break
		}
		bounded.WriteRune(char)
		maximum--
	}
	return bounded.String()
}
