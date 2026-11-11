package notesworkspacesync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/notesworkspace"
)

type referenceFileNative struct{ *fakeNative }

func (n referenceFileNative) Call(ctx context.Context, req map[string]any, out any) error {
	if req["method"] == "publish.reference.file" {
		body, err := os.ReadFile(req["snapshot"].(string))
		if err != nil {
			return err
		}
		if int64(len(body)) != req["size"].(int64) || digest(body)[7:] != req["sha256"] {
			return ErrHeld
		}
		copy := make(map[string]any, len(req))
		for k, v := range req {
			copy[k] = v
		}
		copy["method"], copy["contentBase64"] = "publish.reference", base64.StdEncoding.EncodeToString(body)
		return n.fakeNative.Call(ctx, copy, out)
	}
	return n.fakeNative.Call(ctx, req, out)
}

func TestExportSmallReferencesKeepGenerationBasesMetadataOnly(t *testing.T) {
	for _, path := range []string{"small.pdf", "small.png"} {
		t.Run(path, func(t *testing.T) {
			s, native, store, root, _ := fixture(t)
			s.Native = referenceFileNative{native}
			body := []byte("small binary\x00reference")
			if err := os.WriteFile(filepath.Join(root, path), body, 0600); err != nil {
				t.Fatal(err)
			}
			// Retained inline bases remain readable and byte-for-byte unchanged.
			legacy, err := s.Source.Read(t.Context(), "source", path)
			if err != nil {
				t.Fatal(err)
			}
			legacyRaw, _ := json.Marshal(store.bases[legacy.Base.ID])
			for _, generation := range []string{"generation", "next-generation"} {
				s.Scopes[0].Generation = generation
				s.Source = NotesSource{Service: notesworkspace.Service{Sources: generationResolver{root, generation}, Store: store}}
				read, snapshot, err := readExportSource(t.Context(), s.Source, "source", path, filepath.Join(root, ".spool"))
				if err != nil {
					t.Fatal(err)
				}
				defer os.Remove(snapshot)
				base := store.bases[read.Base.ID]
				raw, _ := json.Marshal(base)
				if len(base.Content) != 0 || base.ReferenceSize != int64(len(body)) || base.Hash != digest(body) || len(raw) > 1024 {
					t.Fatal("reference payload entered the generation journal", string(raw))
				}
				if generation != "generation" {
					// Stream selection remains metadata-only on a new enrollment;
					// the existing generation-fence tests own native rebinding.
					continue
				}
				if err := os.WriteFile(snapshot, []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := s.export(t.Context(), "c", read.File.ID, read.Base.ID, snapshot); err == nil {
					t.Fatal("tampered snapshot published")
				}
				os.Remove(snapshot)
				if _, err := s.export(t.Context(), "c", read.File.ID, read.Base.ID, snapshot); err == nil {
					t.Fatal("missing snapshot published")
				}
				if err := os.WriteFile(snapshot, body, 0600); err != nil {
					t.Fatal(err)
				}
				binding, err := s.export(t.Context(), "c", read.File.ID, read.Base.ID, snapshot)
				if err != nil || binding.Writable || binding.SHA256 != digest(body) {
					t.Fatal("streamed small reference failed", binding, err)
				}
			}
			current, _ := json.Marshal(store.bases[legacy.Base.ID])
			if string(current) != string(legacyRaw) {
				t.Fatal("legacy inline base changed")
			}
			read, snapshot, err := readExportSource(t.Context(), s.Source, "source", "a.md", "unused")
			if err != nil || snapshot != "" || len(read.Base.Content) == 0 {
				t.Fatal("ordinary text no longer retains its merge base", err)
			}
		})
	}
}

type failingReferenceSource struct{ Source }

func (s failingReferenceSource) ReadReference(context.Context, string, string, string) (notesworkspace.ReadResult, string, error) {
	return notesworkspace.ReadResult{}, "", notesworkspace.ErrReferenceLimit
}

func TestExportReferenceFailureDoesNotFallBackToInlineRead(t *testing.T) {
	_, snapshot, err := readExportSource(t.Context(), failingReferenceSource{}, "source", "small.pdf", "/spool")
	if !errors.Is(err, notesworkspace.ErrReferenceLimit) || snapshot != "" {
		t.Fatal("reference failure was hidden", snapshot, err)
	}
}
