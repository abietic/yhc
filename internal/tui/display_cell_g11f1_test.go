package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine"
)

type g11F1SelectorSite struct {
	file, function, selector string
}

type g11F1SelectorClassification struct {
	owner            string
	removalCondition string
	calls            int
}

type g11F1ListedPackage struct {
	ImportPath      string
	Dir             string
	Export          string
	CompiledGoFiles []string
}

type g11F1TypedPackage struct {
	files   []*ast.File
	fileSet *token.FileSet
	info    *types.Info
	root    string
}

type g11F1BuildTarget struct {
	goos, goarch string
}

var g11F1ProductionGeometryTargets = [...]g11F1BuildTarget{
	{goos: "linux", goarch: "amd64"},
	{goos: "darwin", goarch: "amd64"},
	{goos: "darwin", goarch: "arm64"},
	{goos: "windows", goarch: "amd64"},
}

func TestG11F1LegacyChatRowsConsumeSelectedProfile(t *testing.T) {
	profile := g11D1Profile(8)
	env := newRenderEnvironment(defaultStyles(), profile)
	content := "ab\t1234567"

	selected := (&UserMessage{content: content}).RenderWithEnvironment(16, env)
	if strings.Contains(selected, "\t") || len(strings.Split(selected, "\n")) != 1 {
		t.Fatalf("selected-profile user row retained a tab: %q", selected)
	}
	compatibility := (&UserMessage{content: content}).Render(16, defaultStyles())
	if len(strings.Split(compatibility, "\n")) != 2 {
		t.Fatalf("default compatibility row did not retain default-grid wrapping: %q", compatibility)
	}

	item := newAgentTranscriptHistoryItem(engine.AgentTranscriptMessage{
		Role: "assistant", Content: content, Completed: true,
	})
	rendered := item.Render(HistoryRenderContext{
		Width: 12, Environment: env, Mode: HistoryRenderRich,
	})
	for row, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, "\t") || profile.width(line) > 12 {
			t.Fatalf("selected-profile transcript row %d is unbounded: %q", row, line)
		}
	}
}

