// Package attachments implements file attachment and image handling for the TUI.
// Mirrors src/utils/attachments.ts and src/utils/imagePaste.ts from the reference.
package attachments

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/abietic/yhc/internal/identity"
)

// PasteThreshold is the character count for considering text a "large paste".
const PasteThreshold = 800

// MaxAttachmentBytes bounds clipboard and composer payloads retained in memory.
// It matches the practical limit used by modern coding-agent attachment flows.
const MaxAttachmentBytes = 5 * 1024 * 1024

const (
	clipboardImageTimeout     = 3 * time.Second
	clipboardProbeOutputLimit = 64 * 1024
	clipboardImageOutputLimit = MaxAttachmentBytes + 1
	clipboardTempFilePattern  = identity.CommandName + "-clipboard-*.png"
)

var errClipboardOutputTooLarge = errors.New("clipboard command output exceeds limit")

// ImageMaxWidth and ImageMaxHeight are the maximum dimensions for pasted images.
const (
	ImageMaxWidth  = 1568
	ImageMaxHeight = 1568
)

// Attachment represents a file attachment to a message.
type Attachment struct {
	Type     string // "file", "image", "directory"
	Path     string
	Name     string
	Content  string // text content or base64 for images
	MimeType string
	Size     int64
}

// ImagePasteResult holds the result of a clipboard image paste operation.
type ImagePasteResult struct {
	HasImage bool
	Data     []byte
	Format   string // "png", "jpeg", "gif", "webp"
	Error    error
}

type clipboardCommandRunner func(
	context.Context,
	string,
	[]string,
	int,
) ([]byte, error)

type clipboardImageBackend struct {
	goos       string
	run        clipboardCommandRunner
	createTemp func(string, string) (*os.File, error)
	readFile   func(string, int) ([]byte, error)
	remove     func(string) error
	timeout    time.Duration
}

// CheckClipboardImage checks if the system clipboard contains an image using
// one bounded platform adapter. It retains no path or temporary-file identity.
func CheckClipboardImage() ImagePasteResult {
	return ReadClipboardImage(context.Background())
}

// ReadClipboardImage is the injectable, context-aware clipboard image
// boundary used by the TUI's typed async request.
func ReadClipboardImage(ctx context.Context) ImagePasteResult {
	return defaultClipboardImageBackend().read(ctx)
}

func defaultClipboardImageBackend() clipboardImageBackend {
	return clipboardImageBackend{
		goos:       runtime.GOOS,
		run:        runClipboardCommand,
		createTemp: os.CreateTemp,
		readFile:   readBoundedClipboardFile,
		remove:     os.Remove,
		timeout:    clipboardImageTimeout,
	}
}

