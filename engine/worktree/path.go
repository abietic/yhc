package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func canonicalExistingPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func canonicalConfiguredRoot(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("worktree: project root is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func samePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func sameCanonicalPath(left, right string) bool {
	canonicalLeft, leftErr := canonicalExistingPath(left)
	canonicalRight, rightErr := canonicalExistingPath(right)
	if leftErr == nil && rightErr == nil {
		return samePath(canonicalLeft, canonicalRight)
	}
	return samePath(left, right)
}

func pathContained(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return relative == "." ||
		(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func secureMkdirAll(root, target string, mode os.FileMode) error {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if !pathContained(root, target) {
		return fmt.Errorf("worktree: managed directory %q escapes project root", target)
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	current := root
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		if segment == "" || segment == "." {
			continue
		}
		current = filepath.Join(current, segment)
		info, statErr := os.Lstat(current)
		switch {
		case statErr == nil && info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("worktree: managed directory %q is a symlink", current)
		case statErr == nil && !info.IsDir():
			return fmt.Errorf("worktree: managed directory %q is not a directory", current)
		case statErr == nil:
			continue
		case !errors.Is(statErr, os.ErrNotExist):
			return statErr
		}
		if mkdirErr := os.Mkdir(current, mode); mkdirErr != nil &&
			!errors.Is(mkdirErr, os.ErrExist) {
			return mkdirErr
		}
	}
	return nil
}
