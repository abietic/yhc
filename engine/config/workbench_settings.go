package config

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

var (
	ErrSettingsConflict   = errors.New("settings changed; reload before saving")
	ErrSettingsInvalid    = errors.New("invalid settings request")
	ErrSettingsUnsafePath = errors.New("settings path is unavailable or unsafe")
	ErrSettingsBusy       = errors.New("settings are being saved by another process")
)

const settingsDocumentLimit = 1 << 20

// WorkbenchSettingField describes the same persisted setting to every client.
// EffectiveValue is file configuration, not a claim about an already running
// session or higher-priority launch flags/environment. Secrets are never fields.
type WorkbenchSettingField struct {
	Label          string            `json:"label"`
	Type           string            `json:"type"`
	Group          string            `json:"group"`
	Description    string            `json:"description"`
	Options        []string          `json:"options,omitempty"`
	ChoiceOnly     bool              `json:"choice_only,omitempty"`
	ConflictsWith  []string          `json:"conflicts_with,omitempty"`
	BlockedBy      []string          `json:"blocked_by,omitempty"`
	Aliases        map[string]string `json:"aliases,omitempty"`
	Default        json.RawMessage   `json:"default"`
	StoredValue    json.RawMessage   `json:"stored_value"`
	EffectiveValue json.RawMessage   `json:"effective_value"`
	Source         string            `json:"source"`
	Overridden     bool              `json:"overridden"`
	Apply          string            `json:"apply"`
}

type WorkbenchSettingsSnapshot struct {
	Scope    string                           `json:"scope"`
	Revision string                           `json:"revision"`
	Fields   map[string]WorkbenchSettingField `json:"fields"`
}

type workbenchSettingDefinition struct {
	label, kind, group, description string
	options                         []string
	value                           func(*Config) any
}

func workbenchSettingDefinitions() map[string]workbenchSettingDefinition {
	return map[string]workbenchSettingDefinition{
		"provider":           {"Provider", "string", "model", "Default provider; launch flags and environment may override this value.", []string{"anthropic", "openai", "google", "deepseek", "glm", "qwen", "ark"}, func(c *Config) any { return c.Provider }},
		"model":              {"Model", "string", "model", "Default model for new sessions. Use a provider model identifier or configured alias.", nil, func(c *Config) any { return c.Model }},
		"model_profile":      {"Model profile", "string", "model", "Select an existing user-owned model profile; cannot be combined with legacy provider fields.", nil, func(c *Config) any { return c.ModelProfile }},
		"api_base_url":       {"API endpoint", "string", "model", "Optional HTTP(S) provider endpoint, without credentials, query parameters, or fragment.", nil, func(c *Config) any { return c.APIBaseURL }},
		"max_turns":          {"Maximum turns", "integer", "execution", "Maximum agent turns per interaction; zero means unlimited.", nil, func(c *Config) any { return c.MaxTurns }},
		"permission_mode":    {"Permission mode", "string", "execution", "Default permission mode for new sessions. Bypass is available only through explicitly confirmed execution controls.", []string{"default", "plan", "acceptEdits", "dontAsk", "auto"}, func(c *Config) any { return c.PermissionMode }},
		"theme":              {"Theme", "string", "appearance", "Shared theme preference for the next application or terminal session.", []string{"polar-night", "daybreak", "dark-ansi", "light-ansi", "snowy", "aubergine", "dark", "light"}, func(c *Config) any { return c.Theme }},
		"reduced_motion":     {"Reduced motion", "boolean", "appearance", "Disable non-essential animation.", nil, func(c *Config) any { return c.ReducedMotion }},
		"prompt_suggestions": {"Prompt suggestions", "boolean", "appearance", "Enable model-backed next-prompt suggestions in supported clients.", nil, func(c *Config) any { return c.PromptSuggestions == nil || *c.PromptSuggestions }},
	}
}

