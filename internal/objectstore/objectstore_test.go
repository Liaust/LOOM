package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPutFileStoresByHash(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "note.md")
	content := []byte("# Slice 3\n\nObject ingestion smoke file.\n")
	if err := os.WriteFile(sourcePath, content, 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	store := New(root)
	blob, err := store.PutFile(context.Background(), sourcePath)
	if err != nil {
		t.Fatalf("PutFile returned error: %v", err)
	}

	sum := sha256.Sum256(content)
	wantHash := hex.EncodeToString(sum[:])
	if blob.HashHex != wantHash {
		t.Fatalf("hash mismatch: got %q want %q", blob.HashHex, wantHash)
	}
	if blob.HashURI != "sha256:"+wantHash {
		t.Fatalf("hash uri mismatch: got %q", blob.HashURI)
	}
	if blob.SizeBytes != int64(len(content)) {
		t.Fatalf("size mismatch: got %d want %d", blob.SizeBytes, len(content))
	}
	if _, err := os.Stat(blob.StoragePath); err != nil {
		t.Fatalf("stored blob is missing: %v", err)
	}

	second, err := store.PutFile(context.Background(), sourcePath)
	if err != nil {
		t.Fatalf("second PutFile returned error: %v", err)
	}
	if second.StoragePath != blob.StoragePath {
		t.Fatalf("dedupe path mismatch: got %q want %q", second.StoragePath, blob.StoragePath)
	}
}

func TestMaterializeVerified(t *testing.T) {
	for _, variant := range []string{"absolute", "relative", "root_link", "root_link_resolved_path", "empty"} {
		t.Run(variant, func(t *testing.T) {
			store, blob, content := materializeFixture(t)
			if variant == "empty" {
				content = ""
				if err := os.WriteFile(blob.StoragePath, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				blob = materializeMetadata(blob.StoragePath, content)
			}
			if variant == "relative" {
				blob.StoragePath, _ = filepath.Rel(store.Root, blob.StoragePath)
			}
			if strings.HasPrefix(variant, "root_link") {
				alias := filepath.Join(t.TempDir(), "objects")
				if err := os.Symlink(store.Root, alias); err != nil {
					t.Fatal(err)
				}
				if variant == "root_link" {
					relative, _ := filepath.Rel(store.Root, blob.StoragePath)
					blob.StoragePath = filepath.Join(alias, relative)
				}
				store.Root = alias
			}
			dest := filepath.Join(t.TempDir(), "object")
			if err := store.MaterializeVerified(t.Context(), blob, dest); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(dest)
			if err != nil || string(got) != content {
				t.Fatalf("content=%q error=%v", got, err)
			}
			info, err := os.Lstat(dest)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
				t.Fatalf("mode=%v error=%v", info, err)
			}
			materializeOnlyEntries(t, filepath.Dir(dest), "object")
		})
	}
}

