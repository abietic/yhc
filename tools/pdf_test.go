package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

type pdfRunnerFunc func(context.Context, string, ...string) ([]byte, []byte, error)

func (fn pdfRunnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	return fn(ctx, name, args...)
}

func writePDFTestFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\n"+content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeMinimalValidPDF(t *testing.T) string {
	t.Helper()
	stream := "BT /F1 18 Tf 72 720 Td (Hello PDF) Tj ET"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		offsets[i+1] = pdf.Len()
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for i := 1; i < len(offsets); i++ {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	path := filepath.Join(t.TempDir(), "valid.pdf")
	if err := os.WriteFile(path, pdf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParsePDFPageRange(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    pdfPageRange
		wantErr bool
	}{
		{name: "single", raw: "5", want: pdfPageRange{First: 5, Last: 5}},
		{name: "closed", raw: "1-10", want: pdfPageRange{First: 1, Last: 10}},
		{name: "open", raw: "3-", want: pdfPageRange{First: 3}},
		{name: "whitespace", raw: " 4 - 6 ", want: pdfPageRange{First: 4, Last: 6}},
		{name: "empty", raw: "", wantErr: true},
		{name: "zero", raw: "0", wantErr: true},
		{name: "nonnumeric", raw: "one", wantErr: true},
		{name: "inverted", raw: "6-4", wantErr: true},
		{name: "multiple dashes", raw: "1-2-3", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePDFPageRange(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parsePDFPageRange(%q) error = %v", tt.raw, err)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("parsePDFPageRange(%q) = %#v, want %#v", tt.raw, got, tt.want)
			}
		})
	}
	if _, err := parseBoundedPDFPageRange("1-20"); err != nil {
		t.Fatalf("20-page range rejected: %v", err)
	}
	for _, raw := range []string{"1-21", "3-"} {
		if _, err := parseBoundedPDFPageRange(raw); err == nil || !strings.Contains(err.Error(), "maximum of 20") {
			t.Fatalf("bounded range %q error = %v", raw, err)
		}
	}
}

func TestReadPDFExtractsTextAndRequiresRangesForLargeFiles(t *testing.T) {
	path := writePDFTestFile(t, "text fixture")
	runner := pdfRunnerFunc(func(_ context.Context, name string, _ ...string) ([]byte, []byte, error) {
		switch name {
		case "pdfinfo":
			return []byte("Title: fixture\nPages:          3\n"), nil, nil
		case "pdftotext":
			return []byte("first page\fsecond page\fthird page"), nil, nil
		default:
			t.Fatalf("unexpected command %s", name)
			return nil, nil, nil
		}
	})
	result, err := readPDFForTool(context.Background(), path, path, "", runner)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Pages: 1-3 of 3", "--- Page 1 ---", "second page", "--- Page 3 ---"} {
		if !strings.Contains(result, want) {
			t.Fatalf("PDF text result missing %q: %q", want, result)
		}
	}

	largeRunner := pdfRunnerFunc(func(_ context.Context, name string, _ ...string) ([]byte, []byte, error) {
		if name != "pdfinfo" {
			t.Fatalf("large implicit read invoked %s", name)
		}
		return []byte("Pages: 11\n"), nil, nil
	})
	if _, err := readPDFForTool(context.Background(), path, path, "", largeRunner); err == nil || !strings.Contains(err.Error(), "too many to read at once") {
		t.Fatalf("large implicit PDF error = %v", err)
	}
}

func TestReadPDFExplicitRangeSkipsPageCount(t *testing.T) {
	path := writePDFTestFile(t, "range fixture")
	runner := pdfRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		if name != "pdftotext" {
			t.Fatalf("explicit range invoked %s", name)
		}
		if !slices.Contains(args, "4") || !slices.Contains(args, "5") {
			t.Fatalf("pdftotext args = %#v", args)
		}
		return []byte("bounded text"), nil, nil
	})
	result, err := readPDFForTool(context.Background(), path, path, "4-5", runner)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Pages: 4-5") || !strings.Contains(result, "bounded text") {
		t.Fatalf("explicit PDF result = %q", result)
	}
}

func TestReadPDFFallsBackToImageAttachment(t *testing.T) {
	path := writePDFTestFile(t, "scan fixture")
	var renderDir string
	runner := pdfRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		switch name {
		case "pdftotext":
			return nil, nil, nil
		case "pdftoppm":
			prefix := args[len(args)-1]
			renderDir = filepath.Dir(prefix)
			for _, name := range []string{"page-02.jpg", "page-03.jpg"} {
				if err := os.WriteFile(filepath.Join(renderDir, name), []byte{0xff, 0xd8, 0xff, 0xd9}, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			return nil, nil, nil
		default:
			t.Fatalf("unexpected command %s", name)
			return nil, nil, nil
		}
	})
	var attachments []*schema.Message
	ctx := WithMediaSupport(context.Background(), true)
	ctx = WithAttachmentFn(ctx, func(message *schema.Message) { attachments = append(attachments, message) })
	result, err := readPDFForTool(ctx, path, path, "2-3", runner)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "2 image attachment(s)") || len(attachments) != 1 {
		t.Fatalf("result = %q attachments = %#v", result, attachments)
	}
	attachment := attachments[0]
	if attachment.Extra["attachment_kind"] != "pdf_pages" || len(attachment.UserInputMultiContent) != 3 {
		t.Fatalf("attachment = %#v", attachment)
	}
	for _, part := range attachment.UserInputMultiContent[1:] {
		if part.Type != schema.ChatMessagePartTypeImageURL || part.Image == nil || part.Image.Base64Data == nil || part.Image.MIMEType != "image/jpeg" {
			t.Fatalf("image part = %#v", part)
		}
	}
	if _, err := os.Stat(renderDir); !os.IsNotExist(err) {
		t.Fatalf("temporary render directory was retained: %v", err)
	}
}

