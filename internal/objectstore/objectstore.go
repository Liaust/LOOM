package objectstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type Store struct {
	Root string
}

type StoredBlob struct {
	HashAlgorithm string `json:"hash_algorithm"`
	HashHex       string `json:"hash_hex"`
	HashURI       string `json:"hash_uri"`
	SizeBytes     int64  `json:"size_bytes"`
	StoragePath   string `json:"storage_path"`
}

func New(root string) Store {
	return Store{Root: root}
}

func (s Store) PutFile(ctx context.Context, sourcePath string) (StoredBlob, error) {
	if err := ctx.Err(); err != nil {
		return StoredBlob{}, err
	}
	if s.Root == "" {
		return StoredBlob{}, fmt.Errorf("object store root is required")
	}

	sourcePath = filepath.Clean(sourcePath)
	source, err := os.Open(sourcePath)
	if err != nil {
		return StoredBlob{}, fmt.Errorf("open source file: %w", err)
	}
	defer source.Close()

	info, err := source.Stat()
	if err != nil {
		return StoredBlob{}, fmt.Errorf("stat source file: %w", err)
	}
	if info.IsDir() {
		return StoredBlob{}, fmt.Errorf("source path is a directory")
	}

	tempDir := filepath.Join(s.Root, "temp")
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return StoredBlob{}, fmt.Errorf("create object-store temp dir: %w", err)
	}

	temp, err := os.CreateTemp(tempDir, "blob-*")
	if err != nil {
		return StoredBlob{}, fmt.Errorf("create temp blob: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temp, hash), source)
	if syncErr := temp.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return StoredBlob{}, fmt.Errorf("copy blob into object store: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return StoredBlob{}, err
	}

	hashHex := hex.EncodeToString(hash.Sum(nil))
	finalPath := blobPath(s.Root, hashHex)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return StoredBlob{}, fmt.Errorf("create blob shard dir: %w", err)
	}

	if _, err := os.Stat(finalPath); err == nil {
		return StoredBlob{
			HashAlgorithm: "sha256",
			HashHex:       hashHex,
			HashURI:       "sha256:" + hashHex,
			SizeBytes:     written,
			StoragePath:   finalPath,
		}, nil
	}

	if err := os.Rename(tempPath, finalPath); err != nil {
		if _, statErr := os.Stat(finalPath); statErr != nil {
			return StoredBlob{}, fmt.Errorf("store blob: %w", err)
		}
	}

	return StoredBlob{
		HashAlgorithm: "sha256",
		HashHex:       hashHex,
		HashURI:       "sha256:" + hashHex,
		SizeBytes:     written,
		StoragePath:   finalPath,
	}, nil
}

func blobPath(root, hashHex string) string {
	return filepath.Join(root, "blobs", "sha256", hashHex[0:2], hashHex[2:4], hashHex)
}

// These fixed categories may be recorded in job failures. Never attach the
// underlying filesystem error: it can contain managed paths or source data.
var (
	ErrMaterializeMetadata    = errors.New("source_blob_metadata_invalid")
	ErrMaterializeSource      = errors.New("source_blob_unavailable")
	ErrMaterializeContent     = errors.New("source_blob_content_mismatch")
	ErrMaterializeDestination = errors.New("input_destination_unavailable")
	ErrMaterializeIO          = errors.New("input_copy_failed")
)

