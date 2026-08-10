package permission

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/abietic/yhc/internal/statemigration"
)

// ApprovalKey identifies a specific tool invocation pattern that has been approved.
// For bash commands, CommandPattern holds the command prefix.
// For file operations, PathPattern holds the path or directory.
// Mirrors classifierApprovals.ts approval tracking structure.
type ApprovalKey struct {
	ToolName         string
	CommandPattern   string // for bash commands — the command prefix
	PathPattern      string // for file operations — the path or directory
	InputFingerprint string // canonical JSON for tools without a path/command scope
	ExactCommand     bool
	ExactPath        bool
	RecursivePath    bool
}

// ApprovalEntry records a single approval decision.
type ApprovalEntry struct {
	Key           ApprovalKey
	ApprovedAt    time.Time
	Reason        string // "user", "classifier", "rule"
	SessionScoped bool   // only valid for one root-session lineage
	RootSessionID string // empty for persistent and legacy session approvals
}

// ApprovalTracker tracks which tool invocations have been approved for auto-mode.
// Thread-safe for concurrent access.
// Mirrors classifierApprovals.ts CLASSIFIER_APPROVALS map.
type ApprovalTracker struct {
	approvals map[string]*ApprovalEntry
	mu        sync.RWMutex
}

// NewApprovalTracker creates a new empty approval tracker.
func NewApprovalTracker() *ApprovalTracker {
	return &ApprovalTracker{
		approvals: make(map[string]*ApprovalEntry),
	}
}

// Approve records an approval for the given key.
func (t *ApprovalTracker) Approve(key ApprovalKey, reason string, sessionScoped bool) {
	t.approve(key, reason, sessionScoped, "")
}

// ApproveAndSave installs one persistent approval only when its canonical
// private store commits successfully. A failed write restores the exact prior
// in-memory entry so persistence failure never grants new authority.
func (t *ApprovalTracker) ApproveAndSave(key ApprovalKey, reason, path string) error {
	if !key.IsScoped() {
		return errors.New("cannot persist an unscoped approval")
	}
	mapKey := makeKey(key, "")
	entry := &ApprovalEntry{
		Key:        key,
		ApprovedAt: time.Now().UTC(),
		Reason:     reason,
	}
	t.mu.Lock()
	previous, existed := t.approvals[mapKey]
	t.approvals[mapKey] = entry
	t.mu.Unlock()
	if err := t.SaveTo(path); err != nil {
		t.mu.Lock()
		if t.approvals[mapKey] == entry {
			if existed {
				t.approvals[mapKey] = previous
			} else {
				delete(t.approvals, mapKey)
			}
		}
		t.mu.Unlock()
		return err
	}
	return nil
}

// ApproveForRootSession records an in-memory approval scoped to one explicit
// root-session lineage. Parent and child engines share the same root ID.
func (t *ApprovalTracker) ApproveForRootSession(key ApprovalKey, reason, rootSessionID string) {
	t.approve(key, reason, true, strings.TrimSpace(rootSessionID))
}

func (t *ApprovalTracker) approve(key ApprovalKey, reason string, sessionScoped bool, rootSessionID string) {
	if !key.IsScoped() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	k := makeKey(key, rootSessionID)
	t.approvals[k] = &ApprovalEntry{
		Key:           key,
		ApprovedAt:    time.Now(),
		Reason:        reason,
		SessionScoped: sessionScoped,
		RootSessionID: rootSessionID,
	}
}

// IsApproved checks if there's a matching approval for the given tool invocation.
// For bash commands: matches if the command starts with an approved command prefix.
// For file operations: matches if the path is under an approved directory.
func (t *ApprovalTracker) IsApproved(toolName, command, path string) bool {
	return t.IsApprovedInvocation(toolName, command, path, "")
}