// SettingsSchema returns detached metadata for the shared, non-secret settings.
// It never reads configuration files, environment overrides, or credentials.
// Dynamic model-profile choices are available only in SettingsSnapshot.
func SettingsSchema() map[string]WorkbenchSettingField {
	defaults := DefaultConfig()
	fields := make(map[string]WorkbenchSettingField)
	definitions := workbenchSettingDefinitions()
	for key, definition := range definitions {
		value, _ := json.Marshal(definition.value(defaults))
		field := WorkbenchSettingField{
			Label: definition.label, Type: definition.kind, Group: definition.group,
			Description: definition.description, Options: append([]string(nil), definition.options...),
			// An empty dynamic catalog does not make profile creation supported.
			ChoiceOnly: definition.options != nil || key == "model_profile",
			Default:    value, StoredValue: json.RawMessage("null"),
			EffectiveValue: append(json.RawMessage(nil), value...), Source: "default", Apply: "new_session",
		}
		if key == "model_profile" {
			for _, legacy := range workbenchLegacySelectionKeys() {
				if _, editable := definitions[legacy]; editable {
					field.ConflictsWith = append(field.ConflictsWith, legacy)
				}
			}
		} else {
			for _, legacy := range workbenchLegacySelectionKeys() {
				if key == legacy {
					field.ConflictsWith = []string{"model_profile"}
				}
			}
		}
		if key == "theme" {
			field.Aliases = map[string]string{"dark": "polar-night", "light": "daybreak"}
		}
		fields[key] = field
	}
	return fields
}

// SettingsSnapshot reads the user/project files without constructing a model or
// running a tool. Unknown fields are retained privately, never projected.
func SettingsSnapshot(projectDir, scope string) (WorkbenchSettingsSnapshot, error) {
	if err := validateSettingsScope(projectDir, scope); err != nil {
		return WorkbenchSettingsSnapshot{}, err
	}
	user, err := readWorkbenchDocument(UserConfigPath())
	if err != nil {
		return WorkbenchSettingsSnapshot{}, err
	}
	project := workbenchDocument{fields: make(map[string]json.RawMessage)}
	if projectDir != "" {
		project, err = readWorkbenchDocument(ProjectConfigPath(projectDir))
		if err != nil {
			return WorkbenchSettingsSnapshot{}, err
		}
	}
	return buildWorkbenchSnapshot(scope, user, project)
}

// SettingsPatch replaces only explicitly editable keys. Its revision covers the
// entire original document, including fields the renderer is not allowed to see.
func SettingsPatch(projectDir, scope, revision string, changes map[string]json.RawMessage) (WorkbenchSettingsSnapshot, error) {
	return SettingsPatchContext(context.Background(), projectDir, scope, revision, changes)
}

