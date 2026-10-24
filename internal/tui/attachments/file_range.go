package attachments

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// LineRange uses positive, one-based inclusive file line numbers.
type LineRange struct {
	Start int
	End   int
}

func (r LineRange) Valid() bool { return r.Start > 0 && r.End >= r.Start }

func (r LineRange) String() string {
	if r.Start == r.End {
		return fmt.Sprintf("#%d", r.Start)
	}
	return fmt.Sprintf("#%d-%d", r.Start, r.End)
}

// ParseLineRange accepts #N and #N-M; partial or overflowing selectors fail.
func ParseLineRange(selector string) (LineRange, error) {
	invalid := fmt.Errorf("invalid line range: use #N or #N-M with positive inclusive lines")
	if !strings.HasPrefix(selector, "#") {
		return LineRange{}, invalid
	}
	first, last, ranged := strings.Cut(selector[1:], "-")
	if !ranged {
		last = first
	}
	for _, value := range []string{first, last} {
		if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return LineRange{}, invalid
		}
	}
	start, startErr := strconv.Atoi(first)
	end, endErr := strconv.Atoi(last)
	lines := LineRange{Start: start, End: end}
	if startErr != nil || endErr != nil || !lines.Valid() {
		return LineRange{}, invalid
	}
	return lines, nil
}

type FileExcerpt struct {
	Content     string
	TotalLines  int
	SourceBytes int64
}

// ReadFileRange captures an exact text excerpt. It retains the existing 5 MiB
// source-file bound and never silently clamps, truncates or reads whole-file
// content as a fallback when a requested range is invalid.
func ReadFileRange(path string, lines LineRange) (FileExcerpt, error) {
	if !lines.Valid() {
		return FileExcerpt{}, fmt.Errorf("invalid file line range")
	}
	info, err := os.Stat(path)
	if err != nil {
		return FileExcerpt{}, fmt.Errorf("inspect file range: %w", err)
	}
	if !info.Mode().IsRegular() {
		return FileExcerpt{}, fmt.Errorf("file range requires a regular text file")
	}
	if info.Size() > MaxAttachmentBytes {
		return FileExcerpt{}, fmt.Errorf("file range source exceeds the 5 MiB attachment limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return FileExcerpt{}, fmt.Errorf("open file range: %w", err)
	}
	defer file.Close()
	if current, statErr := file.Stat(); statErr != nil || !current.Mode().IsRegular() {
		return FileExcerpt{}, fmt.Errorf("file range source is no longer a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxAttachmentBytes+1))
	if err != nil {
		return FileExcerpt{}, fmt.Errorf("read file range: %w", err)
	}
	if len(data) > MaxAttachmentBytes {
		return FileExcerpt{}, fmt.Errorf("file range source exceeds the 5 MiB attachment limit")
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return FileExcerpt{}, fmt.Errorf("file range source is not UTF-8 text")
	}
	total := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		total++
	}
	if lines.End > total {
		return FileExcerpt{}, fmt.Errorf("line range %d-%d exceeds the file's %d lines", lines.Start, lines.End, total)
	}
	start, end, current := 0, len(data), 1
	for index, value := range data {
		if value != '\n' {
			continue
		}
		if current < lines.Start {
			start = index + 1
		}
		if current == lines.End {
			end = index + 1
			break
		}
		current++
	}
	return FileExcerpt{Content: string(data[start:end]), TotalLines: total, SourceBytes: int64(len(data))}, nil
}
