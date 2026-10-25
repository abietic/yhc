package commands

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxDiffContextBytes bounds a captured Git patch before it becomes selectable
// as conversation context.
const MaxDiffContextBytes = 5 << 20

const (
	maxDiffContextFiles         = 512
	maxDiffContextHunks         = 4096
	maxDiffContextRetainedBytes = 2 * MaxDiffContextBytes
)

// DiffContextHunk is one selectable, self-contained unified-diff hunk.
type DiffContextHunk struct {
	Header             string
	OldStart, OldLines int
	NewStart, NewLines int
	Patch              string
	Body               string // raw hunk body, shared with the file patch
}

// DiffContextFile is one file entry in a Git patch. UnavailableReason prevents
// callers from attaching entries whose textual contents are unsafe or unknown.
type DiffContextFile struct {
	Path              string
	Patch             string
	Hunks             []DiffContextHunk
	UnavailableReason string
}

// ParseDiffContext parses ordinary Git unified diff output without consulting
// the worktree. It intentionally keeps raw byte ranges for selectable patches.
func ParseDiffContext(patch string) ([]DiffContextFile, error) {
	if patch == "" {
		return nil, nil
	}
	if len(patch) > MaxDiffContextBytes {
		return nil, fmt.Errorf("diff context exceeds %d bytes", MaxDiffContextBytes)
	}
	if strings.IndexByte(patch, 0) >= 0 || !utf8.ValidString(patch) {
		return nil, fmt.Errorf("diff context must be valid UTF-8 without NUL bytes")
	}

	data := []byte(patch)
	var files []DiffContextFile
	totalHunks := 0
	retainedBytes := 0
	for offset := 0; offset < len(data); {
		line, next := diffContextLine(data, offset, len(data))
		if !strings.HasPrefix(line.body, "diff --git ") {
			return nil, fmt.Errorf("diff context entry at byte %d has no git file header", offset)
		}
		start := offset
		offset = next
		for offset < len(data) {
			candidate, candidateNext := diffContextLine(data, offset, len(data))
			if strings.HasPrefix(candidate.body, "diff --git ") {
				break
			}
			offset = candidateNext
		}
		if len(files) == maxDiffContextFiles {
			return nil, fmt.Errorf("diff context exceeds %d files", maxDiffContextFiles)
		}
		file, err := parseDiffContextFile(data, start, offset, &retainedBytes)
		if err != nil {
			return nil, err
		}
		totalHunks += len(file.Hunks)
		if totalHunks > maxDiffContextHunks {
			return nil, fmt.Errorf("diff context exceeds %d hunks", maxDiffContextHunks)
		}
		files = append(files, file)
	}
	return files, nil
}

type diffContextRawLine struct {
	start int
	end   int
	body  string
}

func diffContextLine(data []byte, offset, limit int) (diffContextRawLine, int) {
	end := offset
	for end < limit && data[end] != '\n' {
		end++
	}
	rawEnd := end
	if end < limit {
		rawEnd++
	}
	bodyEnd := end
	if bodyEnd > offset && data[bodyEnd-1] == '\r' {
		bodyEnd--
	}
	return diffContextRawLine{start: offset, end: rawEnd, body: string(data[offset:bodyEnd])}, rawEnd
}