// SettingsPatchContext cancels shared-lock waiting and checks cancellation
// before replacing the document. Once the final commit check has passed,
// cancellation does not roll back a saved document or turn success into failure.
func SettingsPatchContext(ctx context.Context, projectDir, scope, revision string, changes map[string]json.RawMessage) (WorkbenchSettingsSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return WorkbenchSettingsSnapshot{}, err
	}
	if err := validateSettingsScope(projectDir, scope); err != nil {
		return WorkbenchSettingsSnapshot{}, err
	}
	if len(revision) != sha256.Size*2 || len(changes) == 0 || len(changes) > len(workbenchSettingDefinitions()) {
		return WorkbenchSettingsSnapshot{}, ErrSettingsInvalid
	}
	if _, err := hex.DecodeString(revision); err != nil {
		return WorkbenchSettingsSnapshot{}, ErrSettingsInvalid
	}
	validated := make(map[string]json.RawMessage, len(changes))
	for key, raw := range changes {
		value, err := validateWorkbenchSetting(key, raw)
		if err != nil {
			return WorkbenchSettingsSnapshot{}, err
		}
		validated[key] = value
	}
	path := UserConfigPath()
	if scope == "project" {
		path = ProjectConfigPath(projectDir)
	}
	store, err := openWorkbenchSettingsStore(path, true)
	if err != nil {
		return WorkbenchSettingsSnapshot{}, err
	}
	defer store.close()
	if err := store.lockContext(ctx); err != nil {
		return WorkbenchSettingsSnapshot{}, err
	}
	defer store.unlock()
	document, err := store.read()
	if err != nil {
		return WorkbenchSettingsSnapshot{}, err
	}
	if document.revision() != revision {
		return WorkbenchSettingsSnapshot{}, ErrSettingsConflict
	}
	for key, value := range validated {
		if bytes.Equal(value, []byte("null")) {
			delete(document.fields, key)
		} else {
			document.fields[key] = value
		}
	}
	if err := validateWorkbenchSelection(document.fields, scope); err != nil {
		return WorkbenchSettingsSnapshot{}, err
	}
	encoded, err := json.MarshalIndent(document.fields, "", "  ")
	if err != nil || len(encoded)+1 > settingsDocumentLimit {
		return WorkbenchSettingsSnapshot{}, ErrSettingsInvalid
	}
	encoded = append(encoded, '\n')
	// Validate the same typed configuration that startup consumes before commit.
	if _, err := decodeWorkbenchConfig(document.fields, scope == "project"); err != nil {
		return WorkbenchSettingsSnapshot{}, err
	}
	document.raw = encoded
	user, project := document, workbenchDocument{fields: make(map[string]json.RawMessage)}
	if scope == "project" {
		project = document
		user, err = readWorkbenchDocument(UserConfigPath())
	} else if projectDir != "" {
		project, err = readWorkbenchDocument(ProjectConfigPath(projectDir))
	}
	if err != nil {
		return WorkbenchSettingsSnapshot{}, err
	}
	if raw, changed := validated["model_profile"]; changed && !bytes.Equal(raw, []byte("null")) {
		if err := validateWorkbenchProfile(user, project, scope, raw); err != nil {
			return WorkbenchSettingsSnapshot{}, err
		}
	}
	snapshot, err := buildWorkbenchSnapshot(scope, user, project)
	if err != nil {
		return WorkbenchSettingsSnapshot{}, err
	}
	if err := store.writeContext(ctx, encoded, revision); err != nil {
		return WorkbenchSettingsSnapshot{}, err
	}
	return snapshot, nil
}

func validateSettingsScope(projectDir, scope string) error {
	if scope != "user" && scope != "project" {
		return ErrSettingsInvalid
	}
	if scope == "user" && projectDir == "" {
		return nil
	}
	if !filepath.IsAbs(projectDir) || strings.IndexByte(projectDir, 0) >= 0 {
		return ErrSettingsInvalid
	}
	return nil
}

type workbenchDocument struct {
	raw    []byte
	fields map[string]json.RawMessage
}

func (d workbenchDocument) revision() string {
	sum := sha256.Sum256(d.raw)
	return hex.EncodeToString(sum[:])
}

func buildWorkbenchSnapshot(scope string, user, project workbenchDocument) (WorkbenchSettingsSnapshot, error) {
	userConfig, err := decodeWorkbenchConfig(user.fields, false)
	if err != nil {
		return WorkbenchSettingsSnapshot{}, err
	}
	projectConfig, err := decodeWorkbenchConfig(project.fields, true)
	if err != nil {
		return WorkbenchSettingsSnapshot{}, err
	}
	effective := MergeConfigs(userConfig, projectConfig)
	selected := user
	if scope == "project" {
		selected = project
	}
	result := WorkbenchSettingsSnapshot{Scope: scope, Revision: selected.revision(), Fields: SettingsSchema()}
	definitions := workbenchSettingDefinitions()
	for key, definition := range definitions {
		stored := json.RawMessage("null")
		if raw, ok := selected.fields[key]; ok {
			stored = safeWorkbenchValue(key, raw)
		}
		value, _ := json.Marshal(definition.value(effective))
		source := "default"
		if workbenchFieldOverrides(key, user.fields, false) {
			source = "user"
		}
		if workbenchFieldOverrides(key, project.fields, true) {
			source = "project"
		}
		field := result.Fields[key]
		field.StoredValue, field.EffectiveValue = stored, safeWorkbenchValue(key, value)
		field.Source, field.Overridden = source, scope == "user" && source == "project"
		if key == "model_profile" {
			for _, conflict := range workbenchLegacySelectionConflicts(selected.fields) {
				if _, editable := definitions[conflict]; !editable {
					// Expose only names: routing values and credentials stay private.
					field.BlockedBy = append(field.BlockedBy, conflict)
				}
			}
		}
		if key == "model_profile" && userConfig != nil {
			for name, profile := range userConfig.ModelProfiles {
				if portfolioIDPattern.MatchString(name) && (profile.Enabled == nil || *profile.Enabled) && (scope == "user" || profile.ProjectSelectable) {
					field.Options = append(field.Options, name)
				}
			}
			sort.Strings(field.Options)
		}
		result.Fields[key] = field
	}
	return result, nil
}

