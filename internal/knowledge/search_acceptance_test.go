package knowledge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	lexical "loom.local/loom/internal/search"
	"loom.local/loom/internal/storagecatalog"
)

func TestNotesSearchAcceptanceMarkdownSignals(t *testing.T) {
	service := NewService(nil)
	object := KnowledgeObject{
		KnowledgeObjectID: ids.NewKnowledgeObjectID(),
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RelativePath:      "reports/threat-intel.md",
		SourcePath:        "/srv/loom-box/Notes/reports/threat-intel.md",
		Title:             "Threat Intel Report",
		FileClass:         storagecatalog.FileClassMarkdown,
		ProcessingState:   ProcessingStateMetadataOnly,
	}
	content := `---
tags:
  - osint
  - reliability
---
# Collection

Source reliability checks should mention satellite imagery.

## Follow Up

The field report mentions a quoted phrase: unusual antenna pattern.
`
	extraction, err := service.BuildTextPipelineExtraction(object, content, ChunkerOptions{TargetCharacters: 600})
	if err != nil {
		t.Fatalf("BuildTextPipelineExtraction returned error: %v", err)
	}
	if len(extraction.Chunks) == 0 {
		t.Fatal("expected markdown chunks")
	}
	version := KnowledgeObjectVersion{
		KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID(),
		Metadata:                 textPipelineVersionMetadata(extraction),
	}
	chunk := KnowledgeChunk{
		KnowledgeChunkID: ids.NewKnowledgeChunkID(),
		StructuralPath:   extraction.Chunks[0].StructuralPath,
		ChunkText:        extraction.Chunks[0].Text,
	}
	document, err := lexical.BuildLexicalDocument(knowledgeChunkLexicalDocumentInput("search_document_markdown", object, version, chunk))
	if err != nil {
		t.Fatalf("BuildLexicalDocument returned error: %v", err)
	}

	for _, want := range []struct {
		field string
		term  string
	}{
		{lexical.LexicalFieldTitle, "threat"},
		{lexical.LexicalFieldHeading, "collection"},
		{lexical.LexicalFieldPath, "reports"},
		{lexical.LexicalFieldTag, "osint"},
		{lexical.LexicalFieldBody, "satellite"},
	} {
		if !hasLexicalTerm(document.Terms, want.field, want.term) {
			t.Fatalf("markdown lexical terms missing %s:%s in %#v", want.field, want.term, document.Terms)
		}
	}
	parsed := ParseNotesSearchQuery(`tag:osint path:reports "unusual antenna pattern" satellite`)
	if strings.Join(parsed.Phrases, ",") != "unusual antenna pattern" {
		t.Fatalf("phrases = %#v", parsed.Phrases)
	}
	if parsed.Input.Path != "reports" || len(parsed.Input.Tags) != 1 || parsed.Input.Tags[0] != "osint" {
		t.Fatalf("parsed filters = %#v", parsed.Input)
	}
	bm25Doc := lexical.NewBM25Document(document.SearchDocumentID, lexicalTokensFromDocument(document))
	stats := lexical.BM25StatsFromDocuments([]lexical.BM25Document{bm25Doc})
	score := lexical.BM25Score([]string{"satellite", "reliability"}, bm25Doc, stats, lexical.DefaultBM25Parameters())
	if score <= 0 {
		t.Fatalf("BM25 score = %.4f, want markdown body terms to score", score)
	}
}

func TestNotesSearchAcceptancePlainTextBody(t *testing.T) {
	service := NewService(nil)
	object := KnowledgeObject{
		KnowledgeObjectID: ids.NewKnowledgeObjectID(),
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RelativePath:      "plain/incident-log.txt",
		SourcePath:        "/srv/loom-box/Notes/plain/incident-log.txt",
		Title:             "incident-log.txt",
		FileClass:         storagecatalog.FileClassText,
		ProcessingState:   ProcessingStateMetadataOnly,
	}
	extraction, err := service.BuildTextPipelineExtraction(object, "Incident timeline: credential rotation completed after alert.", ChunkerOptions{TargetCharacters: 200})
	if err != nil {
		t.Fatalf("BuildTextPipelineExtraction returned error: %v", err)
	}
	version := KnowledgeObjectVersion{KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID(), Metadata: textPipelineVersionMetadata(extraction)}
	chunk := KnowledgeChunk{
		KnowledgeChunkID: ids.NewKnowledgeChunkID(),
		ChunkText:        extraction.Chunks[0].Text,
	}
	document, err := lexical.BuildLexicalDocument(knowledgeChunkLexicalDocumentInput("search_document_text", object, version, chunk))
	if err != nil {
		t.Fatalf("BuildLexicalDocument returned error: %v", err)
	}

	if !hasLexicalTerm(document.Terms, lexical.LexicalFieldBody, "credential") ||
		!hasLexicalTerm(document.Terms, lexical.LexicalFieldPath, "incident") {
		t.Fatalf("plain text lexical terms = %#v", document.Terms)
	}
}