func TestMaterializeRelativeConfiguredRoot(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	const content = "existing PutFile relative root\n"
	if err := os.WriteFile("source.txt", []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"simple", "nested", "parent", "dot", "linked", "genuine_root_relative"} {
		t.Run(variant, func(t *testing.T) {
			root := "objects-" + variant
			switch variant {
			case "nested":
				root = "nested/objects"
			case "parent":
				root = "../objects-parent"
			case "dot":
				root = "."
			case "linked":
				if err := os.Mkdir("physical", 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("physical", root); err != nil {
					t.Fatal(err)
				}
			}
			store := New(root)
			blob, err := store.PutFile(t.Context(), "source.txt")
			if err != nil {
				t.Fatal(err)
			}
			retained, err := os.ReadFile(blob.StoragePath)
			if err != nil || string(retained) != content {
				t.Fatal("PutFile's existing path is not readable")
			}
			if variant == "genuine_root_relative" {
				blob.StoragePath, err = filepath.Rel(root, blob.StoragePath)
				if err != nil {
					t.Fatal(err)
				}
			}
			destination := filepath.Join(t.TempDir(), "object")
			if err := store.MaterializeVerified(t.Context(), blob, destination); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(destination)
			if err != nil || string(got) != content {
				t.Fatalf("bytes=%q error=%v", got, err)
			}
		})
	}
	t.Run("no_existence_driven_fallback", func(t *testing.T) {
		store := New("objects-no-fallback")
		decoy := filepath.Join(store.Root, store.Root, "retained")
		if err := os.MkdirAll(filepath.Dir(decoy), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(decoy, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		blob := materializeMetadata(filepath.Join(store.Root, "retained"), content)
		destination := filepath.Join(t.TempDir(), "object")
		if err := store.MaterializeVerified(t.Context(), blob, destination); !errors.Is(err, ErrMaterializeSource) {
			t.Fatalf("error=%v", err)
		}
		if _, err := os.Lstat(destination); !os.IsNotExist(err) {
			t.Fatal("missing legacy-form source fell back to existing decoy")
		}
	})
}

func TestMaterializeRejectsUnverifiedInput(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, *Store, *StoredBlob)
		want   error
	}{
		{"algorithm", func(t *testing.T, _ *Store, b *StoredBlob) { b.HashAlgorithm = "sha1" }, ErrMaterializeMetadata},
		{"uppercase_hash", func(t *testing.T, _ *Store, b *StoredBlob) { b.HashHex = strings.ToUpper(b.HashHex) }, ErrMaterializeMetadata},
		{"hash_uri", func(t *testing.T, _ *Store, b *StoredBlob) { b.HashURI = "sha256:" + strings.Repeat("0", 64) }, ErrMaterializeMetadata},
		{"negative_size", func(t *testing.T, _ *Store, b *StoredBlob) { b.SizeBytes = -1 }, ErrMaterializeMetadata},
		{"empty_root", func(t *testing.T, s *Store, _ *StoredBlob) { s.Root = "" }, ErrMaterializeSource},
		{"empty_path", func(t *testing.T, _ *Store, b *StoredBlob) { b.StoragePath = "" }, ErrMaterializeSource},
		{"missing", func(t *testing.T, _ *Store, b *StoredBlob) {
			if err := os.Remove(b.StoragePath); err != nil {
				t.Fatal(err)
			}
		}, ErrMaterializeSource},
		{"unreadable", func(t *testing.T, _ *Store, b *StoredBlob) {
			if os.Geteuid() == 0 {
				t.Skip("mode-denial fixture requires nonroot")
			}
			if err := os.Chmod(b.StoragePath, 0); err != nil {
				t.Fatal(err)
			}
		}, ErrMaterializeSource},
		{"directory", func(t *testing.T, _ *Store, b *StoredBlob) { b.StoragePath = filepath.Dir(b.StoragePath) }, ErrMaterializeSource},
		{"fifo", func(t *testing.T, s *Store, b *StoredBlob) {
			p := filepath.Join(s.Root, "fifo")
			if err := unix.Mkfifo(p, 0o600); err != nil {
				t.Fatal(err)
			}
			b.StoragePath = p
		}, ErrMaterializeSource},
		{"outside_absolute", func(t *testing.T, _ *Store, b *StoredBlob) {
			p := filepath.Join(t.TempDir(), "private")
			if err := os.WriteFile(p, []byte("same-length-v1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			b.StoragePath = p
		}, ErrMaterializeSource},
		{"relative_escape", func(t *testing.T, _ *Store, b *StoredBlob) { b.StoragePath = "../private" }, ErrMaterializeSource},
		{"leaf_symlink_inside", func(t *testing.T, s *Store, b *StoredBlob) {
			p := filepath.Join(s.Root, "link")
			if err := os.Symlink(b.StoragePath, p); err != nil {
				t.Fatal(err)
			}
			b.StoragePath = p
		}, ErrMaterializeSource},
		{"leaf_symlink_outside", func(t *testing.T, s *Store, b *StoredBlob) {
			p := filepath.Join(s.Root, "link")
			if err := os.Symlink(filepath.Join(t.TempDir(), "private"), p); err != nil {
				t.Fatal(err)
			}
			b.StoragePath = p
		}, ErrMaterializeSource},
		{"ancestor_escape", func(t *testing.T, s *Store, b *StoredBlob) {
			p := filepath.Join(s.Root, "escape")
			if err := os.Symlink(t.TempDir(), p); err != nil {
				t.Fatal(err)
			}
			b.StoragePath = filepath.Join(p, "private")
		}, ErrMaterializeSource},
		{"same_length_corruption", func(t *testing.T, _ *Store, b *StoredBlob) {
			if err := os.WriteFile(b.StoragePath, []byte("same-length-v2\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, ErrMaterializeContent},
		{"truncate", func(t *testing.T, _ *Store, b *StoredBlob) {
			if err := os.Truncate(b.StoragePath, 2); err != nil {
				t.Fatal(err)
			}
		}, ErrMaterializeContent},
		{"grow", func(t *testing.T, _ *Store, b *StoredBlob) {
			if err := os.WriteFile(b.StoragePath, []byte("same-length-v1\nextra"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, ErrMaterializeContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, blob, _ := materializeFixture(t)
			tc.change(t, &store, &blob)
			dir := t.TempDir()
			err := store.MaterializeVerified(t.Context(), blob, filepath.Join(dir, "object"))
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
			materializeOnlyEntries(t, dir)
		})
	}
}

func TestMaterializeNoClobber(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "symlink"}[symlink], func(t *testing.T) {
			store, blob, _ := materializeFixture(t)
			dir := t.TempDir()
			dest := filepath.Join(dir, "object")
			other := filepath.Join(dir, "untouched")
			if err := os.WriteFile(other, []byte("owned-by-other"), 0o600); err != nil {
				t.Fatal(err)
			}
			if symlink {
				if err := os.Symlink(other, dest); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(dest, []byte("existing"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(dest)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.MaterializeVerified(t.Context(), blob, dest); !errors.Is(err, ErrMaterializeDestination) {
				t.Fatalf("error=%v", err)
			}
			after, err := os.Lstat(dest)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("pre-existing destination changed")
			}
			want := "existing"
			if symlink {
				want = "owned-by-other"
			}
			got, _ := os.ReadFile(dest)
			if string(got) != want {
				t.Fatal("destination bytes changed")
			}
			got, _ = os.ReadFile(other)
			if string(got) != "owned-by-other" {
				t.Fatal("unrelated bytes changed")
			}
			materializeOnlyEntries(t, dir, "object", "untouched")
		})
	}
}

func TestMaterializeCancellationAndIO(t *testing.T) {
	for _, scenario := range []string{"cancel_before", "cancel_during", "read_error", "truncated_stream", "overflow_stream", "writer_error"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			blob := materializeMetadata("unused", "expected bytes")
			var reader io.Reader = strings.NewReader("expected bytes")
			want := ErrMaterializeIO
			switch scenario {
			case "cancel_before":
				cancel()
				want = context.Canceled
			case "cancel_during":
				reader = &materializeFaultReader{cancel: cancel}
				want = context.Canceled
			case "read_error":
				reader = &materializeFaultReader{}
			case "truncated_stream":
				reader = strings.NewReader("short")
				want = ErrMaterializeContent
			case "overflow_stream":
				reader = strings.NewReader("expected bytes more")
				want = ErrMaterializeContent
			case "writer_error":
				err := copyMaterialized(ctx, reader, materializeFaultWriter{}, blob.SizeBytes, blob.HashHex)
				if !errors.Is(err, want) {
					t.Fatalf("error=%v", err)
				}
				return
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "untouched"), []byte("other"), 0o600); err != nil {
				t.Fatal(err)
			}
			err := materializeVerifiedInput(ctx, reader, blob, filepath.Join(dir, "object"))
			if !errors.Is(err, want) {
				t.Fatalf("error=%v want=%v", err, want)
			}
			materializeOnlyEntries(t, dir, "untouched")
		})
	}
}

type materializeFaultReader struct {
	cancel context.CancelFunc
	read   bool
}

func (r *materializeFaultReader) Read(p []byte) (int, error) {
	if !r.read {
		r.read = true
		n := copy(p, "expe")
		if r.cancel != nil {
			r.cancel()
		}
		return n, nil
	}
	return 0, &os.PathError{Op: "read", Path: "private/source/credential", Err: errors.New("secret payload")}
}

type materializeFaultWriter struct{}

func (materializeFaultWriter) Write([]byte) (int, error) {
	return 0, errors.New("private write failure")
}

func materializeFixture(t *testing.T) (Store, StoredBlob, string) {
	t.Helper()
	store := New(t.TempDir())
	content := "same-length-v1\n"
	path := filepath.Join(store.Root, "retained")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return store, materializeMetadata(path, content), content
}
func materializeMetadata(path, content string) StoredBlob {
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])
	return StoredBlob{HashAlgorithm: "sha256", HashHex: hash, HashURI: "sha256:" + hash, SizeBytes: int64(len(content)), StoragePath: path}
}
func materializeOnlyEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(want) {
		t.Fatalf("unexpected staged entries: %v", entries)
	}
	for i, entry := range entries {
		if entry.Name() != want[i] {
			t.Fatalf("entry=%s want=%s", entry.Name(), want[i])
		}
	}
}