func validateWorkbenchProfile(user, project workbenchDocument, scope string, raw json.RawMessage) error {
	userConfig, err := decodeWorkbenchConfig(user.fields, false)
	if err != nil {
		return err
	}
	projectConfig, err := decodeWorkbenchConfig(project.fields, true)
	if err != nil {
		return err
	}
	var name string
	if json.Unmarshal(raw, &name) != nil || userConfig == nil {
		return fmt.Errorf("%w: model profile is unavailable", ErrSettingsInvalid)
	}
	profile, exists := userConfig.ModelProfiles[name]
	if !exists || (scope == "project" && !profile.ProjectSelectable) {
		return fmt.Errorf("%w: model profile is unavailable for this scope", ErrSettingsInvalid)
	}
	_, err = CompilePortfolio(PortfolioCompileInput{Sources: &ConfigSources{User: userConfig, Project: projectConfig, Effective: MergeConfigs(userConfig, projectConfig)}, ExplicitModelProfile: name, Getenv: func(string) string { return "" }})
	if err != nil {
		return fmt.Errorf("%w: model profile configuration is invalid", ErrSettingsInvalid)
	}
	return nil
}

func workbenchFieldOverrides(key string, fields map[string]json.RawMessage, project bool) bool {
	raw, ok := fields[key]
	if !ok {
		return false
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		// Existing files decode explicit null into zero/false while retaining
		// presence for these non-pointer fields. Match startup provenance;
		// editor reset uses deletion rather than writing a JSON null.
		return key == "max_turns" || key == "reduced_motion"
	}
	if key == "model_profile" && project {
		for forbidden := range forbiddenProjectPortfolioKeys {
			if _, exists := fields[forbidden]; exists {
				return false
			}
		}
	}
	var text string
	if json.Unmarshal(raw, &text) == nil && text == "" {
		return false
	}
	return true
}

func decodeWorkbenchConfig(fields map[string]json.RawMessage, project bool) (*Config, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	copyFields := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		copyFields[key] = value
	}
	if project {
		delete(copyFields, "sandbox")
		for forbidden := range forbiddenProjectPortfolioKeys {
			if _, exists := copyFields[forbidden]; exists {
				delete(copyFields, "model_profile")
			}
			delete(copyFields, forbidden)
		}
	}
	data, err := json.Marshal(copyFields)
	if err != nil {
		return nil, ErrSettingsInvalid
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("%w: stored configuration cannot be decoded", ErrSettingsInvalid)
	}
	if err := validateGoalConfig(&config); err != nil {
		return nil, fmt.Errorf("%w: stored goal configuration is invalid", ErrSettingsInvalid)
	}
	return &config, nil
}

func safeWorkbenchValue(key string, raw json.RawMessage) json.RawMessage {
	if key == "api_base_url" {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return json.RawMessage("null")
		}
		if text == "" {
			return json.RawMessage(`""`)
		}
		parsed, err := url.Parse(text)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
			return json.RawMessage("null")
		}
		parsed.User, parsed.RawQuery, parsed.Fragment, parsed.RawFragment, parsed.ForceQuery = nil, "", "", "", false
		data, _ := json.Marshal(parsed.String())
		return data
	}
	return append(json.RawMessage(nil), raw...)
}

