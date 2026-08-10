package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abietic/yhc/engine/memdir"
	"github.com/cloudwego/eino/schema"
)

const writeToolPrompt = `Writes a file to the local filesystem.

Usage:
- This tool will overwrite the existing file if there is one at the provided path.
- If this is an existing file, you MUST use the Read tool first to read the file's contents. This tool will fail if you did not read the file first.
- Prefer the Edit tool for modifying existing files — it only sends the diff. Only use this tool to create new files or for complete rewrites.
- NEVER create documentation files (*.md) or README files unless explicitly requested by the User.
- Only use emojis if the user explicitly requests it. Avoid writing emojis to files unless asked.`

func WriteTool() ToolImpl {
	impl := ToolImpl{
		Info: &schema.ToolInfo{
			Name: "Write",
			Desc: writeToolPrompt,
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"file_path": {Type: schema.String, Desc: "The absolute path to the file to write (must be absolute, not relative)", Required: true},
				"content":   {Type: schema.String, Desc: "The content to write to the file", Required: true},
			}),
		},
		Execute: func(input string) (string, error) {
			var params struct {
				FilePath string `json:"file_path"`
				Content  string `json:"content"`
			}
			if err := json.Unmarshal([]byte(input), &params); err != nil {
				return "", fmt.Errorf("write: invalid params: %w", err)
			}
			if params.FilePath == "" {
				return "", fmt.Errorf("write: file_path is required")
			}

			// Resolve absolute path.
			fullPath := params.FilePath
			if !filepath.IsAbs(fullPath) {
				if wd, err := os.Getwd(); err == nil {
					fullPath = filepath.Join(wd, fullPath)
				}
			}
			fullPath = filepath.Clean(fullPath)
			if err := memdir.ValidateTeamMemoryContent(fullPath, params.Content); err != nil {
				return "", fmt.Errorf("write: %w", err)
			}

			// File-read guard: if the file already exists, it must have been read first.
			if _, err := os.Stat(fullPath); err == nil {
				// File exists — check read state.
				if !HasFileBeenRead(fullPath) {
					return "File has not been read yet. Read it first before writing to it.", nil
				}
			}
			// If file doesn't exist (os.IsNotExist), allow creation without prior read.

			dir := filepath.Dir(fullPath)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("write: create directory: %w", err)
			}
			if err := os.WriteFile(fullPath, []byte(params.Content), 0o644); err != nil {
				return "", fmt.Errorf("write: %w", err)
			}

			// Update read state after write so subsequent edits don't fail the file-read guard.
			// Mirrors TS: readFileState updated with new content + mtime after write.
			RecordFileRead(fullPath, false)

			return fmt.Sprintf("Wrote %d bytes to %s", len(params.Content), params.FilePath), nil
		},
	}
	impl.ExecuteCtx = func(ctx context.Context, input string) (string, error) {
		rewritten, err := rewriteExecutionPathInput(ctx, input, "file_path", false)
		if err != nil {
			return "", fmt.Errorf("write: %w", err)
		}
		return impl.Execute(rewritten)
	}
	return impl
}
