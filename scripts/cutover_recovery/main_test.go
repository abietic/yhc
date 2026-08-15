package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIRejectsInvalidCommandsAndArguments(t *testing.T) {
	abs := mustResolve(t, t.TempDir())
	cases := []struct {
		name string
		args []string
	}{
		{"missing command", nil},
		{"unknown command", []string{"move"}},
		{"capture missing flags", []string{"capture"}},
		{"capture positional argument", []string{"capture", "extra"}},
		{"capture repeated flag", []string{"capture", "--private-root", abs, "--private-root", abs}},
		{"capture relative private root", []string{"capture", "--private-root", "private", "--public-root", abs, "--archive-root", filepath.Join(abs, "archive"), "--input", filepath.Join(abs, "input.json"), "--output", filepath.Join(abs, "manifest.json")}},
		{"capture archive equals private", []string{"capture", "--private-root", abs, "--public-root", filepath.Join(abs, "public"), "--archive-root", abs, "--input", filepath.Join(abs, "input.json"), "--output", filepath.Join(abs, "manifest.json")}},
		{"verify missing flags", []string{"verify"}},
		{"verify relative manifest", []string{"verify", "--manifest", "manifest.json", "--phase", "pre-move"}},
		{"verify unknown phase", []string{"verify", "--manifest", filepath.Join(abs, "manifest.json"), "--phase", "capture"}},
		{"verify positional argument", []string{"verify", "--manifest", filepath.Join(abs, "manifest.json"), "--phase", "post-move", "extra"}},
	}
	deps := dependencies{Git: fakeGitReader{}, Processes: unavailableProcessReader{}, Now: time.Now}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr, deps); code != 2 {
				t.Fatalf("run(%q) = %d, stdout=%q stderr=%q", test.args, code, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestValidatePublicSeparationRejectsMappedRoots(t *testing.T) {
	for name, mappings := range map[string][]archiveMappingRecord{
		"source inside public":      {{Source: "/public/private", Destination: "/archive"}},
		"destination inside public": {{Source: "/private", Destination: "/public/archive"}},
		"public inside destination": {{Source: "/private", Destination: "/"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validatePublicSeparation("/public", mappings); err == nil {
				t.Fatal("public overlap was accepted")
			}
		})
	}
	if err := validatePublicSeparation("/public", []archiveMappingRecord{{Source: "/private", Destination: "/archive"}}); err != nil {
		t.Fatalf("disjoint public root rejected: %v", err)
	}
}

func TestValidateExpectedRepositoriesRequiresCanonicalDistinctIdentities(t *testing.T) {
	for name, input := range map[string]cutoverInput{
		"same repository": {ExpectedPublicRepository: "abietic/yhc", ExpectedPrivateRepository: "abietic/yhc"},
		"uppercase":       {ExpectedPublicRepository: "Abietic/YHC", ExpectedPrivateRepository: "abietic/yhc-private-history"},
		"url":             {ExpectedPublicRepository: "https://github.com/abietic/yhc", ExpectedPrivateRepository: "abietic/yhc-private-history"},
		"empty":           {ExpectedPrivateRepository: "abietic/yhc-private-history"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateExpectedRepositories(input); err == nil {
				t.Fatal("invalid repository identities were accepted")
			}
		})
	}
	if err := validateExpectedRepositories(cutoverInput{ExpectedPublicRepository: "abietic/yhc", ExpectedPrivateRepository: "abietic/yhc-private-history"}); err != nil {
		t.Fatalf("canonical distinct identities rejected: %v", err)
	}
}

func TestCLICaptureRejectsInvalidIdentitiesBeforeRepositoryReads(t *testing.T) {
	base := mustResolve(t, t.TempDir())
	private := filepath.Join(base, "private")
	public := filepath.Join(base, "public")
	for _, root := range []string{private, public} {
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	input := cutoverInput{
		SchemaVersion:             schemaVersion,
		ExpectedPublicRepository:  "abietic/yhc",
		ExpectedPrivateRepository: "abietic/yhc",
	}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(base, "cutover-input.json")
	if err := os.WriteFile(inputPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	collector := func(context.Context, processReader, []string) ([]processRecord, error) {
		t.Fatal("process collection ran before repository identity validation")
		return nil, nil
	}
	deps := dependencies{Git: fakeGitReader{}, Processes: zeroProcessReader{}, Now: time.Now}
	args := []string{
		"capture",
		"--private-root", private,
		"--public-root", public,
		"--archive-root", filepath.Join(base, "archive"),
		"--input", inputPath,
		"--output", filepath.Join(base, "manifest.json"),
	}
	var stdout, stderr bytes.Buffer
	if code := runWithProcessCollector(args, &stdout, &stderr, deps, collector); code != 1 {
		t.Fatalf("capture code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "validate repository identities") || strings.Contains(stderr.String(), "unexpected git command") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestCLICaptureSealsAndStrictlyRereadsOutput(t *testing.T) {
	base := mustResolve(t, t.TempDir())
	private := filepath.Join(base, "private")
	linked := filepath.Join(base, "linked")
	public := filepath.Join(base, "public")
	archive := filepath.Join(base, "archive")
	for _, root := range []string{private, linked, public} {
		if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	reader := inventoryReader(private, linked)
	reader.responses[gitKey(public, "rev-parse", "--show-toplevel")] = []byte(public + "\n")
	reader.responses[gitKey(public, "rev-parse", "--verify", "HEAD")] = []byte(strings.Repeat("d", 40) + "\n")
	reader.responses[gitKey(public, "rev-parse", "--git-common-dir")] = []byte(".git\n")
	reader.responses[gitKey(public, "remote", "get-url", "--all", "origin")] = []byte("git@github.com:abietic/yhc.git\n")
	reader.responses[gitKey(public, "symbolic-ref", "--quiet", "--short", "HEAD")] = []byte("master\n")

	input := testCutoverInput(private, linked, archive)
	input.Defaults[0].Owner = "SENSITIVE_MANIFEST_MARKER"
	inputPath := filepath.Join(base, "cutover-input.json")
	inputBytes, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inputPath, inputBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	outputDir := mustResolve(t, t.TempDir())
	output := filepath.Join(outputDir, "manifest.json")
	deps := dependencies{Git: reader, Processes: zeroProcessReader{}, Now: func() time.Time {
		return time.Date(2026, 8, 16, 2, 3, 4, 0, time.UTC)
	}}
	args := []string{"capture", "--private-root", private, "--public-root", public, "--archive-root", archive, "--input", inputPath, "--output", output}
	collector := func(context.Context, processReader, []string) ([]processRecord, error) { return nil, nil }
	insideArgs := append([]string(nil), args...)
	insideArgs[len(insideArgs)-1] = filepath.Join(public, "manifest.json")
	var rejectedOutput, rejectedError bytes.Buffer
	if code := runWithProcessCollector(insideArgs, &rejectedOutput, &rejectedError, deps, collector); code != 1 || rejectedOutput.Len() != 0 || !strings.Contains(rejectedError.String(), "protected root") {
		t.Fatalf("protected output code=%d stdout=%q stderr=%q", code, rejectedOutput.String(), rejectedError.String())
	}
	var stdout, stderr bytes.Buffer
	if code := runWithProcessCollector(args, &stdout, &stderr, deps, collector); code != 0 {
		t.Fatalf("capture code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 || !strings.Contains(stdout.String(), "status=ok") || !strings.Contains(stdout.String(), "worktrees=2") || strings.Contains(stdout.String(), "SENSITIVE_MANIFEST_MARKER") {
		t.Fatalf("capture stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	m, err := readManifest(output)
	if err != nil {
		t.Fatalf("strict manifest reread: %v", err)
	}
	if m.CapturedAt != "2026-08-16T02:03:04Z" || m.Public.OriginRepository != "abietic/yhc" || m.Private.OriginRepository != "abietic/yhc-private-history" {
		t.Fatalf("manifest identity=%+v %+v captured=%q", m.Public, m.Private, m.CapturedAt)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode=%#o", info.Mode().Perm())
	}
}

func TestReadCutoverInputIsStrict(t *testing.T) {
	for name, payload := range map[string]string{
		"unknown field":  `{"schema_version":1,"unknown":true}`,
		"trailing value": `{"schema_version":1} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input.json")
			if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readCutoverInput(path); err == nil {
				t.Fatal("non-strict input was accepted")
			}
		})
	}
}

func TestCLIVerifyPrintsOnlyStatusAndCounts(t *testing.T) {
	base := mustResolve(t, t.TempDir())
	private := filepath.Join(base, "private")
	public := filepath.Join(base, "public")
	archive := filepath.Join(base, "archive")
	for _, root := range []string{public, archive} {
		if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := testManifest(t)
	m.CapturedAt = time.Date(2026, 8, 16, 2, 3, 4, 0, time.UTC).Format(time.RFC3339)
	m.Public.Root, m.Public.CommonDir = public, filepath.Join(public, ".git")
	m.Private.Root, m.Private.CommonDir = private, filepath.Join(private, ".git")
	m.ArchiveMapping[0].Source, m.ArchiveMapping[0].Destination = private, archive
	m.Worktrees[0].Source, m.Worktrees[0].CommonDir = private, filepath.Join(private, ".git")
	m.Worktrees[0].PorcelainBase64 = base64.StdEncoding.EncodeToString([]byte("worktree " + private + "\x00HEAD " + strings.Repeat("2", 40) + "\x00branch refs/heads/master"))
	refreshTestRecordIDs(&m)
	sealed, err := sealManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(base, "manifest.json")
	payload, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	reader := postMoveReader(sealed, archive)
	var stdout, stderr bytes.Buffer
	deps := dependencies{Git: reader, Processes: unavailableProcessReader{}, Now: time.Now}
	if code := run([]string{"verify", "--manifest", manifestPath, "--phase", "post-move"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("verify code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 || !strings.Contains(stdout.String(), "status=ok") || !strings.Contains(stdout.String(), "worktrees=1") || strings.Contains(stdout.String(), sealed.Checksum) || strings.Contains(stdout.String(), sealed.Refs[0].ObjectID) {
		t.Fatalf("verify disclosed manifest: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func postMoveReader(m manifest, archive string) fakeGitReader {
	responses := map[string][]byte{}
	add := func(root, output string, args ...string) { responses[gitKey(root, args...)] = []byte(output) }
	add(m.Public.Root, m.Public.Root+"\n", "rev-parse", "--show-toplevel")
	add(m.Public.Root, m.Public.Head+"\n", "rev-parse", "--verify", "HEAD")
	add(m.Public.Root, ".git\n", "rev-parse", "--git-common-dir")
	add(m.Public.Root, "git@github.com:"+m.Public.OriginRepository+".git\n", "remote", "get-url", "--all", "origin")
	add(m.Public.Root, m.Public.Branch+"\n", "symbolic-ref", "--quiet", "--short", "HEAD")
	add(archive, archive+"\n", "rev-parse", "--show-toplevel")
	add(archive, m.Private.Head+"\n", "rev-parse", "--verify", "HEAD")
	add(archive, ".git\n", "rev-parse", "--git-common-dir")
	add(archive, "git@github.com:"+m.Private.OriginRepository+".git\n", "remote", "get-url", "--all", "origin")
	add(archive, m.Private.Branch+"\n", "symbolic-ref", "--quiet", "--short", "HEAD")
	add(archive, "worktree "+archive+"\x00HEAD "+m.Worktrees[0].Head+"\x00branch refs/heads/"+m.Worktrees[0].Branch+"\x00\x00", "worktree", "list", "--porcelain", "-z")
	add(archive, m.Worktrees[0].Head+"\n", "rev-parse", "--verify", "HEAD")
	add(archive, "refs/heads/main\x00"+m.Refs[0].ObjectID+"\x00\n", "for-each-ref", "--format=%(refname)%00%(objectname)%00")
	add(archive, "", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	add(archive, "", "stash", "list", "--format=%gd%x00%H%x00%ct%x00")
	return fakeGitReader{responses: responses, errors: map[string]error{}}
}