func parseDiffContextFile(data []byte, start, end int, retainedBytes *int) (DiffContextFile, error) {
	if end-start > maxDiffContextRetainedBytes-*retainedBytes {
		return DiffContextFile{}, fmt.Errorf("diff context retained patch data exceeds %d bytes", maxDiffContextRetainedBytes)
	}
	*retainedBytes += end - start
	file := DiffContextFile{Patch: string(data[start:end])}
	line, offset := diffContextLine(data, start, end)
	header := line.body
	path, headerValid := parseDiffContextGitHeader(header)
	var oldPath, newPath, renamedPath, renamedFrom string
	oldMarker, newMarker := false, false
	binary := false
	metadataEnd := -1
	hunks := 0
	for offset < end {
		line, next := diffContextLine(data, offset, end)
		switch {
		case strings.HasPrefix(line.body, "@@@"):
			return DiffContextFile{}, fmt.Errorf("combined diff hunk is unsupported")
		case strings.HasPrefix(line.body, "@@ "):
			if binary {
				return DiffContextFile{}, fmt.Errorf("binary diff contains a unified hunk")
			}
			if !oldMarker || !newMarker {
				return DiffContextFile{}, fmt.Errorf("unified hunk is missing ---/+++ file markers")
			}
			if metadataEnd < 0 {
				metadataEnd = line.start
			}
			hunk, after, err := parseDiffContextHunk(data, line, next, end)
			if err != nil {
				return DiffContextFile{}, err
			}
			hunks++
			if hunks > maxDiffContextHunks {
				return DiffContextFile{}, fmt.Errorf("diff context exceeds %d hunks", maxDiffContextHunks)
			}
			hunkBytes := metadataEnd - start + after - line.start
			if hunkBytes > maxDiffContextRetainedBytes-*retainedBytes {
				return DiffContextFile{}, fmt.Errorf("diff context retained patch data exceeds %d bytes", maxDiffContextRetainedBytes)
			}
			*retainedBytes += hunkBytes
			hunk.Patch = string(data[start:metadataEnd]) + string(data[line.start:after])
			hunk.Body = file.Patch[next-start : after-start]
			file.Hunks = append(file.Hunks, hunk)
			offset = after
			continue
		case strings.HasPrefix(line.body, "@@"):
			return DiffContextFile{}, fmt.Errorf("malformed hunk header %q", line.body)
		case strings.HasPrefix(line.body, "--- "):
			var ok bool
			oldPath, ok = parseDiffContextTransportPath(line.body[4:])
			if !ok {
				return DiffContextFile{}, fmt.Errorf("malformed --- file marker %q", line.body)
			}
			oldMarker = true
		case strings.HasPrefix(line.body, "+++ "):
			var ok bool
			newPath, ok = parseDiffContextTransportPath(line.body[4:])
			if !ok {
				return DiffContextFile{}, fmt.Errorf("malformed +++ file marker %q", line.body)
			}
			newMarker = true
		case strings.HasPrefix(line.body, "rename to "):
			var ok bool
			renamedPath, ok = parseDiffContextPath(line.body[len("rename to "):])
			if !ok {
				return DiffContextFile{}, fmt.Errorf("malformed rename target %q", line.body)
			}
		case strings.HasPrefix(line.body, "copy to "):
			var ok bool
			renamedPath, ok = parseDiffContextPath(line.body[len("copy to "):])
			if !ok {
				return DiffContextFile{}, fmt.Errorf("malformed copy target %q", line.body)
			}
		case strings.HasPrefix(line.body, "rename from "), strings.HasPrefix(line.body, "copy from "):
			prefix := "rename from "
			if strings.HasPrefix(line.body, "copy from ") {
				prefix = "copy from "
			}
			var ok bool
			renamedFrom, ok = parseDiffContextPath(strings.TrimPrefix(line.body, prefix))
			if !ok {
				return DiffContextFile{}, fmt.Errorf("malformed rename/copy source")
			}
		case strings.HasPrefix(line.body, "GIT binary patch") || strings.HasPrefix(line.body, "Binary files "):
			binary = true
		case strings.HasPrefix(line.body, "index "),
			strings.HasPrefix(line.body, "old mode "),
			strings.HasPrefix(line.body, "new mode "),
			strings.HasPrefix(line.body, "new file mode "),
			strings.HasPrefix(line.body, "deleted file mode "),
			strings.HasPrefix(line.body, "similarity index "),
			strings.HasPrefix(line.body, "dissimilarity index "):
			// Recognized Git metadata. The raw patch remains the source of truth.
		case binary:
			// Git binary payload lines are not unified-diff text and may be base85.
		default:
			return DiffContextFile{}, fmt.Errorf("unrecognized Git diff metadata %q", line.body)
		}
		offset = next
	}
	if !headerValid {
		// Git leaves spaces unquoted in some headers. For a rename/copy with
		// different names, accept that ambiguous form only when its complete
		// bytes agree with the explicit file/rename markers.
		old, new := oldPath, newPath
		if renamedFrom != "" {
			old = renamedFrom
		}
		if renamedPath != "" {
			new = renamedPath
		}
		if old == "/dev/null" {
			old = new
		}
		if new == "/dev/null" {
			new = old
		}
		if old == "" || new == "" || header != "diff --git a/"+old+" b/"+new {
			return DiffContextFile{}, fmt.Errorf("malformed Git diff file header")
		}
	}

	// The destination marker is most reliable for normal and added files. A
	// rename target is next; then fall back to the deleted/source path or the
	// quoted diff header needed by mode-only patches.
	if newPath != "" && newPath != "/dev/null" {
		path = newPath
	} else if renamedPath != "" && renamedPath != "/dev/null" {
		path = renamedPath
	} else if oldPath != "" && oldPath != "/dev/null" {
		path = oldPath
	}
	file.Path = path
	if binary {
		file.UnavailableReason = "binary diff cannot be attached as text"
	}
	if file.Path == "" {
		if file.UnavailableReason == "" {
			file.UnavailableReason = "diff path is unavailable"
		} else {
			file.UnavailableReason += "; diff path is unavailable"
		}
	}
	return file, nil
}

