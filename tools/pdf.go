package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

const (
	pdfMaxPagesPerRead       = 20
	pdfInlinePageThreshold   = 10
	pdfMaxExtractSize        = 100 * 1024 * 1024
	pdfMaxExtractedTextBytes = 256 * 1024
	pdfMaxRenderedImageBytes = 3750 * 1024
	pdfInfoTimeout           = 10 * time.Second
	pdfProcessTimeout        = 120 * time.Second
)

var pdfPagesPattern = regexp.MustCompile(`(?m)^Pages:\s+(\d+)\s*$`)

type pdfPageRange struct {
	First int
	Last  int // zero means an open-ended range
}

type pdfCommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, []byte, error)
}

type execPDFCommandRunner struct{}

func (execPDFCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	stdout := newBoundedCommandBuffer(pdfCommandStdoutLimit(name))
	stderr := newBoundedCommandBuffer(64 * 1024)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err == nil && stdout.exceeded {
		err = &pdfCommandError{kind: pdfFailureTooLarge, message: fmt.Sprintf("%s output exceeds the %d KB limit; request a smaller PDF page range", name, stdout.limit/1024)}
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

type boundedCommandBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func newBoundedCommandBuffer(limit int) *boundedCommandBuffer {
	return &boundedCommandBuffer{limit: limit}
}

func (b *boundedCommandBuffer) Write(data []byte) (int, error) {
	originalLen := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.exceeded = b.exceeded || originalLen > 0
		return originalLen, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		b.exceeded = true
	}
	_, _ = b.buffer.Write(data)
	return originalLen, nil
}

func (b *boundedCommandBuffer) Bytes() []byte { return b.buffer.Bytes() }

func pdfCommandStdoutLimit(name string) int {
	if name == "pdftotext" {
		return pdfMaxExtractedTextBytes
	}
	return 64 * 1024
}

type pdfFailureKind string

const (
	pdfFailureUnavailable pdfFailureKind = "unavailable"
	pdfFailurePassword    pdfFailureKind = "password-protected"
	pdfFailureCorrupt     pdfFailureKind = "corrupt"
	pdfFailureTimeout     pdfFailureKind = "timeout"
	pdfFailureCancelled   pdfFailureKind = "cancelled"
	pdfFailureTooLarge    pdfFailureKind = "too-large"
	pdfFailureUnknown     pdfFailureKind = "unknown"
)

type pdfCommandError struct {
	kind    pdfFailureKind
	message string
}

func (e *pdfCommandError) Error() string { return e.message }

var errPDFNoText = errors.New("PDF contains no extractable text")

func parsePDFPageRange(raw string) (pdfPageRange, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return pdfPageRange{}, fmt.Errorf("pages must not be empty")
	}
	if strings.Count(value, "-") > 1 {
		return pdfPageRange{}, fmt.Errorf("invalid PDF page range %q", raw)
	}
	if strings.HasSuffix(value, "-") {
		first, err := strconv.Atoi(strings.TrimSuffix(value, "-"))
		if err != nil || first < 1 {
			return pdfPageRange{}, fmt.Errorf("invalid PDF page range %q", raw)
		}
		return pdfPageRange{First: first}, nil
	}
	if !strings.Contains(value, "-") {
		page, err := strconv.Atoi(value)
		if err != nil || page < 1 {
			return pdfPageRange{}, fmt.Errorf("invalid PDF page range %q", raw)
		}
		return pdfPageRange{First: page, Last: page}, nil
	}
	parts := strings.SplitN(value, "-", 2)
	first, firstErr := strconv.Atoi(strings.TrimSpace(parts[0]))
	last, lastErr := strconv.Atoi(strings.TrimSpace(parts[1]))
	if firstErr != nil || lastErr != nil || first < 1 || last < first {
		return pdfPageRange{}, fmt.Errorf("invalid PDF page range %q", raw)
	}
	return pdfPageRange{First: first, Last: last}, nil
}