func TestNotesSearchAcceptanceExtractedFileFamilies(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		fileClass  string
		extract    func(t *testing.T, object KnowledgeObject) ExtractionResult
		textSource string
		terms      []struct {
			field string
			term  string
		}
	}{
		{
			name:      "json",
			path:      "structured/case.json",
			fileClass: storagecatalog.FileClassText,
			extract: func(t *testing.T, object KnowledgeObject) ExtractionResult {
				t.Helper()
				extraction, err := structuredDataExtractor{}.Extract(ExtractionInput{
					Object:  object,
					Content: `{"case":"alpha beacon","nested":{"signal":"delta marker"}}`,
					Chunker: ChunkerOptions{TargetCharacters: 200},
				})
				if err != nil {
					t.Fatalf("structured JSON extraction failed: %v", err)
				}
				return extraction
			},
			textSource: TextSourceStructuredText,
			terms: []struct {
				field string
				term  string
			}{
				{lexical.LexicalFieldBody, "alpha"},
				{lexical.LexicalFieldBody, "delta"},
			},
		},
		{
			name:      "yaml",
			path:      "structured/case.yaml",
			fileClass: storagecatalog.FileClassText,
			extract: func(t *testing.T, object KnowledgeObject) ExtractionResult {
				t.Helper()
				extraction, err := structuredDataExtractor{}.Extract(ExtractionInput{
					Object:  object,
					Content: "case: beta relay\nnested:\n  signal: charlie marker\n",
					Chunker: ChunkerOptions{TargetCharacters: 200},
				})
				if err != nil {
					t.Fatalf("structured YAML extraction failed: %v", err)
				}
				return extraction
			},
			textSource: TextSourceStructuredText,
			terms: []struct {
				field string
				term  string
			}{
				{lexical.LexicalFieldBody, "beta"},
				{lexical.LexicalFieldBody, "charlie"},
			},
		},
		{
			name:      "python",
			path:      "code/osint_tools.py",
			fileClass: storagecatalog.FileClassCode,
			extract: func(t *testing.T, object KnowledgeObject) ExtractionResult {
				t.Helper()
				extraction, err := codeExtractor{}.Extract(ExtractionInput{
					Object:  object,
					Content: "def collect_indicator():\n    return 'needle phrase'\n",
					Chunker: ChunkerOptions{TargetCharacters: 200},
				})
				if err != nil {
					t.Fatalf("code extraction failed: %v", err)
				}
				return extraction
			},
			textSource: TextSourceEmbeddedText,
			terms: []struct {
				field string
				term  string
			}{
				{lexical.LexicalFieldBody, "collect"},
				{lexical.LexicalFieldBody, "needle"},
			},
		},
		{
			name:      "pdf embedded text",
			path:      "pdf/research.pdf",
			fileClass: storagecatalog.FileClassPDF,
			extract: func(t *testing.T, object KnowledgeObject) ExtractionResult {
				t.Helper()
				object.SourcePath = "/tmp/research.pdf"
				runner := fakeCommandRunner{outputs: map[string][]byte{
					"pdfinfo -rawdates /tmp/research.pdf":              []byte("Title: Evidence PDF\nPages: 1\nEncrypted: no\n"),
					"pdfimages -list /tmp/research.pdf":                []byte("page num type\n"),
					"pdftotext -layout -enc UTF-8 /tmp/research.pdf -": []byte("embedded horizon phrase\n"),
				}}
				extraction, err := newPDFExtractor(&runner).Extract(ExtractionInput{
					Object:  object,
					Chunker: ChunkerOptions{TargetCharacters: 200},
				})
				if err != nil {
					t.Fatalf("PDF extraction failed: %v", err)
				}
				return extraction
			},
			textSource: TextSourceEmbeddedText,
			terms: []struct {
				field string
				term  string
			}{
				{lexical.LexicalFieldBody, "embedded"},
				{lexical.LexicalFieldBody, "horizon"},
			},
		},
		{
			name:      "docx",
			path:      "office/brief.docx",
			fileClass: storagecatalog.FileClassOfficeDocument,
			extract: func(t *testing.T, object KnowledgeObject) ExtractionResult {
				t.Helper()
				object.SourcePath = writeTestDOCX(t, map[string]string{
					"word/document.xml": `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Operations Brief</w:t></w:r></w:p>
    <w:p><w:r><w:t>docx atlas phrase</w:t></w:r></w:p>
  </w:body>
</w:document>`,
				})
				extraction, err := docxExtractor{}.Extract(ExtractionInput{
					Object:  object,
					Chunker: ChunkerOptions{TargetCharacters: 200},
				})
				if err != nil {
					t.Fatalf("DOCX extraction failed: %v", err)
				}
				return extraction
			},
			textSource: TextSourceEmbeddedText,
			terms: []struct {
				field string
				term  string
			}{
				{lexical.LexicalFieldHeading, "operations"},
				{lexical.LexicalFieldBody, "atlas"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			object := acceptanceKnowledgeObject(tc.path, tc.fileClass, "")
			extraction := tc.extract(t, object)
			if extraction.Status != ExtractionStatusExtracted {
				t.Fatalf("status = %q, want extracted", extraction.Status)
			}
			document, metadata := lexicalDocumentFromExtraction(t, object, extraction)
			for _, want := range tc.terms {
				if !hasLexicalTerm(document.Terms, want.field, want.term) {
					t.Fatalf("%s lexical terms missing %s:%s in %#v", tc.name, want.field, want.term, document.Terms)
				}
			}
			if metadata["text_source"] != tc.textSource || metadata["extraction_status"] != ExtractionStatusExtracted {
				t.Fatalf("metadata = %#v, want text_source=%s extraction_status=extracted", metadata, tc.textSource)
			}
		})
	}
}