// IsApprovedInvocation checks a complete, parameter-scoped tool invocation.
func (t *ApprovalTracker) IsApprovedInvocation(toolName, command, path, inputFingerprint string) bool {
	return t.isApprovedInvocation(toolName, command, path, inputFingerprint, "")
}

// IsApprovedInvocationForRootSession checks persistent approvals plus session
// approvals belonging to the given root-session lineage.
func (t *ApprovalTracker) IsApprovedInvocationForRootSession(toolName, command, path, inputFingerprint, rootSessionID string) bool {
	return t.isApprovedInvocation(toolName, command, path, inputFingerprint, strings.TrimSpace(rootSessionID))
}

func (t *ApprovalTracker) isApprovedInvocation(toolName, command, path, inputFingerprint, rootSessionID string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	for _, entry := range t.approvals {
		if entry.SessionScoped && entry.RootSessionID != rootSessionID {
			continue
		}
		if entry.Key.MatchesInvocation(toolName, command, path, inputFingerprint) {
			return true
		}
	}

	return false
}

// MatchesInvocation reports whether this approval key matches the given tool
// invocation. It matches only when the tool names are equal and one of the
// key's command, path, or fingerprint constraints is satisfied.
func (key ApprovalKey) MatchesInvocation(toolName, command, path, inputFingerprint string) bool {
	if key.ToolName != toolName {
		return false
	}

	// For bash-style tools: match command prefix
	if key.CommandPattern != "" && command != "" {
		if (key.ExactCommand && key.CommandPattern == command) ||
			(!key.ExactCommand && matchCommandPrefix(key.CommandPattern, command)) {
			return true
		}
	}

	// For file operation tools: match path or parent directory
	if key.PathPattern != "" && path != "" {
		if (key.RecursivePath && matchPathPattern(key.PathPattern, path)) ||
			(key.ExactPath && filepath.Clean(key.PathPattern) == filepath.Clean(path)) {
			return true
		}
	}

	if key.InputFingerprint != "" && key.InputFingerprint == inputFingerprint {
		return true
	}

	return false
}

// IsScoped reports whether the key constrains approval to invocation parameters.
func (key ApprovalKey) IsScoped() bool {
	return key.ToolName != "" && (key.CommandPattern != "" || key.PathPattern != "" || key.InputFingerprint != "")
}

// Revoke removes all approvals for the given tool name.
func (t *ApprovalTracker) Revoke(toolName string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for k, entry := range t.approvals {
		if entry.Key.ToolName == toolName {
			delete(t.approvals, k)
		}
	}
}

// RevokeAll clears all approvals.
func (t *ApprovalTracker) RevokeAll() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.approvals = make(map[string]*ApprovalEntry)
}

// Count returns the number of active approvals.
func (t *ApprovalTracker) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return len(t.approvals)
}

// List returns a snapshot of all current approvals.
func (t *ApprovalTracker) List() []ApprovalEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()

	entries := make([]ApprovalEntry, 0, len(t.approvals))
	for _, entry := range t.approvals {
		entries = append(entries, *entry)
	}
	return entries
}

// makeKey creates a string key for map lookup from an ApprovalKey.
func makeKey(key ApprovalKey, rootSessionID string) string {
	// Use a separator unlikely to appear in tool names, commands, or paths.
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%t\x00%t\x00%t\x00%s", key.ToolName, key.CommandPattern, key.PathPattern, key.InputFingerprint, key.ExactCommand, key.ExactPath, key.RecursivePath, rootSessionID)
}

// persistedApproval is the JSON-serializable form of an approval entry.
type persistedApproval struct {
	ToolName         string `json:"tool_name"`
	CommandPattern   string `json:"command_pattern,omitempty"`
	PathPattern      string `json:"path_pattern,omitempty"`
	InputFingerprint string `json:"input_fingerprint,omitempty"`
	ExactCommand     bool   `json:"exact_command,omitempty"`
	ExactPath        bool   `json:"exact_path,omitempty"`
	RecursivePath    bool   `json:"recursive_path,omitempty"`
	ApprovedAt       string `json:"approved_at"`
	Reason           string `json:"reason"`
}

