package attachments

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsLargePasteCountsCharactersNotUTF8Bytes(t *testing.T) {
	if IsLargePaste(strings.Repeat("界", PasteThreshold)) {
		t.Fatal("paste at the character threshold was classified as large")
	}
	if !IsLargePaste(strings.Repeat("界", PasteThreshold+1)) {
		t.Fatal("paste over the character threshold was not classified as large")
	}
}

func TestNormalizePastedImagePath(t *testing.T) {
	path := filepath.Join("tmp", "my image.png")
	if got, ok := NormalizePastedImagePath(`"` + path + `"`); !ok || got != path {
		t.Fatalf("quoted path = %q, %v", got, ok)
	}
	if _, ok := NormalizePastedImagePath("notes.txt"); ok {
		t.Fatal("text file was classified as an image path")
	}
}

func TestClipboardImageBackendLinuxUsesBoundedTypedCommands(t *testing.T) {
	var calls []string
	backend := clipboardImageBackend{
		goos: "linux",
		run: func(_ context.Context, name string, args []string, limit int) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			switch len(calls) {
			case 1:
				if limit != clipboardProbeOutputLimit {
					t.Fatalf("probe limit = %d", limit)
				}
				return []byte("TARGETS\nimage/png\n"), nil
			case 2:
				if limit != clipboardImageOutputLimit {
					t.Fatalf("image limit = %d", limit)
				}
				return []byte("png-bytes"), nil
			default:
				t.Fatalf("unexpected command call %d", len(calls))
				return nil, nil
			}
		},
		timeout: time.Second,
	}
	result := backend.read(context.Background())
	if result.Error != nil || !result.HasImage || result.Format != "png" ||
		string(result.Data) != "png-bytes" {
		t.Fatalf("result = %#v", result)
	}
	if len(calls) != 2 ||
		!strings.Contains(calls[0], "-t TARGETS") ||
		!strings.Contains(calls[1], "-t image/png") {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestClipboardImageBackendsUseUniquePrivateTemporaryFiles(t *testing.T) {
	for _, goos := range []string{"darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			dir := t.TempDir()
			var created []string
			var commandCalls int
			backend := clipboardImageBackend{
				goos: goos,
				run: func(_ context.Context, name string, args []string, limit int) ([]byte, error) {
					commandCalls++
					if limit != clipboardProbeOutputLimit {
						t.Fatalf("command limit = %d", limit)
					}
					if commandCalls%2 == 1 {
						return []byte("has_image"), nil
					}
					if goos == "darwin" && name != "osascript" {
						t.Fatalf("darwin command = %q", name)
					}
					if goos == "windows" && name != "powershell" {
						t.Fatalf("windows command = %q", name)
					}
					path := created[len(created)-1]
					info, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					if info.Mode().Perm() != 0o600 {
						t.Fatalf("temporary mode = %o", info.Mode().Perm())
					}
					return nil, os.WriteFile(path, []byte("fixture-png"), 0o600)
				},
				createTemp: func(_, pattern string) (*os.File, error) {
					file, err := os.CreateTemp(dir, pattern)
					if err == nil {
						created = append(created, file.Name())
					}
					return file, err
				},
				readFile: readBoundedClipboardFile,
				remove:   os.Remove,
				timeout:  time.Second,
			}
			for range 2 {
				result := backend.read(context.Background())
				if result.Error != nil || string(result.Data) != "fixture-png" {
					t.Fatalf("result = %#v", result)
				}
			}
			if len(created) != 2 || created[0] == created[1] {
				t.Fatalf("temporary paths = %#v", created)
			}
			for _, path := range created {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("temporary path was not removed: %s err=%v", path, err)
				}
			}
		})
	}
}

func TestClipboardImageBackendHonorsDeadlineAndSizeLimit(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		backend := clipboardImageBackend{
			goos: "linux",
			run: func(ctx context.Context, _ string, _ []string, _ int) ([]byte, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
			timeout: time.Millisecond,
		}
		result := backend.read(context.Background())
		if result.HasImage || result.Error != nil {
			t.Fatalf("deadline probe result = %#v", result)
		}
	})

	t.Run("size", func(t *testing.T) {
		oversized := make([]byte, MaxAttachmentBytes+1)
		backend := clipboardImageBackend{
			goos: "linux",
			run: func(_ context.Context, _ string, args []string, _ int) ([]byte, error) {
				if strings.Contains(strings.Join(args, " "), "TARGETS") {
					return []byte("image/png"), nil
				}
				return oversized, nil
			},
			timeout: time.Second,
		}
		result := backend.read(context.Background())
		if !result.HasImage || result.Error == nil ||
			!strings.Contains(result.Error.Error(), "too large") {
			t.Fatalf("oversized result = %#v", result)
		}
		for index, value := range oversized {
			if value != 0 {
				t.Fatalf("oversized byte %d was not cleared", index)
			}
		}
	})
}

func TestBoundedClipboardBufferRejectsExcessOutput(t *testing.T) {
	buffer := boundedClipboardBuffer{limit: 4}
	if _, err := buffer.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Write([]byte("5")); !errors.Is(err, errClipboardOutputTooLarge) ||
		!buffer.tooLarge {
		t.Fatalf("overflow err=%v tooLarge=%v", err, buffer.tooLarge)
	}
}
