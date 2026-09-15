package knowledge

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

const (
	ExtractorKeyPDF         = "pdf_poppler"
	ExtractorVersionPDF     = "pdf_poppler_extractor_v1"
	DefaultPDFMaxPages      = 250
	defaultPDFCommandTimout = 30 * time.Second
)

var pdfAbsoluteTimePattern = regexp.MustCompile(`^D:(\d{14})(Z|([+-])(\d{2})'?(\d{2})'?)$`)

type commandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.Command(name, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		return output.Bytes(), err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return output.Bytes(), err
	case <-ctx.Done():
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-done
		return output.Bytes(), ctx.Err()
	}
}

type pdfExtractor struct {
	runner commandRunner
}

func newPDFExtractor(runner commandRunner) pdfExtractor {
	if runner == nil {
		runner = execCommandRunner{}
	}
	return pdfExtractor{runner: runner}
}

func (pdfExtractor) Key() string {
	return ExtractorKeyPDF
}

func (pdfExtractor) Version() string {
	return ExtractorVersionPDF
}

func (pdfExtractor) Supports(object KnowledgeObject) bool {
	return object.FileClass == storagecatalog.FileClassPDF || knowledgeObjectExtension(object) == ".pdf"
}

func (pdfExtractor) RequiresContent(KnowledgeObject) bool {
	return false
}

func (extractor pdfExtractor) Extract(input ExtractionInput) (ExtractionResult, error) {
	sourcePath := strings.TrimSpace(input.Object.SourcePath)
	if sourcePath == "" {
		return metadataOnlyExtraction(input.Object, ExtractionStatusSourceUnavailable, "source_path is required for PDF extraction"), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultPDFCommandTimout)
	defer cancel()

	metadata := map[string]any{
		"file_class": input.Object.FileClass,
		"format":     "pdf",
	}
	warnings := []string{}
	// pdfinfo's default date rendering is locale dependent. Request the raw PDF
	// date strings so the strict D:... parser below sees the document metadata
	// contract defined by Poppler rather than a human display format.
	infoOutput, err := extractor.runner.Run(ctx, "pdfinfo", "-rawdates", sourcePath)
	if err != nil {
		if pdfCommandIndicatesPassword(infoOutput, err) {
			return pdfMetadataOnly(input.Object, ExtractionStatusPasswordRequired, metadata, append(warnings, "pdf_password_required")), nil
		}
		return ExtractionResult{}, fmt.Errorf("pdfinfo failed: %w", err)
	}
	info := parsePDFInfoOutput(string(infoOutput))
	for key, value := range info.Metadata {
		metadata[key] = value
	}
	if info.PageCount > 0 {
		metadata["page_count"] = info.PageCount
	}
	if info.Encrypted {
		metadata["encrypted"] = true
		return attachPDFAbsoluteTime(pdfMetadataOnly(input.Object, ExtractionStatusPasswordRequired, metadata, append(warnings, "pdf_password_required")), metadata), nil
	}
	if info.PageCount > DefaultPDFMaxPages {
		metadata["max_pages"] = DefaultPDFMaxPages
		return attachPDFAbsoluteTime(pdfMetadataOnly(input.Object, ExtractionStatusTooLarge, metadata, append(warnings, fmt.Sprintf("pdf page count %d exceeds max_pages %d", info.PageCount, DefaultPDFMaxPages))), metadata), nil
	}

	if imageOutput, imageErr := extractor.runner.Run(ctx, "pdfimages", "-list", sourcePath); imageErr == nil {
		metadata["image_count"] = parsePDFImagesListCount(string(imageOutput))
	} else {
		warnings = append(warnings, "pdf_image_count_unavailable")
	}

	textOutput, err := extractor.runner.Run(ctx, "pdftotext", "-layout", "-enc", "UTF-8", sourcePath, "-")
	if err != nil {
		if pdfCommandIndicatesPassword(textOutput, err) {
			return attachPDFAbsoluteTime(pdfMetadataOnly(input.Object, ExtractionStatusPasswordRequired, metadata, append(warnings, "pdf_password_required")), metadata), nil
		}
		return ExtractionResult{}, fmt.Errorf("pdftotext failed: %w", err)
	}
	text := normalizeTextNewlines(string(textOutput))
	if strings.TrimSpace(text) == "" {
		metadata["ocr_deferred"] = true
		metadata["no_embedded_text"] = true
		return attachPDFAbsoluteTime(pdfMetadataOnly(input.Object, ExtractionStatusNoEmbeddedText, metadata, append(warnings, "no_embedded_text", "ocr_deferred")), metadata), nil
	}
	document := TextDocument{Text: text, Warnings: warnings}
	result, err := extractionResultFromDocument(input, extractor, ExtractionStatusExtracted, TextSourceEmbeddedText, document, metadata)
	if err == nil {
		result.TextSections = pdfEmbeddedTextSections(SplitPDFPages(text, 1))
	}
	return attachPDFAbsoluteTime(result, metadata), err
}

