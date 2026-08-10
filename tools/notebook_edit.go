package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// Notebook represents the top-level structure of a .ipynb file.
type Notebook struct {
	Cells         []NotebookCell `json:"cells"`
	Metadata      map[string]any `json:"metadata"`
	NBFormat      int            `json:"nbformat"`
	NBFormatMinor int            `json:"nbformat_minor"`
}

// NotebookCell represents a single cell in a Jupyter notebook.
type NotebookCell struct {
	CellType string         `json:"cell_type"`
	Source   []string       `json:"source"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Outputs  []any          `json:"outputs,omitempty"`
}

// NotebookEditTool returns the tool implementation for editing Jupyter notebook cells.
func NotebookEditTool() ToolImpl {
	impl := ToolImpl{
		Info: &schema.ToolInfo{
			Name: "NotebookEdit",
			Desc: "Edits cells in Jupyter notebook (.ipynb) files. Can insert, replace, or delete cells by index.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"notebook_path": {Type: schema.String, Desc: "Path to the .ipynb notebook file", Required: true},
				"command":       {Type: schema.String, Desc: "Operation: 'insert', 'replace', 'delete', 'move'", Required: true},
				"cell_index":    {Type: schema.Integer, Desc: "The 0-based index of the cell to operate on", Required: true},
				"cell_type":     {Type: schema.String, Desc: "Cell type: 'code', 'markdown', 'raw' (for insert/replace)"},
				"source":        {Type: schema.String, Desc: "The cell content/source (for insert/replace)"},
				"target_index":  {Type: schema.Integer, Desc: "Target index for move operation"},
			}),
		},
		Execute: executeNotebookEdit,
	}
	impl.ExecuteCtx = func(ctx context.Context, input string) (string, error) {
		rewritten, err := rewriteExecutionPathInput(
			ctx,
			input,
			"notebook_path",
			false,
		)
		if err != nil {
			return "", fmt.Errorf("notebook_edit: %w", err)
		}
		return impl.Execute(rewritten)
	}
	return impl
}

func executeNotebookEdit(input string) (string, error) {
	var params struct {
		NotebookPath string `json:"notebook_path"`
		Command      string `json:"command"`
		CellIndex    int    `json:"cell_index"`
		CellType     string `json:"cell_type"`
		Source       string `json:"source"`
		TargetIndex  *int   `json:"target_index"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("notebook_edit: invalid params: %w", err)
	}
	if params.NotebookPath == "" {
		return "", fmt.Errorf("notebook_edit: notebook_path is required")
	}
	if params.Command == "" {
		return "", fmt.Errorf("notebook_edit: command is required")
	}

	// Validate command
	switch params.Command {
	case "insert", "replace", "delete", "move":
	default:
		return "", fmt.Errorf("notebook_edit: invalid command %q, must be one of: insert, replace, delete, move", params.Command)
	}

	// Read the notebook
	nb, err := readNotebook(params.NotebookPath)
	if err != nil {
		return "", fmt.Errorf("notebook_edit: %w", err)
	}

	numCells := len(nb.Cells)

	switch params.Command {
	case "insert":
		if params.CellIndex < 0 || params.CellIndex > numCells {
			return "", fmt.Errorf("notebook_edit: cell_index %d out of range [0, %d] for insert", params.CellIndex, numCells)
		}
		if params.CellType == "" {
			return "", fmt.Errorf("notebook_edit: cell_type is required for insert")
		}
		if !isValidCellType(params.CellType) {
			return "", fmt.Errorf("notebook_edit: invalid cell_type %q, must be one of: code, markdown, raw", params.CellType)
		}
		newCell := makeCell(params.CellType, params.Source)
		// Insert at index
		nb.Cells = append(nb.Cells, NotebookCell{})
		copy(nb.Cells[params.CellIndex+1:], nb.Cells[params.CellIndex:])
		nb.Cells[params.CellIndex] = newCell

		if err := writeNotebook(params.NotebookPath, nb); err != nil {
			return "", fmt.Errorf("notebook_edit: %w", err)
		}
		return fmt.Sprintf("Inserted %s cell at index %d in %s (notebook now has %d cells)",
			params.CellType, params.CellIndex, params.NotebookPath, len(nb.Cells)), nil

	case "replace":
		if params.CellIndex < 0 || params.CellIndex >= numCells {
			return "", fmt.Errorf("notebook_edit: cell_index %d out of range [0, %d) for replace", params.CellIndex, numCells)
		}
		if params.CellType == "" {
			// Keep the existing cell type if not specified
			params.CellType = nb.Cells[params.CellIndex].CellType
		}
		if !isValidCellType(params.CellType) {
			return "", fmt.Errorf("notebook_edit: invalid cell_type %q, must be one of: code, markdown, raw", params.CellType)
		}
		nb.Cells[params.CellIndex] = makeCell(params.CellType, params.Source)

		if err := writeNotebook(params.NotebookPath, nb); err != nil {
			return "", fmt.Errorf("notebook_edit: %w", err)
		}
		return fmt.Sprintf("Replaced cell at index %d with %s cell in %s",
			params.CellIndex, params.CellType, params.NotebookPath), nil

	case "delete":
		if params.CellIndex < 0 || params.CellIndex >= numCells {
			return "", fmt.Errorf("notebook_edit: cell_index %d out of range [0, %d) for delete", params.CellIndex, numCells)
		}
		deletedType := nb.Cells[params.CellIndex].CellType
		nb.Cells = append(nb.Cells[:params.CellIndex], nb.Cells[params.CellIndex+1:]...)

		if err := writeNotebook(params.NotebookPath, nb); err != nil {
			return "", fmt.Errorf("notebook_edit: %w", err)
		}
		return fmt.Sprintf("Deleted %s cell at index %d from %s (notebook now has %d cells)",
			deletedType, params.CellIndex, params.NotebookPath, len(nb.Cells)), nil

	case "move":
		if params.CellIndex < 0 || params.CellIndex >= numCells {
			return "", fmt.Errorf("notebook_edit: cell_index %d out of range [0, %d) for move", params.CellIndex, numCells)
		}
		if params.TargetIndex == nil {
			return "", fmt.Errorf("notebook_edit: target_index is required for move")
		}
		targetIdx := *params.TargetIndex
		if targetIdx < 0 || targetIdx >= numCells {
			return "", fmt.Errorf("notebook_edit: target_index %d out of range [0, %d) for move", targetIdx, numCells)
		}
		if params.CellIndex == targetIdx {
			return fmt.Sprintf("Cell at index %d is already at target index %d in %s",
				params.CellIndex, targetIdx, params.NotebookPath), nil
		}

		// Remove cell from source position and insert at target
		cell := nb.Cells[params.CellIndex]
		nb.Cells = append(nb.Cells[:params.CellIndex], nb.Cells[params.CellIndex+1:]...)
		// Insert at target (adjust for removal if needed)
		nb.Cells = append(nb.Cells, NotebookCell{})
		copy(nb.Cells[targetIdx+1:], nb.Cells[targetIdx:])
		nb.Cells[targetIdx] = cell

		if err := writeNotebook(params.NotebookPath, nb); err != nil {
			return "", fmt.Errorf("notebook_edit: %w", err)
		}
		return fmt.Sprintf("Moved %s cell from index %d to index %d in %s",
			cell.CellType, params.CellIndex, targetIdx, params.NotebookPath), nil
	}

	return "", fmt.Errorf("notebook_edit: unexpected command %q", params.Command)
}

