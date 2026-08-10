package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
)

const coverageProfileLimit = 64 << 20

type CoverageAdvisory struct {
	SchemaVersion int               `json:"schema_version"`
	DiffDigest    string            `json:"diff_digest"`
	Available     bool              `json:"available"`
	Packages      []PackageCoverage `json:"packages"`
}

type PackageCoverage struct {
	Package      string   `json:"package"`
	Statements   float64  `json:"statements_percent"`
	ChangedFiles []string `json:"changed_files"`
}

// writeCoverageAdvisory produces an advisory artifact; its failure never
// changes gate evidence or the evidence-ready state.
func writeCoverageAdvisory(root string, plan Plan) (CoverageAdvisory, error) {
	advisory := CoverageAdvisory{
		SchemaVersion: 1,
		DiffDigest:    plan.DiffDigest,
		Packages:      []PackageCoverage{},
	}
	if !digestPattern.MatchString(plan.DiffDigest) {
		return CoverageAdvisory{}, errors.New("coverage diff digest must be 64 lowercase hexadecimal characters")
	}
	packages, err := changedProductionPackages(root, plan)
	if err != nil {
		return CoverageAdvisory{}, err
	}
	if len(packages) == 0 {
		return writeCoverageArtifact(root, advisory)
	}

	profile, err := readRepositoryFile(root, "build/coverage.out")
	if errors.Is(err, os.ErrNotExist) {
		return writeCoverageArtifact(root, advisory)
	}
	if err != nil {
		return CoverageAdvisory{}, fmt.Errorf("read coverage profile: %w", err)
	}
	advisory.Packages, err = parseCoverageProfile(profile, packages)
	if err != nil {
		return CoverageAdvisory{}, err
	}
	advisory.Available = true
	return writeCoverageArtifact(root, advisory)
}

func changedProductionPackages(root string, plan Plan) (map[string][]string, error) {
	files := make([]string, 0, len(plan.Changed))
	for _, change := range plan.Changed {
		if change.Kind != PathProduction || deletedCoveragePath(change.Status) ||
			!strings.HasSuffix(change.Path, ".go") || strings.HasSuffix(change.Path, "_test.go") {
			continue
		}
		if err := validateRepositoryPath(change.Path); err != nil {
			return nil, fmt.Errorf("invalid changed Go path: %w", err)
		}
		files = append(files, change.Path)
	}
	if len(files) == 0 {
		return map[string][]string{}, nil
	}
	module, err := modulePath(root)
	if err != nil {
		return nil, err
	}
	packages := make(map[string][]string)
	for _, name := range files {
		pkg := module
		if directory := path.Dir(name); directory != "." {
			pkg += "/" + directory
		}
		packages[pkg] = append(packages[pkg], name)
	}
	for pkg := range packages {
		slices.Sort(packages[pkg])
	}
	return packages, nil
}

func parseCoverageProfile(profile []byte, wanted map[string][]string) ([]PackageCoverage, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(profile)))
	if !scanner.Scan() {
		return nil, errors.New("malformed coverage profile header")
	}
	mode, ok := strings.CutPrefix(scanner.Text(), "mode: ")
	if !ok || !oneOf(strings.TrimSpace(mode), "set", "count", "atomic") {
		return nil, errors.New("malformed coverage profile header")
	}

	type totals struct{ covered, statements int64 }
	counts := make(map[string]totals, len(wanted))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("malformed coverage profile record %q", line)
		}
		colon := strings.IndexByte(fields[0], ':')
		if colon <= 0 || !validCoverageRange(fields[0][colon+1:]) {
			return nil, fmt.Errorf("malformed coverage profile location %q", fields[0])
		}
		statements, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || statements < 0 {
			return nil, fmt.Errorf("malformed coverage statement count %q", fields[1])
		}
		count, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || count < 0 {
			return nil, fmt.Errorf("malformed coverage execution count %q", fields[2])
		}

		fileName := fields[0][:colon]
		slash := strings.LastIndexByte(fileName, '/')
		if slash <= 0 {
			return nil, fmt.Errorf("malformed coverage file %q", fileName)
		}
		pkg := fileName[:slash]
		if _, selected := wanted[pkg]; !selected {
			continue
		}
		total := counts[pkg]
		total.statements += statements
		if count > 0 {
			total.covered += statements
		}
		counts[pkg] = total
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read coverage profile: %w", err)
	}

	packages := make([]PackageCoverage, 0, len(wanted))
	for pkg, files := range wanted {
		total := counts[pkg]
		percentage := float64(0)
		if total.statements > 0 {
			percentage = float64(total.covered) * 100 / float64(total.statements)
		}
		packages = append(packages, PackageCoverage{
			Package: pkg, Statements: percentage, ChangedFiles: slices.Clone(files),
		})
	}
	slices.SortFunc(packages, func(left, right PackageCoverage) int {
		return strings.Compare(left.Package, right.Package)
	})
	return packages, nil
}

func writeCoverageArtifact(root string, advisory CoverageAdvisory) (CoverageAdvisory, error) {
	store := newFileEvidenceStore(root)
	repository, directory, err := store.openEvidenceDir(Plan{DiffDigest: advisory.DiffDigest}, true)
	if err != nil {
		return CoverageAdvisory{}, err
	}
	defer repository.Close()
	defer directory.Close()

	existing, err := strictRegularFile(directory, "coverage.json")
	if err == nil {
		_ = existing.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return CoverageAdvisory{}, err
	}
	for index := range advisory.Packages {
		advisory.Packages[index].Statements = math.Round(advisory.Packages[index].Statements*10) / 10
	}
	if err := writeJSONAtomically(directory, "coverage.json", advisory, nil); err != nil {
		return CoverageAdvisory{}, fmt.Errorf("write coverage advisory: %w", err)
	}
	return advisory, nil
}

func modulePath(root string) (string, error) {
	data, err := readRepositoryFile(root, "go.mod")
	if err != nil {
		return "", fmt.Errorf("read module path: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" && !strings.ContainsAny(fields[1], "\\\x00") {
			return fields[1], nil
		}
	}
	return "", errors.New("go.mod has no module path")
}

func readRepositoryFile(root, name string) ([]byte, error) {
	if err := validateRepositoryPath(name); err != nil {
		return nil, err
	}
	repository, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer repository.Close()
	directory := repository
	directoryName, fileName := path.Split(name)
	if directoryName != "" {
		directory, err = openStrictDir(repository, strings.TrimSuffix(directoryName, "/"), false)
		if err != nil {
			return nil, err
		}
		defer directory.Close()
	}
	file, err := strictRegularFile(directory, fileName)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > coverageProfileLimit {
		return nil, errors.New("coverage input exceeds limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, coverageProfileLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > coverageProfileLimit {
		return nil, errors.New("coverage input exceeds limit")
	}
	return data, nil
}

func deletedCoveragePath(status string) bool {
	return strings.HasPrefix(status, "D") || strings.HasSuffix(status, "-from")
}

func validCoverageRange(value string) bool {
	positions := strings.Split(value, ",")
	if len(positions) != 2 {
		return false
	}
	for _, position := range positions {
		parts := strings.Split(position, ".")
		if len(parts) != 2 {
			return false
		}
		for _, part := range parts {
			if _, err := strconv.ParseUint(part, 10, 64); err != nil {
				return false
			}
		}
	}
	return true
}
