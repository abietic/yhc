package attachments

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLineRange(t *testing.T) {
	for _, spec := range []string{"#1", "#20-80", "#2-2", "#0002-0003"} {
		lines, err := ParseLineRange(spec)
		if err != nil || !lines.Valid() {
			t.Fatalf("ParseLineRange(%q) = %#v, %v", spec, lines, err)
		}
	}
	for _, spec := range []string{"", "1-2", "#", "#0", "#-1", "#3-2", "#1-", "#1-2-3", "# 1", "#1-x", "#１", "#999999999999999999999999"} {
		if lines, err := ParseLineRange(spec); err == nil {
			t.Errorf("accepted invalid selector %q: %#v", spec, lines)
		}
	}
}

func TestReadFileRangePreservesExactText(t *testing.T) {
	for _, content := range []string{"outside\n第二行\r\nthird\nlast", "outside\n第二行\r\nthird\n"} {
		path := filepath.Join(t.TempDir(), "sample.txt")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ReadFileRange(path, LineRange{Start: 2, End: 3})
		if err != nil || got.Content != "第二行\r\nthird\n" || got.SourceBytes != int64(len(content)) {
			t.Fatalf("excerpt=%#v err=%v", got, err)
		}
	}
}

func TestReadFileRangeRejectsInvalidSourcesAndBounds(t *testing.T) {
	for _, test := range []struct {
		name, content string
		lines         LineRange
	}{
		{"empty", "", LineRange{1, 1}},
		{"past eof", "one\ntwo\n", LineRange{1, 3}},
		{"zero", "one\n", LineRange{0, 1}},
		{"reversed", "one\ntwo\n", LineRange{2, 1}},
		{"nul", "one\n\x00\n", LineRange{1, 1}},
		{"invalid utf8", "one\n\xff\n", LineRange{1, 1}},
		{"too large", strings.Repeat("x", MaxAttachmentBytes+1), LineRange{1, 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sample.txt")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadFileRange(path, test.lines); err == nil {
				t.Fatal("invalid range source was accepted")
			}
		})
	}
	if _, err := ReadFileRange(t.TempDir(), LineRange{1, 1}); err == nil {
		t.Fatal("directory accepted as text range")
	}
}
