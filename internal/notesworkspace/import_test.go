package notesworkspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/objectstore"
)

func TestImportCreatesBinaryWithoutInlineSnapshotAndReplaysAfterMove(t *testing.T) {
	s, m, r := fixture(t)
	s.Blobs = objectstore.New(t.TempDir())
	payload := []byte{0, 1, 2, 3, 255}
	source := filepath.Join(t.TempDir(), "source.bin")
	if e := os.WriteFile(source, payload, 0600); e != nil {
		t.Fatal(e)
	}
	blob, e := s.Blobs.PutFile(t.Context(), source)
	if e != nil {
		t.Fatal(e)
	}
	generation, pin, e := s.ImportDestination(t.Context(), "collection", "data.bin")
	if e != nil {
		t.Fatal(e)
	}
	op, e := s.CreateImport(t.Context(), "collection", "data.bin", "upload", generation, pin, blob)
	if e != nil {
		t.Fatal(e)
	}
	if op.State != Accepted || len(op.Request.Content) != 0 || len(op.Base.Content) != 0 {
		t.Fatalf("unexpected receipt %+v", op)
	}
	base, e := m.Base(t.Context(), op.ResultBaseID)
	if e != nil || len(base.Content) != 0 || base.MetadataAction == nil {
		t.Fatalf("not a payload-free base %+v %v", base, e)
	}
	if e = os.Rename(filepath.Join(r.root, "data.bin"), filepath.Join(r.root, "moved.bin")); e != nil {
		t.Fatal(e)
	}
	again, e := s.CreateImport(t.Context(), "collection", "data.bin", "upload", generation, pin, blob)
	if e != nil || again.ResultBaseID != op.ResultBaseID {
		t.Fatalf("receipt replay %+v %v", again, e)
	}
	if _, e = os.Stat(filepath.Join(r.root, "data.bin")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("receipt recreated source")
	}
	if _, e = s.CreateImport(t.Context(), "collection", "other.bin", "upload", generation, pin, blob); !errors.Is(e, ErrIntentMismatch) {
		t.Fatalf("changed intent: %v", e)
	}
}

func TestImportRecoversPublicationBeforeReceiptAndRefusesOverwrite(t *testing.T) {
	s, m, r := fixture(t)
	s.Blobs = objectstore.New(t.TempDir())
	source := filepath.Join(t.TempDir(), "source")
	if e := os.WriteFile(source, []byte("content"), 0600); e != nil {
		t.Fatal(e)
	}
	blob, e := s.Blobs.PutFile(t.Context(), source)
	if e != nil {
		t.Fatal(e)
	}
	generation, pin, e := s.ImportDestination(t.Context(), "collection", "new.dat")
	if e != nil {
		t.Fatal(e)
	}
	m.beforeCommitPath = func(Operation) error { return errors.New("interrupted commit") }
	if _, e = s.CreateImport(t.Context(), "collection", "new.dat", "resume", generation, pin, blob); e == nil {
		t.Fatal("expected interruption")
	}
	m.beforeCommitPath = nil
	op, e := s.CreateImport(t.Context(), "collection", "new.dat", "resume", generation, pin, blob)
	if e != nil || op.State != Accepted {
		t.Fatalf("resume %+v %v", op, e)
	}
	if _, _, e = s.ImportDestination(t.Context(), "collection", "new.dat"); !errors.Is(e, os.ErrExist) {
		t.Fatalf("collision %v", e)
	}
	if e = os.WriteFile(filepath.Join(r.root, "other.dat"), []byte("do not replace"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreateImport(t.Context(), "collection", "other.dat", "collision", generation, pin, blob); !errors.Is(e, os.ErrExist) {
		t.Fatalf("collision %v", e)
	}
}

func TestUploadIntentRejectsUnsafeIdentityAndDestination(t *testing.T) {
	v := UploadIntent{ID: "desktop_upload_ok", Root: "box", Path: "Documents/file.pdf", Hash: digest(nil), Collision: "skip"}
	if e := ValidateUploadIntent(v); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{"../escape", "Documents/.secret", "/absolute"} {
		changed := v
		changed.Path = p
		if ValidateUploadIntent(changed) == nil {
			t.Fatalf("accepted %s", p)
		}
	}
	v.ID = "desktop_upload_../escape"
	if ValidateUploadIntent(v) == nil {
		t.Fatal("unsafe identity")
	}
}

func TestImportDoesNotAcceptAnIndependentStagedWrite(t *testing.T) {
	s, m, r := fixture(t)
	s.Blobs = objectstore.New(t.TempDir())
	source := filepath.Join(t.TempDir(), "source")
	if e := os.WriteFile(source, []byte("original"), 0600); e != nil {
		t.Fatal(e)
	}
	blob, e := s.Blobs.PutFile(t.Context(), source)
	if e != nil {
		t.Fatal(e)
	}
	generation, pin, e := s.ImportDestination(t.Context(), "collection", "new.bin")
	if e != nil {
		t.Fatal(e)
	}
	m.beforeSave = func(op Operation) error {
		if op.PathStage == "import_prepared" {
			return os.WriteFile(filepath.Join(r.root, op.Journal, "imported"), []byte("modified"), 0600)
		}
		return nil
	}
	op, e := s.CreateImport(t.Context(), "collection", "new.bin", "modified-upload", generation, pin, blob)
	if e == nil || op.State == Accepted {
		t.Fatalf("modified bytes accepted %+v %v", op, e)
	}
	if body, e := os.ReadFile(filepath.Join(r.root, "new.bin")); e != nil || string(body) != "modified" {
		t.Fatalf("independent bytes not preserved %q %v", body, e)
	}
	if op.State != Conflict {
		t.Fatalf("changed bytes did not release the failed publication reservation: %+v", op)
	}
	if _, e = s.ReadMetadataAction(t.Context(), "collection", "new.bin"); e != nil {
		t.Fatalf("current source remains blocked after upload conflict: %v", e)
	}
}
