// Command migration_scan re-scans both the Go port and the TypeScript reference,
// producing a structured report for the migration-loop align phase.
//
// Usage:
//
//	go run ./scripts/migration_scan
//	go run ./scripts/migration_scan -json
//	go run ./scripts/migration_scan -reference /path/to/claude-code-ripe
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

var (
	jsonOut   = flag.Bool("json", false, "emit JSON instead of Markdown")
	reference = flag.String("reference", ".reference/claude-code-ripe", "path to reference repo")
)

type Report struct {
	GoProject        GoStats         `json:"go_project"`
	ReferenceProject ReferenceStats  `json:"reference_project"`
	TUI              TUIStats        `json:"tui"`
	Liveness         PackageLiveness `json:"package_liveness"`
}

type PackageLiveness struct {
	Packages                  int                `json:"packages"`
	ByPackage                 []PackageImporters `json:"by_package"`
	ZeroProductionImportMain  []string           `json:"zero_production_import_main_entrypoints"`
	ZeroProductionImportOther []string           `json:"zero_production_import_non_entrypoints"`
}

type PackageImporters struct {
	Path                string   `json:"path"`
	ProductionImporters []string `json:"production_importers"`
	TestImporters       []string `json:"test_importers"`
}

type goListPackage struct {
	Dir          string   `json:"Dir"`
	ImportPath   string   `json:"ImportPath"`
	Name         string   `json:"Name"`
	Imports      []string `json:"Imports"`
	TestImports  []string `json:"TestImports"`
	XTestImports []string `json:"XTestImports"`
}

type GoStats struct {
	ProductionFiles  int `json:"production_files"`
	ProductionLines  int `json:"production_lines"`
	TestFiles        int `json:"test_files"`
	TestLines        int `json:"test_lines"`
	Packages         int `json:"packages"`
	ToolConstructors int `json:"tool_constructors"`
	CommandFiles     int `json:"command_files"`
}

type ReferenceStats struct {
	TSFiles     int `json:"ts_files"`
	TSLines     int `json:"ts_lines"`
	CommandDirs int `json:"command_dirs"`
	ToolDirs    int `json:"tool_dirs"`
}

type TUIStats struct {
	ProductionFiles int `json:"production_files"`
	ProductionLines int `json:"production_lines"`
	TestFiles       int `json:"test_files"`
	TestLines       int `json:"test_lines"`
}

func main() {
	flag.Parse()

	root, err := repositoryRoot()
	if err != nil {
		fatal(err)
	}

	refPath := *reference
	if !filepath.IsAbs(refPath) {
		refPath = filepath.Join(root, filepath.FromSlash(refPath))
	}

	liveness, err := scanPackageLiveness(root)
	if err != nil {
		fatal(err)
	}

	report := Report{
		GoProject:        scanGoProject(root),
		ReferenceProject: scanReference(refPath),
		TUI:              scanTUI(root),
		Liveness:         liveness,
	}

	if *jsonOut {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fatal(err)
		}
	} else {
		printMarkdown(report)
	}
}

func scanGoProject(root string) GoStats {
	var s GoStats
	pkgSet := make(map[string]bool)

	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		// Skip vendor, hidden, reference, build, docs, scripts
		if skipPath(rel) {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".go" {
			return nil
		}

		lines, _ := countLines(path)
		if strings.HasSuffix(filepath.Base(path), "_test.go") {
			s.TestFiles++
			s.TestLines += lines
		} else {
			s.ProductionFiles++
			s.ProductionLines += lines
		}

		// Track package directory
		dir := filepath.Dir(rel)
		if dir != "." {
			pkgSet[dir] = true
		}
		return nil
	})

	s.Packages = len(pkgSet)
	s.ToolConstructors = countToolFactories(root)
	s.CommandFiles = countCommandFiles(root)
	return s
}