func validateWorkbenchSetting(key string, raw json.RawMessage) (json.RawMessage, error) {
	definition, ok := workbenchSettingDefinitions()[key]
	if !ok {
		return nil, fmt.Errorf("%w: unsupported field", ErrSettingsInvalid)
	}
	if len(raw) > 4096 || !json.Valid(raw) {
		return nil, fmt.Errorf("%w: %s has an invalid value", ErrSettingsInvalid, key)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return json.RawMessage("null"), nil
	}
	switch definition.kind {
	case "boolean":
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("%w: %s requires a boolean", ErrSettingsInvalid, key)
		}
		return json.Marshal(value)
	case "integer":
		var value int
		if err := json.Unmarshal(raw, &value); err != nil || value < 0 {
			return nil, fmt.Errorf("%w: %s requires a non-negative integer", ErrSettingsInvalid, key)
		}
		return json.Marshal(value)
	default:
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("%w: %s requires text", ErrSettingsInvalid, key)
		}
		if strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("%w: %s has invalid text", ErrSettingsInvalid, key)
		}
		value = strings.TrimSpace(value)
		limit := 512
		if key == "api_base_url" {
			limit = 2048
		}
		if value == "" || len(value) > limit || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("%w: %s has invalid text", ErrSettingsInvalid, key)
		}
		if len(definition.options) > 0 {
			found := false
			for _, option := range definition.options {
				if value == option {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("%w: %s has an unsupported choice", ErrSettingsInvalid, key)
			}
		}
		if key == "api_base_url" {
			parsed, err := url.Parse(value)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
				return nil, fmt.Errorf("%w: endpoint must be HTTP(S) without credentials, query, or fragment", ErrSettingsInvalid)
			}
		}
		return json.Marshal(value)
	}
}

func workbenchLegacySelectionKeys() []string {
	return []string{"provider", "model", "api_base_url", "fallback_model"}
}

// Keep metadata and validation on the same source-layer presence predicate.
func workbenchLegacySelectionConflicts(fields map[string]json.RawMessage) []string {
	var conflicts []string
	for _, key := range workbenchLegacySelectionKeys() {
		if raw, ok := fields[key]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			var text string
			if json.Unmarshal(raw, &text) == nil && text == "" {
				continue
			}
			conflicts = append(conflicts, key)
		}
	}
	return conflicts
}

func validateWorkbenchSelection(fields map[string]json.RawMessage, scope string) error {
	var profile string
	_ = json.Unmarshal(fields["model_profile"], &profile)
	if profile == "" {
		return nil
	}
	if len(workbenchLegacySelectionConflicts(fields)) > 0 {
		return fmt.Errorf("%w: model_profile cannot be combined with legacy provider fields", ErrSettingsInvalid)
	}
	if scope == "project" {
		for key := range forbiddenProjectPortfolioKeys {
			if _, ok := fields[key]; ok {
				return fmt.Errorf("%w: project portfolio definitions must be configured by the user", ErrSettingsInvalid)
			}
		}
	}
	return nil
}

type workbenchSettingsStore struct {
	path         string
	filename     string
	parent, root *os.Root
	info         os.FileInfo
	lockFile     *os.File
}

func openWorkbenchSettingsStore(path string, create bool) (*workbenchSettingsStore, error) {
	filename := filepath.Base(path)
	if !filepath.IsAbs(path) || (filename != "settings.json" && filename != "settings.local.json") {
		return nil, ErrSettingsUnsafePath
	}
	directory := filepath.Dir(path)
	parent, err := os.OpenRoot(filepath.Dir(directory))
	if err != nil {
		return nil, ErrSettingsUnsafePath
	}
	name := filepath.Base(directory)
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) && create {
		err = parent.Mkdir(name, 0o700)
		if err == nil || errors.Is(err, os.ErrExist) {
			info, err = parent.Lstat(name)
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		_ = parent.Close()
		return nil, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		_ = parent.Close()
		return nil, ErrSettingsUnsafePath
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		_ = parent.Close()
		return nil, ErrSettingsUnsafePath
	}
	store := &workbenchSettingsStore{path: directory, filename: filename, parent: parent, root: root, info: info}
	if err := store.revalidate(); err != nil {
		store.close()
		return nil, err
	}
	return store, nil
}

func (s *workbenchSettingsStore) close() {
	s.unlock()
	_ = s.root.Close()
	_ = s.parent.Close()
}

func (s *workbenchSettingsStore) revalidate() error {
	opened, err := s.root.Stat(".")
	current, currentErr := s.parent.Lstat(filepath.Base(s.path))
	absolute, absoluteErr := os.Lstat(s.path)
	if err != nil || currentErr != nil || absoluteErr != nil || !os.SameFile(s.info, opened) || !os.SameFile(s.info, current) || !os.SameFile(s.info, absolute) {
		return ErrSettingsUnsafePath
	}
	if s.lockFile != nil {
		return s.revalidateLockFile(s.lockFile)
	}
	return nil
}

