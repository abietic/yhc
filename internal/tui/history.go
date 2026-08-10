package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	historyPkg "github.com/abietic/yhc/engine/history"
	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
)

const (
	historyFileName          = "history"
	maxHistoryLines          = 500
	maxHistoryLineBytes      = 64 << 10
	maxHistoryMigrationBytes = maxHistoryLines * maxHistoryLineBytes
)

type historyLocations struct {
	projectRoot            string
	compatibilityConfigDir string
}

var resolveHistoryLocations = defaultHistoryLocations

func defaultHistoryLocations() (historyLocations, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return historyLocations{}, errors.New("history path is invalid")
	}
	if resolved, resolveErr := filepath.EvalSymlinks(cwd); resolveErr == nil {
		cwd = resolved
	}
	return historyLocations{
		projectRoot:            cwd,
		compatibilityConfigDir: filepath.Join(os.Getenv("HOME"), ".claude"),
	}, nil
}

// loadHistory returns entries newest last. Claude-compatible JSONL remains the
// first source; canonical project-local plain history is the fallback.
func loadHistory() []string {
	locations, err := resolveHistoryLocations()
	if err != nil {
		return nil
	}
	return loadHistoryAt(locations)
}

func loadHistoryAt(locations historyLocations) []string {
	// Try engine history manager first (JSONL, project-scoped)
	mgr := historyPkg.NewManager(
		locations.compatibilityConfigDir,
		locations.projectRoot,
		"",
	)
	if display := mgr.GetDisplayHistory(); len(display) > 0 {
		// GetDisplayHistory returns newest-first; callers expect newest-last.
		for i, j := 0, len(display)-1; i < j; i, j = i+1, j-1 {
			display[i], display[j] = display[j], display[i]
		}
		return display
	}

	// Fall back to the canonical plain-text format.
	store, exists, err := openHistoryStoreAt(locations.projectRoot, false)
	if err != nil || !exists {
		return nil
	}
	defer store.Close() //nolint:errcheck
	file, info, exists, err := store.OpenRegular(historyFileName, os.O_RDONLY, false)
	if err != nil || !exists || info.Size() < 0 || info.Size() > maxHistoryMigrationBytes {
		return nil
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxHistoryMigrationBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(data)) != info.Size() ||
		len(data) > maxHistoryMigrationBytes || store.ValidateRegular(historyFileName, info) != nil {
		return nil
	}
	entries, err := parseHistoryEntries(data)
	if err != nil {
		return nil
	}
	return entries
}

// saveHistoryEntry saves to Claude-compatible JSONL and canonical plain state.
func saveHistoryEntry(entry string) error {
	locations, err := resolveHistoryLocations()
	if err != nil {
		return errors.New("history path is invalid")
	}
	return saveHistoryEntryAt(locations, entry)
}

func saveHistoryEntryAt(locations historyLocations, entry string) error {
	if err := validateHistoryEntry(entry); err != nil {
		return errors.New("history entry is invalid")
	}

	// Save to engine JSONL history (project-scoped, session-aware)
	mgr := historyPkg.NewManager(
		locations.compatibilityConfigDir,
		locations.projectRoot,
		"",
	)
	mgr.AddSimple(entry)
	_ = mgr.Flush()

	// Also save to the canonical plain-text format for backward compatibility.
	store, exists, err := openHistoryStoreAt(locations.projectRoot, true)
	if err != nil || !exists {
		return errors.New("history state root is invalid")
	}
	defer store.Close() //nolint:errcheck
	file, info, _, err := store.OpenRegular(
		historyFileName,
		os.O_APPEND|os.O_WRONLY,
		true,
	)
	if err != nil {
		return errors.New("history file is invalid")
	}
	line := escapeHistoryEntry(entry) + "\n"
	written, writeErr := file.WriteString(line)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || written != len(line) || syncErr != nil || closeErr != nil ||
		store.ValidateRegular(historyFileName, info) != nil || store.Sync() != nil {
		return errors.New("history write failed")
	}
	return nil
}