func TestReadPDFFallbackRequiresMediaDelivery(t *testing.T) {
	path := writePDFTestFile(t, "scan fixture")
	runner := pdfRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		if name == "pdftotext" {
			return nil, nil, &exec.Error{Name: name, Err: exec.ErrNotFound}
		}
		prefix := args[len(args)-1]
		return nil, nil, os.WriteFile(prefix+"-1.jpg", []byte{0xff, 0xd8, 0xff, 0xd9}, 0o600)
	})
	if _, err := readPDFForTool(context.Background(), path, path, "1", runner); err == nil || !strings.Contains(err.Error(), "active model cannot receive") {
		t.Fatalf("non-media fallback error = %v", err)
	}
	ctx := WithMediaSupport(context.Background(), true)
	if _, err := readPDFForTool(ctx, path, path, "1", runner); err == nil || !strings.Contains(err.Error(), "cannot deliver media attachments") {
		t.Fatalf("missing emitter error = %v", err)
	}
}

func TestReadPDFValidationAndCommandFailures(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.pdf")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validatePDFFile(empty); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty PDF error = %v", err)
	}
	invalid := filepath.Join(t.TempDir(), "invalid.pdf")
	if err := os.WriteFile(invalid, []byte("not a pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validatePDFFile(invalid); err == nil || !strings.Contains(err.Error(), "missing %PDF-") {
		t.Fatalf("invalid PDF error = %v", err)
	}

	for _, tt := range []struct {
		name   string
		stderr string
		want   string
	}{
		{name: "password", stderr: "Incorrect password", want: "password-protected"},
		{name: "corrupt", stderr: "Document is damaged", want: "corrupted or invalid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyPDFCommandError("pdftoppm", []byte(tt.stderr), errors.New("exit 1"))
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("classified error = %v", err)
			}
		})
	}
}

func TestRunPDFCommandHonorsCancellationAndTimeout(t *testing.T) {
	blocking := pdfRunnerFunc(func(ctx context.Context, _ string, _ ...string) ([]byte, []byte, error) {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := runPDFCommand(cancelled, time.Second, blocking, "pdfinfo"); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("cancelled command error = %v", err)
	}
	if _, _, err := runPDFCommand(context.Background(), time.Millisecond, blocking, "pdfinfo"); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timed command error = %v", err)
	}
}

func TestPDFCommandBufferAndRenderedPageOrdering(t *testing.T) {
	buffer := newBoundedCommandBuffer(4)
	if n, err := buffer.Write([]byte("abcdef")); err != nil || n != 6 {
		t.Fatalf("bounded write = %d err=%v", n, err)
	}
	if string(buffer.Bytes()) != "abcd" || !buffer.exceeded {
		t.Fatalf("bounded buffer = %q exceeded=%v", buffer.Bytes(), buffer.exceeded)
	}
	if n, err := buffer.Write([]byte("gh")); err != nil || n != 2 || string(buffer.Bytes()) != "abcd" {
		t.Fatalf("discarded write = %d err=%v buffer=%q", n, err, buffer.Bytes())
	}

	names := []string{"page-10.jpg", "page-2.jpg", "page-01.jpg", "page-3.jpg"}
	sortRenderedPageNames(names)
	want := []string{"page-01.jpg", "page-2.jpg", "page-3.jpg", "page-10.jpg"}
	if !slices.Equal(names, want) {
		t.Fatalf("rendered page order = %#v, want %#v", names, want)
	}
}

func TestPDFPopplerSmoke(t *testing.T) {
	for _, command := range []string{"pdfinfo", "pdftoppm"} {
		if _, err := exec.LookPath(command); err != nil {
			t.Skipf("%s unavailable: %v", command, err)
		}
	}
	path := writeMinimalValidPDF(t)
	count, err := getPDFPageCount(context.Background(), path, execPDFCommandRunner{})
	if err != nil || count != 1 {
		t.Fatalf("real pdfinfo count = %d err=%v", count, err)
	}
	images, err := renderPDFPages(context.Background(), path, pdfPageRange{First: 1, Last: 1}, execPDFCommandRunner{})
	if err != nil || len(images) != 1 {
		t.Fatalf("real pdftoppm images = %d err=%v", len(images), err)
	}
	if _, err := exec.LookPath("pdftotext"); err == nil {
		text, extractErr := extractPDFText(context.Background(), path, pdfPageRange{First: 1, Last: 1}, execPDFCommandRunner{})
		if extractErr != nil || !strings.Contains(text, "Hello PDF") {
			t.Fatalf("real pdftotext text = %q err=%v", text, extractErr)
		}
	}
}
