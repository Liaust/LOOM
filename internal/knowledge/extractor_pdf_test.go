package knowledge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

func TestPDFExtractorExtractsMetadataAndEmbeddedText(t *testing.T) {
	object := testKnowledgeObjectForExtraction(storagecatalog.FileClassPDF, "paper.pdf")
	object.SourcePath = "/tmp/paper.pdf"
	runner := fakeCommandRunner{
		outputs: map[string][]byte{
			"pdfinfo -rawdates /tmp/paper.pdf":              []byte("Title: Research Paper\nAuthor: Ada\nPages: 2\nEncrypted: no\nProducer: test\nCreationDate: D:20200102030405Z\nModDate: D:20210203060506+02'00'\n"),
			"pdfimages -list /tmp/paper.pdf":                []byte("page num type width height color comp bpc enc interp object ID x-ppi y-ppi size ratio\n1 0 image 10 10 rgb 3 8 image no 1 0 72 72 1K 10%\n"),
			"pdftotext -layout -enc UTF-8 /tmp/paper.pdf -": []byte("network evidence\n\nsecond page phrase\n"),
		},
	}

	extraction, err := newPDFExtractor(&runner).Extract(ExtractionInput{
		Object:  object,
		Chunker: ChunkerOptions{TargetCharacters: 100},
	})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if extraction.Status != ExtractionStatusExtracted {
		t.Fatalf("status = %q, want extracted", extraction.Status)
	}
	if extraction.Metadata["page_count"] != 2 || extraction.Metadata["title"] != "Research Paper" || extraction.Metadata["image_count"] != 1 {
		t.Fatalf("metadata = %#v", extraction.Metadata)
	}
	if len(extraction.Chunks) == 0 || extraction.Chunks[0].TextSource != TextSourceEmbeddedText {
		t.Fatalf("chunks = %#v, want embedded text chunks", extraction.Chunks)
	}
	if !strings.Contains(extraction.Document.Text, "second page phrase") {
		t.Fatalf("document text = %q", extraction.Document.Text)
	}
	if len(extraction.AbsoluteTimeCandidates) != 2 || extraction.AbsoluteTimeCandidates[0].Timestamp == nil ||
		extraction.AbsoluteTimeCandidates[0].Timestamp.Format(time.RFC3339) != "2021-02-03T04:05:06Z" {
		t.Fatalf("absolute time candidates = %#v", extraction.AbsoluteTimeCandidates)
	}
	if !runner.called("pdfinfo -rawdates /tmp/paper.pdf") || runner.called("pdfinfo /tmp/paper.pdf") {
		t.Fatalf("pdfinfo calls = %#v, want the raw-date command contract", runner.calls)
	}
}

func TestPDFExtractorPreservesMalformedDateWarning(t *testing.T) {
	object := testKnowledgeObjectForExtraction(storagecatalog.FileClassPDF, "malformed-date.pdf")
	object.SourcePath = "/tmp/malformed-date.pdf"
	runner := fakeCommandRunner{outputs: map[string][]byte{
		"pdfinfo -rawdates /tmp/malformed-date.pdf":              []byte("Pages: 1\nEncrypted: no\nModDate: D:20261399999999Z\n"),
		"pdfimages -list /tmp/malformed-date.pdf":                []byte("page num type\n"),
		"pdftotext -layout -enc UTF-8 /tmp/malformed-date.pdf -": []byte("body\n"),
	}}

	extraction, err := newPDFExtractor(&runner).Extract(ExtractionInput{Object: object})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if extraction.Status != ExtractionStatusExtracted || len(extraction.AbsoluteTimeWarnings) != 1 {
		t.Fatalf("extraction = %#v, want successful extraction with timestamp warning", extraction)
	}
	if extraction.AbsoluteTimeWarnings[0].RawValue != "D:20261399999999Z" || extraction.Metadata["moddate"] != "D:20261399999999Z" {
		t.Fatalf("raw malformed PDF date was not retained: %#v", extraction)
	}
	versionMetadata := extractionPipelineVersionMetadata(extraction)
	if !strings.Contains(string(versionMetadata), "D:20261399999999Z") || !strings.Contains(string(versionMetadata), AbsoluteTimeWarningInvalidTimestamp) {
		t.Fatalf("version metadata = %s, want raw PDF date and warning", versionMetadata)
	}
}