func readWorkbenchDocument(path string) (workbenchDocument, error) {
	store, err := openWorkbenchSettingsStore(path, false)
	if err != nil {
		return workbenchDocument{}, err
	}
	if store == nil {
		return workbenchDocument{fields: make(map[string]json.RawMessage)}, nil
	}
	defer store.close()
	return store.read()
}

func (s *workbenchSettingsStore) read() (workbenchDocument, error) {
	if err := s.revalidate(); err != nil {
		return workbenchDocument{}, err
	}
	info, err := s.root.Lstat(s.filename)
	if errors.Is(err, os.ErrNotExist) {
		return workbenchDocument{fields: make(map[string]json.RawMessage)}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > settingsDocumentLimit {
		return workbenchDocument{}, ErrSettingsUnsafePath
	}
	file, err := s.root.Open(s.filename)
	if err != nil {
		return workbenchDocument{}, ErrSettingsUnsafePath
	}
	defer file.Close() //nolint:errcheck
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return workbenchDocument{}, ErrSettingsUnsafePath
	}
	data, err := io.ReadAll(io.LimitReader(file, settingsDocumentLimit+1))
	if err != nil || len(data) > settingsDocumentLimit {
		return workbenchDocument{}, ErrSettingsUnsafePath
	}
	current, err := s.root.Lstat(s.filename)
	if err != nil || !os.SameFile(opened, current) {
		return workbenchDocument{}, ErrSettingsConflict
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return workbenchDocument{}, fmt.Errorf("%w: settings must be a JSON object", ErrSettingsInvalid)
	}
	return workbenchDocument{raw: data, fields: fields}, nil
}

func (s *workbenchSettingsStore) lock() error {
	return s.lockContext(context.Background())
}

func (s *workbenchSettingsStore) lockContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.lockFile != nil {
		return ErrSettingsBusy
	}
	file, err := s.openLockFile()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(time.Second)
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return err
		}
		if err := s.revalidate(); err != nil {
			_ = file.Close()
			return err
		}
		if err := s.revalidateLockFile(file); err != nil {
			_ = file.Close()
			return err
		}
		locked, err := tryLockWorkbenchSettings(file)
		if err != nil {
			_ = file.Close()
			return ErrSettingsUnsafePath
		}
		if locked {
			s.lockFile = file
			if err := s.revalidate(); err != nil {
				s.unlock()
				return err
			}
			return nil
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return ErrSettingsBusy
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// The guard inode stays in place after unlock, so existing waiters and new
// writers always contend on the same OS lock. Process exit releases the lock.
func (s *workbenchSettingsStore) unlock() {
	if s.lockFile == nil {
		return
	}
	_ = unlockWorkbenchSettings(s.lockFile)
	_ = s.lockFile.Close()
	s.lockFile = nil
}

func (s *workbenchSettingsStore) openLockFile() (*os.File, error) {
	const name = ".settings-write.lock"
	if err := s.revalidate(); err != nil {
		return nil, err
	}
	// O_EXCL prevents a raced-in symlink from creating its target. Existing
	// files are opened without O_CREATE and rechecked before applying a lock.
	for attempt := 0; attempt < 2; attempt++ {
		info, err := s.root.Lstat(name)
		flags := os.O_RDWR
		if errors.Is(err, os.ErrNotExist) {
			flags |= os.O_CREATE | os.O_EXCL
		} else if err != nil || !info.Mode().IsRegular() {
			return nil, ErrSettingsUnsafePath
		}
		file, err := s.root.OpenFile(name, flags, 0o600)
		if errors.Is(err, os.ErrExist) && flags&os.O_EXCL != 0 {
			continue
		}
		if err != nil {
			return nil, ErrSettingsUnsafePath
		}
		if err := s.revalidateLockFile(file); err != nil {
			_ = file.Close()
			return nil, err
		}
		return file, nil
	}
	return nil, ErrSettingsUnsafePath
}

func (s *workbenchSettingsStore) revalidateLockFile(file *os.File) error {
	opened, err := file.Stat()
	current, currentErr := s.root.Lstat(".settings-write.lock")
	if err != nil || currentErr != nil || !opened.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return ErrSettingsUnsafePath
	}
	return nil
}