// readNotebook reads and parses a .ipynb file.
func readNotebook(path string) (*Notebook, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading notebook: %w", err)
	}
	var nb Notebook
	if err := json.Unmarshal(data, &nb); err != nil {
		return nil, fmt.Errorf("parsing notebook JSON: %w", err)
	}
	return &nb, nil
}

// writeNotebook writes a notebook back to disk with proper formatting (1-space indent).
func writeNotebook(path string, nb *Notebook) error {
	data, err := json.MarshalIndent(nb, "", " ")
	if err != nil {
		return fmt.Errorf("marshaling notebook: %w", err)
	}
	// Ensure trailing newline
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing notebook: %w", err)
	}
	return nil
}

// sourceToLines splits source text into the notebook's line format
// (each line ends with \n except possibly the last).
func sourceToLines(source string) []string {
	if source == "" {
		return []string{}
	}
	lines := strings.Split(source, "\n")
	result := make([]string, len(lines))
	for i, line := range lines {
		if i < len(lines)-1 {
			result[i] = line + "\n"
		} else {
			result[i] = line
		}
	}
	return result
}

// isValidCellType checks whether the given cell type is valid.
func isValidCellType(ct string) bool {
	return ct == "code" || ct == "markdown" || ct == "raw"
}

// makeCell creates a new NotebookCell with the given type and source content.
func makeCell(cellType, source string) NotebookCell {
	cell := NotebookCell{
		CellType: cellType,
		Source:   sourceToLines(source),
		Metadata: map[string]any{},
	}
	if cellType == "code" {
		cell.Outputs = []any{}
	}
	return cell
}