// SaveTo persists non-session-scoped approvals to a JSON file.
// Creates parent directories as needed.
// Mirrors classifierApprovals.ts persistence.
func (t *ApprovalTracker) SaveTo(path string) error {
	t.mu.RLock()
	entries := make([]persistedApproval, 0, len(t.approvals))
	for _, entry := range t.approvals {
		if entry.SessionScoped {
			continue
		}
		entries = append(entries, persistedApproval{
			ToolName:         entry.Key.ToolName,
			CommandPattern:   entry.Key.CommandPattern,
			PathPattern:      entry.Key.PathPattern,
			InputFingerprint: entry.Key.InputFingerprint,
			ExactCommand:     entry.Key.ExactCommand,
			ExactPath:        entry.Key.ExactPath,
			RecursivePath:    entry.Key.RecursivePath,
			ApprovedAt:       entry.ApprovedAt.Format(time.RFC3339),
			Reason:           entry.Reason,
		})
	}
	t.mu.RUnlock()

	sort.Slice(entries, func(i, j int) bool {
		left, _ := json.Marshal(entries[i])
		right, _ := json.Marshal(entries[j])
		return bytes.Compare(left, right) < 0
	})
	data, err := marshalPersistedApprovals(entries)
	if err != nil {
		return fmt.Errorf("marshal approvals: %w", err)
	}
	projectRoot, err := approvalProjectRoot(filepath.Dir(filepath.Dir(path)))
	if err != nil {
		return fmt.Errorf("validate approvals: %w", err)
	}
	if _, err := parsePersistedApprovals(data, projectRoot); err != nil {
		return fmt.Errorf("validate approvals: %w", err)
	}
	if err := atomicWritePrivateApprovalFile(path, data); err != nil {
		return fmt.Errorf("write approvals: %w", err)
	}
	return nil
}

// LoadFrom reads persisted approvals from a JSON file.
// Returns nil error if the file does not exist.
func (t *ApprovalTracker) LoadFrom(path string) error {
	data, exists, err := readPrivateApprovalFile(path)
	if err != nil {
		return fmt.Errorf("read approvals: %w", err)
	}
	if !exists {
		return nil
	}

	projectRoot, err := approvalProjectRoot(filepath.Dir(filepath.Dir(path)))
	if err != nil {
		return fmt.Errorf("validate approvals: %w", err)
	}
	entries, err := parsePersistedApprovals(data, projectRoot)
	if err != nil {
		return fmt.Errorf("parse approvals: %w", err)
	}
	loaded := make(map[string]*ApprovalEntry, len(entries))
	for _, entry := range entries {
		approvedAt, _ := time.Parse(time.RFC3339, entry.ApprovedAt)
		key := ApprovalKey{
			ToolName:         entry.ToolName,
			CommandPattern:   entry.CommandPattern,
			PathPattern:      entry.PathPattern,
			InputFingerprint: entry.InputFingerprint,
			ExactCommand:     entry.ExactCommand,
			ExactPath:        entry.ExactPath,
			RecursivePath:    entry.RecursivePath,
		}
		loaded[makeKey(key, "")] = &ApprovalEntry{Key: key, ApprovedAt: approvedAt, Reason: entry.Reason}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	for key, entry := range loaded {
		t.approvals[key] = entry
	}
	return nil
}

func readPrivateApprovalFile(path string) ([]byte, bool, error) {
	store, exists, err := statemigration.OpenCanonicalStore(filepath.Dir(path), ".", false)
	if err != nil || !exists {
		return nil, false, err
	}
	defer store.Close() //nolint:errcheck
	file, info, exists, err := store.OpenRegular(filepath.Base(path), os.O_RDONLY, false)
	if err != nil || !exists {
		return nil, false, err
	}
	if info.Size() < 0 || info.Size() > approvalMigrationMaxBytes {
		_ = file.Close()
		return nil, false, errors.New("approval file exceeds bound")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, approvalMigrationMaxBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(data)) != info.Size() ||
		len(data) > approvalMigrationMaxBytes || store.ValidateRegular(filepath.Base(path), info) != nil {
		return nil, false, errors.New("approval file is invalid")
	}
	return data, true, nil
}

func marshalPersistedApprovals(entries []persistedApproval) ([]byte, error) {
	return json.MarshalIndent(entries, "", "  ")
}

func atomicWritePrivateApprovalFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	store, exists, err := statemigration.OpenCanonicalStore(dir, ".", true)
	if err != nil || !exists {
		return errors.New("approval state root is invalid")
	}
	defer store.Close() //nolint:errcheck
	target := filepath.Base(path)
	var (
		tmp     *os.File
		tmpInfo os.FileInfo
		tmpName string
	)
	for range 16 {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return errors.New("approval staging failed")
		}
		tmpName = ".approvals-" + hex.EncodeToString(token[:])
		tmp, tmpInfo, _, err = store.OpenRegular(tmpName, os.O_WRONLY, true)
		if err == nil {
			break
		}
	}
	if tmp == nil || tmpInfo == nil {
		return errors.New("approval staging failed")
	}
	defer store.RemoveRegularIfSame(tmpName, tmpInfo)
	written, writeErr := tmp.Write(data)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if writeErr != nil || written != len(data) || syncErr != nil || closeErr != nil ||
		store.ValidateRegular(tmpName, tmpInfo) != nil {
		return errors.New("approval staging failed")
	}
	if err := store.PromoteRegular(tmpName, target, tmpInfo); err != nil {
		return errors.New("approval promotion failed")
	}
	return nil
}

