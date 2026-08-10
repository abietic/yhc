package permission

// FileEditToolNames are tools that perform file writes/edits.
// Auto-allowed in acceptEdits mode when the target path is within the working directory.
var FileEditToolNames = map[string]bool{
	"Write": true,
	"Edit":  true,
}

// AcceptEditsCheck evaluates whether a tool use should be auto-allowed
// under acceptEdits mode. Returns (true, "") if allowed, (false, "") if
// the mode does not apply (should fall through to normal permission check).
// Mirrors filesystem.ts:1360-1375.
func AcceptEditsCheck(toolName string, input map[string]any, cwd string, additionalDirs ...string) (allowed bool) {
	if cwd == "" {
		return false
	}

	// File edit tools: auto-allow if path is within cwd
	if FileEditToolNames[toolName] {
		filePath, _ := input["file_path"].(string)
		if filePath == "" {
			return false
		}
		roots := append([]string{cwd}, additionalDirs...)
		return PermissionPathsWithinRoots(ResolvePermissionPath(filePath, cwd), roots)
	}

	return false
}

// isPathInWorkingDir checks if the given path is within the working directory.
// Mirrors filesystem.ts pathInAllowedWorkingPath.
func isPathInWorkingDir(path, cwd string) bool {
	return PermissionPathsWithinRoots(ResolvePermissionPath(path, cwd), []string{cwd})
}