// MaterializeVerified copies only this managed, pinned blob. It never repairs a
// blob or falls back to another source. The final destination is published only
// after verification and is never replaced, including when it is a symlink.
func (s Store) MaterializeVerified(ctx context.Context, blob StoredBlob, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if blob.HashAlgorithm != "sha256" || len(blob.HashHex) != 64 || strings.Trim(blob.HashHex, "0123456789abcdef") != "" || blob.HashURI != "sha256:"+blob.HashHex || blob.SizeBytes < 0 {
		return ErrMaterializeMetadata
	}
	if s.Root == "" || blob.StoragePath == "" {
		return ErrMaterializeSource
	}
	rootPath, err := filepath.Abs(s.Root)
	if err != nil {
		return ErrMaterializeSource
	}
	resolvedRoot, err := filepath.EvalSymlinks(rootPath)
	if err != nil {
		return ErrMaterializeSource
	}
	name := blob.StoragePath
	if !filepath.IsAbs(name) && !filepath.IsAbs(s.Root) {
		// PutFile with a relative configured root records a cwd-relative path
		// including that root. Give this lexical form precedence; otherwise the
		// name is root-relative. Existence never chooses between interpretations.
		cwdPath, absErr := filepath.Abs(name)
		if absErr != nil {
			return ErrMaterializeSource
		}
		fromRoot, relErr := filepath.Rel(rootPath, cwdPath)
		if relErr == nil && filepath.IsLocal(fromRoot) {
			name = fromRoot
		}
	}
	if filepath.IsAbs(name) {
		name, err = filepath.Rel(rootPath, blob.StoragePath)
		if err != nil || !filepath.IsLocal(name) {
			// Resolve only ancestors so an absolute path using another spelling
			// of the configured mount/link remains valid. Never resolve the leaf.
			var parentPath string
			parentPath, err = filepath.EvalSymlinks(filepath.Dir(blob.StoragePath))
			if err == nil {
				name, err = filepath.Rel(resolvedRoot, filepath.Join(parentPath, filepath.Base(blob.StoragePath)))
			}
		}
	}
	if err != nil || !filepath.IsLocal(name) {
		return ErrMaterializeSource
	}
	root, err := os.OpenRoot(resolvedRoot)
	if err != nil {
		return ErrMaterializeSource
	}
	defer root.Close()
	// Root confines ancestor resolution; openat separately refuses a symlink leaf
	// in the kernel. Nonblocking open also prevents a substituted FIFO from hanging.
	parent, err := root.OpenFile(filepath.Dir(name), os.O_RDONLY|unix.O_DIRECTORY|unix.O_NONBLOCK, 0)
	if err != nil {
		return ErrMaterializeSource
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(name), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrMaterializeSource
	}
	source := os.NewFile(uintptr(fd), "managed-input")
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ErrMaterializeSource
	}
	if info.Size() != blob.SizeBytes {
		return ErrMaterializeContent
	}
	return materializeVerifiedInput(ctx, source, blob, destination)
}

// The reader is held by the caller while this private staging/publication path runs.
func materializeVerifiedInput(ctx context.Context, source io.Reader, blob StoredBlob, destination string) error {
	if destination == "" {
		return ErrMaterializeDestination
	}
	input, err := os.OpenRoot(filepath.Dir(destination))
	if err != nil {
		return ErrMaterializeDestination
	}
	defer input.Close()
	tempName := ".object-" + rand.Text()
	temp, err := input.OpenFile(tempName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ErrMaterializeDestination
	}
	defer input.Remove(tempName)
	defer temp.Close()
	if err := temp.Chmod(0o600); err != nil {
		return ErrMaterializeIO
	}
	if err := copyMaterialized(ctx, source, temp, blob.SizeBytes, blob.HashHex); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return ErrMaterializeIO
	}
	if err := temp.Close(); err != nil {
		return ErrMaterializeIO
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// A hard link is atomic and fails if any entry already occupies the name.
	if err := input.Link(tempName, filepath.Base(destination)); err != nil {
		return ErrMaterializeDestination
	}
	return nil
}

func copyMaterialized(ctx context.Context, source io.Reader, destination io.Writer, size int64, wantHash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if size < 0 {
		return ErrMaterializeMetadata
	}
	hash := sha256.New()
	reader := materializeReader{ctx: ctx, reader: source}
	written, err := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(reader, size))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return ErrMaterializeIO
	}
	if written != size {
		return ErrMaterializeContent
	}
	var overflow [1]byte
	n, err := io.ReadFull(reader, overflow[:])
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if n != 0 {
		return ErrMaterializeContent
	}
	if err != io.EOF {
		return ErrMaterializeIO
	}
	if hex.EncodeToString(hash.Sum(nil)) != wantHash {
		return ErrMaterializeContent
	}
	return nil
}

type materializeReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r materializeReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
