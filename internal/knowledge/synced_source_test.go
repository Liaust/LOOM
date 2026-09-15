package knowledge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSyncedSourceVerifiedBytes(t *testing.T) {
	for _, payload := range [][]byte{nil, []byte("Unicode: \u03bb\n"), {0, 255, 1}} {
		path := filepath.Join(t.TempDir(), "blob")
		if err := os.WriteFile(path, payload, 0600); err != nil {
			t.Fatal(err)
		}
		binding := syncedSourceBinding{Path: path, Hash: hashArtifactValue(string(payload)), Size: int64(len(payload))}
		got, err := readVerifiedSyncedBlob(t.Context(), binding, 1024)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("verified bytes: %q %v", got, err)
		}
	}
}

func TestSyncedSourceBlobRefusals(t *testing.T) {
	for _, name := range []string{"hash", "short", "long", "negative", "oversize", "default-bound", "invalid-hash", "relative", "unclean", "link", "directory", "fifo", "missing", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "blob")
			payload := []byte("retained bytes")
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			binding := syncedSourceBinding{Path: path, Hash: hashArtifactValue(string(payload)), Size: int64(len(payload))}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			maxBytes := int64(1024)
			switch name {
			case "hash":
				binding.Hash = hashArtifactValue("different bytes")
			case "short":
				binding.Size--
			case "long":
				binding.Size++
			case "negative":
				binding.Size = -1
			case "oversize":
				maxBytes = 1
			case "default-bound":
				binding.Size, maxBytes = defaultSyncedSourceMaxBytes+1, 0
			case "invalid-hash":
				binding.Hash = "sha256:wrong"
			case "relative":
				binding.Path = "blob"
			case "unclean":
				binding.Path = filepath.Dir(path) + "/./blob"
			case "link", "directory", "fifo", "missing":
				if err := os.Rename(path, path+".retained"); err != nil {
					t.Fatal(err)
				}
				var err error
				switch name {
				case "link":
					err = os.Symlink(path+".retained", path)
				case "directory":
					err = os.Mkdir(path, 0700)
				case "fifo":
					err = unix.Mkfifo(path, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancel()
			}
			got, err := readVerifiedSyncedBlob(ctx, binding, maxBytes)
			if err == nil || len(got) != 0 {
				t.Fatalf("invalid binding exposed bytes: %q %v", got, err)
			}
			if name == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestSyncedSourcePrivateSnapshotCleanup(t *testing.T) {
	payload := []byte("private snapshot")
	object := KnowledgeObject{SourcePath: "/foreign/document.docx", RelativePath: "document.docx"}
	path, cleanup, err := writeExtractionTempFile(object, payload)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !strings.HasSuffix(path, ".docx") {
		t.Fatalf("private snapshot: %v %v", info, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("snapshot contents: %q %v", got, err)
	}
	cleanup()
	if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot cleanup: %v", err)
	}
	if object.SourcePath != "/foreign/document.docx" {
		t.Fatal("snapshot rewrote citation identity")
	}
}