func (b clipboardImageBackend) read(parent context.Context) ImagePasteResult {
	if parent == nil {
		parent = context.Background()
	}
	timeout := b.timeout
	if timeout <= 0 {
		timeout = clipboardImageTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	switch b.goos {
	case "darwin":
		return b.readDarwin(ctx)
	case "linux":
		return b.readLinux(ctx)
	case "windows":
		return b.readWindows(ctx)
	default:
		return ImagePasteResult{Error: fmt.Errorf("unsupported platform: %s", b.goos)}
	}
}

func (b clipboardImageBackend) readDarwin(ctx context.Context) ImagePasteResult {
	probe := `try
the clipboard as «class PNGf»
return "has_image"
on error
return ""
end try`
	out, err := b.run(ctx, "osascript", []string{"-e", probe}, clipboardProbeOutputLimit)
	if err != nil || !strings.Contains(string(out), "has_image") {
		return ImagePasteResult{HasImage: false}
	}
	file, err := b.createTemp("", clipboardTempFilePattern)
	if err != nil {
		return ImagePasteResult{HasImage: true, Error: err}
	}
	tmpPath := file.Name()
	if chmodErr := file.Chmod(0o600); chmodErr != nil {
		_ = file.Close()
		_ = b.remove(tmpPath)
		return ImagePasteResult{HasImage: true, Error: chmodErr}
	}
	if closeErr := file.Close(); closeErr != nil {
		_ = b.remove(tmpPath)
		return ImagePasteResult{HasImage: true, Error: closeErr}
	}
	defer func() {
		_ = b.remove(tmpPath)
	}()
	script := fmt.Sprintf(
		`set png_data to (the clipboard as «class PNGf»)
	set fp to open for access POSIX file "%s" with write permission
	set eof fp to 0
	write png_data to fp
	close access fp`, escapeAppleScriptString(tmpPath))
	if _, err := b.run(ctx, "osascript", []string{"-e", script}, clipboardProbeOutputLimit); err != nil {
		return ImagePasteResult{HasImage: true, Error: err}
	}
	data, err := b.readFile(tmpPath, clipboardImageOutputLimit)
	if err != nil {
		return ImagePasteResult{HasImage: true, Error: err}
	}
	return clipboardImageResult(data, "png")
}

func (b clipboardImageBackend) readLinux(ctx context.Context) ImagePasteResult {
	out, err := b.run(
		ctx,
		"xclip",
		[]string{"-selection", "clipboard", "-t", "TARGETS", "-o"},
		clipboardProbeOutputLimit,
	)
	if err != nil {
		return ImagePasteResult{HasImage: false}
	}
	if !strings.Contains(string(out), "image/png") {
		return ImagePasteResult{HasImage: false}
	}
	data, err := b.run(
		ctx,
		"xclip",
		[]string{"-selection", "clipboard", "-t", "image/png", "-o"},
		clipboardImageOutputLimit,
	)
	if err != nil {
		return ImagePasteResult{HasImage: true, Error: err}
	}
	return clipboardImageResult(data, "png")
}

func (b clipboardImageBackend) readWindows(ctx context.Context) ImagePasteResult {
	script := `
$img = Get-Clipboard -Format Image
if ($img -ne $null) { Write-Output "has_image" }
`
	out, err := b.run(
		ctx,
		"powershell",
		[]string{"-NoProfile", "-NonInteractive", "-Command", script},
		clipboardProbeOutputLimit,
	)
	if err != nil || !strings.Contains(string(out), "has_image") {
		return ImagePasteResult{HasImage: false}
	}
	file, err := b.createTemp("", clipboardTempFilePattern)
	if err != nil {
		return ImagePasteResult{HasImage: true, Error: err}
	}
	tmpPath := file.Name()
	if chmodErr := file.Chmod(0o600); chmodErr != nil {
		_ = file.Close()
		_ = b.remove(tmpPath)
		return ImagePasteResult{HasImage: true, Error: chmodErr}
	}
	if closeErr := file.Close(); closeErr != nil {
		_ = b.remove(tmpPath)
		return ImagePasteResult{HasImage: true, Error: closeErr}
	}
	defer func() {
		_ = b.remove(tmpPath)
	}()
	saveScript := fmt.Sprintf(`
$img = Get-Clipboard -Format Image
$img.Save('%s')
`, strings.ReplaceAll(tmpPath, "'", "''"))
	if _, err := b.run(
		ctx,
		"powershell",
		[]string{"-NoProfile", "-NonInteractive", "-Command", saveScript},
		clipboardProbeOutputLimit,
	); err != nil {
		return ImagePasteResult{HasImage: true, Error: err}
	}
	data, err := b.readFile(tmpPath, clipboardImageOutputLimit)
	if err != nil {
		return ImagePasteResult{HasImage: true, Error: err}
	}
	return clipboardImageResult(data, "png")
}

func clipboardImageResult(data []byte, format string) ImagePasteResult {
	if len(data) == 0 {
		return ImagePasteResult{HasImage: true, Error: fmt.Errorf("clipboard image is empty")}
	}
	if len(data) > MaxAttachmentBytes {
		clearClipboardBytes(data)
		return ImagePasteResult{HasImage: true, Error: fmt.Errorf("clipboard image is too large (maximum 5 MiB)")}
	}
	return ImagePasteResult{HasImage: true, Data: data, Format: format}
}

func runClipboardCommand(
	ctx context.Context,
	name string,
	args []string,
	limit int,
) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("clipboard command output limit must be positive")
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout boundedClipboardBuffer
	stdout.limit = limit
	var stderr boundedClipboardBuffer
	stderr.limit = clipboardProbeOutputLimit
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stdout.tooLarge || stderr.tooLarge {
			return nil, errClipboardOutputTooLarge
		}
		return nil, err
	}
	if stdout.tooLarge {
		return nil, errClipboardOutputTooLarge
	}
	return stdout.Bytes(), nil
}