func TestParsePDFAbsoluteTimestampRequiresStandardPrefixAndTimezone(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want string
		ok   bool
	}{
		{raw: "D:20260705120000Z", want: "2026-07-05T12:00:00Z", ok: true},
		{raw: "D:20260705140000+02'00'", want: "2026-07-05T12:00:00Z", ok: true},
		{raw: "D:20260705120000", ok: false},
		{raw: "Mon Jul 5 12:00:00 2026", ok: false},
	} {
		got, ok := parsePDFAbsoluteTimestamp(test.raw)
		if ok != test.ok {
			t.Fatalf("parsePDFAbsoluteTimestamp(%q) ok = %v, want %v", test.raw, ok, test.ok)
		}
		if ok && got.Format(time.RFC3339) != test.want {
			t.Fatalf("parsePDFAbsoluteTimestamp(%q) = %s, want %s", test.raw, got, test.want)
		}
	}
}

func TestPDFExtractorPasswordRequiredIsMetadataOnly(t *testing.T) {
	object := testKnowledgeObjectForExtraction(storagecatalog.FileClassPDF, "locked.pdf")
	object.SourcePath = "/tmp/locked.pdf"
	runner := fakeCommandRunner{
		outputs: map[string][]byte{
			"pdfinfo -rawdates /tmp/locked.pdf": []byte("Command Line Error: Incorrect password\n"),
		},
		errors: map[string]error{
			"pdfinfo -rawdates /tmp/locked.pdf": errors.New("exit status 1"),
		},
	}

	extraction, err := newPDFExtractor(&runner).Extract(ExtractionInput{Object: object})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if extraction.Status != ExtractionStatusPasswordRequired || len(extraction.Chunks) != 0 {
		t.Fatalf("extraction = %#v, want password-required metadata-only", extraction)
	}
}

func TestPDFExtractorNoEmbeddedTextDefersOCR(t *testing.T) {
	object := testKnowledgeObjectForExtraction(storagecatalog.FileClassPDF, "scan.pdf")
	object.SourcePath = "/tmp/scan.pdf"
	runner := fakeCommandRunner{
		outputs: map[string][]byte{
			"pdfinfo -rawdates /tmp/scan.pdf":              []byte("Pages: 1\nEncrypted: no\n"),
			"pdfimages -list /tmp/scan.pdf":                []byte("page num type\n1 0 image\n"),
			"pdftotext -layout -enc UTF-8 /tmp/scan.pdf -": []byte("   \n"),
		},
	}

	extraction, err := newPDFExtractor(&runner).Extract(ExtractionInput{Object: object})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if extraction.Status != ExtractionStatusNoEmbeddedText {
		t.Fatalf("status = %q, want no_embedded_text", extraction.Status)
	}
	if extraction.Metadata["ocr_deferred"] != true || extraction.Metadata["no_embedded_text"] != true {
		t.Fatalf("metadata = %#v, want OCR deferred metadata", extraction.Metadata)
	}
}

func TestPDFExtractorPageLimitIsMetadataOnly(t *testing.T) {
	object := testKnowledgeObjectForExtraction(storagecatalog.FileClassPDF, "huge.pdf")
	object.SourcePath = "/tmp/huge.pdf"
	runner := fakeCommandRunner{
		outputs: map[string][]byte{
			"pdfinfo -rawdates /tmp/huge.pdf": []byte("Pages: 251\nEncrypted: no\n"),
		},
	}

	extraction, err := newPDFExtractor(&runner).Extract(ExtractionInput{Object: object})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if extraction.Status != ExtractionStatusTooLarge {
		t.Fatalf("status = %q, want too_large", extraction.Status)
	}
	if runner.called("pdftotext -layout -enc UTF-8 /tmp/huge.pdf -") {
		t.Fatal("pdftotext should not run after page limit is exceeded")
	}
}

type fakeCommandRunner struct {
	outputs map[string][]byte
	errors  map[string]error
	calls   []string
}

func (r *fakeCommandRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, key)
	if err := r.errors[key]; err != nil {
		return r.outputs[key], err
	}
	return r.outputs[key], nil
}

func (r fakeCommandRunner) called(key string) bool {
	for _, call := range r.calls {
		if call == key {
			return true
		}
	}
	return false
}