func TestNotesSearchAcceptanceMetadataOnlyPDFAndImages(t *testing.T) {
	cases := []KnowledgeObject{
		metadataOnlyObject("dossiers/network-map.pdf", storagecatalog.FileClassPDF, "application/pdf"),
		metadataOnlyObject("captures/antenna.png", storagecatalog.FileClassImage, "image/png"),
		metadataOnlyObject("captures/license-plate.jpeg", storagecatalog.FileClassImage, "image/jpeg"),
	}
	for _, object := range cases {
		t.Run(object.RelativePath, func(t *testing.T) {
			if !shouldIndexKnowledgeObjectMetadata(object) {
				t.Fatalf("expected metadata-only indexing for %#v", object)
			}
			document, err := lexical.BuildLexicalDocument(knowledgeMetadataLexicalDocumentInput("search_document_"+strings.ReplaceAll(object.FileClass+"_"+object.MimeType, "/", "_"), object))
			if err != nil {
				t.Fatalf("BuildLexicalDocument returned error: %v", err)
			}
			if document.FieldLengths[lexical.LexicalFieldHeading] != 0 || document.FieldLengths[lexical.LexicalFieldTag] != 0 {
				t.Fatalf("metadata-only document should not claim heading/tag extraction: %#v", document.FieldLengths)
			}
			if !hasLexicalTerm(document.Terms, lexical.LexicalFieldPath, strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(object.FileClass, "image"), "pdf"), "_")) &&
				!hasLexicalTerm(document.Terms, lexical.LexicalFieldBody, object.FileClass) {
				t.Fatalf("metadata-only terms missing file class/path signal: %#v", document.Terms)
			}
			var metadata map[string]any
			if err := json.Unmarshal(document.Metadata, &metadata); err != nil {
				t.Fatalf("metadata is invalid JSON: %v", err)
			}
			if metadata["metadata_only"] != true {
				t.Fatalf("metadata = %#v, want metadata_only true", metadata)
			}
		})
	}
}

