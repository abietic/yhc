package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/scanner"
	"go/token"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
)

const (
	maxGoSourceFileBytes = 8 << 20
	maxGoSourceTreeBytes = 256 << 20
)

type ImportEdge struct {
	FromModule string `json:"from_module"`
	ToModule   string `json:"to_module"`
	ImportPath string `json:"import_path"`
	TestOnly   bool   `json:"test_only"`
}

type BoundaryReport struct {
	SchemaVersion            int          `json:"schema_version"`
	Base                     string       `json:"base"`
	Head                     string       `json:"head"`
	DiffDigest               string       `json:"diff_digest"`
	NewProductionEdges       []ImportEdge `json:"new_production_edges"`
	NewTestEdges             []ImportEdge `json:"new_test_edges"`
	ForbiddenNewEdges        []ImportEdge `json:"forbidden_new_edges"`
	NewFlatPackageViolations []string     `json:"new_flat_package_violations"`
}

type TreeSource interface {
	ListFiles(ctx context.Context, revision string) ([]string, error)
	ReadFile(ctx context.Context, revision, name string) ([]byte, error)
}

type boundaryTreeState struct {
	edges          map[string]ImportEdge
	flatViolations map[string]struct{}
}

type boundaryAnalysis struct {
	report                 BoundaryReport
	currentProductionEdges []ImportEdge
	currentTestEdges       []ImportEdge
	currentFlatViolations  []string
}

type boundaryDiagnostics struct {
	BoundaryReport
	CurrentProductionEdges       []ImportEdge `json:"current_production_edges"`
	CurrentTestEdges             []ImportEdge `json:"current_test_edges"`
	CurrentFlatPackageViolations []string     `json:"current_flat_package_violations"`
}

func buildBoundaryReport(
	ctx context.Context,
	plan Plan,
	policy Policy,
	source TreeSource,
) (BoundaryReport, error) {
	analysis, err := buildBoundaryAnalysis(ctx, plan, policy, source)
	return analysis.report, err
}

func buildBoundaryAnalysis(
	ctx context.Context,
	plan Plan,
	policy Policy,
	source TreeSource,
) (boundaryAnalysis, error) {
	if source == nil {
		return boundaryAnalysis{}, errors.New("tree source is required")
	}
	if strings.TrimSpace(plan.Base) == "" || strings.TrimSpace(plan.Head) == "" ||
		!digestPattern.MatchString(plan.DiffDigest) {
		return boundaryAnalysis{}, errors.New("boundary plan identity is invalid")
	}
	base, err := collectBoundaryTree(ctx, plan.Base, policy, source)
	if err != nil {
		return boundaryAnalysis{}, fmt.Errorf("classify base tree: %w", err)
	}
	head, err := collectBoundaryTree(ctx, plan.Head, policy, source)
	if err != nil {
		return boundaryAnalysis{}, fmt.Errorf("classify head tree: %w", err)
	}

	newProduction, newTest := newBoundaryEdges(base.edges, head.edges)
	forbidden := make([]ImportEdge, 0, len(newProduction))
	for _, edge := range newProduction {
		if forbiddenProductionEdge(policy.Boundaries, edge) {
			forbidden = append(forbidden, edge)
		}
	}
	newProduction = nonNilImportEdges(newProduction)
	newTest = nonNilImportEdges(newTest)
	newFlat := nonNilStrings(setDifference(head.flatViolations, base.flatViolations))
	currentProduction, currentTest := splitAndSortEdges(head.edges)
	return boundaryAnalysis{
		report: BoundaryReport{
			SchemaVersion:            1,
			Base:                     plan.Base,
			Head:                     plan.Head,
			DiffDigest:               plan.DiffDigest,
			NewProductionEdges:       newProduction,
			NewTestEdges:             newTest,
			ForbiddenNewEdges:        forbidden,
			NewFlatPackageViolations: newFlat,
		},
		currentProductionEdges: nonNilImportEdges(currentProduction),
		currentTestEdges:       nonNilImportEdges(currentTest),
		currentFlatViolations:  nonNilStrings(sortedSet(head.flatViolations)),
	}, nil
}

