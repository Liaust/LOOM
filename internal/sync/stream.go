package sync

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func writeSyncedObjectStream(ctx context.Context, source io.Reader, name string, size int64) (string, string, func(), error) {
	noop := func() {}
	if size < 0 || size > MaxStreamObjectUploadBytes {
		return "", "", noop, fmt.Errorf("invalid streamed object size")
	}
	dir, err := os.MkdirTemp("", "loom-sync-object-*")
	if err != nil {
		return "", "", noop, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	// Keep the extension for file-type detection, never a caller-selected path.
	file, err := os.CreateTemp(dir, "content-*"+filepath.Ext(filepath.Base(name)))
	if err != nil {
		cleanup()
		return "", "", noop, err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(contextReader{ctx, source}, size+1))
	closeErr := file.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr == nil && n != size {
		copyErr = fmt.Errorf("size_bytes does not match content length")
	}
	if copyErr == nil {
		copyErr = ctx.Err()
	}
	if copyErr != nil {
		cleanup()
		return "", "", noop, copyErr
	}
	return file.Name(), fmt.Sprintf("sha256:%x", hash.Sum(nil)), cleanup, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
