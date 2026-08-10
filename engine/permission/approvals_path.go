package permission

import (
	"errors"
	"path/filepath"

	"github.com/abietic/yhc/internal/statepath"
)

// ApprovalStorePath resolves the canonical project-local persistent approval
// file. Runtime owners write only this path; legacy state is import-only.
func ApprovalStorePath(projectRoot string) (string, error) {
	roots, err := statepath.ProjectRoots(projectRoot)
	if err != nil {
		return "", errors.New("approval store path is invalid")
	}
	return filepath.Join(roots.Canonical, "approvals.json"), nil
}