func TestG11F1ProductionGeometrySelectorsAreClassified(t *testing.T) {
	g11F1AssertProductionGeometryTargetMatrix(t)

	// These are semantic counters, source offsets, or explicit library adapters;
	// none selects the cell grid used to compose the final frame.
	allowed := map[g11F1SelectorSite]g11F1SelectorClassification{
		// Owner: streaming statistics. Removal condition: character/token
		// estimates stop being defined in Unicode scalar values.
		{"streaming.go", "StreamingRenderer.OnDelta", "unicode/utf8.RuneCountInString"}: {
			owner: "streaming statistics", removalCondition: "metric definition changes", calls: 1,
		},
		// Owner: attachment admission. Removal condition: the paste threshold
		// stops being defined in Unicode scalar values.
		{"attachments/attachments.go", "IsLargePaste", "unicode/utf8.RuneCountInString"}: {
			owner: "attachment admission", removalCondition: "paste threshold unit changes", calls: 1,
		},
		// Owner: Bubbles composer source offsets. Removal condition: the editor
		// exposes byte/EGC-native element offsets instead of rune offsets.
		{"composer_elements.go", "App.handleComposerPaste", "unicode/utf8.RuneCountInString"}: {
			owner: "composer rune offsets", removalCondition: "editor offset contract changes", calls: 2,
		},
		{"composer_elements.go", "App.addComposerImageAt", "unicode/utf8.RuneCountInString"}: {
			owner: "composer rune offsets", removalCondition: "editor offset contract changes", calls: 1,
		},
		{"queued_input.go", "App.restoreQueuedPromptDraft", "unicode/utf8.RuneCountInString"}: {
			owner: "composer rune offsets", removalCondition: "editor offset contract changes", calls: 2,
		},
		{"composer_elements.go", "textareaCursorRuneOffset", "unicode/utf8.RuneCountInString"}: {
			owner: "composer rune offsets", removalCondition: "editor offset contract changes", calls: 2,
		},
		{"composer_mentions.go", "App.acceptMentionHint", "unicode/utf8.RuneCountInString"}: {
			owner: "composer rune offsets", removalCondition: "editor offset contract changes", calls: 1,
		},
		{"key_actions.go", "App.handleVimEditorKey", "unicode/utf8.RuneCountInString"}: {
			owner: "Vim editor cursor", removalCondition: "Vim adapter accepts native editor offsets", calls: 1,
		},
		{"app.go", "App.sendSlashCommand", "unicode/utf8.RuneCountInString"}: {
			owner: "Vim editor cursor", removalCondition: "Vim adapter accepts native editor offsets", calls: 1,
		},
		// Owner: DisplayCellProfile low-level policy. Removal condition: the
		// selected width library exposes lone/pair regional-indicator metadata.
		{"display_cell.go", "DisplayCellProfile.clusterWidth", "unicode/utf8.RuneCountInString"}: {
			owner: "DisplayCellProfile", removalCondition: "width library exposes RI metadata", calls: 1,
		},
		// Owner: DisplayCellProfile's selected grapheme iterator. Removal
		// condition: the width library exposes projected clusters without its
		// measured-cell input.
		{"content_geometry.go", "contentWrapLines", "github.com/clipperhouse/displaywidth.Width"}: {
			owner: "DisplayCellProfile iterator", removalCondition: "iterator exposes projected clusters", calls: 1,
		},
		{"display_cell.go", "DisplayCellProfile.measure", "github.com/clipperhouse/displaywidth.Width"}: {
			owner: "DisplayCellProfile iterator", removalCondition: "iterator exposes projected clusters", calls: 1,
		},
		{"display_cell.go", "DisplayCellProfile.clusters", "github.com/clipperhouse/displaywidth.Width"}: {
			owner: "DisplayCellProfile iterator", removalCondition: "iterator exposes projected clusters", calls: 1,
		},
		{"display_cell.go", "DisplayCellProfile.projectCluster", "github.com/clipperhouse/displaywidth.Width"}: {
			owner: "DisplayCellProfile iterator", removalCondition: "iterator exposes projected clusters", calls: 1,
		},
		{"display_cell.go", "DisplayCellProfile.expandTabs", "github.com/clipperhouse/displaywidth.Width"}: {
			owner: "DisplayCellProfile iterator", removalCondition: "iterator exposes projected clusters", calls: 1,
		},
		{"display_cell.go", "DisplayCellProfile.truncateAt", "github.com/clipperhouse/displaywidth.Width"}: {
			owner: "DisplayCellProfile iterator", removalCondition: "iterator exposes projected clusters", calls: 1,
		},
		{"display_cell.go", "DisplayCellProfile.wrapLine", "github.com/clipperhouse/displaywidth.Width"}: {
			owner: "DisplayCellProfile iterator", removalCondition: "iterator exposes projected clusters", calls: 1,
		},
		{"display_cell.go", "displayCellControlState.observe", "github.com/clipperhouse/displaywidth.Width"}: {
			owner: "DisplayCellProfile iterator", removalCondition: "iterator exposes projected clusters", calls: 1,
		},
		// Owner: Bubbles textarea height estimation. Removal condition: Bubbles
		// exposes its wrapped row count or accepts DisplayCellProfile.
		{"layout.go", "countWrappedLines", "github.com/rivo/uniseg.StringWidth"}: {
			owner: "Bubbles textarea adapter", removalCondition: "Bubbles exposes wrapped rows", calls: 2,
		},
		{"plan_dialog.go", "PlanDialog.feedbackEditorView", "charm.land/bubbles/v2/textarea.Width"}: {
			owner: "Bubbles textarea adapter", removalCondition: "Bubbles exposes editor content width", calls: 1,
		},
		// Owner: rendered-widget row accounting. Removal condition: hint and
		// task-tree renderers return their row count with the rendered value.
		{"app.go", "App.updateLayout", "charm.land/lipgloss/v2.Height"}: {
			owner: "rendered widget row accounting", removalCondition: "renderers return row count", calls: 2,
		},
	}
	seen := make(map[g11F1SelectorSite]int, len(allowed))
	seenLocations := make(map[string]bool)
	seenDeclarations := make(map[string]bool)

	forbiddenDeclarations := map[string]bool{
		"terminalLayoutSafetyWidth": true,
		"truncateDisplay":           true,
		"overlayCentered":           true,
		"widthProfile":              true,
		"defaultWidthProfile":       true,
		"defaultWidthProfileID":     true,
		"renderTable":               true,
		"renderTableWithTheme":      true,
		"renderTableWithProfile":    true,
		"cellMinWidth":              true,
	}
	forbiddenSelectors := map[string]map[string]bool{
		"charm.land/lipgloss/v2": {
			"Width": true, "Height": true, "Place": true,
			"PlaceHorizontal": true, "PlaceVertical": true,
		},
		"github.com/charmbracelet/x/ansi": {
			"StringWidth": true, "Truncate": true, "TruncateLeft": true,
			"TruncateRight": true, "Wrap": true,
		},
		"charm.land/glamour/v2/ansi": {
			"StringWidth": true, "Truncate": true, "TruncateLeft": true,
			"TruncateRight": true, "Wrap": true,
		},
		"github.com/rivo/uniseg": {"StringWidth": true},
		"unicode/utf8":           {"RuneCountInString": true},
	}
	packagesUnderTest := g11F1LoadTypedProductionPackages(t)
	for _, loaded := range packagesUnderTest {
		for _, file := range loaded.files {
			path := loaded.fileSet.Position(file.Pos()).Filename
			relativePath, relativeErr := filepath.Rel(loaded.root, path)
			if relativeErr != nil {
				t.Fatalf("relativize production TUI source %s: %v", path, relativeErr)
			}
			normalizedPath := filepath.ToSlash(relativePath)
			inspectSelectors := func(function string, node ast.Node) {
				ast.Inspect(node, func(node ast.Node) bool {
					selector, ok := node.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					selectorKey := ""
					var object types.Object
					if selection := loaded.info.Selections[selector]; selection != nil {
						object = selection.Obj()
					} else {
						object = loaded.info.Uses[selector.Sel]
					}
					method, ok := object.(*types.Func)
					if !ok || method.Pkg() == nil {
						return true
					}
					importPath := method.Pkg().Path()
					if forbiddenSelectors[importPath][method.Name()] ||
						method.Name() == "Width" {
						// Type information distinguishes methods from Width fields
						// and covers calls, method values, method expressions, and
						// chained receivers without a syntax-shape bypass.
						selectorKey = importPath + "." + method.Name()
					}
					if selectorKey == "" {
						return true
					}
					position := loaded.fileSet.Position(selector.Pos())
					location := fmt.Sprintf(
						"%s:%d:%d:%s",
						normalizedPath,
						position.Line,
						position.Column,
						selectorKey,
					)
					if seenLocations[location] {
						return true
					}
					seenLocations[location] = true
					site := g11F1SelectorSite{
						file: normalizedPath, function: function,
						selector: selectorKey,
					}
					classification, classified := allowed[site]
					if !classified {
						t.Errorf(
							"%s:%d %s selects unclassified production geometry method %s",
							normalizedPath,
							position.Line,
							function,
							site.selector,
						)
						return true
					}
					if classification.owner == "" ||
						classification.removalCondition == "" {
						t.Errorf("%s has incomplete classification: %#v", site.selector, classification)
					}
					seen[site]++
					return true
				})
			}

			for _, declaration := range file.Decls {
				switch typed := declaration.(type) {
				case *ast.FuncDecl:
					function := g11F1FunctionKey(typed)
					declaration := normalizedPath + ":" + function
					if forbiddenDeclarations[typed.Name.Name] &&
						!seenDeclarations[declaration] {
						seenDeclarations[declaration] = true
						t.Errorf("%s declares deleted compatibility owner %s", normalizedPath, function)
					}
					inspectSelectors(function, typed.Body)
				case *ast.GenDecl:
					for _, spec := range typed.Specs {
						switch named := spec.(type) {
						case *ast.TypeSpec:
							declaration := normalizedPath + ":" + named.Name.Name
							if forbiddenDeclarations[named.Name.Name] &&
								!seenDeclarations[declaration] {
								seenDeclarations[declaration] = true
								t.Errorf("%s declares deleted compatibility type %s", normalizedPath, named.Name.Name)
							}
						case *ast.ValueSpec:
							for _, name := range named.Names {
								declaration := normalizedPath + ":" + name.Name
								if forbiddenDeclarations[name.Name] &&
									!seenDeclarations[declaration] {
									seenDeclarations[declaration] = true
									t.Errorf("%s declares deleted compatibility value %s", normalizedPath, name.Name)
								}
							}
							for _, value := range named.Values {
								inspectSelectors("package initializer", value)
							}
						}
					}
				}
			}
		}
	}
	for site, classification := range allowed {
		if seen[site] != classification.calls {
			t.Errorf(
				"classified selector %#v observed %d calls, want %d; remove stale "+
					"allowlist entries or classify the changed owner",
				site,
				seen[site],
				classification.calls,
			)
		}
	}
}

