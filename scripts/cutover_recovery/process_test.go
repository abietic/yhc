package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeProcessReader struct {
	results map[string]commandResult
	errors  map[string]error
	calls   [][]string
}

func (f *fakeProcessReader) Run(_ context.Context, argv ...string) (commandResult, error) {
	f.calls = append(f.calls, append([]string(nil), argv...))
	key := strings.Join(argv, "\x00")
	if err := f.errors[key]; err != nil {
		return commandResult{}, err
	}
	result, ok := f.results[key]
	if !ok {
		return commandResult{}, errors.New("unexpected process command: " + key)
	}
	return result, nil
}

func processKey(root string) string {
	return strings.Join([]string{"lsof", "-nP", "-Fpfn", "+D", root}, "\x00")
}

func TestProcessOccupancyCapturesCWDAndOpenFiles(t *testing.T) {
	root := processTestRoot(t, "repo")
	child := filepath.Join(root, "nested", "file")
	reader := &fakeProcessReader{results: map[string]commandResult{processKey(root): {Stdout: []byte("p101\nfcwd\nn" + root + "\nf5\nn" + child + "\np202\nf7\nn" + root + "\n")}}, errors: map[string]error{}}
	records, err := collectProcessOccupancy(context.Background(), reader, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %+v", records)
	}
	for i := 1; i < len(records); i++ {
		if records[i-1].RecordID > records[i].RecordID {
			t.Fatalf("records are not sorted by RecordID: %+v", records)
		}
	}
	cwd := processRecordFor(t, records, 101, "cwd", root)
	if cwd.RootRecordID != makeRecordID("process_root", root, root) {
		t.Fatalf("cwd record = %+v", cwd)
	}
	_ = processRecordFor(t, records, 101, "open_file", child)
	_ = processRecordFor(t, records, 202, "open_file", root)
	if reader.calls[0][0] != "lsof" || reader.calls[0][4] != root {
		t.Fatalf("lsof argv = %q", reader.calls[0])
	}
}

func TestProcessOccupancyUsesExactRootBoundariesAndDeduplicates(t *testing.T) {
	root := processTestRoot(t, "repo")
	reader := &fakeProcessReader{results: map[string]commandResult{processKey(root): {Stdout: []byte("p9\nfcwd\nn" + root + "\nf8\nn" + root + "\n")}}, errors: map[string]error{}}
	records, err := collectProcessOccupancy(context.Background(), reader, []string{root, root})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || len(reader.calls) != 1 {
		t.Fatalf("dedupe records=%+v calls=%d", records, len(reader.calls))
	}
	if _, err := parseLsofOccupancy([]byte("p1\nfcwd\nn"+root+"-old\n"), root); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("prefix sibling accepted: %v", err)
	}
}

func TestProcessOccupancyFailsClosed(t *testing.T) {
	root := processTestRoot(t, "repo")
	key := processKey(root)
	for name, reader := range map[string]*fakeProcessReader{
		"exit one empty":     {results: map[string]commandResult{key: {ExitCode: 1}}, errors: map[string]error{}},
		"exit zero empty":    {results: map[string]commandResult{key: {}}, errors: map[string]error{}},
		"command failure":    {results: map[string]commandResult{}, errors: map[string]error{key: errors.New("missing lsof")}},
		"permission stderr":  {results: map[string]commandResult{key: {ExitCode: 1, Stderr: []byte("permission denied")}}, errors: map[string]error{}},
		"partial record":     {results: map[string]commandResult{key: {Stdout: []byte("p1\nfcwd\n")}}, errors: map[string]error{}},
		"truncated output":   {results: map[string]commandResult{key: {Stdout: []byte("p1\nf3\nn" + root)}}, errors: map[string]error{}},
		"p only":             {results: map[string]commandResult{key: {Stdout: []byte("p1\n")}}, errors: map[string]error{}},
		"consecutive p":      {results: map[string]commandResult{key: {Stdout: []byte("p1\np2\nf3\nn" + root + "\n")}}, errors: map[string]error{}},
		"outside root":       {results: map[string]commandResult{key: {Stdout: []byte("p1\nf3\nn" + root + "-old\n")}}, errors: map[string]error{}},
		"non canonical path": {results: map[string]commandResult{key: {Stdout: []byte("p1\nf3\nn" + root + "/nested/../file\n")}}, errors: map[string]error{}},
	} {
		t.Run(name, func(t *testing.T) {
			records, err := collectProcessOccupancy(context.Background(), reader, []string{root})
			if name == "exit one empty" {
				if err != nil || len(records) != 0 {
					t.Fatalf("zero result = %+v, %v", records, err)
				}
				return
			}
			if err == nil {
				t.Fatal("fail-closed condition was accepted")
			}
		})
	}
}

func processRecordFor(t *testing.T, records []processRecord, pid int, kind, path string) processRecord {
	t.Helper()
	for _, record := range records {
		if record.PID == pid && record.OccupancyKind == kind && record.Path == path {
			return record
		}
	}
	t.Fatalf("process record pid=%d kind=%s path=%q missing from %+v", pid, kind, path, records)
	return processRecord{}
}

func processTestRoot(t *testing.T, leaf string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), leaf)
	if err := os.MkdirAll(filepath.Join(path, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
