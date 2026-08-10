package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/abietic/yhc/internal/statemigration"
)

const (
	settingsMigrationFile     = "settings.json"
	settingsMigrationMaxBytes = 1 << 20
)

type settingsMigrationPayload struct {
	Model       *string                       `json:"model,omitempty"`
	Theme       *string                       `json:"theme,omitempty"`
	Permissions *settingsMigrationPermissions `json:"permissions,omitempty"`
	Compact     *settingsMigrationCompact     `json:"compact,omitempty"`
	Memory      *settingsMigrationMemory      `json:"memory,omitempty"`
	Provider    *string                       `json:"provider,omitempty"`
}

type settingsMigrationPermissions struct {
	DefaultMode *string   `json:"defaultMode,omitempty"`
	Allow       *[]string `json:"allow,omitempty"`
	Deny        *[]string `json:"deny,omitempty"`
}

type settingsMigrationCompact struct {
	Threshold *json.Number `json:"threshold,omitempty"`
	Strategy  *string      `json:"strategy,omitempty"`
}

type settingsMigrationMemory struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// SettingsMigrationSpec returns the exact project- or user-settings owner.
// The importer supplies the selected canonical and immutable legacy roots.
func SettingsMigrationSpec(scope string) statemigration.ArtifactSpec {
	return statemigration.ArtifactSpec{
		Owner:     "settings",
		Scope:     scope,
		SourceRel: settingsMigrationFile,
		TargetRel: settingsMigrationFile,
		Kind:      statemigration.RegularFile,
		MaxFiles:  1,
		MaxBytes:  settingsMigrationMaxBytes,
		Validate: func(_ context.Context, snapshot statemigration.Snapshot) error {
			_, err := parseSettingsMigrationSnapshot(snapshot)
			return err
		},
		Stage: stageSettingsMigration,
	}
}

func stageSettingsMigration(
	_ context.Context,
	snapshot statemigration.Snapshot,
	stage *os.Root,
) error {
	payload, err := parseSettingsMigrationSnapshot(snapshot)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return errors.New("settings migration schema is invalid")
	}
	encoded = append(encoded, '\n')
	output, err := stage.OpenFile(
		settingsMigrationFile,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return errors.New("settings migration staging failed")
	}
	_, writeErr := output.Write(encoded)
	closeErr := output.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("settings migration staging failed")
	}
	return nil
}

func parseSettingsMigrationSnapshot(
	snapshot statemigration.Snapshot,
) (settingsMigrationPayload, error) {
	reader, _, err := snapshot.Open(".")
	if err != nil {
		return settingsMigrationPayload{}, errors.New("settings migration source is invalid")
	}
	defer reader.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(reader, settingsMigrationMaxBytes+1))
	if err != nil || len(data) > settingsMigrationMaxBytes {
		return settingsMigrationPayload{}, errors.New("settings migration source is invalid")
	}
	return parseSettingsMigrationPayload(data)
}

func parseSettingsMigrationPayload(data []byte) (settingsMigrationPayload, error) {
	object, err := decodeStrictJSONObject(data)
	if err != nil {
		return settingsMigrationPayload{}, errors.New("settings migration schema is invalid")
	}
	payload := settingsMigrationPayload{}
	for key, raw := range object {
		switch key {
		case "model":
			payload.Model, err = decodeMigrationString(raw, nil)
		case "theme":
			payload.Theme, err = decodeMigrationString(raw, SupportedConfigSettings["theme"].Options)
		case "permissions":
			payload.Permissions, err = decodeMigrationPermissions(raw)
		case "compact":
			payload.Compact, err = decodeMigrationCompact(raw)
		case "memory":
			payload.Memory, err = decodeMigrationMemory(raw)
		case "provider":
			payload.Provider, err = decodeMigrationString(raw, nil)
		default:
			return settingsMigrationPayload{}, errors.New("settings migration schema is invalid")
		}
		if err != nil {
			return settingsMigrationPayload{}, errors.New("settings migration schema is invalid")
		}
	}
	return payload, nil
}

func decodeMigrationPermissions(
	raw json.RawMessage,
) (*settingsMigrationPermissions, error) {
	object, err := decodeStrictJSONObject(raw)
	if err != nil {
		return nil, err
	}
	permissions := &settingsMigrationPermissions{}
	for key, value := range object {
		switch key {
		case "defaultMode":
			permissions.DefaultMode, err = decodeMigrationString(
				value,
				SupportedConfigSettings["permissions.defaultMode"].Options,
			)
		case "allow":
			permissions.Allow, err = decodeMigrationStrings(value)
		case "deny":
			permissions.Deny, err = decodeMigrationStrings(value)
		default:
			return nil, errors.New("settings migration schema is invalid")
		}
		if err != nil {
			return nil, err
		}
	}
	return permissions, nil
}