func parsePersistedApprovals(data []byte, projectRoot string) ([]persistedApproval, error) {
	if len(data) > approvalMigrationMaxBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("approval data is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('[') {
		return nil, errors.New("approval data is invalid")
	}
	entries := make([]persistedApproval, 0)
	seen := make(map[string]struct{})
	for decoder.More() {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, errors.New("approval data is invalid")
		}
		entry, err := decodePersistedApproval(raw, projectRoot)
		if err != nil {
			return nil, err
		}
		key := makeKey(ApprovalKey{ToolName: entry.ToolName, CommandPattern: entry.CommandPattern, PathPattern: entry.PathPattern, InputFingerprint: entry.InputFingerprint, ExactCommand: entry.ExactCommand, ExactPath: entry.ExactPath, RecursivePath: entry.RecursivePath}, "")
		if _, duplicate := seen[key]; duplicate {
			return nil, errors.New("approval data is invalid")
		}
		seen[key] = struct{}{}
		entries = append(entries, entry)
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim(']') {
		return nil, errors.New("approval data is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("approval data is invalid")
	}
	return entries, nil
}

func decodePersistedApproval(raw json.RawMessage, projectRoot string) (persistedApproval, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return persistedApproval{}, errors.New("approval entry is invalid")
	}
	allowed := map[string]bool{"tool_name": true, "command_pattern": true, "path_pattern": true, "input_fingerprint": true, "exact_command": true, "exact_path": true, "recursive_path": true, "approved_at": true, "reason": true}
	values := make(map[string]json.RawMessage)
	for decoder.More() {
		name, err := decoder.Token()
		if err != nil {
			return persistedApproval{}, errors.New("approval entry is invalid")
		}
		key, ok := name.(string)
		if !ok || !allowed[key] || values[key] != nil {
			return persistedApproval{}, errors.New("approval entry is invalid")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return persistedApproval{}, errors.New("approval entry is invalid")
		}
		values[key] = value
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return persistedApproval{}, errors.New("approval entry is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return persistedApproval{}, errors.New("approval entry is invalid")
	}
	decodeString := func(name string, required bool, maxBytes int) (string, error) {
		raw, ok := values[name]
		if !ok {
			if required {
				return "", errors.New("approval entry is invalid")
			}
			return "", nil
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || value == "" ||
			len(value) > maxBytes || !safeApprovalString(value) {
			return "", errors.New("approval entry is invalid")
		}
		return value, nil
	}
	decodeBool := func(name string) (bool, bool, error) {
		raw, ok := values[name]
		if !ok {
			return false, false, nil
		}
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return false, false, errors.New("approval entry is invalid")
		}
		return value, true, nil
	}
	entry := persistedApproval{}
	if entry.ToolName, err = decodeString("tool_name", true, 128); err != nil {
		return persistedApproval{}, err
	}
	if entry.CommandPattern, err = decodeString("command_pattern", false, 16<<10); err != nil {
		return persistedApproval{}, err
	}
	if entry.PathPattern, err = decodeString("path_pattern", false, 16<<10); err != nil {
		return persistedApproval{}, err
	}
	if entry.InputFingerprint, err = decodeString("input_fingerprint", false, 512); err != nil {
		return persistedApproval{}, err
	}
	if entry.ApprovedAt, err = decodeString("approved_at", true, 64); err != nil {
		return persistedApproval{}, err
	}
	if entry.Reason, err = decodeString("reason", true, 256); err != nil {
		return persistedApproval{}, err
	}
	if entry.ExactCommand, _, err = decodeBool("exact_command"); err != nil {
		return persistedApproval{}, err
	}
	var exactPathPresent bool
	if entry.ExactPath, exactPathPresent, err = decodeBool("exact_path"); err != nil {
		return persistedApproval{}, err
	}
	var recursivePathPresent bool
	if entry.RecursivePath, recursivePathPresent, err = decodeBool("recursive_path"); err != nil {
		return persistedApproval{}, err
	}
	if entry.PathPattern != "" && !exactPathPresent && !recursivePathPresent {
		// Legacy path approvals were recursive and predate explicit scope flags.
		entry.RecursivePath = true
	}
	if approvedAt, err := time.Parse(time.RFC3339, entry.ApprovedAt); err != nil || approvedAt.IsZero() {
		return persistedApproval{}, errors.New("approval timestamp is invalid")
	}
	if !validApprovalScope(entry, values, projectRoot) {
		return persistedApproval{}, errors.New("approval scope is invalid")
	}
	if approvalContainsCredential(entry) {
		return persistedApproval{}, errors.New("approval content is invalid")
	}
	return entry, nil
}

func validApprovalScope(entry persistedApproval, raw map[string]json.RawMessage, projectRoot string) bool {
	scopes := 0
	if entry.CommandPattern != "" {
		scopes++
	}
	if entry.PathPattern != "" {
		scopes++
	}
	if entry.InputFingerprint != "" {
		scopes++
	}
	if scopes != 1 {
		return false
	}
	if entry.CommandPattern != "" {
		return raw["exact_path"] == nil && raw["recursive_path"] == nil
	}
	if entry.InputFingerprint != "" {
		return raw["exact_command"] == nil && raw["exact_path"] == nil && raw["recursive_path"] == nil
	}
	_, exactPathPresent := raw["exact_path"]
	_, recursivePathPresent := raw["recursive_path"]
	if raw["exact_command"] != nil || (exactPathPresent || recursivePathPresent) &&
		(entry.ExactPath == entry.RecursivePath) {
		return false
	}
	return approvalPathWithinProject(entry.PathPattern, projectRoot)
}

func approvalContainsCredential(entry persistedApproval) bool {
	command := strings.ToLower(entry.CommandPattern)
	if sensitiveApprovalShellVariableRE.MatchString(entry.CommandPattern) ||
		sensitiveApprovalAssignmentRE.MatchString(entry.CommandPattern) {
		return true
	}
	value := strings.Join([]string{entry.ToolName, entry.CommandPattern, entry.InputFingerprint, entry.Reason}, "\n")
	if sensitiveApprovalTermRE.MatchString(value) || highConfidenceApprovalCredentialRE.MatchString(value) ||
		strings.Contains(command, ".env") || strings.Contains(command, "/.ssh") ||
		strings.Contains(command, ".netrc") || strings.Contains(command, ".kube") ||
		strings.Contains(command, "aws/credentials") || strings.Contains(command, "gcloud") {
		return true
	}
	path := strings.ToLower(filepath.ToSlash(entry.PathPattern))
	for _, segment := range strings.Split(path, "/") {
		switch segment {
		case ".env", ".ssh", ".netrc", ".kube", ".aws", "credentials", "gcloud", "aws":
			return true
		}
	}
	return strings.Contains(path, "/credentials/") || strings.Contains(path, "/.ssh/") || strings.Contains(path, "/.aws/")
}

var (
	sensitiveApprovalShellVariableRE   = regexp.MustCompile(`(?i)\$(?:\{)?[A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|PASSWD|API[_-]?KEY|PRIVATE[_-]?KEY|CREDENTIAL)[A-Z0-9_]*(?:\})?`)
	sensitiveApprovalAssignmentRE      = regexp.MustCompile(`(?i)(?:^|[;&|[:space:]])(?:export[[:space:]]+)?[A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|PASSWD|API[_-]?KEY|PRIVATE[_-]?KEY|CREDENTIAL)[A-Z0-9_]*[[:space:]]*=`)
	sensitiveApprovalTermRE            = regexp.MustCompile(`(?i)(^|[^a-z0-9])(token|password|passwd|credential|secret|api[_-]?key|private[_-]?key)([^a-z0-9]|$)`)
	highConfidenceApprovalCredentialRE = regexp.MustCompile(
		`(?i)(gh[pousr]_[a-z0-9]{20,}|sk-[a-z0-9]{16,}|AKIA[0-9A-Z]{16}|-----BEGIN[[:space:]]+(?:RSA[[:space:]]+)?PRIVATE[[:space:]]+KEY-----)`,
	)
)

func safeApprovalString(value string) bool {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}
	for _, current := range value {
		if current < 0x20 && current != '\n' && current != '\r' && current != '\t' {
			return false
		}
	}
	return true
}

