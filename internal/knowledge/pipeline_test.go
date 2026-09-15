package knowledge

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
)

func TestBuildTextPipelineExtractionForMarkdown(t *testing.T) {
	service := NewService(nil)
	object := KnowledgeObject{
		KnowledgeObjectID: ids.NewKnowledgeObjectID(),
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RelativePath:      "daily.md",
		FileClass:         storagecatalog.FileClassMarkdown,
		ProcessingState:   ProcessingStateMetadataOnly,
	}

	extraction, err := service.BuildTextPipelineExtraction(object, "# Daily\n\nSee [[Project]].", ChunkerOptions{TargetCharacters: 100})
	if err != nil {
		t.Fatalf("BuildTextPipelineExtraction returned error: %v", err)
	}
	if len(extraction.Document.Headings) != 1 || len(extraction.Document.Links) != 1 {
		t.Fatalf("extraction headings/links = %d/%d, want 1/1", len(extraction.Document.Headings), len(extraction.Document.Links))
	}
	if len(extraction.Chunks) != 1 {
		t.Fatalf("chunks len = %d, want 1", len(extraction.Chunks))
	}
	if extraction.Extraction.Status != ExtractionStatusExtracted {
		t.Fatalf("extraction status = %q, want extracted", extraction.Extraction.Status)
	}
	if extraction.Chunks[0].TextSource != TextSourceEmbeddedText {
		t.Fatalf("chunk text source = %q, want embedded_text", extraction.Chunks[0].TextSource)
	}
}

func TestBuildTextPipelineExtractionRejectsUnsupportedFileClass(t *testing.T) {
	service := NewService(nil)
	object := KnowledgeObject{
		KnowledgeObjectID: ids.NewKnowledgeObjectID(),
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RelativePath:      "image.png",
		FileClass:         storagecatalog.FileClassImage,
		ProcessingState:   ProcessingStateMetadataOnly,
	}

	_, err := service.BuildTextPipelineExtraction(object, "not text", ChunkerOptions{})
	if err == nil {
		t.Fatal("BuildTextPipelineExtraction returned nil error for image object")
	}
}

func TestExtractionMetadataOnlyForReadError(t *testing.T) {
	object := KnowledgeObject{
		KnowledgeObjectID: ids.NewKnowledgeObjectID(),
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RelativePath:      "missing.md",
		FileClass:         storagecatalog.FileClassMarkdown,
		ProcessingState:   ProcessingStateMetadataOnly,
	}

	extraction, ok := extractionMetadataOnlyForReadError(object, errSourcePathRequiredForTest())
	if !ok {
		t.Fatal("expected source-path error to become metadata-only")
	}
	if extraction.Status != ExtractionStatusSourceUnavailable {
		t.Fatalf("status = %q, want source_unavailable", extraction.Status)
	}
}

func errSourcePathRequiredForTest() error {
	_, err := readTextSourceFile("", 1024)
	return err
}

func TestReadTextBackupArtifactReadsContentMember(t *testing.T) {
	path := writeBackupArtifactForTest(t, "content", []byte("line one\r\nline two\n"))

	content, err := readTextBackupArtifact(path, "content", 1024)
	if err != nil {
		t.Fatalf("readTextBackupArtifact returned error: %v", err)
	}
	if content != "line one\nline two\n" {
		t.Fatalf("content = %q", content)
	}
}

func TestReadTextBackupArtifactHonorsMaxBytes(t *testing.T) {
	path := writeBackupArtifactForTest(t, "content", []byte("too large"))

	_, err := readTextBackupArtifact(path, "content", 3)
	if err == nil || !strings.Contains(err.Error(), "exceeds max_bytes") {
		t.Fatalf("error = %v, want max_bytes failure", err)
	}
}

func TestReadTextPhysicalRefReadsLocalPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("line one\r\nline two\n"), 0o600); err != nil {
		t.Fatalf("write local ref: %v", err)
	}
	ref := storagecatalog.PhysicalRef{
		RefKind: storagecatalog.PhysicalRefKindLocalPath,
		URI:     path,
		Status:  storagecatalog.PhysicalRefStatusAvailable,
	}

	content, err := readTextPhysicalRef(ref, 1024)
	if err != nil {
		t.Fatalf("readTextPhysicalRef returned error: %v", err)
	}
	if content != "line one\nline two\n" {
		t.Fatalf("content = %q", content)
	}
}

func TestReadTextPhysicalRefHonorsMaxBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "too-large.txt")
	if err := os.WriteFile(path, []byte("too large"), 0o600); err != nil {
		t.Fatalf("write local ref: %v", err)
	}
	ref := storagecatalog.PhysicalRef{
		RefKind: storagecatalog.PhysicalRefKindLocalPath,
		URI:     path,
		Status:  storagecatalog.PhysicalRefStatusAvailable,
	}

	_, err := readTextPhysicalRef(ref, 3)
	if err == nil || !strings.Contains(err.Error(), "exceeds max_bytes") {
		t.Fatalf("error = %v, want max_bytes failure", err)
	}
}

func TestPrepareLocalPhysicalRefForExtraction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4"), 0o600); err != nil {
		t.Fatalf("write local ref: %v", err)
	}
	object := KnowledgeObject{
		KnowledgeObjectID: ids.NewKnowledgeObjectID(),
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RelativePath:      "source.pdf",
		FileClass:         storagecatalog.FileClassPDF,
		ProcessingState:   ProcessingStateMetadataOnly,
	}
	ref := storagecatalog.PhysicalRef{
		RefKind: storagecatalog.PhysicalRefKindLocalPath,
		URI:     path,
		Status:  storagecatalog.PhysicalRefStatusAvailable,
	}

	prepared, cleanup, err := prepareLocalPhysicalRefForExtraction(object, ref, 1024)
	defer cleanup()
	if err != nil {
		t.Fatalf("prepareLocalPhysicalRefForExtraction returned error: %v", err)
	}
	if prepared.SourcePath != path {
		t.Fatalf("source path = %q, want %q", prepared.SourcePath, path)
	}
}

func writeBackupArtifactForTest(t *testing.T, member string, payload []byte) string {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(`{}`))}); err != nil {
		t.Fatalf("write manifest header: %v", err)
	}
	if _, err := writer.Write([]byte(`{}`)); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := writer.WriteHeader(&tar.Header{Name: member, Mode: 0o600, Size: int64(len(payload))}); err != nil {
		t.Fatalf("write content header: %v", err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	path := filepath.Join(t.TempDir(), "payload.tar")
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	return path
}
