package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/abietic/yhc/engine/memdir"
	"github.com/cloudwego/eino/schema"
)

func EditTool() ToolImpl {
	impl := ToolImpl{
		Info: &schema.ToolInfo{
			Name: "Edit",
			Desc: "Performs exact string replacements in files.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"file_path":   {Type: schema.String, Desc: "The absolute path to the file to modify", Required: true},
				"old_string":  {Type: schema.String, Desc: "The text to replace", Required: true},
				"new_string":  {Type: schema.String, Desc: "The text to replace it with (must be different from old_string)", Required: true},
				"replace_all": {Type: schema.Boolean, Desc: "Replace all occurrences of old_string (default false)"},
			}),
		},
		Execute: func(input string) (string, error) {
			var params struct {
				FilePath   string `json:"file_path"`
				OldString  string `json:"old_string"`
				NewString  string `json:"new_string"`
				ReplaceAll *bool  `json:"replace_all"`
			}
			if err := json.Unmarshal([]byte(input), &params); err != nil {
				return "", fmt.Errorf("edit: invalid params: %w", err)
			}
			if params.FilePath == "" {
				return "", fmt.Errorf("edit: file_path is required")
			}
			if params.OldString == "" {
				return "", fmt.Errorf("edit: old_string is required")
			}

			// Reject no-op edits: old_string == new_string.
			if params.OldString == params.NewString {
				return "No changes to make: old_string and new_string are exactly the same.", nil
			}

			// Reject .ipynb files — use NotebookEdit instead.
			if strings.HasSuffix(strings.ToLower(params.FilePath), ".ipynb") {
				return "File is a Jupyter Notebook. Use the NotebookEdit tool to edit this file.", nil
			}

			// Resolve to absolute path for read-state lookup.
			fullPath := params.FilePath
			if !filepath.IsAbs(fullPath) {
				if wd, err := os.Getwd(); err == nil {
					fullPath = filepath.Join(wd, fullPath)
				}
			}
			fullPath = filepath.Clean(fullPath)

			// File-not-read guard: reject if file was never read via Read tool.
			if !HasFileBeenRead(fullPath) {
				return "File has not been read yet. Read it first before editing it.", nil
			}

			data, err := os.ReadFile(fullPath)
			if err != nil {
				return "", fmt.Errorf("edit: %w", err)
			}

			content := string(data)
			replaceAll := params.ReplaceAll != nil && *params.ReplaceAll
			count := strings.Count(content, params.OldString)

			if count == 0 {
				return "", fmt.Errorf("edit: old_string not found in %s", params.FilePath)
			}
			if count > 1 && !replaceAll {
				return "", fmt.Errorf("edit: old_string found %d times in %s — provide a larger string with more surrounding context to make it unique or use replace_all", count, params.FilePath)
			}
			// Mirrors TS: replace_all requires ≥2 occurrences to be meaningful.
			if replaceAll && count < 2 {
				return "", fmt.Errorf("edit: replace_all was set but old_string was only found %d time in %s — either remove replace_all or provide a string that appears multiple times", count, params.FilePath)
			}

			var newContent string
			if replaceAll {
				newContent = strings.ReplaceAll(content, params.OldString, params.NewString)
			} else {
				newContent = strings.Replace(content, params.OldString, params.NewString, 1)
			}
			if err := memdir.ValidateTeamMemoryContent(fullPath, newContent); err != nil {
				return "", fmt.Errorf("edit: %w", err)
			}

			if err := os.WriteFile(fullPath, []byte(newContent), 0o644); err != nil {
				return "", fmt.Errorf("edit: write: %w", err)
			}

			// Update read state after successful edit so subsequent edits don't fail the guard.
			// Mirrors TS: readFileState is updated with new content after write.
			RecordFileRead(fullPath, false)

			if replaceAll {
				return fmt.Sprintf("Replaced %d occurrences in %s", count, params.FilePath), nil
			}
			return fmt.Sprintf("Replaced 1 occurrence in %s", params.FilePath), nil
		},
	}
	impl.ExecuteCtx = func(ctx context.Context, input string) (string, error) {
		rewritten, err := rewriteExecutionPathInput(ctx, input, "file_path", false)
		if err != nil {
			return "", fmt.Errorf("edit: %w", err)
		}
		return impl.Execute(rewritten)
	}
	return impl
}