func TestNotesSearchAcceptanceMetadataOnlyExtractionStatuses(t *testing.T) {
	gdocPath := filepath.Join(t.TempDir(), "Cloud Case.gdoc")
	if err := os.WriteFile(gdocPath, []byte(`{"url":"https://docs.google.com/document/d/doc_acceptance/edit","title":"Cloud Case","email":"agent@example.com"}`), 0o644); err != nil {
		t.Fatalf("write gdoc: %v", err)
	}

	cases := []struct {
		name       string
		object     KnowledgeObject
		extraction ExtractionResult
		wantTerms  []string
	}{
		{
			name:       "too large",
			object:     acceptanceKnowledgeObject("large/oversized.md", storagecatalog.FileClassMarkdown, "text/markdown"),
			extraction: metadataOnlyExtraction(acceptanceKnowledgeObject("large/oversized.md", storagecatalog.FileClassMarkdown, "text/markdown"), ExtractionStatusTooLarge, "source file size exceeds max bytes"),
			wantTerms:  []string{"oversized", "large"},
		},
		{
			name:   "password required",
			object: acceptanceKnowledgeObject("pdf/locked.pdf", storagecatalog.FileClassPDF, "application/pdf"),
			extraction: func() ExtractionResult {
				object := acceptanceKnowledgeObject("pdf/locked.pdf", storagecatalog.FileClassPDF, "application/pdf")
				object.SourcePath = "/tmp/locked-acceptance.pdf"
				runner := fakeCommandRunner{
					outputs: map[string][]byte{"pdfinfo -rawdates /tmp/locked-acceptance.pdf": []byte("Command Line Error: Incorrect password\n")},
					errors:  map[string]error{"pdfinfo -rawdates /tmp/locked-acceptance.pdf": errAcceptancePDFPassword{}},
				}
				extraction, err := newPDFExtractor(&runner).Extract(ExtractionInput{Object: object})
				if err != nil {
					t.Fatalf("password PDF extraction failed: %v", err)
				}
				return extraction
			}(),
			wantTerms: []string{"locked", "password"},
		},
		{
			name:   "ocr deferred",
			object: acceptanceKnowledgeObject("pdf/scanned.pdf", storagecatalog.FileClassPDF, "application/pdf"),
			extraction: func() ExtractionResult {
				object := acceptanceKnowledgeObject("pdf/scanned.pdf", storagecatalog.FileClassPDF, "application/pdf")
				object.SourcePath = "/tmp/scanned-acceptance.pdf"
				runner := fakeCommandRunner{outputs: map[string][]byte{
					"pdfinfo -rawdates /tmp/scanned-acceptance.pdf":              []byte("Pages: 1\nEncrypted: no\n"),
					"pdfimages -list /tmp/scanned-acceptance.pdf":                []byte("page num type\n1 0 image\n"),
					"pdftotext -layout -enc UTF-8 /tmp/scanned-acceptance.pdf -": []byte("   \n"),
				}}
				extraction, err := newPDFExtractor(&runner).Extract(ExtractionInput{Object: object})
				if err != nil {
					t.Fatalf("scanned PDF extraction failed: %v", err)
				}
				return extraction
			}(),
			wantTerms: []string{"scanned", "ocr", "deferred"},
		},
		{
			name: "gdoc pointer",
			object: func() KnowledgeObject {
				object := acceptanceKnowledgeObject("office/Cloud Case.gdoc", storagecatalog.FileClassOfficeDocument, "application/vnd.google-apps.document")
				object.SourcePath = gdocPath
				return object
			}(),
			extraction: func() ExtractionResult {
				object := acceptanceKnowledgeObject("office/Cloud Case.gdoc", storagecatalog.FileClassOfficeDocument, "application/vnd.google-apps.document")
				object.SourcePath = gdocPath
				extraction, err := officeMetadataExtractor{}.Extract(ExtractionInput{Object: object, MaxSourceBytes: 5 * 1024 * 1024})
				if err != nil {
					t.Fatalf("gdoc extraction failed: %v", err)
				}
				return extraction
			}(),
			wantTerms: []string{"cloud", "doc", "acceptance"},
		},
		{
			name:       "image",
			object:     acceptanceKnowledgeObject("captures/antenna.png", storagecatalog.FileClassImage, "image/png"),
			extraction: metadataOnlyExtraction(acceptanceKnowledgeObject("captures/antenna.png", storagecatalog.FileClassImage, "image/png"), ExtractionStatusMetadataOnly, "image metadata only"),
			wantTerms:  []string{"antenna", "image"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			object := withAcceptanceExtractionMetadata(tc.object, tc.extraction)
			if !shouldIndexKnowledgeObjectMetadata(object) {
				t.Fatalf("expected metadata-only object to be indexed: %#v", object)
			}
			document, err := lexical.BuildLexicalDocument(knowledgeMetadataLexicalDocumentInput("search_document_metadata_"+strings.ReplaceAll(tc.name, " ", "_"), object))
			if err != nil {
				t.Fatalf("BuildLexicalDocument returned error: %v", err)
			}
			metadata := jsonMap(t, document.Metadata)
			if metadata["metadata_only"] != true || metadata["text_source"] != TextSourceMetadataText || metadata["extraction_status"] != tc.extraction.Status {
				t.Fatalf("metadata = %#v, want metadata-only status %s", metadata, tc.extraction.Status)
			}
			for _, term := range tc.wantTerms {
				if !hasLexicalTerm(document.Terms, lexical.LexicalFieldBody, term) && !hasLexicalTerm(document.Terms, lexical.LexicalFieldPath, term) {
					t.Fatalf("%s metadata terms missing %q in %#v", tc.name, term, document.Terms)
				}
			}
		})
	}
}