func approvalPathWithinProject(pattern, projectRoot string) bool {
	candidate := pattern
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(projectRoot, candidate)
	}
	candidate = filepath.Clean(candidate)
	resolved, err := resolveApprovalPath(candidate)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(projectRoot, resolved)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func resolveApprovalPath(candidate string) (string, error) {
	missing := make([]string, 0)
	for current := candidate; ; current = filepath.Dir(current) {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
	}
}

// matchCommandPrefix returns true if the command starts with the given prefix.
// Matches either an exact match or a prefix followed by a space (i.e. the prefix
// is the command name and additional args follow).
func matchCommandPrefix(pattern, command string) bool {
	if command == pattern {
		return true
	}
	return strings.HasPrefix(command, pattern+" ")
}

// matchPathPattern returns true if path is equal to or is a child of the pattern directory.
// Handles both exact file paths and parent directory matching.
func matchPathPattern(pattern, path string) bool {
	// Clean both paths for consistent comparison.
	cleanPattern := filepath.Clean(pattern)
	cleanPath := filepath.Clean(path)

	// Exact match
	if cleanPath == cleanPattern {
		return true
	}

	// Parent directory match: path is under the pattern directory.
	// Ensure the pattern acts as a directory prefix with a separator.
	dir := cleanPattern
	if !strings.HasSuffix(dir, string(filepath.Separator)) {
		dir += string(filepath.Separator)
	}
	return strings.HasPrefix(cleanPath, dir)
}
