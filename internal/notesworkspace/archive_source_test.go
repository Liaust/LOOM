package notesworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/knowledge"
)

type archiveReferenceReader struct {
	body       []byte
	generation string
	changed    bool
}

func (r *archiveReferenceReader) WithFile(_ context.Context, _, _, generation string, limit int64, fn func(knowledge.WorkspaceArchiveSource, knowledge.WorkspaceArchiveFile, io.Reader) error) error {
	if generation != r.generation || int64(len(r.body)) > limit {
		return ErrMembership
	}
	err := fn(knowledge.WorkspaceArchiveSource{Generation: r.generation}, knowledge.WorkspaceArchiveFile{SourceHash: digest(r.body), SizeBytes: int64(len(r.body))}, bytes.NewReader(r.body))
	if err == nil && r.changed {
		return ErrMembership
	}
	return err
}

func TestArchiveReferenceSnapshotPreservesLegacyBasesAndCustodyChecks(t *testing.T) {
	store := newMemoryStore()
	reader := &archiveReferenceReader{body: []byte("%PDF-immutable\x00bytes"), generation: "archive-1"}
	a := ArchiveReader{Reader: reader, Store: store}
	legacy, err := a.Read(t.Context(), "collection", "small.pdf", reader.generation)
	if err != nil {
		t.Fatal(err)
	}
	legacyRaw, _ := json.Marshal(legacy.Base)
	spool := filepath.Join(t.TempDir(), "spool")
	for _, generation := range []string{"archive-1", "archive-2"} {
		reader.generation = generation
		read, snapshot, err := a.ReadReference(t.Context(), "collection", "small.pdf", generation, spool)
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(snapshot)
		os.Remove(snapshot)
		if err != nil || !bytes.Equal(body, reader.body) || len(read.Base.Content) != 0 || read.Base.ReferenceSize != int64(len(body)) {
			t.Fatal("archive reference retained bytes in the database", read, err)
		}
		if err := a.WithExport(t.Context(), read.File.ID, read.Base.ID, generation, func(File, Base) error { return nil }); err != nil {
			t.Fatal("metadata-only archive base could not export", err)
		}
		reader.body = append(reader.body, 'x')
		if err := a.WithExport(t.Context(), read.File.ID, read.Base.ID, generation, func(File, Base) error { return nil }); err == nil {
			t.Fatal("changed archive admitted")
		}
		reader.body = reader.body[:len(reader.body)-1]
	}
	reader.generation = "archive-1"
	if err := a.WithExport(t.Context(), legacy.File.ID, legacy.Base.ID, reader.generation, func(File, Base) error { return nil }); err != nil {
		t.Fatal("legacy inline archive base rejected", err)
	}
	current, _ := store.Base(t.Context(), legacy.Base.ID)
	raw, _ := json.Marshal(current)
	if !bytes.Equal(raw, legacyRaw) {
		t.Fatal("legacy base was rewritten")
	}
	reader.changed = true
	if _, snapshot, err := a.ReadReference(t.Context(), "collection", "small.pdf", reader.generation, spool); !errors.Is(err, ErrMembership) || snapshot != "" {
		t.Fatal("post-read custody change retained a snapshot", snapshot, err)
	}
	entries, _ := os.ReadDir(spool)
	if len(entries) != 0 {
		t.Fatal("failed snapshot leaked", entries)
	}
}