func scanReference(refPath string) ReferenceStats {
	var s ReferenceStats
	if _, err := os.Stat(refPath); err != nil {
		return s // empty if not present
	}

	// Resolve symlinks so WalkDir can traverse into the real directory.
	realPath, err := filepath.EvalSymlinks(refPath)
	if err != nil {
		realPath = refPath
	}

	_ = filepath.WalkDir(realPath, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(realPath, path)
		if skipPath(rel) {
			return nil
		}
		ext := filepath.Ext(path)
		if ext == ".ts" || ext == ".tsx" {
			lines, _ := countLines(path)
			s.TSFiles++
			s.TSLines += lines
		}
		return nil
	})

	// Count command directories in reference
	cmdDir := filepath.Join(realPath, "src", "commands")
	if entries, err := os.ReadDir(cmdDir); err == nil {
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				s.CommandDirs++
			}
		}
	}

	// Count tool directories in reference
	toolDir := filepath.Join(realPath, "src", "tools")
	if entries, err := os.ReadDir(toolDir); err == nil {
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				s.ToolDirs++
			}
		}
	}

	return s
}

func scanTUI(root string) TUIStats {
	var s TUIStats
	tuiRoot := filepath.Join(root, "internal", "tui")
	_ = filepath.WalkDir(tuiRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		lines, _ := countLines(path)
		if strings.HasSuffix(filepath.Base(path), "_test.go") {
			s.TestFiles++
			s.TestLines += lines
		} else {
			s.ProductionFiles++
			s.ProductionLines += lines
		}
		return nil
	})
	return s
}

func scanPackageLiveness(root string) (PackageLiveness, error) {
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return PackageLiveness{}, fmt.Errorf("go list failed: %w\n%s", err, ee.Stderr)
		}
		return PackageLiveness{}, fmt.Errorf("go list failed: %w", err)
	}

	var pkgs []goListPackage
	decoder := json.NewDecoder(bytes.NewReader(out))
	for {
		var p goListPackage
		if err := decoder.Decode(&p); err != nil {
			if err == io.EOF {
				break
			}
			return PackageLiveness{}, fmt.Errorf("parse go list output: %w", err)
		}
		pkgs = append(pkgs, p)
	}

	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].ImportPath < pkgs[j].ImportPath })

	inModule := make(map[string]bool, len(pkgs))
	for _, p := range pkgs {
		inModule[p.ImportPath] = true
	}

	prod := make(map[string][]string, len(pkgs))
	test := make(map[string][]string, len(pkgs))
	for _, p := range pkgs {
		for _, imp := range p.Imports {
			if inModule[imp] {
				prod[imp] = append(prod[imp], p.ImportPath)
			}
		}
		for _, imp := range p.TestImports {
			if inModule[imp] {
				test[imp] = append(test[imp], p.ImportPath)
			}
		}
		for _, imp := range p.XTestImports {
			if inModule[imp] {
				test[imp] = append(test[imp], p.ImportPath)
			}
		}
	}

	liveness := PackageLiveness{
		Packages:  len(pkgs),
		ByPackage: make([]PackageImporters, 0, len(pkgs)),
	}
	for _, p := range pkgs {
		prodImps := uniqueSorted(prod[p.ImportPath])
		testImps := uniqueSorted(test[p.ImportPath])
		liveness.ByPackage = append(liveness.ByPackage, PackageImporters{
			Path:                p.ImportPath,
			ProductionImporters: prodImps,
			TestImporters:       testImps,
		})
		if len(prodImps) == 0 {
			if p.Name == "main" {
				liveness.ZeroProductionImportMain = append(liveness.ZeroProductionImportMain, p.ImportPath)
			} else {
				liveness.ZeroProductionImportOther = append(liveness.ZeroProductionImportOther, p.ImportPath)
			}
		}
	}
	return liveness, nil
}

func uniqueSorted(ss []string) []string {
	sort.Strings(ss)
	out := ss[:0]
	var prev string
	for _, s := range ss {
		if len(out) == 0 || s != prev {
			out = append(out, s)
			prev = s
		}
	}
	return out
}

func countToolFactories(root string) int {
	return countGoFuncPatterns(filepath.Join(root, "tools"))
}

func countCommandFiles(root string) int {
	cmdDir := filepath.Join(root, "engine", "commands")
	// Count files matching cmd_*.go (excluding test and registry/dispatch)
	entries, err := os.ReadDir(cmdDir)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "cmd_") && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			count++
		}
	}
	return count
}

func countGoFuncPatterns(dir string) int {
	count := 0
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil // skip unparseable files
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name != nil {
				name := fn.Name.Name
				if strings.HasSuffix(name, "Tool") && fn.Type != nil && fn.Type.Results != nil && len(fn.Type.Results.List) == 1 {
					// Check if result type is ToolImpl
					if ident, ok := fn.Type.Results.List[0].Type.(*ast.Ident); ok && ident.Name == "ToolImpl" {
						count++
					}
				}
			}
		}
		return nil
	})
	return count
}