type boundedClipboardBuffer struct {
	bytes.Buffer
	limit    int
	tooLarge bool
}

func (b *boundedClipboardBuffer) Write(data []byte) (int, error) {
	if b.limit <= 0 || b.Len()+len(data) > b.limit {
		b.tooLarge = true
		return 0, errClipboardOutputTooLarge
	}
	return b.Buffer.Write(data)
}

func readBoundedClipboardFile(path string, limit int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)))
	if err != nil {
		return nil, err
	}
	if len(data) >= limit {
		clearClipboardBytes(data)
		return nil, errClipboardOutputTooLarge
	}
	return data, nil
}

func escapeAppleScriptString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

func clearClipboardBytes(data []byte) {
	for index := range data {
		data[index] = 0
	}
}

// ImageToBase64Block converts image data to a base64-encoded content block.
func ImageToBase64Block(data []byte, format string) map[string]interface{} {
	mediaType := "image/" + format
	if format == "jpg" {
		mediaType = "image/jpeg"
	}
	return map[string]interface{}{
		"type": "image",
		"source": map[string]interface{}{
			"type":       "base64",
			"media_type": mediaType,
			"data":       base64.StdEncoding.EncodeToString(data),
		},
	}
}

// ResolveAttachment resolves a file path into an attachment with content.
func ResolveAttachment(path string) (*Attachment, error) {
	absPath := path
	if !filepath.IsAbs(path) {
		cwd, _ := os.Getwd()
		absPath = filepath.Join(cwd, path)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("attachment not found: %s", path)
	}

	if info.IsDir() {
		return &Attachment{
			Type: "directory",
			Path: absPath,
			Name: filepath.Base(absPath),
			Size: 0,
		}, nil
	}
	if info.Size() > MaxAttachmentBytes {
		return nil, fmt.Errorf("attachment is too large (maximum 5 MiB): %s", path)
	}

	// Check if it's an image
	ext := strings.ToLower(filepath.Ext(absPath))
	imageExts := map[string]string{
		".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
		".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml",
	}

	if mime, isImage := imageExts[ext]; isImage {
		data, err := os.ReadFile(absPath)
		if err != nil {
			return nil, err
		}
		return &Attachment{
			Type:     "image",
			Path:     absPath,
			Name:     filepath.Base(absPath),
			Content:  base64.StdEncoding.EncodeToString(data),
			MimeType: mime,
			Size:     info.Size(),
		}, nil
	}

	// Text file
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	return &Attachment{
		Type:     "file",
		Path:     absPath,
		Name:     filepath.Base(absPath),
		Content:  string(data),
		MimeType: "text/plain",
		Size:     info.Size(),
	}, nil
}

// NormalizePastedImagePath recognizes a single pasted local image path without
// touching the filesystem. Resolution and file I/O can then run asynchronously.
func NormalizePastedImagePath(text string) (string, bool) {
	path := strings.TrimSpace(text)
	path = strings.Trim(path, "\"'")
	path = strings.ReplaceAll(path, `\ `, " ")
	if strings.HasPrefix(path, "file://") {
		parsed, err := url.Parse(path)
		if err != nil {
			return "", false
		}
		path = parsed.Path
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg":
		return path, path != ""
	default:
		return "", false
	}
}

// IsLargePaste returns true if the text exceeds the paste threshold.
func IsLargePaste(text string) bool {
	return utf8.RuneCountInString(text) > PasteThreshold
}