func openHistoryStoreAt(projectRoot string, create bool) (*statemigration.CanonicalStore, bool, error) {
	roots, err := statepath.ProjectRoots(projectRoot)
	if err != nil {
		return nil, false, err
	}
	return statemigration.OpenCanonicalStore(roots.Canonical, ".", create)
}

// HistoryMigrationSpec returns the exact project plain-history artifact owner.
func HistoryMigrationSpec() statemigration.ArtifactSpec {
	return statemigration.ArtifactSpec{
		Owner:      "history",
		Scope:      "project",
		SourceRel:  historyFileName,
		TargetRel:  historyFileName,
		Kind:       statemigration.RegularFile,
		LegacyMode: statemigration.LegacyOwnerControlled,
		MaxFiles:   1,
		MaxBytes:   maxHistoryMigrationBytes,
		Validate: func(_ context.Context, snapshot statemigration.Snapshot) error {
			_, err := readHistoryMigrationEntries(snapshot)
			return err
		},
		Stage: func(_ context.Context, snapshot statemigration.Snapshot, stage *os.Root) error {
			entries, err := readHistoryMigrationEntries(snapshot)
			if err != nil {
				return err
			}
			output, err := stage.OpenFile(historyFileName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return errors.New("history migration staging failed")
			}
			_, writeErr := output.Write(formatHistoryEntries(entries))
			closeErr := output.Close()
			if writeErr != nil || closeErr != nil {
				return errors.New("history migration staging failed")
			}
			return nil
		},
	}
}

func readHistoryMigrationEntries(snapshot statemigration.Snapshot) ([]string, error) {
	reader, _, err := snapshot.Open(".")
	if err != nil {
		return nil, errors.New("history migration source is invalid")
	}
	defer reader.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(reader, maxHistoryMigrationBytes+1))
	if err != nil || len(data) > maxHistoryMigrationBytes {
		return nil, errors.New("history migration source is invalid")
	}
	entries, err := parseHistoryEntries(data)
	if err != nil {
		return nil, errors.New("history migration source is invalid")
	}
	return entries, nil
}

func parseHistoryEntries(data []byte) ([]string, error) {
	if len(data) > maxHistoryMigrationBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("history is invalid")
	}
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != '\n' {
		return nil, errors.New("history is invalid")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	entries := make([]string, 0, len(lines))
	for _, line := range lines {
		if len(line) > maxHistoryLineBytes {
			return nil, errors.New("history is invalid")
		}
		entry, err := unescapeHistoryEntry(line)
		if err != nil || validateHistoryEntry(entry) != nil {
			return nil, errors.New("history is invalid")
		}
		entries = append(entries, entry)
	}
	if len(entries) > maxHistoryLines {
		entries = entries[len(entries)-maxHistoryLines:]
	}
	return entries, nil
}

func unescapeHistoryEntry(line []byte) (string, error) {
	var entry strings.Builder
	entry.Grow(len(line))
	for index := 0; index < len(line); index++ {
		if line[index] != '\\' {
			entry.WriteByte(line[index])
			continue
		}
		index++
		if index == len(line) {
			return "", errors.New("history escape is invalid")
		}
		switch line[index] {
		case 'n':
			entry.WriteByte('\n')
		case '\\':
			entry.WriteByte('\\')
		default:
			return "", errors.New("history escape is invalid")
		}
	}
	return entry.String(), nil
}

func formatHistoryEntries(entries []string) []byte {
	var output strings.Builder
	for _, entry := range entries {
		output.WriteString(escapeHistoryEntry(entry))
		output.WriteByte('\n')
	}
	return []byte(output.String())
}

func escapeHistoryEntry(entry string) string {
	escaped := strings.ReplaceAll(entry, "\\", "\\\\")
	return strings.ReplaceAll(escaped, "\n", "\\n")
}

func validateHistoryEntry(entry string) error {
	if len(entry) > maxHistoryLineBytes || !utf8.ValidString(entry) || strings.ContainsRune(entry, '\x00') {
		return errors.New("history entry is invalid")
	}
	for _, value := range entry {
		if unicode.IsControl(value) && value != '\n' && value != '\t' {
			return errors.New("history entry is invalid")
		}
	}
	return nil
}