func countLines(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if len(b) == 0 {
		return 0, nil
	}
	lines := strings.Count(string(b), "\n")
	if b[len(b)-1] != '\n' {
		lines++
	}
	return lines, nil
}

func skipPath(rel string) bool {
	// Skip hidden, vendor, build, docs, scripts, .reference, .agents, .codex
	parts := strings.Split(rel, string(filepath.Separator))
	for _, p := range parts {
		if strings.HasPrefix(p, ".") || p == "vendor" || p == "build" || p == "node_modules" {
			return true
		}
	}
	// Skip top-level docs and scripts
	if len(parts) >= 1 && (parts[0] == "docs" || parts[0] == "scripts") {
		return true
	}
	return false
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
			return "", fmt.Errorf("could not locate repository root")
		}
		dir = parent
	}
}

func printMarkdown(r Report) {
	fmt.Println("# Migration Scan Report")
	fmt.Println()
	fmt.Println(goProjectHeading)
	fmt.Printf("| Metric | Value |\n|---|---:|\n")
	fmt.Printf("| Production files | %d |\n", r.GoProject.ProductionFiles)
	fmt.Printf("| Production lines | %d |\n", r.GoProject.ProductionLines)
	fmt.Printf("| Test files | %d |\n", r.GoProject.TestFiles)
	fmt.Printf("| Test lines | %d |\n", r.GoProject.TestLines)
	fmt.Printf("| Packages | %d |\n", r.GoProject.Packages)
	fmt.Printf("| Built-in tool constructors | %d |\n", r.GoProject.ToolConstructors)
	fmt.Printf("| Command implementation files | %d |\n", r.GoProject.CommandFiles)
	fmt.Println()
	fmt.Println("## Reference Project (claude-code-ripe)")
	fmt.Printf("| Metric | Value |\n|---|---:|\n")
	fmt.Printf("| TS/TSX files | %d |\n", r.ReferenceProject.TSFiles)
	fmt.Printf("| TS/TSX lines | %d |\n", r.ReferenceProject.TSLines)
	fmt.Printf("| Command dirs | %d |\n", r.ReferenceProject.CommandDirs)
	fmt.Printf("| Tool dirs | %d |\n", r.ReferenceProject.ToolDirs)
	fmt.Println()
	fmt.Println("## TUI")
	fmt.Printf("| Metric | Value |\n|---|---:|\n")
	fmt.Printf("| Production files | %d |\n", r.TUI.ProductionFiles)
	fmt.Printf("| Production lines | %d |\n", r.TUI.ProductionLines)
	fmt.Printf("| Test files | %d |\n", r.TUI.TestFiles)
	fmt.Printf("| Test lines | %d |\n", r.TUI.TestLines)
	fmt.Println()
	fmt.Println("## Package Liveness")
	fmt.Printf("| Metric | Value |\n|---|---:|\n")
	fmt.Printf("| Go-list packages (including scripts) | %d |\n", r.Liveness.Packages)
	fmt.Printf("| Zero-production-import main entrypoints | %d |\n", len(r.Liveness.ZeroProductionImportMain))
	fmt.Printf("| Zero-production-import non-entrypoints | %d |\n", len(r.Liveness.ZeroProductionImportOther))
	fmt.Println()
	if len(r.Liveness.ZeroProductionImportMain) > 0 {
		fmt.Println("### Zero-production-import main entrypoints")
		for _, p := range r.Liveness.ZeroProductionImportMain {
			fmt.Printf("- `%s`\n", p)
		}
		fmt.Println()
	}
	if len(r.Liveness.ZeroProductionImportOther) > 0 {
		fmt.Println("### Zero-production-import non-entrypoints")
		for _, p := range r.Liveness.ZeroProductionImportOther {
			fmt.Printf("- `%s`\n", p)
		}
		fmt.Println()
	}
	fmt.Println("---")
	fmt.Println("Compare these numbers against `docs/migration/STATUS.md`, then run `go run ./scripts/migration_manifest.go check` separately.")
}

const goProjectHeading = "## Go Project (YHC)"

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "migration scan:", err)
	os.Exit(1)
}