func parseDiffContextHunk(data []byte, header diffContextRawLine, offset, end int) (DiffContextHunk, int, error) {
	oldStart, oldLines, newStart, newLines, err := parseDiffContextHunkHeader(header.body)
	if err != nil {
		return DiffContextHunk{}, 0, err
	}
	oldSeen, newSeen := 0, 0
	lastWasContent := false
	for offset < end {
		line, next := diffContextLine(data, offset, end)
		if strings.HasPrefix(line.body, "@@@") {
			return DiffContextHunk{}, 0, fmt.Errorf("combined diff hunk is unsupported")
		}
		if strings.HasPrefix(line.body, "@@ ") {
			break
		}
		if line.body == `\ No newline at end of file` {
			if !lastWasContent {
				return DiffContextHunk{}, 0, fmt.Errorf("orphan no-newline marker")
			}
			lastWasContent = false
			offset = next
			continue
		}
		if len(line.body) == 0 {
			return DiffContextHunk{}, 0, fmt.Errorf("malformed empty hunk line")
		}
		switch line.body[0] {
		case ' ':
			oldSeen++
			newSeen++
		case '-':
			oldSeen++
		case '+':
			newSeen++
		default:
			return DiffContextHunk{}, 0, fmt.Errorf("malformed hunk line %q", line.body)
		}
		lastWasContent = true
		offset = next
	}
	if oldSeen != oldLines || newSeen != newLines {
		return DiffContextHunk{}, 0, fmt.Errorf("hunk line counts mismatch: got -%d +%d, want -%d +%d", oldSeen, newSeen, oldLines, newLines)
	}
	return DiffContextHunk{Header: header.body, OldStart: oldStart, OldLines: oldLines, NewStart: newStart, NewLines: newLines}, offset, nil
}

func parseDiffContextHunkHeader(header string) (int, int, int, int, error) {
	if !strings.HasPrefix(header, "@@ ") {
		return 0, 0, 0, 0, fmt.Errorf("malformed hunk header %q", header)
	}
	close := strings.Index(header[3:], " @@")
	if close < 0 {
		return 0, 0, 0, 0, fmt.Errorf("malformed hunk header %q", header)
	}
	tail := header[3+close+3:]
	if tail != "" && tail[0] != ' ' {
		return 0, 0, 0, 0, fmt.Errorf("malformed hunk header %q", header)
	}
	parts := strings.Fields(header[3 : 3+close])
	if len(parts) != 2 {
		return 0, 0, 0, 0, fmt.Errorf("malformed hunk header %q", header)
	}
	oldStart, oldLines, err := parseDiffContextRange(parts[0], '-')
	if err != nil {
		return 0, 0, 0, 0, err
	}
	newStart, newLines, err := parseDiffContextRange(parts[1], '+')
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return oldStart, oldLines, newStart, newLines, nil
}