func (s *workbenchSettingsStore) write(data []byte, revision string) error {
	return s.writeContext(context.Background(), data, revision)
}

func (s *workbenchSettingsStore) writeContext(ctx context.Context, data []byte, revision string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ErrSettingsUnsafePath
	}
	name := ".settings-" + hex.EncodeToString(nonce[:]) + ".tmp"
	file, err := s.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ErrSettingsUnsafePath
	}
	defer s.root.Remove(name) //nolint:errcheck
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return ErrSettingsUnsafePath
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return ErrSettingsUnsafePath
	}
	if err := file.Close(); err != nil {
		return ErrSettingsUnsafePath
	}
	current, err := s.read()
	if err != nil {
		return err
	}
	if current.revision() != revision {
		return ErrSettingsConflict
	}
	if err := s.revalidate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.root.Rename(name, s.filename); err != nil {
		return ErrSettingsUnsafePath
	}
	return s.revalidate()
}

// UpdateSettingsDocument is a host-only transaction for specialized settings
// owners, such as permission rules. The callback runs under the same process-
// shared lock as SettingsPatch and must only mutate its supplied document.
// Unknown values retain their JSON representation. Renderers must use the
// schema-restricted SettingsPatch API; they never supply this path or callback.
func UpdateSettingsDocument(path string, update func(map[string]json.RawMessage) (bool, error)) error {
	_, err := updateSettingsDocument(context.Background(), path, "", update)
	return err
}

// ReadSettingsDocument is a host-only, read-only seam for specialized owners.
// It shares the bounded, identity-checked reader with SettingsSnapshot. Its
// detached document may contain secrets and must never be sent to a renderer.
func ReadSettingsDocument(path string) (map[string]json.RawMessage, string, error) {
	document, err := readWorkbenchDocument(path)
	if err != nil {
		return nil, "", err
	}
	return document.fields, document.revision(), nil
}

// UpdateSettingsDocumentRevision admits a specialized mutation only if the
// complete document still matches its inspected revision under the shared OS
// lock. Paths and callbacks remain host-owned, not renderer input.
func UpdateSettingsDocumentRevision(path, revision string, update func(map[string]json.RawMessage) (bool, error)) (string, error) {
	return UpdateSettingsDocumentRevisionContext(context.Background(), path, revision, update)
}

// UpdateSettingsDocumentRevisionContext also cancels OS-lock waiting and checks
// cancellation before committing. Cancellation after the final commit check
// does not roll back a saved document or turn success into an unchanged claim.
func UpdateSettingsDocumentRevisionContext(ctx context.Context, path, revision string, update func(map[string]json.RawMessage) (bool, error)) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(revision) != sha256.Size*2 {
		return "", ErrSettingsInvalid
	}
	if _, err := hex.DecodeString(revision); err != nil {
		return "", ErrSettingsInvalid
	}
	return updateSettingsDocument(ctx, path, revision, update)
}

func updateSettingsDocument(ctx context.Context, path, revision string, update func(map[string]json.RawMessage) (bool, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if update == nil {
		return "", ErrSettingsInvalid
	}
	store, err := openWorkbenchSettingsStore(path, true)
	if err != nil {
		return "", err
	}
	defer store.close()
	if err := store.lockContext(ctx); err != nil {
		return "", err
	}
	defer store.unlock()
	document, err := store.read()
	if err != nil {
		return "", err
	}
	if revision != "" && document.revision() != revision {
		return "", ErrSettingsConflict
	}
	changed, err := update(document.fields)
	if err != nil || !changed {
		return document.revision(), err
	}
	encoded, err := json.MarshalIndent(document.fields, "", "  ")
	if err != nil || len(encoded)+1 > settingsDocumentLimit {
		return "", ErrSettingsInvalid
	}
	encoded = append(encoded, '\n')
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := store.writeContext(ctx, encoded, document.revision()); err != nil {
		return "", err
	}
	return (workbenchDocument{raw: encoded}).revision(), nil
}
