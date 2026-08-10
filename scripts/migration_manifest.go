// Command migration_manifest synchronizes and validates the file-by-file
// migration ledger against the TypeScript reference repository.
//
// Usage:
//
//	go run ./scripts/migration_manifest.go sync
//	go run ./scripts/migration_manifest.go check
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const manifestPath = "docs/migration/manifest.yaml"

var validScopes = map[string]bool{
	"required":       true,
	"adapted":        true,
	"excluded":       true,
	"pending_review": true,
}

var validStatuses = map[string]bool{
	"not_started":            true,
	"minimal":                true,
	"partial":                true,
	"implemented_unverified": true,
	"done":                   true,
	"blocked":                true,
}

var retiredTUIPathMarkers = []string{
	"internal/tui/collapse",
	"internal/tui/components",
	"internal/tui/input",
	"internal/tui/rendering",
	"internal/tui/state",
	"internal/tui/ui",
}

type Manifest struct {
	Version       int         `yaml:"version"`
	Updated       string      `yaml:"updated"`
	ReferenceRepo string      `yaml:"reference_repo"`
	PortRepo      string      `yaml:"port_repo"`
	Authority     Authority   `yaml:"authority"`
	Policy        Policy      `yaml:"policy"`
	Summary       Summary     `yaml:"summary"`
	Files         []FileEntry `yaml:"files"`
}

type Authority struct {
	Guideline   string `yaml:"guideline"`
	HumanStatus string `yaml:"human_status"`
	Rule        string `yaml:"rule"`
}

type Policy struct {
	Granularity   string   `yaml:"granularity"`
	DefaultScope  string   `yaml:"default_scope"`
	DefaultStatus string   `yaml:"default_status"`
	ScopeValues   []string `yaml:"scope_values"`
	StatusValues  []string `yaml:"status_values"`
	DoneRequires  []string `yaml:"done_requires"`
}

type Summary struct {
	ReferenceFiles  int            `yaml:"reference_files"`
	LedgerFiles     int            `yaml:"ledger_files"`
	ClassifiedFiles int            `yaml:"classified_files"`
	MappedFiles     int            `yaml:"mapped_files"`
	ScopeCounts     map[string]int `yaml:"scope_counts"`
	StatusCounts    map[string]int `yaml:"status_counts"`
}

type FileEntry struct {
	Path        string   `yaml:"path"`
	Domain      string   `yaml:"domain"`
	Scope       string   `yaml:"scope"`
	Status      string   `yaml:"status"`
	Symbols     []string `yaml:"symbols,omitempty"`
	Targets     []string `yaml:"targets,omitempty"`
	Tests       []string `yaml:"tests,omitempty"`
	Entrypoints []string `yaml:"entrypoints,omitempty"`
	Notes       string   `yaml:"notes,omitempty"`
}

func main() {
	if len(os.Args) != 2 ||
		(os.Args[1] != "sync" &&
			os.Args[1] != "check" &&
			os.Args[1] != "check-ledger") {
		fatal(errors.New("usage: go run ./scripts/migration_manifest.go <sync|check|check-ledger>"))
	}

	m, err := loadManifest(manifestPath)
	if err != nil {
		fatal(err)
	}
	root, err := repositoryRoot()
	if err != nil {
		fatal(err)
	}
	var referenceFiles []string
	if os.Args[1] == "check-ledger" {
		referenceFiles = ledgerReferenceFiles(m)
	} else {
		referenceRepo, err := resolveReferenceRepoPath(root, m.ReferenceRepo, os.Getenv("REFERENCE_DIR"))
		if err != nil {
			fatal(err)
		}
		referenceFiles, err = listReferenceFiles(referenceRepo)
		if err != nil {
			fatal(err)
		}
	}

	synchronized, added, removed := synchronize(m, referenceFiles)
	if err := validate(synchronized, root, referenceFiles); err != nil {
		fatal(err)
	}

	if os.Args[1] == "check" || os.Args[1] == "check-ledger" {
		if added != 0 || removed != 0 || !summariesEqual(m.Summary, synchronized.Summary) {
			fatal(fmt.Errorf("manifest is stale: %d reference files missing, %d stale entries; run sync", added, removed))
		}
		label := "manifest valid"
		if os.Args[1] == "check-ledger" {
			label = "manifest ledger valid"
		}
		fmt.Printf("%s: %d files, %d classified, %d mapped\n",
			label,
			synchronized.Summary.LedgerFiles,
			synchronized.Summary.ClassifiedFiles,
			synchronized.Summary.MappedFiles)
		return
	}

	synchronized.Updated = time.Now().Format("2006-01-02")
	if err := writeManifest(manifestPath, synchronized); err != nil {
		fatal(err)
	}
	fmt.Printf("manifest synchronized: %d files (%d added, %d removed)\n",
		synchronized.Summary.LedgerFiles, added, removed)
}

func ledgerReferenceFiles(m *Manifest) []string {
	if m == nil {
		return nil
	}
	files := make([]string, 0, len(m.Files))
	for _, entry := range m.Files {
		files = append(files, entry.Path)
	}
	return files
}

func loadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if m.Version != 4 {
		return nil, fmt.Errorf("unsupported manifest version %d; expected 4", m.Version)
	}
	return &m, nil
}

