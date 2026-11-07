package tools

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"strings"
)

func extractWebFetchText(ctx context.Context, response webFetchResponse, pages string, runner pdfCommandRunner) (string, error) {
	declared, _, _ := mime.ParseMediaType(response.contentType)
	detected, _, _ := mime.ParseMediaType(http.DetectContentType(response.body))
	if declared == "application/pdf" || detected == "application/pdf" {
		return extractWebFetchPDF(ctx, response, pages, runner)
	}
	if !webFetchTextMediaType(detected) {
		return "", fmt.Errorf("unsupported content type %s; download the file and use an appropriate file reader", detected)
	}
	if declared != "" && declared != "application/octet-stream" && !webFetchTextMediaType(declared) {
		return "", fmt.Errorf("unsupported content type %s; download the file and use an appropriate file reader", declared)
	}
	if pages != "" {
		return "", fmt.Errorf("pages is only supported for PDF content")
	}
	return htmlToText(string(response.body)), nil
}

func webFetchTextMediaType(value string) bool {
	return strings.HasPrefix(value, "text/") || value == "application/json" || value == "application/xml" ||
		value == "application/javascript" ||
		(strings.HasPrefix(value, "application/") && (strings.HasSuffix(value, "+json") || strings.HasSuffix(value, "+xml")))
}

func extractWebFetchPDF(ctx context.Context, response webFetchResponse, pages string, runner pdfCommandRunner) (string, error) {
	if runner == nil {
		runner = execPDFCommandRunner{}
	}
	file, err := os.CreateTemp("", "yhc-webfetch-*.pdf")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(response.body); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := validatePDFFile(file.Name()); err != nil {
		return "", err
	}

	var selected pdfPageRange
	pageCount := 0
	if strings.TrimSpace(pages) == "" {
		pageCount, err = getPDFPageCount(ctx, file.Name(), runner)
		if err != nil {
			return "", fmt.Errorf("cannot determine PDF page count; install poppler-utils or provide an explicit pages range: %w", err)
		}
		if pageCount > pdfInlinePageThreshold {
			return "", fmt.Errorf("PDF has %d pages; provide pages (for example, 1-5), with at most %d pages per request", pageCount, pdfMaxPagesPerRead)
		}
		selected = pdfPageRange{First: 1, Last: pageCount}
	} else {
		selected, err = parseBoundedPDFPageRange(pages)
		if err != nil {
			return "", err
		}
	}
	text, err := extractPDFText(ctx, file.Name(), selected, runner)
	if errors.Is(err, errPDFNoText) {
		return "", fmt.Errorf("PDF has no extractable text; download it and use Read with pages to read rendered pages")
	}
	if err != nil {
		return "", fmt.Errorf("cannot extract PDF text: %w", err)
	}
	return formatPDFText(response.finalURL, selected, pageCount, text), nil
}