func parseDiffContextRange(value string, sign byte) (int, int, error) {
	if len(value) < 2 || value[0] != sign {
		return 0, 0, fmt.Errorf("malformed hunk range %q", value)
	}
	fields := strings.Split(value[1:], ",")
	if len(fields) > 2 || fields[0] == "" || (len(fields) == 2 && fields[1] == "") {
		return 0, 0, fmt.Errorf("malformed hunk range %q", value)
	}
	if !diffContextDigits(fields[0]) || (len(fields) == 2 && !diffContextDigits(fields[1])) {
		return 0, 0, fmt.Errorf("malformed hunk range %q", value)
	}
	start, err := strconv.Atoi(fields[0])
	if err != nil || start < 0 {
		return 0, 0, fmt.Errorf("malformed hunk range %q", value)
	}
	lines := 1
	if len(fields) == 2 {
		lines, err = strconv.Atoi(fields[1])
		if err != nil || lines < 0 {
			return 0, 0, fmt.Errorf("malformed hunk range %q", value)
		}
	}
	return start, lines, nil
}

func diffContextDigits(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return value != ""
}

func parseDiffContextGitHeader(header string) (string, bool) {
	if !strings.HasPrefix(header, "diff --git ") {
		return "", false
	}
	rawPaths := header[len("diff --git "):]
	left, rest, ok := parseDiffContextToken(rawPaths)
	if ok {
		rest = strings.TrimLeft(rest, " \t")
		path, trailing, rightOK := parseDiffContextToken(rest)
		if rightOK && strings.TrimSpace(trailing) == "" && strings.HasPrefix(left, "a/") && len(left) > 2 && strings.HasPrefix(path, "b/") && len(path) > 2 {
			return normalizeDiffContextTransportPath(path), true
		}
	}
	// Unquoted Git headers with spaces are otherwise ambiguous. Accept only the
	// ordinary mode-only form where both transport paths are demonstrably equal.
	for index := strings.Index(rawPaths, " b/"); index >= 0; {
		left, right := rawPaths[:index], rawPaths[index+1:]
		if strings.HasPrefix(left, "a/") && strings.HasPrefix(right, "b/") && left[2:] == right[2:] {
			return right[2:], true
		}
		next := strings.Index(rawPaths[index+3:], " b/")
		if next < 0 {
			break
		}
		index += 3 + next
	}
	return "", false
}

func parseDiffContextPath(value string) (string, bool) {
	if value == "" {
		return "", false
	}
	if value[0] == '"' {
		path, rest, ok := parseDiffContextToken(value)
		if !ok || (rest != "" && rest[0] != '\t') {
			return "", false
		}
		return path, path != ""
	}
	if tab := strings.IndexByte(value, '\t'); tab >= 0 {
		value = value[:tab]
	}
	return value, value != ""
}

func parseDiffContextTransportPath(value string) (string, bool) {
	path, ok := parseDiffContextPath(value)
	if !ok {
		return "", false
	}
	return normalizeDiffContextTransportPath(path), true
}

// parseDiffContextToken reads a Git path token without treating spaces inside
// C-quoted paths as separators.
func parseDiffContextToken(value string) (string, string, bool) {
	if value == "" {
		return "", "", false
	}
	if value[0] != '"' {
		end := strings.IndexAny(value, " \t")
		if end < 0 {
			return value, "", true
		}
		return value[:end], value[end:], true
	}
	escaped := false
	for i := 1; i < len(value); i++ {
		if escaped {
			escaped = false
			continue
		}
		if value[i] == '\\' {
			escaped = true
			continue
		}
		if value[i] == '"' {
			decoded, err := strconv.Unquote(value[:i+1])
			if err != nil {
				return "", "", false
			}
			return decoded, value[i+1:], true
		}
	}
	return "", "", false
}

func normalizeDiffContextTransportPath(path string) string {
	if path == "/dev/null" {
		return "/dev/null"
	}
	if strings.HasPrefix(path, "a/") || strings.HasPrefix(path, "b/") {
		return path[2:]
	}
	return path
}
