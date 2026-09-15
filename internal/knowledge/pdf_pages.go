package knowledge

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

const PDFPageAnalysisVersion = "pdf_page_usefulness.v1"

type PDFPage struct {
	Number             int
	Text               string
	UsefulEmbeddedText bool
}

func AnalyzePDFPageText(text string, minimumNonWhitespace int) bool {
	if minimumNonWhitespace <= 0 {
		minimumNonWhitespace = 24
	}
	count := 0
	for _, value := range text {
		if !unicode.IsSpace(value) {
			count++
		}
	}
	return count >= minimumNonWhitespace
}

func SplitPDFPages(text string, minimumNonWhitespace int) []PDFPage {
	raw := strings.Split(normalizeTextNewlines(text), "\f")
	pages := make([]PDFPage, 0, len(raw))
	for index, page := range raw {
		if index == len(raw)-1 && strings.TrimSpace(page) == "" {
			continue
		}
		pages = append(pages, PDFPage{Number: index + 1, Text: strings.TrimSpace(page), UsefulEmbeddedText: AnalyzePDFPageText(page, minimumNonWhitespace)})
	}
	return pages
}

func AnalyzePDFPages(ctx context.Context, runner commandRunner, sourcePath string, minimumNonWhitespace int) ([]PDFPage, error) {
	if runner == nil {
		runner = execCommandRunner{}
	}
	output, err := runner.Run(ctx, "pdftotext", "-layout", "-enc", "UTF-8", sourcePath, "-")
	if err != nil {
		if pdfCommandIndicatesPassword(output, err) {
			return nil, fmt.Errorf("%w: pdf password required", ErrInvalid)
		}
		return nil, err
	}
	return SplitPDFPages(string(output), minimumNonWhitespace), nil
}
