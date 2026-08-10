package main

import (
	"context"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

type memoryTreeSource map[string]fstest.MapFS

func (source memoryTreeSource) ListFiles(_ context.Context, revision string) ([]string, error) {
	tree, ok := source[revision]
	if !ok {
		return nil, fs.ErrNotExist
	}
	var names []string
	err := fs.WalkDir(tree, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name != "." && !entry.IsDir() {
			names = append(names, name)
		}
		return nil
	})
	return names, err
}

func (source memoryTreeSource) ReadFile(_ context.Context, revision, name string) ([]byte, error) {
	tree, ok := source[revision]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return fs.ReadFile(tree, name)
}

func TestBuildBoundaryReportClassifiesProductionAndTestImports(t *testing.T) {
	policy := boundaryTestPolicy()
	source := memoryTreeSource{
		"base": {},
		"head": {
			"engine/a.go": {Data: []byte(`package engine
import (
	alias "github.com/abietic/yhc/tools"
	_ "github.com/abietic/yhc/tools"
	. "github.com/abietic/yhc/tools"
	"fmt"
	"example.com/external"
)
var _ = alias.Read
`)},
			"engine/a_test.go": {Data: []byte(`package engine
import _ "github.com/abietic/yhc/tools"
`)},
		},
	}

	report, err := buildBoundaryReport(context.Background(), boundaryTestPlan(), policy, source)
	if err != nil {
		t.Fatal(err)
	}
	wantProduction := []ImportEdge{{
		FromModule: "engine-runtime", ToModule: "tool-runtime",
		ImportPath: "github.com/abietic/yhc/tools",
	}}
	wantTest := []ImportEdge{{
		FromModule: "engine-runtime", ToModule: "tool-runtime",
		ImportPath: "github.com/abietic/yhc/tools", TestOnly: true,
	}}
	if !reflect.DeepEqual(report.NewProductionEdges, wantProduction) ||
		!reflect.DeepEqual(report.NewTestEdges, wantTest) {
		t.Fatalf("report edges = production %#v, test %#v", report.NewProductionEdges, report.NewTestEdges)
	}
}

func TestBuildBoundaryReportRejectsSyntaxErrorWithoutSource(t *testing.T) {
	source := memoryTreeSource{
		"base": {},
		"head": {
			"engine/bad.go": {Data: []byte("package engine\nimport SUPER_SECRET_SOURCE\n")},
		},
	}
	_, err := buildBoundaryReport(context.Background(), boundaryTestPlan(), boundaryTestPolicy(), source)
	if err == nil || !strings.Contains(err.Error(), "engine/bad.go") {
		t.Fatalf("buildBoundaryReport() error = %v", err)
	}
	if strings.Contains(err.Error(), "SUPER_SECRET_SOURCE") {
		t.Fatalf("parser diagnostic reflected source: %v", err)
	}
}

func TestBuildBoundaryReportGlobalDiffIgnoresRenameAndDuplicates(t *testing.T) {
	data := []byte(`package engine
import _ "github.com/abietic/yhc/tools"
`)
	source := memoryTreeSource{
		"base": {"engine/old.go": {Data: data}},
		"head": {
			"engine/new.go":   {Data: data},
			"engine/other.go": {Data: data},
		},
	}
	report, err := buildBoundaryReport(context.Background(), boundaryTestPlan(), boundaryTestPolicy(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.NewProductionEdges) != 0 || len(report.NewTestEdges) != 0 {
		t.Fatalf("rename/duplicate produced new edges: %#v", report)
	}
}

func TestModuleForImportUsesLongestPackagePrefix(t *testing.T) {
	policy := boundaryTestPolicy()
	for importPath, want := range map[string]string{
		"github.com/abietic/yhc/internal/tui/render": "tui-adapter",
		"github.com/abietic/yhc/engine/session":      "engine-runtime",
	} {
		got, err := moduleForImport(policy, importPath)
		if err != nil {
			t.Fatalf("moduleForImport(%q): %v", importPath, err)
		}
		if got != want {
			t.Fatalf("moduleForImport(%q) = %q, want %q", importPath, got, want)
		}
	}

	ambiguous := policy
	ambiguous.Modules["other-engine"] = ModulePolicy{Packages: []string{"./engine/..."}}
	if _, err := moduleForImport(ambiguous, "github.com/abietic/yhc/engine/session"); err == nil {
		t.Fatal("moduleForImport accepted equal-length owners")
	}
	if _, err := moduleForImport(policy, "github.com/abietic/yhc/unowned"); err == nil {
		t.Fatal("moduleForImport accepted an unowned internal import")
	}
}

func TestBuildBoundaryReportFindsOnlyNewForbiddenAndFlatViolations(t *testing.T) {
	forbidden := []byte(`package engine
import _ "github.com/abietic/yhc/internal/tui"
`)
	testOnly := []byte(`package engine
import _ "github.com/abietic/yhc/internal/tui"
`)
	nested := []byte("package nested\n")
	source := memoryTreeSource{
		"base": {},
		"head": {
			"engine/forbidden.go":      {Data: forbidden},
			"engine/forbidden_test.go": {Data: testOnly},
			"tools/nested/new.go":      {Data: nested},
		},
	}
	report, err := buildBoundaryReport(context.Background(), boundaryTestPlan(), boundaryTestPolicy(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ForbiddenNewEdges) != 1 || report.ForbiddenNewEdges[0].TestOnly {
		t.Fatalf("forbidden edges = %#v", report.ForbiddenNewEdges)
	}
	if !reflect.DeepEqual(report.NewFlatPackageViolations, []string{"tools/nested"}) {
		t.Fatalf("flat violations = %#v", report.NewFlatPackageViolations)
	}

	source["base"] = fstest.MapFS{
		"engine/forbidden.go":      {Data: forbidden},
		"engine/forbidden_test.go": {Data: testOnly},
		"tools/nested/new.go":      {Data: nested},
	}
	report, err = buildBoundaryReport(context.Background(), boundaryTestPlan(), boundaryTestPolicy(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.NewProductionEdges) != 0 || len(report.NewTestEdges) != 0 ||
		len(report.ForbiddenNewEdges) != 0 || len(report.NewFlatPackageViolations) != 0 {
		t.Fatalf("baseline violations became new: %#v", report)
	}
}

func boundaryTestPlan() Plan {
	return Plan{
		SchemaVersion: 1,
		Base:          "base",
		Head:          "head",
		DiffDigest:    strings.Repeat("a", 64),
	}
}

func boundaryTestPolicy() Policy {
	return Policy{
		Repository: "github.com/abietic/yhc",
		Modules: map[string]ModulePolicy{
			"engine-runtime": {Packages: []string{"./engine/..."}},
			"tool-runtime":   {Packages: []string{"./tools"}},
			"tui-adapter":    {Packages: []string{"./internal/tui"}},
			"cli-entrypoint": {Packages: []string{"./cmd/yhc/..."}},
			"acp-adapter":    {Packages: []string{"./server/acp"}},
			"mcp-adapter":    {Packages: []string{"./server/mcp"}},
		},
		Boundaries: BoundaryPolicy{
			ForbiddenProductionEdges: []ForbiddenEdge{{
				From: []string{"engine-runtime", "tool-runtime"},
				To:   []string{"cli-entrypoint", "tui-adapter", "acp-adapter", "mcp-adapter"},
			}},
			FlatPackageRoots: []string{"tools"},
		},
	}
}