func pdfEmbeddedTextSections(pages []PDFPage) []ExtractedTextSection {
	sections := make([]ExtractedTextSection, 0, len(pages))
	for _, page := range pages {
		if strings.TrimSpace(page.Text) == "" {
			continue
		}
		sections = append(sections, ExtractedTextSection{Index: len(sections) + 1, Text: page.Text, TextSource: TextSourceEmbeddedText, StructuralPath: fmt.Sprintf("page:%d", page.Number), Metadata: map[string]any{"page_number": page.Number, "analysis_version": PDFPageAnalysisVersion}})
	}
	return sections
}

type pdfInfo struct {
	PageCount int
	Encrypted bool
	Metadata  map[string]any
}

func parsePDFInfoOutput(output string) pdfInfo {
	info := pdfInfo{Metadata: map[string]any{}}
	for _, line := range strings.Split(normalizeTextNewlines(output), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.ToLower(strings.ReplaceAll(key, " ", "_")))
		value = strings.TrimSpace(value)
		switch key {
		case "pages":
			if pages, err := strconv.Atoi(value); err == nil {
				info.PageCount = pages
			}
		case "encrypted":
			normalized := strings.ToLower(value)
			info.Encrypted = strings.HasPrefix(normalized, "yes")
			info.Metadata["encrypted"] = info.Encrypted
		case "title", "author", "creator", "producer", "creationdate", "moddate", "subject", "keywords":
			if value != "" {
				info.Metadata[key] = value
			}
		}
	}
	return info
}

func parsePDFImagesListCount(output string) int {
	count := 0
	for _, line := range strings.Split(normalizeTextNewlines(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "page ") || strings.HasPrefix(line, "----") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			if _, err := strconv.Atoi(fields[0]); err == nil {
				count++
			}
		}
	}
	return count
}

func pdfMetadataOnly(object KnowledgeObject, status string, metadata map[string]any, warnings []string) ExtractionResult {
	result := metadataOnlyExtraction(object, status, strings.Join(warnings, "; "))
	result.ExtractorKey = ExtractorKeyPDF
	result.ExtractorVersion = ExtractorVersionPDF
	result.Metadata = metadata
	result.Warnings = warnings
	return result
}

func attachPDFAbsoluteTime(result ExtractionResult, metadata map[string]any) ExtractionResult {
	candidates := []AbsoluteTimeCandidate{}
	for _, field := range []struct {
		key   string
		kind  string
		basis string
	}{
		{key: "moddate", kind: AbsoluteTimeKindModified, basis: AbsoluteTimeBasisEmbeddedModifiedAt},
		{key: "creationdate", kind: AbsoluteTimeKindCreated, basis: AbsoluteTimeBasisEmbeddedCreatedAt},
	} {
		raw, _ := metadata[field.key].(string)
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		candidate := AbsoluteTimeCandidate{Kind: field.kind, Basis: field.basis, RawValue: raw}
		if timestamp, ok := parsePDFAbsoluteTimestamp(raw); ok {
			candidate.Timestamp = &timestamp
		}
		candidates = append(candidates, candidate)
	}
	return attachExtractionAbsoluteTime(result, candidates)
}

func parsePDFAbsoluteTimestamp(raw string) (time.Time, bool) {
	match := pdfAbsoluteTimePattern.FindStringSubmatch(strings.TrimSpace(raw))
	if len(match) != 6 {
		return time.Time{}, false
	}
	offset := "+0000"
	if match[2] != "Z" {
		offset = match[3] + match[4] + match[5]
	}
	value, err := time.Parse("20060102150405 -0700", match[1]+" "+offset)
	if err != nil {
		return time.Time{}, false
	}
	return value.UTC(), true
}

func pdfCommandIndicatesPassword(output []byte, err error) bool {
	text := strings.ToLower(string(output) + " " + err.Error())
	return strings.Contains(text, "incorrect password") ||
		strings.Contains(text, "password") ||
		strings.Contains(text, "encrypted")
}