func TestNotesSearchAcceptanceGroupingKeepsFileLevelResult(t *testing.T) {
	results := groupNotesSearchResults([]NotesSearchResult{
		{SearchDocumentID: "search_document_chunk_1", KnowledgeObjectID: "knowledge_object_report", RelativePath: "reports/a.md", FinalScore: 8},
		{SearchDocumentID: "search_document_chunk_2", KnowledgeObjectID: "knowledge_object_report", RelativePath: "reports/a.md", FinalScore: 7},
		{SearchDocumentID: "search_document_pdf", KnowledgeObjectID: "knowledge_object_pdf", SourceKind: KnowledgeMetadataSearchSourceKind, RelativePath: "captures/map.pdf", FinalScore: 5},
	}, 10)

	if len(results) != 2 {
		t.Fatalf("grouped len = %d, want file-level results for markdown and pdf", len(results))
	}
	if results[0].SearchDocumentID != "search_document_chunk_1" || results[1].SourceKind != KnowledgeMetadataSearchSourceKind {
		t.Fatalf("grouped results = %#v", results)
	}
}

func acceptanceKnowledgeObject(relativePath string, fileClass string, mimeType string) KnowledgeObject {
	return KnowledgeObject{
		KnowledgeObjectID: ids.NewKnowledgeObjectID(),
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RelativePath:      relativePath,
		SourcePath:        "/srv/loom-box/Notes/" + relativePath,
		Title:             relativePath,
		FileClass:         fileClass,
		MimeType:          mimeType,
		ProcessingState:   ProcessingStateMetadataOnly,
	}
}

func lexicalDocumentFromExtraction(t *testing.T, object KnowledgeObject, extraction ExtractionResult) (lexical.LexicalDocument, map[string]any) {
	t.Helper()
	if len(extraction.Chunks) == 0 {
		t.Fatalf("extraction has no chunks: %#v", extraction)
	}
	version := KnowledgeObjectVersion{
		KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID(),
		Metadata:                 extractionPipelineVersionMetadata(extraction),
	}
	input := extraction.Chunks[0]
	chunk := KnowledgeChunk{
		KnowledgeChunkID: ids.NewKnowledgeChunkID(),
		ChunkIndex:       input.Index,
		ChunkText:        input.Text,
		StructuralPath:   input.StructuralPath,
		Metadata:         chunkMetadata(input),
	}
	document, err := lexical.BuildLexicalDocument(knowledgeChunkLexicalDocumentInput("search_document_"+strings.ReplaceAll(object.RelativePath, "/", "_"), object, version, chunk))
	if err != nil {
		t.Fatalf("BuildLexicalDocument returned error: %v", err)
	}
	return document, jsonMap(t, document.Metadata)
}

func withAcceptanceExtractionMetadata(object KnowledgeObject, extraction ExtractionResult) KnowledgeObject {
	version := KnowledgeObjectVersion{KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID()}
	object.Metadata = extractionPipelineObjectMetadata(object.Metadata, version, extraction)
	object.ProcessingState = processingStateForExtraction(extraction, len(extraction.Chunks))
	object.PipelineKey = KnowledgeObjectPipelineNotesFileExtraction
	object.PipelineVersion = KnowledgeFileExtractionPipelineVersion
	return object
}

func jsonMap(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatalf("metadata is invalid JSON: %v", err)
	}
	return metadata
}

type errAcceptancePDFPassword struct{}

func (errAcceptancePDFPassword) Error() string {
	return "exit status 1"
}

func metadataOnlyObject(relativePath, fileClass, mimeType string) KnowledgeObject {
	return KnowledgeObject{
		KnowledgeObjectID: ids.NewKnowledgeObjectID(),
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RelativePath:      relativePath,
		SourcePath:        "/srv/loom-box/Notes/" + relativePath,
		Title:             relativePath,
		FileClass:         fileClass,
		MimeType:          mimeType,
		ProcessingState:   ProcessingStateMetadataOnly,
		PipelineKey:       KnowledgeObjectPipelineMetadata,
	}
}

func lexicalTokensFromDocument(document lexical.LexicalDocument) []string {
	tokens := []string{}
	for _, term := range document.Terms {
		for i := 0; i < term.TermFrequency; i++ {
			tokens = append(tokens, term.Term)
		}
	}
	return tokens
}
