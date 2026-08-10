package permission

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateFilePathNormalizesRelativeTraversalWithinWorkspace(t *testing.T) {
	cwd := filepath.Clean("/tmp/project")
	result := ValidateFilePath("src/../README.md", "write", cwd)
	if !result.Allowed || result.RequiresAsk {
		t.Fatalf("expected normalized in-workspace write to be allowed, got %#v", result)
	}
	if got := NormalizePath("src/../README.md", cwd); got != filepath.Join(cwd, "README.md") {
		t.Fatalf("unexpected normalized path: %q", got)
	}
}

func TestValidateFilePathDeniesRelativeTraversalOutsideWorkspace(t *testing.T) {
	cwd := filepath.Clean("/tmp/project")

	write := ValidateFilePath("../outside.txt", "write", cwd)
	if write.Allowed || !strings.Contains(write.Reason, "outside the project directory") {
		t.Fatalf("expected outside write to be denied, got %#v", write)
	}

	read := ValidateFilePath("../outside.txt", "read", cwd)
	if !read.Allowed || !read.RequiresAsk || !strings.Contains(read.Reason, "requires confirmation") {
		t.Fatalf("expected outside read to require ask, got %#v", read)
	}
}

func TestValidateFilePathDenySystemPathBeforeOutsideProjectAsk(t *testing.T) {
	result := ValidateFilePath("/etc/passwd", "read", "/tmp/project")
	if result.Allowed || !strings.Contains(result.Reason, "system directory") {
		t.Fatalf("expected system path to be denied before outside-project ask, got %#v", result)
	}
}

func TestValidateFilePathSensitivePatternsByOperation(t *testing.T) {
	cwd := filepath.Clean("/tmp/project")

	env := ValidateFilePath(".env.local", "read", cwd)
	if env.Allowed || !env.RequiresAsk || !strings.Contains(env.Reason, "Environment file variant") {
		t.Fatalf("expected .env.local read to be sensitive, got %#v", env)
	}

	gitHook := ValidateFilePath(".git/hooks/pre-commit", "write", cwd)
	if gitHook.Allowed || !gitHook.RequiresAsk || !strings.Contains(gitHook.Reason, "Git hooks") {
		t.Fatalf("expected git hook write to be sensitive, got %#v", gitHook)
	}

	gitHookRead := ValidateFilePath(".git/hooks/pre-commit", "read", cwd)
	if !gitHookRead.Allowed {
		t.Fatalf("expected git hook read to be allowed because pattern blocks write only, got %#v", gitHookRead)
	}
}

func TestValidateFilePathSensitivePatternsAreCaseInsensitive(t *testing.T) {
	cwd := filepath.Clean("/tmp/project")

	env := ValidateFilePath(".ENV.Production", "read", cwd)
	if env.Allowed || !env.RequiresAsk || !strings.Contains(env.Reason, "Environment file variant") {
		t.Fatalf("expected mixed-case .env read to be sensitive, got %#v", env)
	}

	settings := ValidateFilePath(".cLauDe/Settings.locaL.json", "write", cwd)
	if settings.Allowed || !settings.RequiresAsk || !strings.Contains(settings.Reason, "Claude") {
		t.Fatalf("expected mixed-case Claude settings write to be sensitive, got %#v", settings)
	}

	gitHook := ValidateFilePath(".GIT/Hooks/pre-commit", "delete", cwd)
	if gitHook.Allowed || !gitHook.RequiresAsk || !strings.Contains(gitHook.Reason, "Git hooks") {
		t.Fatalf("expected mixed-case git hook delete to be sensitive, got %#v", gitHook)
	}
}

func TestIsOutsideProjectHandlesSiblingPrefixSafely(t *testing.T) {
	cwd := filepath.Clean("/tmp/project")
	if IsOutsideProject("/tmp/project-other/file.go", cwd) {
		// This should be outside. Keep separate branch for clearer failure below.
	} else {
		t.Fatal("sibling path with same prefix should be outside project")
	}
	if IsOutsideProject(filepath.Join(cwd, "sub/file.go"), cwd) {
		t.Fatal("child path should be inside project")
	}
	if IsOutsideProject(cwd, cwd) {
		t.Fatal("cwd itself should be inside project")
	}
}