func parseBoundedPDFPageRange(raw string) (pdfPageRange, error) {
	pageRange, err := parsePDFPageRange(raw)
	if err != nil {
		return pdfPageRange{}, fmt.Errorf(`invalid pages parameter %q; use formats like "1-5", "3", or "10-20" with 1-indexed pages`, raw)
	}
	if pageRange.Last == 0 || pageRange.Last-pageRange.First+1 > pdfMaxPagesPerRead {
		return pdfPageRange{}, fmt.Errorf("PDF page range %q exceeds the maximum of %d pages per request", raw, pdfMaxPagesPerRead)
	}
	return pageRange, nil
}

func validatePDFPagesInput(input map[string]any) error {
	raw, exists := input["pages"]
	if !exists || raw == nil {
		return nil
	}
	pages, ok := raw.(string)
	if !ok {
		return fmt.Errorf("pages must be a string")
	}
	_, err := parseBoundedPDFPageRange(pages)
	return err
}

func readPDFForTool(ctx context.Context, displayPath, fullPath, pages string, runner pdfCommandRunner) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if runner == nil {
		runner = execPDFCommandRunner{}
	}
	if err := validatePDFFile(fullPath); err != nil {
		return "", err
	}

	var selected pdfPageRange
	pageCount := 0
	if strings.TrimSpace(pages) == "" {
		var err error
		pageCount, err = getPDFPageCount(ctx, fullPath, runner)
		if err != nil {
			return "", fmt.Errorf("cannot determine PDF page count; install poppler-utils or provide an explicit pages range: %w", err)
		}
		if pageCount > pdfInlinePageThreshold {
			return "", fmt.Errorf("this PDF has %d pages, which is too many to read at once; use pages (for example, \"1-5\"), with at most %d pages per request", pageCount, pdfMaxPagesPerRead)
		}
		selected = pdfPageRange{First: 1, Last: pageCount}
	} else {
		var err error
		selected, err = parseBoundedPDFPageRange(pages)
		if err != nil {
			return "", err
		}
	}

	text, textErr := extractPDFText(ctx, fullPath, selected, runner)
	if textErr == nil {
		return formatPDFText(displayPath, selected, pageCount, text), nil
	}
	if isTerminalPDFExtractionError(textErr) {
		return "", textErr
	}
	if !MediaSupported(ctx) {
		return "", fmt.Errorf("PDF text extraction failed and the active model cannot receive rendered page images; install poppler-utils with pdftotext or use an image-capable model: %w", textErr)
	}

	images, err := renderPDFPages(ctx, fullPath, selected, runner)
	if err != nil {
		return "", fmt.Errorf("PDF text extraction and page rendering failed: %w", errors.Join(textErr, err))
	}
	attachment := buildPDFImageAttachment(displayPath, selected, images)
	if !EmitAttachment(ctx, attachment) {
		return "", fmt.Errorf("PDF pages were rendered but this execution path cannot deliver media attachments")
	}
	return fmt.Sprintf("PDF pages %s rendered as %d image attachment(s) from %s", formatPDFRange(selected), len(images), displayPath), nil
}

func validatePDFFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("PDF file not found: %s", path)
		}
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("PDF path is not a regular file: %s", path)
	}
	if info.Size() == 0 {
		return fmt.Errorf("PDF file is empty: %s", path)
	}
	if info.Size() > pdfMaxExtractSize {
		return fmt.Errorf("PDF file exceeds the %d MB extraction limit", pdfMaxExtractSize/(1024*1024))
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	header := make([]byte, 5)
	if _, err := io.ReadFull(file, header); err != nil || string(header) != "%PDF-" {
		return fmt.Errorf("file is not a valid PDF (missing %%PDF- header): %s", path)
	}
	return nil
}

