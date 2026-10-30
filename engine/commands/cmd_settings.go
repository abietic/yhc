package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	engineconfig "github.com/abietic/yhc/engine/config"
)

func executeSettings(ctx context.Context, cmdCtx *CommandContext) (*CommandResult, error) {
	if cmdCtx == nil || strings.TrimSpace(cmdCtx.CWD) == "" {
		return nil, fmt.Errorf("shared settings require an active project directory")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Command tokenization strips JSON quotes and escapes. Read the JSON tail
	// from the original input so strings, spacing, and false/zero stay intact.
	_, input := settingsToken(cmdCtx.RawInput)
	scope, input := settingsToken(input)
	if scope == "" {
		scope = "user"
	}
	if scope != "user" && scope != "project" {
		return nil, fmt.Errorf("usage: /settings [user|project [<key> <JSON value>]]; use null to reset an override")
	}
	key, value := settingsToken(input)
	if key != "" && !json.Valid([]byte(value)) {
		return nil, fmt.Errorf("settings requires a JSON value; use null to reset an override")
	}
	snapshot, err := engineconfig.SettingsSnapshot(cmdCtx.CWD, scope)
	if err != nil {
		return nil, fmt.Errorf("read shared settings: %w", err)
	}
	if key != "" {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		snapshot, err = engineconfig.SettingsPatchContext(ctx, cmdCtx.CWD, scope, snapshot.Revision, map[string]json.RawMessage{
			key: json.RawMessage(value),
		})
		if err != nil {
			return nil, fmt.Errorf("update shared settings: %w", err)
		}
	}
	var output strings.Builder
	fmt.Fprintf(&output, "Shared settings (%s scope)\n", scope)
	if key != "" {
		fmt.Fprintf(&output, "Saved %s. Changes apply to new sessions.\n", key)
	}
	output.WriteString("File values below may be overridden by launch flags or environment.\n\n")
	keys := make([]string, 0, len(snapshot.Fields))
	for name := range snapshot.Fields {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		field := snapshot.Fields[name]
		fmt.Fprintf(&output, "%s = %s (source: %s)\n", name, field.EffectiveValue, field.Source)
	}
	output.WriteString("\n/settings user|project <key> <JSON value>\nExample: /settings project max_turns 0\nReset: /settings project max_turns null")
	return &CommandResult{Output: output.String()}, nil
}

func settingsToken(input string) (string, string) {
	input = strings.TrimSpace(input)
	index := strings.IndexFunc(input, unicode.IsSpace)
	if index < 0 {
		return input, ""
	}
	return input[:index], strings.TrimSpace(input[index:])
}