func decodeMigrationCompact(raw json.RawMessage) (*settingsMigrationCompact, error) {
	object, err := decodeStrictJSONObject(raw)
	if err != nil {
		return nil, err
	}
	compact := &settingsMigrationCompact{}
	for key, value := range object {
		switch key {
		case "threshold":
			compact.Threshold, err = decodeMigrationNumber(value)
		case "strategy":
			compact.Strategy, err = decodeMigrationString(
				value,
				SupportedConfigSettings["compact.strategy"].Options,
			)
		default:
			return nil, errors.New("settings migration schema is invalid")
		}
		if err != nil {
			return nil, err
		}
	}
	return compact, nil
}

func decodeMigrationMemory(raw json.RawMessage) (*settingsMigrationMemory, error) {
	object, err := decodeStrictJSONObject(raw)
	if err != nil {
		return nil, err
	}
	memory := &settingsMigrationMemory{}
	for key, value := range object {
		if key != "enabled" {
			return nil, errors.New("settings migration schema is invalid")
		}
		memory.Enabled, err = decodeMigrationBool(value)
		if err != nil {
			return nil, err
		}
	}
	return memory, nil
}

func decodeStrictJSONObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, errors.New("settings migration schema is invalid")
	}
	object := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, errors.New("settings migration schema is invalid")
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("settings migration schema is invalid")
		}
		if _, duplicate := object[key]; duplicate {
			return nil, errors.New("settings migration schema is invalid")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, errors.New("settings migration schema is invalid")
		}
		object[key] = append(json.RawMessage(nil), raw...)
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, errors.New("settings migration schema is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("settings migration schema is invalid")
	}
	return object, nil
}

func decodeMigrationString(raw json.RawMessage, options []string) (*string, error) {
	if isJSONNull(raw) {
		return nil, errors.New("settings migration schema is invalid")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || credentialLikeConfigValue(value) {
		return nil, errors.New("settings migration schema is invalid")
	}
	if len(options) > 0 && !containsFold(options, value) {
		return nil, errors.New("settings migration schema is invalid")
	}
	return &value, nil
}

func decodeMigrationStrings(raw json.RawMessage) (*[]string, error) {
	if isJSONNull(raw) {
		return nil, errors.New("settings migration schema is invalid")
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, errors.New("settings migration schema is invalid")
	}
	for _, value := range values {
		if credentialLikeConfigValue(value) {
			return nil, errors.New("settings migration schema is invalid")
		}
	}
	return &values, nil
}

func decodeMigrationNumber(raw json.RawMessage) (*json.Number, error) {
	if isJSONNull(raw) {
		return nil, errors.New("settings migration schema is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("settings migration schema is invalid")
	}
	number, ok := value.(json.Number)
	if !ok {
		return nil, errors.New("settings migration schema is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("settings migration schema is invalid")
	}
	return &number, nil
}

func decodeMigrationBool(raw json.RawMessage) (*bool, error) {
	if isJSONNull(raw) {
		return nil, errors.New("settings migration schema is invalid")
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, errors.New("settings migration schema is invalid")
	}
	return &value, nil
}

func containsFold(options []string, value string) bool {
	for _, option := range options {
		if strings.EqualFold(option, value) {
			return true
		}
	}
	return false
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func credentialLikeConfigValue(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{
		"github_pat_", "ghp_", "gho_", "ghu_", "ghs_", "ghr_",
		"xoxb-", "xoxp-", "xoxa-", "xoxr-", "sk-", "sk_",
	} {
		if strings.HasPrefix(lower, prefix) && len(trimmed) >= len(prefix)+8 {
			return true
		}
	}
	if strings.HasPrefix(strings.ToUpper(trimmed), "AKIA") && len(trimmed) >= 16 {
		return true
	}
	for _, marker := range []string{
		"bearer ",
		"-----begin " + "private" + " key-----",
		"-----begin rsa " + "private" + " key-----",
		"-----begin ec " + "private" + " key-----",
		"api_key=",
		"api-key=",
		"apikey=",
		"token=",
		"password=",
		"secret=",
		"_authtoken=",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