func getPDFPageCount(ctx context.Context, path string, runner pdfCommandRunner) (int, error) {
	stdout, stderr, err := runPDFCommand(ctx, pdfInfoTimeout, runner, "pdfinfo", path)
	if err != nil {
		return 0, classifyPDFCommandError("pdfinfo", stderr, err)
	}
	match := pdfPagesPattern.FindSubmatch(stdout)
	if len(match) != 2 {
		return 0, fmt.Errorf("pdfinfo did not report a page count")
	}
	count, err := strconv.Atoi(string(match[1]))
	if err != nil || count < 1 {
		return 0, fmt.Errorf("pdfinfo reported an invalid page count")
	}
	return count, nil
}

func extractPDFText(ctx context.Context, path string, selected pdfPageRange, runner pdfCommandRunner) (string, error) {
	args := []string{"-f", strconv.Itoa(selected.First), "-l", strconv.Itoa(selected.Last), "-layout", path, "-"}
	stdout, stderr, err := runPDFCommand(ctx, pdfProcessTimeout, runner, "pdftotext", args...)
	if err != nil {
		return "", classifyPDFCommandError("pdftotext", stderr, err)
	}
	if len(stdout) > pdfMaxExtractedTextBytes {
		return "", &pdfCommandError{kind: pdfFailureTooLarge, message: fmt.Sprintf("extracted PDF text exceeds %d KB; request a smaller page range", pdfMaxExtractedTextBytes/1024)}
	}
	text := strings.TrimSpace(strings.ReplaceAll(string(stdout), "\x00", ""))
	if text == "" {
		return "", errPDFNoText
	}
	return text, nil
}

func formatPDFText(displayPath string, selected pdfPageRange, pageCount int, text string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "PDF file: %s\nPages: %s", displayPath, formatPDFRange(selected))
	if pageCount > 0 {
		fmt.Fprintf(&b, " of %d", pageCount)
	}
	b.WriteString("\n")

	parts := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\f")
	nonEmpty := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			nonEmpty = append(nonEmpty, trimmed)
		}
	}
	if len(nonEmpty) <= 1 {
		fmt.Fprintf(&b, "\n--- Pages %s ---\n%s\n", formatPDFRange(selected), strings.TrimSpace(text))
		return b.String()
	}
	for i, part := range nonEmpty {
		fmt.Fprintf(&b, "\n--- Page %d ---\n%s\n", selected.First+i, part)
	}
	return b.String()
}

func renderPDFPages(ctx context.Context, path string, selected pdfPageRange, runner pdfCommandRunner) ([][]byte, error) {
	dir, err := os.MkdirTemp("", "yhc-pdf-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	prefix := filepath.Join(dir, "page")
	args := []string{
		"-jpeg", "-r", "100", "-scale-to", "2000", "-jpegopt", "quality=80",
		"-f", strconv.Itoa(selected.First), "-l", strconv.Itoa(selected.Last), path, prefix,
	}
	_, stderr, err := runPDFCommand(ctx, pdfProcessTimeout, runner, "pdftoppm", args...)
	if err != nil {
		return nil, classifyPDFCommandError("pdftoppm", stderr, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.EqualFold(filepath.Ext(entry.Name()), ".jpg") {
			names = append(names, entry.Name())
		}
	}
	sortRenderedPageNames(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("pdftoppm produced no JPEG pages")
	}
	if len(names) > pdfMaxPagesPerRead {
		return nil, fmt.Errorf("pdftoppm produced %d pages, exceeding the %d-page limit", len(names), pdfMaxPagesPerRead)
	}
	images := make([][]byte, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if info.Size() > pdfMaxRenderedImageBytes {
			return nil, fmt.Errorf("rendered page %s exceeds the %d KB image limit", name, pdfMaxRenderedImageBytes/1024)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(data) < 3 || data[0] != 0xff || data[1] != 0xd8 || data[2] != 0xff {
			return nil, fmt.Errorf("rendered page %s is not a valid JPEG", name)
		}
		images = append(images, data)
	}
	return images, nil
}

func sortRenderedPageNames(names []string) {
	sort.SliceStable(names, func(i, j int) bool {
		left, leftOK := renderedPageNumber(names[i])
		right, rightOK := renderedPageNumber(names[j])
		if leftOK && rightOK && left != right {
			return left < right
		}
		return names[i] < names[j]
	})
}

func renderedPageNumber(name string) (int, bool) {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	dash := strings.LastIndexByte(stem, '-')
	if dash < 0 || dash == len(stem)-1 {
		return 0, false
	}
	page, err := strconv.Atoi(stem[dash+1:])
	return page, err == nil && page > 0
}

func buildPDFImageAttachment(displayPath string, selected pdfPageRange, images [][]byte) *schema.Message {
	summary := fmt.Sprintf("Rendered PDF pages %s from %s", formatPDFRange(selected), displayPath)
	parts := make([]schema.MessageInputPart, 0, len(images)+1)
	parts = append(parts, schema.MessageInputPart{Type: schema.ChatMessagePartTypeText, Text: summary})
	for i, image := range images {
		data := base64.StdEncoding.EncodeToString(image)
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{
				Base64Data: &data,
				MIMEType:   "image/jpeg",
			}},
			Extra: map[string]any{"page": selected.First + i, "path": displayPath},
		})
	}
	return &schema.Message{
		Role:                  schema.User,
		Content:               summary,
		UserInputMultiContent: parts,
		Extra: map[string]any{
			"is_meta":         true,
			"attachment_kind": "pdf_pages",
			"file_path":       displayPath,
			"pages":           formatPDFRange(selected),
		},
	}
}

