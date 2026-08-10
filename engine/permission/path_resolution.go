package permission

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const maxPermissionSymlinkDepth = 40

// PathResolution contains every filesystem representation relevant to one
// authorization decision. Paths are absolute, cleaned, and deduplicated.
type PathResolution struct {
	Logical string
	Paths   []string
	Unsafe  bool
}

func (r PathResolution) Effective() string {
	if len(r.Paths) > 0 {
		return r.Paths[len(r.Paths)-1]
	}
	return r.Logical
}

// ResolvePermissionPath resolves symlink aliases without requiring the target
// file to exist. It intentionally does not cache input authorization state.
func ResolvePermissionPath(path, cwd string) PathResolution {
	if path == "" {
		return PathResolution{}
	}
	if isUNCPath(path) {
		return PathResolution{Logical: path, Paths: []string{path}, Unsafe: true}
	}
	logical := path
	if !filepath.IsAbs(logical) {
		logical = filepath.Join(cwd, logical)
	}
	logical = filepath.Clean(logical)
	result := PathResolution{Logical: logical}
	addPermissionPath(&result, logical)

	current := logical
	visited := make(map[string]struct{})
	for range maxPermissionSymlinkDepth {
		if _, ok := visited[current]; ok {
			result.Unsafe = true
			break
		}
		visited[current] = struct{}{}
		info, err := os.Lstat(current)
		if err != nil {
			break
		}
		if isSpecialPermissionFile(info.Mode()) {
			result.Unsafe = true
			return result
		}
		if info.Mode()&os.ModeSymlink == 0 {
			break
		}
		target, err := os.Readlink(current)
		if err != nil {
			result.Unsafe = true
			break
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(current), target)
		}
		current = filepath.Clean(target)
		addPermissionPath(&result, current)
	}

	if resolved, err := filepath.EvalSymlinks(logical); err == nil {
		addPermissionPath(&result, resolved)
		return result
	}
	if resolved := resolveDeepestPermissionAncestor(logical); resolved != "" {
		addPermissionPath(&result, resolved)
	}
	return result
}

// PermissionPathsWithinRoots requires every input representation to be inside
// at least one representation of an allowed root.
func PermissionPathsWithinRoots(resolution PathResolution, roots []string) bool {
	if resolution.Unsafe || len(resolution.Paths) == 0 || len(roots) == 0 {
		return false
	}
	var rootPaths []string
	for _, root := range roots {
		resolvedRoot := ResolvePermissionPath(root, "")
		if resolvedRoot.Unsafe {
			continue
		}
		rootPaths = append(rootPaths, resolvedRoot.Paths...)
	}
	if len(rootPaths) == 0 {
		return false
	}
	for _, path := range resolution.Paths {
		inside := false
		for _, root := range rootPaths {
			if permissionPathWithin(path, root) {
				inside = true
				break
			}
		}
		if !inside {
			return false
		}
	}
	return true
}

func resolveDeepestPermissionAncestor(path string) string {
	current := path
	var tail []string
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if isSpecialPermissionFile(info.Mode()) {
				return ""
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, readErr := os.Readlink(current)
				if readErr != nil {
					return ""
				}
				if !filepath.IsAbs(target) {
					target = filepath.Join(filepath.Dir(current), target)
				}
				return filepath.Join(append([]string{filepath.Clean(target)}, tail...)...)
			}
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return ""
			}
			return filepath.Join(append([]string{resolved}, tail...)...)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		tail = append([]string{filepath.Base(current)}, tail...)
		current = parent
	}
}

func addPermissionPath(result *PathResolution, path string) {
	cleaned := filepath.Clean(path)
	for _, existing := range result.Paths {
		if existing == cleaned {
			return
		}
	}
	result.Paths = append(result.Paths, cleaned)
}

func permissionPathWithin(path, root string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		path = strings.ToLower(path)
		root = strings.ToLower(root)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func isUNCPath(path string) bool {
	return strings.HasPrefix(path, "//") || strings.HasPrefix(path, "\\\\")
}

func isSpecialPermissionFile(mode os.FileMode) bool {
	return mode&(os.ModeNamedPipe|os.ModeSocket|os.ModeDevice|os.ModeCharDevice) != 0
}