func writeManifest(path string, m *Manifest) error {
	b, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	header := []byte("# Generated inventory with reviewed fields preserved by:\n#   go run ./scripts/migration_manifest.go sync\n# Progress percentages belong only in STATUS.md.\n\n")
	b = append(header, b...)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func listReferenceFiles(repo string) ([]string, error) {
	root := filepath.Join(repo, "src")
	var files []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".ts" && ext != ".tsx" {
			return nil
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func resolveRepoPath(root, repo string) string {
	if filepath.IsAbs(repo) {
		return repo
	}
	return filepath.Join(root, filepath.FromSlash(repo))
}

func resolveReferenceRepoPath(root, repo, referenceRoot string) (string, error) {
	if strings.TrimSpace(referenceRoot) == "" {
		return resolveRepoPath(root, repo), nil
	}

	cleanRepo := filepath.ToSlash(filepath.Clean(filepath.FromSlash(repo)))
	const namespace = ".reference/"
	if !strings.HasPrefix(cleanRepo, namespace) {
		return "", fmt.Errorf("reference repo %q must be beneath .reference when REFERENCE_DIR is set", repo)
	}
	relativeRepo := strings.TrimPrefix(cleanRepo, namespace)
	if relativeRepo == "" || relativeRepo == "." {
		return "", fmt.Errorf("reference repo %q must name a repository beneath .reference", repo)
	}
	if !filepath.IsAbs(referenceRoot) {
		referenceRoot = filepath.Join(root, referenceRoot)
	}
	return filepath.Join(referenceRoot, filepath.FromSlash(relativeRepo)), nil
}

func synchronize(m *Manifest, referenceFiles []string) (*Manifest, int, int) {
	existing := make(map[string]FileEntry, len(m.Files))
	for _, entry := range m.Files {
		existing[entry.Path] = entry
	}

	result := *m
	result.Files = make([]FileEntry, 0, len(referenceFiles))
	added := 0
	for _, path := range referenceFiles {
		entry, ok := existing[path]
		if !ok {
			added++
			entry = FileEntry{
				Path:   path,
				Domain: domainFor(path),
				Scope:  "pending_review",
				Status: "not_started",
			}
		}
		entry.Path = path
		entry.Domain = domainFor(path)
		result.Files = append(result.Files, entry)
		delete(existing, path)
	}
	result.Summary = calculateSummary(result.Files, len(referenceFiles))
	return &result, added, len(existing)
}

func calculateSummary(files []FileEntry, referenceCount int) Summary {
	s := Summary{
		ReferenceFiles: referenceCount,
		LedgerFiles:    len(files),
		ScopeCounts:    map[string]int{},
		StatusCounts:   map[string]int{},
	}
	for _, entry := range files {
		s.ScopeCounts[entry.Scope]++
		s.StatusCounts[entry.Status]++
		if entry.Scope != "pending_review" {
			s.ClassifiedFiles++
		}
		if len(entry.Targets) > 0 {
			s.MappedFiles++
		}
	}
	return s
}

func validate(m *Manifest, repoRoot string, referenceFiles []string) error {
	if m.Summary.ReferenceFiles != len(referenceFiles) || m.Summary.LedgerFiles != len(m.Files) {
		return errors.New("summary counts do not match ledger")
	}
	seen := map[string]bool{}
	var problems []string
	for _, entry := range m.Files {
		if seen[entry.Path] {
			problems = append(problems, entry.Path+": duplicate entry")
		}
		seen[entry.Path] = true
		if !validScopes[entry.Scope] {
			problems = append(problems, entry.Path+": invalid scope "+entry.Scope)
		}
		if !validStatuses[entry.Status] {
			problems = append(problems, entry.Path+": invalid status "+entry.Status)
		}
		if entry.Scope == "excluded" && strings.TrimSpace(entry.Notes) == "" {
			problems = append(problems, entry.Path+": excluded entries require notes")
		}
		if entry.Status == "done" && (len(entry.Targets) == 0 || len(entry.Tests) == 0) {
			problems = append(problems, entry.Path+": done entries require targets and tests")
		}
		for _, marker := range retiredTUIPathMarkers {
			for _, target := range entry.Targets {
				if strings.Contains(target, marker) {
					problems = append(problems, fmt.Sprintf("%s: targets contains retired TUI path marker %q", entry.Path, marker))
				}
			}
			for _, test := range entry.Tests {
				if strings.Contains(test, marker) {
					problems = append(problems, fmt.Sprintf("%s: tests contains retired TUI path marker %q", entry.Path, marker))
				}
			}
			if strings.Contains(entry.Notes, marker) {
				problems = append(problems, fmt.Sprintf("%s: notes contains retired TUI path marker %q", entry.Path, marker))
			}
		}
		for _, path := range append(append([]string{}, entry.Targets...), entry.Tests...) {
			if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(path))); err != nil {
				problems = append(problems, entry.Path+": mapped path does not exist: "+path)
			}
		}
	}
	if len(problems) > 0 {
		limit := len(problems)
		if limit > 20 {
			limit = 20
		}
		return fmt.Errorf("manifest validation failed:\n  %s", strings.Join(problems[:limit], "\n  "))
	}
	return nil
}

func domainFor(path string) string {
	rel := strings.TrimPrefix(filepath.ToSlash(path), "src/")
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return "root"
}

func repositoryRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not locate repository root")
		}
		dir = parent
	}
}

func summariesEqual(a, b Summary) bool {
	return a.ReferenceFiles == b.ReferenceFiles &&
		a.LedgerFiles == b.LedgerFiles &&
		a.ClassifiedFiles == b.ClassifiedFiles &&
		a.MappedFiles == b.MappedFiles &&
		mapsEqual(a.ScopeCounts, b.ScopeCounts) &&
		mapsEqual(a.StatusCounts, b.StatusCounts)
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "migration manifest:", err)
	os.Exit(1)
}