func g11F1LoadTypedProductionPackages(t *testing.T) []g11F1TypedPackage {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve production TUI source root: %v", err)
	}

	// The repository's supported build matrix is Linux, Darwin, and Windows.
	// Load each target so build-constrained production files cannot bypass the
	// type-aware selector inventory. Common source locations are de-duplicated
	// by the caller before exact allowlist counts are checked.
	var typed []g11F1TypedPackage
	for _, target := range g11F1ProductionGeometryTargets {
		command := exec.Command(
			"go",
			"list",
			"-compiled",
			"-export",
			"-deps",
			"-json",
			"./...",
		)
		command.Env = append(
			os.Environ(),
			"CGO_ENABLED=0",
			"GOARCH="+target.goarch,
			"GOOS="+target.goos,
		)
		output, commandErr := command.CombinedOutput()
		if commandErr != nil {
			t.Fatalf(
				"go list production TUI packages for %s: %v\n%s",
				target.goos+"/"+target.goarch,
				commandErr,
				output,
			)
		}

		decoder := json.NewDecoder(bytes.NewReader(output))
		var listed []g11F1ListedPackage
		exportFiles := make(map[string]string)
		for {
			var pkg g11F1ListedPackage
			if decodeErr := decoder.Decode(&pkg); decodeErr != nil {
				if errors.Is(decodeErr, io.EOF) {
					break
				}
				t.Fatalf(
					"decode go list output for %s: %v",
					target.goos+"/"+target.goarch,
					decodeErr,
				)
			}
			if pkg.Export != "" {
				exportFiles[pkg.ImportPath] = pkg.Export
			}
			relativeDir, relativeErr := filepath.Rel(root, pkg.Dir)
			if relativeErr == nil &&
				relativeDir != ".." &&
				!strings.HasPrefix(relativeDir, ".."+string(filepath.Separator)) {
				listed = append(listed, pkg)
			}
		}

		for _, pkg := range listed {
			fileSet := token.NewFileSet()
			files := make([]*ast.File, 0, len(pkg.CompiledGoFiles))
			for _, source := range pkg.CompiledGoFiles {
				if !filepath.IsAbs(source) {
					source = filepath.Join(pkg.Dir, source)
				}
				file, parseErr := parser.ParseFile(fileSet, source, nil, 0)
				if parseErr != nil {
					t.Fatalf(
						"parse %s production TUI source %s: %v",
						target.goos+"/"+target.goarch,
						source,
						parseErr,
					)
				}
				files = append(files, file)
			}
			info := &types.Info{
				Selections: make(map[*ast.SelectorExpr]*types.Selection),
				Uses:       make(map[*ast.Ident]types.Object),
			}
			compilerImporter := importer.ForCompiler(
				fileSet,
				"gc",
				func(importPath string) (io.ReadCloser, error) {
					exportFile := exportFiles[importPath]
					if exportFile == "" {
						return nil, os.ErrNotExist
					}
					return os.Open(exportFile)
				},
			)
			if _, checkErr := (&types.Config{Importer: compilerImporter}).Check(
				pkg.ImportPath,
				fileSet,
				files,
				info,
			); checkErr != nil {
				t.Fatalf(
					"type-check %s production package %s: %v",
					target.goos+"/"+target.goarch,
					pkg.ImportPath,
					checkErr,
				)
			}
			typed = append(typed, g11F1TypedPackage{
				files:   files,
				fileSet: fileSet,
				info:    info,
				root:    root,
			})
		}
	}
	return typed
}