func collectBoundaryTree(
	ctx context.Context,
	revision string,
	policy Policy,
	source TreeSource,
) (boundaryTreeState, error) {
	names, err := source.ListFiles(ctx, revision)
	if err != nil {
		return boundaryTreeState{}, err
	}
	slices.Sort(names)
	edges := make(map[string]ImportEdge)
	flat := make(map[string]struct{})
	seen := make(map[string]struct{}, len(names))
	totalBytes := 0
	for _, name := range names {
		if err := validateRepositoryPath(name); err != nil {
			return boundaryTreeState{}, err
		}
		if _, ok := seen[name]; ok {
			return boundaryTreeState{}, fmt.Errorf("duplicate tree path %q", name)
		}
		seen[name] = struct{}{}
		if path.Ext(name) != ".go" {
			continue
		}
		collectFlatViolation(policy.Boundaries.FlatPackageRoots, name, flat)
		fromModule, owned, err := moduleForSourcePath(policy, name)
		if err != nil {
			return boundaryTreeState{}, err
		}
		if !owned {
			continue
		}
		data, err := source.ReadFile(ctx, revision, name)
		if err != nil {
			return boundaryTreeState{}, fmt.Errorf("read Go source %q: %w", name, err)
		}
		if len(data) > maxGoSourceFileBytes {
			return boundaryTreeState{}, fmt.Errorf("go source %q exceeds %d bytes", name, maxGoSourceFileBytes)
		}
		totalBytes += len(data)
		if totalBytes > maxGoSourceTreeBytes {
			return boundaryTreeState{}, fmt.Errorf("parsed Go source exceeds %d bytes", maxGoSourceTreeBytes)
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, data, parser.ImportsOnly)
		if err != nil {
			return boundaryTreeState{}, sanitizedParserError(name, err)
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return boundaryTreeState{}, fmt.Errorf("parse imports in %s: invalid import string", name)
			}
			if !internalImport(policy.Repository, importPath) {
				continue
			}
			toModule, err := moduleForImport(policy, importPath)
			if err != nil {
				return boundaryTreeState{}, fmt.Errorf("classify import in %s: %w", name, err)
			}
			edge := ImportEdge{
				FromModule: fromModule,
				ToModule:   toModule,
				ImportPath: importPath,
				TestOnly:   strings.HasSuffix(name, "_test.go"),
			}
			edges[importEdgeKey(edge)] = edge
		}
	}
	return boundaryTreeState{edges: edges, flatViolations: flat}, nil
}

func sanitizedParserError(name string, err error) error {
	var list scanner.ErrorList
	if errors.As(err, &list) && len(list) > 0 {
		return fmt.Errorf(
			"parse imports in %s:%d:%d: invalid Go import syntax",
			name,
			list[0].Pos.Line,
			list[0].Pos.Column,
		)
	}
	return fmt.Errorf("parse imports in %s: invalid Go import syntax", name)
}

func internalImport(repository, importPath string) bool {
	return importPath == repository || strings.HasPrefix(importPath, repository+"/")
}

func moduleForSourcePath(policy Policy, name string) (string, bool, error) {
	directory := path.Dir(name)
	importPath := policy.Repository
	if directory != "." {
		importPath += "/" + directory
	}
	owner, found, err := resolvePackageOwner(policy, importPath)
	return owner, found, err
}

func moduleForImport(policy Policy, importPath string) (string, error) {
	if !internalImport(policy.Repository, importPath) {
		return "", fmt.Errorf("import %q is not repository-internal", importPath)
	}
	owner, found, err := resolvePackageOwner(policy, importPath)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("internal import %q has no module owner", importPath)
	}
	return owner, nil
}

func resolvePackageOwner(policy Policy, importPath string) (string, bool, error) {
	bestOwner := ""
	bestLength := -1
	for owner, module := range policy.Modules {
		for _, pattern := range module.Packages {
			prefix, err := packagePrefix(policy.Repository, pattern)
			if err != nil {
				return "", false, fmt.Errorf("module %q package pattern: %w", owner, err)
			}
			if importPath != prefix && !strings.HasPrefix(importPath, prefix+"/") {
				continue
			}
			switch {
			case len(prefix) > bestLength:
				bestOwner, bestLength = owner, len(prefix)
			case len(prefix) == bestLength && owner != bestOwner:
				return "", false, fmt.Errorf(
					"package %q has equal-length module owners %q and %q",
					importPath,
					bestOwner,
					owner,
				)
			}
		}
	}
	return bestOwner, bestOwner != "", nil
}

func packagePrefix(repository, pattern string) (string, error) {
	if !strings.HasPrefix(pattern, "./") {
		return "", fmt.Errorf("invalid package pattern %q", pattern)
	}
	relative := strings.TrimPrefix(pattern, "./")
	relative = strings.TrimSuffix(relative, "/...")
	if relative == "..." {
		relative = ""
	}
	if strings.ContainsAny(relative, "*?[") {
		return "", fmt.Errorf("invalid package pattern %q", pattern)
	}
	if relative != "" {
		if err := validateRepositoryPath(relative); err != nil {
			return "", err
		}
		return repository + "/" + relative, nil
	}
	return repository, nil
}

func collectFlatViolation(roots []string, name string, violations map[string]struct{}) {
	directory := path.Dir(name)
	for _, root := range roots {
		root = strings.TrimSuffix(path.Clean(root), "/")
		if directory != root && strings.HasPrefix(directory, root+"/") {
			violations[directory] = struct{}{}
		}
	}
}

func newBoundaryEdges(base, head map[string]ImportEdge) ([]ImportEdge, []ImportEdge) {
	newEdges := make(map[string]ImportEdge)
	for key, edge := range head {
		if _, exists := base[key]; !exists {
			newEdges[key] = edge
		}
	}
	return splitAndSortEdges(newEdges)
}

