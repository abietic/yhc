package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNotebookEditCellOperations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example.ipynb")
	initial := &Notebook{
		Cells: []NotebookCell{
			makeCell("markdown", "first"),
			makeCell("code", "second"),
		},
		Metadata:      map[string]any{"language": "python"},
		NBFormat:      4,
		NBFormatMinor: 5,
	}
	writeNotebookFixture(t, path, initial)

	steps := []struct {
		input string
		check func(*Notebook)
	}{
		{`{"notebook_path":` + quoteJSON(path) + `,"command":"insert","cell_index":1,"cell_type":"raw","source":"inserted"}`, func(nb *Notebook) {
			if len(nb.Cells) != 3 || nb.Cells[1].CellType != "raw" || notebookSource(nb.Cells[1]) != "inserted" {
				t.Fatalf("insert result = %#v", nb.Cells)
			}
		}},
		{`{"notebook_path":` + quoteJSON(path) + `,"command":"replace","cell_index":0,"cell_type":"code","source":"replaced"}`, func(nb *Notebook) {
			if nb.Cells[0].CellType != "code" || notebookSource(nb.Cells[0]) != "replaced" {
				t.Fatalf("replace result = %#v", nb.Cells[0])
			}
		}},
		{`{"notebook_path":` + quoteJSON(path) + `,"command":"move","cell_index":2,"target_index":0}`, func(nb *Notebook) {
			if notebookSource(nb.Cells[0]) != "second" {
				t.Fatalf("move result = %#v", nb.Cells)
			}
		}},
		{`{"notebook_path":` + quoteJSON(path) + `,"command":"delete","cell_index":1}`, func(nb *Notebook) {
			if len(nb.Cells) != 2 || notebookSource(nb.Cells[0]) != "second" || notebookSource(nb.Cells[1]) != "inserted" {
				t.Fatalf("delete result = %#v", nb.Cells)
			}
		}},
	}

	for _, step := range steps {
		if _, err := executeNotebookEdit(step.input); err != nil {
			t.Fatal(err)
		}
		nb, err := readNotebook(path)
		if err != nil {
			t.Fatal(err)
		}
		step.check(nb)
		if nb.NBFormat != 4 || nb.Metadata["language"] != "python" {
			t.Fatalf("notebook metadata changed: %#v", nb)
		}
	}
}

func writeNotebookFixture(t *testing.T, path string, notebook *Notebook) {
	t.Helper()
	data, err := json.Marshal(notebook)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func quoteJSON(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func notebookSource(cell NotebookCell) string {
	result := ""
	for _, line := range cell.Source {
		result += line
	}
	return result
}