func g11F1AssertProductionGeometryTargetMatrix(t *testing.T) {
	t.Helper()
	makefile, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile build matrix: %v", err)
	}
	targetPattern := regexp.MustCompile(
		`build/([a-z0-9]+)-([a-z0-9]+)/yhc(?:\.exe)?`,
	)
	makeTargets := make(map[g11F1BuildTarget]bool)
	for _, match := range targetPattern.FindAllStringSubmatch(string(makefile), -1) {
		makeTargets[g11F1BuildTarget{goos: match[1], goarch: match[2]}] = true
	}
	if len(makeTargets) != len(g11F1ProductionGeometryTargets) {
		t.Fatalf(
			"source-gate build targets=%v do not cover Makefile targets=%v",
			g11F1ProductionGeometryTargets,
			makeTargets,
		)
	}
	for _, target := range g11F1ProductionGeometryTargets {
		if !makeTargets[target] {
			t.Fatalf(
				"source-gate target %s/%s is absent from Makefile build matrix",
				target.goos,
				target.goarch,
			)
		}
	}
}

func g11F1FunctionKey(function *ast.FuncDecl) string {
	key := function.Name.Name
	if function.Recv == nil || len(function.Recv.List) != 1 {
		return key
	}
	receiver := function.Recv.List[0].Type
	if pointer, ok := receiver.(*ast.StarExpr); ok {
		receiver = pointer.X
	}
	if identifier, ok := receiver.(*ast.Ident); ok {
		return identifier.Name + "." + key
	}
	return key
}