func splitAndSortEdges(edges map[string]ImportEdge) ([]ImportEdge, []ImportEdge) {
	var production, tests []ImportEdge
	for _, edge := range edges {
		if edge.TestOnly {
			tests = append(tests, edge)
		} else {
			production = append(production, edge)
		}
	}
	sortImportEdges(production)
	sortImportEdges(tests)
	return production, tests
}

func sortImportEdges(edges []ImportEdge) {
	slices.SortFunc(edges, func(left, right ImportEdge) int {
		if order := cmp.Compare(left.FromModule, right.FromModule); order != 0 {
			return order
		}
		if order := cmp.Compare(left.ToModule, right.ToModule); order != 0 {
			return order
		}
		if order := cmp.Compare(left.ImportPath, right.ImportPath); order != 0 {
			return order
		}
		if left.TestOnly == right.TestOnly {
			return 0
		}
		if left.TestOnly {
			return 1
		}
		return -1
	})
}

func importEdgeKey(edge ImportEdge) string {
	return edge.FromModule + "\x00" + edge.ToModule + "\x00" + edge.ImportPath + "\x00" +
		strconv.FormatBool(edge.TestOnly)
}

func forbiddenProductionEdge(policy BoundaryPolicy, edge ImportEdge) bool {
	if edge.TestOnly {
		return false
	}
	for _, forbidden := range policy.ForbiddenProductionEdges {
		if slices.Contains(forbidden.From, edge.FromModule) &&
			slices.Contains(forbidden.To, edge.ToModule) {
			return true
		}
	}
	return false
}

func setDifference(head, base map[string]struct{}) []string {
	result := make(map[string]struct{})
	for value := range head {
		if _, exists := base[value]; !exists {
			result[value] = struct{}{}
		}
	}
	return sortedSet(result)
}

func boundaryFailed(report BoundaryReport) bool {
	return len(report.ForbiddenNewEdges) > 0 || len(report.NewFlatPackageViolations) > 0
}

func nonNilImportEdges(edges []ImportEdge) []ImportEdge {
	if edges == nil {
		return []ImportEdge{}
	}
	return edges
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func renderBoundaryJSON(analysis boundaryAnalysis, all bool, writer io.Writer) error {
	if !all {
		return renderJSON(analysis.report, writer)
	}
	return renderJSON(boundaryDiagnostics{
		BoundaryReport:               analysis.report,
		CurrentProductionEdges:       analysis.currentProductionEdges,
		CurrentTestEdges:             analysis.currentTestEdges,
		CurrentFlatPackageViolations: analysis.currentFlatViolations,
	}, writer)
}

func renderBoundaryMarkdown(analysis boundaryAnalysis, all bool, writer io.Writer) error {
	var output strings.Builder
	fmt.Fprintf(&output, "# Module Boundary Report\n\n")
	fmt.Fprintf(&output, "- Base: `%s`\n", analysis.report.Base)
	fmt.Fprintf(&output, "- Head: `%s`\n", analysis.report.Head)
	fmt.Fprintf(&output, "- Diff digest: `%s`\n", analysis.report.DiffDigest)
	renderImportEdgeTable(&output, "New production edges", analysis.report.NewProductionEdges)
	renderImportEdgeTable(&output, "New test-only edges", analysis.report.NewTestEdges)
	renderImportEdgeTable(&output, "Forbidden new production edges", analysis.report.ForbiddenNewEdges)
	renderStringTable(&output, "New flat-package violations", analysis.report.NewFlatPackageViolations)
	if all {
		renderImportEdgeTable(&output, "Current production edges", analysis.currentProductionEdges)
		renderImportEdgeTable(&output, "Current test-only edges", analysis.currentTestEdges)
		renderStringTable(&output, "Current flat-package violations", analysis.currentFlatViolations)
	}
	_, err := io.WriteString(writer, output.String())
	return err
}

func renderImportEdgeTable(output *strings.Builder, title string, edges []ImportEdge) {
	fmt.Fprintf(output, "\n## %s\n\n", title)
	output.WriteString("| From module | To module | Import | Test only |\n")
	output.WriteString("|---|---|---|---|\n")
	if len(edges) == 0 {
		output.WriteString("| _None_ |  |  |  |\n")
		return
	}
	for _, edge := range edges {
		fmt.Fprintf(
			output,
			"| %s | %s | %s | %t |\n",
			escapeMarkdownCell(edge.FromModule),
			escapeMarkdownCell(edge.ToModule),
			escapeMarkdownCell(edge.ImportPath),
			edge.TestOnly,
		)
	}
}

func renderStringTable(output *strings.Builder, title string, values []string) {
	fmt.Fprintf(output, "\n## %s\n\n", title)
	output.WriteString("| Package directory |\n")
	output.WriteString("|---|\n")
	if len(values) == 0 {
		output.WriteString("| _None_ |\n")
		return
	}
	for _, value := range values {
		fmt.Fprintf(output, "| %s |\n", escapeMarkdownCell(value))
	}
}
