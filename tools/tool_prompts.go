package tools

// withPrompt adds a Prompt function to a ToolImpl.
func withPrompt(impl ToolImpl, fn func() string) ToolImpl {
	impl.Prompt = fn
	return impl
}

// Tool prompt functions return per-tool instructions. AssembleToolPool includes
// them in the selected tool's description without mutating the registry.

func bashToolPrompt() string {
	return `# Instructions for Bash tool usage

- Prefer dedicated tools over Bash when one fits (Read, Edit, Write, Glob, Grep) — reserve Bash for shell-only operations.
- Use the ` + "`run_in_background`" + ` parameter only when the result is not needed immediately. Use BashOutput with the returned Shell ID to inspect output and completion.
- You may specify an optional timeout in milliseconds (up to 600000ms / 10 minutes). By default, your command will timeout after 120000ms (2 minutes).
- Always quote file paths that contain spaces with double quotes in your command.
- Try to maintain your current working directory throughout the session by using absolute paths and avoiding usage of cd.
- When issuing multiple commands: if they are independent and can run in parallel, make multiple Bash tool calls. If they depend on each other, use && to chain them.
- For git commands: prefer to create a new commit rather than amending an existing commit. Never skip hooks (--no-verify) unless the user explicitly asks.`
}

func readToolPrompt() string {
	return `# Instructions for Read tool usage

- The file_path parameter must be an absolute path, not a relative path.
- By default, it reads up to 250 lines starting from the beginning of the file; one call can read at most 2000 lines.
- When you already know which part of the file you need, only read that part using offset and limit.
- Results are returned using cat -n format, with line numbers starting at 1.
- This tool can read images (PNG, JPG, etc), PDFs, and Jupyter notebooks (.ipynb).
- For PDFs over 10 pages, provide the pages parameter (for example, "1-5"). PDF pages are 1-indexed and one call is limited to 20 pages.
- This tool can only read files, not directories. To read a directory, use ls via the Bash tool.
- A successful Edit confirms the text replacement, not behavioral correctness. Inspect the relevant final changes and run checks when needed to verify the requested behavior.`
}

func editToolPrompt() string {
	return `# Instructions for Edit tool usage

- You must use the Read tool at least once before editing a file. This tool will error if you attempt an edit without reading the file.
- When editing text from Read tool output, preserve the exact indentation (tabs/spaces) as it appears AFTER the line number prefix.
- ALWAYS prefer editing existing files. NEVER write new files unless explicitly required.
- The edit will FAIL if old_string is not unique in the file. Provide a larger string with more context to make it unique, or use replace_all.
- Use replace_all for replacing and renaming strings across the file (useful for variable renames).`
}

func writeToolPromptSection() string {
	return writeToolPrompt
}

func grepToolPrompt() string {
	return `# Instructions for Grep tool usage

- Supports full regex syntax (e.g., "log.*Error", "function\\s+\\w+").
- Filter files with glob parameter (e.g., "*.js", "**/*.tsx") or type parameter (e.g., "js", "py", "rust").
- Output modes: "content" shows matching lines, "files_with_matches" shows only file paths (default), "count" shows match counts.
- Use -A/-B/-C for context lines around matches.
- Pattern syntax uses ripgrep — literal braces need escaping.`
}