func formatPDFRange(selected pdfPageRange) string {
	if selected.First == selected.Last {
		return strconv.Itoa(selected.First)
	}
	return fmt.Sprintf("%d-%d", selected.First, selected.Last)
}

func runPDFCommand(ctx context.Context, timeout time.Duration, runner pdfCommandRunner, name string, args ...string) ([]byte, []byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr, err := runner.Run(commandCtx, name, args...)
	if err == nil {
		return stdout, stderr, nil
	}
	if ctx.Err() != nil {
		return stdout, stderr, &pdfCommandError{kind: pdfFailureCancelled, message: fmt.Sprintf("%s cancelled: %v", name, ctx.Err())}
	}
	if commandCtx.Err() != nil {
		return stdout, stderr, &pdfCommandError{kind: pdfFailureTimeout, message: fmt.Sprintf("%s timed out after %s", name, timeout)}
	}
	return stdout, stderr, err
}

func classifyPDFCommandError(name string, stderr []byte, err error) error {
	var execErr *exec.Error
	if errors.As(err, &execErr) && errors.Is(execErr.Err, exec.ErrNotFound) {
		return &pdfCommandError{kind: pdfFailureUnavailable, message: fmt.Sprintf("%s is unavailable; install poppler-utils", name)}
	}
	var failure *pdfCommandError
	if errors.As(err, &failure) {
		return failure
	}
	detail := strings.TrimSpace(string(stderr))
	lower := strings.ToLower(detail)
	if strings.Contains(lower, "password") || strings.Contains(lower, "encrypted") {
		return &pdfCommandError{kind: pdfFailurePassword, message: "PDF is password-protected; provide an unprotected version"}
	}
	if strings.Contains(lower, "damaged") || strings.Contains(lower, "corrupt") || strings.Contains(lower, "invalid") {
		return &pdfCommandError{kind: pdfFailureCorrupt, message: "PDF is corrupted or invalid"}
	}
	if len(detail) > 500 {
		detail = detail[:500] + "..."
	}
	if detail == "" {
		detail = err.Error()
	}
	return &pdfCommandError{kind: pdfFailureUnknown, message: fmt.Sprintf("%s failed: %s", name, detail)}
}

func isTerminalPDFExtractionError(err error) bool {
	var failure *pdfCommandError
	if !errors.As(err, &failure) {
		return false
	}
	switch failure.kind {
	case pdfFailurePassword, pdfFailureCorrupt, pdfFailureTimeout, pdfFailureCancelled, pdfFailureTooLarge:
		return true
	default:
		return false
	}
}
