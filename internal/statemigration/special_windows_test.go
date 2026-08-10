//go:build windows

package statemigration

import "testing"

func createSpecialMigrationFile(t *testing.T, _ string) bool {
	t.Helper()
	return false
}
