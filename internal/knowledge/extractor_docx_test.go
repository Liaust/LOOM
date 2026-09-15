package knowledge

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

func TestDOCXExtractorExtractsTextMetadataLinksAndMedia(t *testing.T) {
	docxPath := writeTestDOCX(t, map[string]string{
		"word/document.xml": `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <w:body>
    <w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Case Overview</w:t></w:r></w:p>
    <w:p><w:r><w:t>Alpha phrase before </w:t></w:r><w:hyperlink r:id="rId5"><w:r><w:t>source link</w:t></w:r></w:hyperlink><w:r><w:t> after.</w:t></w:r></w:p>
    <w:tbl><w:tr><w:tc><w:p><w:r><w:t>Indicator</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>example.com</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
  </w:body>
</w:document>`,
		"word/_rels/document.xml.rels": `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://example.com/source" TargetMode="External"/>
</Relationships>`,
		"docProps/core.xml": `<?xml version="1.0" encoding="UTF-8"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/">
  <dc:title>Investigation Brief</dc:title>
  <dc:creator>Ada</dc:creator>
  <cp:keywords>osint, loom</cp:keywords>
  <dcterms:created>2026-07-05T12:00:00Z</dcterms:created>
  <dcterms:modified>2026-07-06T14:30:00+02:00</dcterms:modified>
</cp:coreProperties>`,
		"word/footnotes.xml":    `<w:footnotes xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:footnote><w:p><w:r><w:t>Footnote phrase</w:t></w:r></w:p></w:footnote></w:footnotes>`,
		"word/media/image1.png": "png",
	})
	object := testKnowledgeObjectForExtraction(storagecatalog.FileClassOfficeDocument, "brief.docx")
	object.SourcePath = docxPath

	extraction, err := docxExtractor{}.Extract(ExtractionInput{
		Object:  object,
		Chunker: ChunkerOptions{TargetCharacters: 120},
	})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if extraction.Status != ExtractionStatusExtracted {
		t.Fatalf("status = %q, want extracted", extraction.Status)
	}
	for _, want := range []string{"# Case Overview", "Alpha phrase before source link after.", "Indicator | example.com", "Footnote phrase"} {
		if !strings.Contains(extraction.Document.Text, want) {
			t.Fatalf("document text missing %q:\n%s", want, extraction.Document.Text)
		}
	}
	if extraction.Metadata["title"] != "Investigation Brief" ||
		extraction.Metadata["creator"] != "Ada" ||
		extraction.Metadata["embedded_media_count"] != 1 {
		t.Fatalf("metadata = %#v", extraction.Metadata)
	}
	if len(extraction.Document.Headings) != 2 || extraction.Document.Headings[0].StructuralPath != "Case Overview" {
		t.Fatalf("headings = %#v", extraction.Document.Headings)
	}
	if len(extraction.Links) != 1 || extraction.Links[0].RawTarget != "https://example.com/source" || extraction.Links[0].LinkText != "source link" {
		t.Fatalf("links = %#v", extraction.Links)
	}
	if len(extraction.Chunks) == 0 || extraction.Chunks[0].TextSource != TextSourceEmbeddedText {
		t.Fatalf("chunks = %#v, want embedded text chunks", extraction.Chunks)
	}
	if len(extraction.AbsoluteTimeCandidates) != 2 || extraction.AbsoluteTimeCandidates[0].Timestamp == nil ||
		extraction.AbsoluteTimeCandidates[0].Timestamp.Format(time.RFC3339) != "2026-07-06T12:30:00Z" {
		t.Fatalf("absolute time candidates = %#v", extraction.AbsoluteTimeCandidates)
	}
}

func TestDOCXExtractorPreservesMalformedCoreDateWarning(t *testing.T) {
	docxPath := writeTestDOCX(t, map[string]string{
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Body</w:t></w:r></w:p></w:body></w:document>`,
		"docProps/core.xml": `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dcterms="http://purl.org/dc/terms/"><dcterms:modified>tomorrow morning</dcterms:modified></cp:coreProperties>`,
	})
	object := testKnowledgeObjectForExtraction(storagecatalog.FileClassOfficeDocument, "malformed.docx")
	object.SourcePath = docxPath

	extraction, err := docxExtractor{}.Extract(ExtractionInput{Object: object})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if extraction.Status != ExtractionStatusExtracted || len(extraction.AbsoluteTimeWarnings) != 1 {
		t.Fatalf("extraction = %#v, want successful extraction with date warning", extraction)
	}
	if extraction.AbsoluteTimeWarnings[0].RawValue != "tomorrow morning" || extraction.Metadata["modified"] != "tomorrow morning" {
		t.Fatalf("raw malformed DOCX date was not retained: %#v", extraction)
	}
}

func writeTestDOCX(t *testing.T, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.docx")
	handle, err := os.Create(path)
	if err != nil {
		t.Fatalf("create docx: %v", err)
	}
	writer := zip.NewWriter(handle)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close docx: %v", err)
	}
	return path
}
