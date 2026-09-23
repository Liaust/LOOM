package sync

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestSyncedObjectStreamBoundaries(t *testing.T) {
	for _, size := range []int64{0, MaxInlineObjectUploadBytes + 1, MaxStreamObjectUploadBytes, MaxStreamObjectUploadBytes + 1} {
		in := syncedObjectInputForNormalizeTest()
		in.SizeBytes, in.ContentBase64 = size, ""
		if _, err := normalizeSyncedObjectUpload(in, true); (err == nil) != (size <= MaxStreamObjectUploadBytes) {
			t.Fatalf("size %d: %v", size, err)
		}
	}
	in := syncedObjectInputForNormalizeTest()
	if _, err := normalizeSyncedObjectUpload(in, true); err == nil {
		t.Fatal("stream metadata accepted inline content")
	}
}

func TestSyncedObjectStreamLargeAndCleanup(t *testing.T) {
	size := int64(MaxInlineObjectUploadBytes + 1)
	path, digest, cleanup, err := writeSyncedObjectStream(t.Context(), io.LimitReader(zeroReader{}, size), "large.pdf", size)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	h := sha256.New()
	_, _ = io.Copy(h, io.LimitReader(zeroReader{}, size))
	info, err := os.Stat(path)
	if err != nil || info.Size() != size || info.Mode().Perm() != 0o600 || digest != fmt.Sprintf("sha256:%x", h.Sum(nil)) {
		t.Fatalf("file/digest: %v %s %v", info, digest, err)
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("temp remains: %v", err)
	}
}

func TestSyncedObjectStreamRefusesLengthAndCancellation(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for _, size := range []int64{2, 4} {
		if _, _, _, err := writeSyncedObjectStream(t.Context(), strings.NewReader("abc"), "a.pdf", size); err == nil {
			t.Fatal("accepted wrong size")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, _, err := writeSyncedObjectStream(ctx, strings.NewReader("abc"), "a.pdf", 3); err == nil {
		t.Fatal("accepted cancelled copy")
	}
	entries, err := os.ReadDir(os.TempDir())
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed copy residue: %v %v", entries, err)
	}
}
