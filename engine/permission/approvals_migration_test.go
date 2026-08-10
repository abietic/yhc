package permission

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
)

func TestApprovalMigrationPreservesScopeWithoutCredentialCopy(t *testing.T) {
	project := t.TempDir()
	roots, err := statepath.ProjectRoots(project)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := ApprovalMigrationSpec(project)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(roots.Legacy, filepath.FromSlash(spec.SourceRel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	entries := []persistedApproval{
		{ToolName: "Bash", CommandPattern: "GOOS=linux go test ./internal/tui/key_actions", ExactCommand: true, ApprovedAt: "2026-08-10T01:02:03Z", Reason: "user"},
		{ToolName: "Read", PathPattern: filepath.Join(project, "internal"), RecursivePath: true, ApprovedAt: "2026-08-10T01:02:04Z", Reason: "user"},
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (statemigration.Importer{}).Import(context.Background(), roots, spec)
	if err != nil || result.Status != statemigration.StatusImported {
		t.Fatalf("import result=%#v err=%v", result, err)
	}
	canonical, err := os.ReadFile(filepath.Join(roots.Canonical, filepath.FromSlash(spec.TargetRel)))
	if err != nil {
		t.Fatal(err)
	}
	var got []persistedApproval
	if err := json.Unmarshal(canonical, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(entries) || !got[0].ExactCommand || !got[1].RecursivePath {
		t.Fatalf("migrated approvals = %#v", got)
	}
	info, err := os.Stat(filepath.Join(roots.Canonical, filepath.FromSlash(spec.TargetRel)))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("canonical mode = %v err=%v", infoMode(info), err)
	}

	t.Run("credential command", func(t *testing.T) {
		credentialRoots, err := statepath.ProjectRoots(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		credentialSpec, err := ApprovalMigrationSpec(filepath.Dir(credentialRoots.Legacy))
		if err != nil {
			t.Fatal(err)
		}
		credentialPath := filepath.Join(credentialRoots.Legacy, filepath.FromSlash(credentialSpec.SourceRel))
		if err := os.MkdirAll(filepath.Dir(credentialPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(credentialPath, []byte(`[{"tool_name":"Bash","command_pattern":"cat .env","exact_command":true,"approved_at":"2026-08-10T01:02:03Z","reason":"user"}]`), 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := (statemigration.Importer{}).Inspect(context.Background(), credentialRoots, credentialSpec)
		if err == nil || result.Status != statemigration.StatusUnsafe {
			t.Fatalf("credential inspect result=%#v err=%v", result, err)
		}
		if _, err := os.Lstat(filepath.Join(credentialRoots.Canonical, filepath.FromSlash(credentialSpec.TargetRel))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("credential approval reached canonical root: %v", err)
		}
	})

	t.Run("credential shell expansion", func(t *testing.T) {
		credentialRoots, err := statepath.ProjectRoots(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		credentialSpec, err := ApprovalMigrationSpec(filepath.Dir(credentialRoots.Legacy))
		if err != nil {
			t.Fatal(err)
		}
		credentialPath := filepath.Join(credentialRoots.Legacy, filepath.FromSlash(credentialSpec.SourceRel))
		if err := os.MkdirAll(filepath.Dir(credentialPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(credentialPath, []byte(`[{"tool_name":"Bash","command_pattern":"printf %s $API_TOKEN","exact_command":true,"approved_at":"2026-08-10T01:02:03Z","reason":"user"}]`), 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := (statemigration.Importer{}).Inspect(context.Background(), credentialRoots, credentialSpec)
		if err == nil || result.Status != statemigration.StatusUnsafe {
			t.Fatalf("credential expansion inspect result=%#v err=%v", result, err)
		}
	})
}

func TestApprovalMigrationRejectsUnsafePathAndDestinationCollision(t *testing.T) {
	project := t.TempDir()

	t.Run("unsafe path", func(t *testing.T) {
		roots, err := statepath.ProjectRoots(project)
		if err != nil {
			t.Fatal(err)
		}
		spec, err := ApprovalMigrationSpec(project)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(roots.Legacy, filepath.FromSlash(spec.SourceRel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		data := []byte(`[{"tool_name":"Read","path_pattern":"/tmp/outside","recursive_path":true,"approved_at":"2026-08-10T01:02:03Z","reason":"user"}]`)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := (statemigration.Importer{}).Inspect(context.Background(), roots, spec)
		if err == nil || result.Status != statemigration.StatusUnsafe {
			t.Fatalf("inspect result=%#v err=%v", result, err)
		}
	})

	t.Run("path flags explicitly disable scope", func(t *testing.T) {
		roots, err := statepath.ProjectRoots(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		spec, err := ApprovalMigrationSpec(filepath.Dir(roots.Legacy))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(roots.Legacy, filepath.FromSlash(spec.SourceRel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		data := []byte(`[{"tool_name":"Read","path_pattern":".","exact_path":false,"recursive_path":false,"approved_at":"2026-08-10T01:02:03Z","reason":"user"}]`)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := (statemigration.Importer{}).Inspect(context.Background(), roots, spec)
		if err == nil || result.Status != statemigration.StatusUnsafe {
			t.Fatalf("disabled path scope inspect result=%#v err=%v", result, err)
		}
	})

	t.Run("destination collision", func(t *testing.T) {
		roots, err := statepath.ProjectRoots(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		spec, err := ApprovalMigrationSpec(project)
		if err != nil {
			t.Fatal(err)
		}
		legacy := filepath.Join(roots.Legacy, filepath.FromSlash(spec.SourceRel))
		if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(legacy, []byte(`[]`), 0o644); err != nil {
			t.Fatal(err)
		}
		canonical := filepath.Join(roots.Canonical, filepath.FromSlash(spec.TargetRel))
		if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(canonical, []byte(`[]`), 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := (statemigration.Importer{}).Import(context.Background(), roots, spec)
		if err != nil || result.Status != statemigration.StatusDestinationExists {
			t.Fatalf("import result=%#v err=%v", result, err)
		}
		if _, err := os.Stat(legacy); err != nil {
			t.Fatalf("legacy source changed: %v", err)
		}
	})
}

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}
