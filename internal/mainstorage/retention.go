package mainstorage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultRetentionRoot = "/var/lib/loom/storage-retention"
)

type retentionPayload struct {
	Path       string
	VerifiedAt time.Time
}

func ensureRetentionPayload(ctx context.Context, file discoveredFile, config Config, checksum string, verifiedAt time.Time) (retentionPayload, error) {
	checksum = strings.ToLower(strings.TrimSpace(checksum))
	if checksum == "" {
		return retentionPayload{}, fmt.Errorf("retention checksum is required")
	}
	if strings.TrimSpace(config.RetentionRoot) == "" {
		return retentionPayload{}, fmt.Errorf("retention root is required")
	}
	target := retentionPayloadPath(config.RetentionRoot, checksum)
	if err := verifyRetainedPayload(target, file.SizeBytes, checksum); err == nil {
		return retentionPayload{Path: target, VerifiedAt: verifiedAt.UTC()}, nil
	} else if !os.IsNotExist(err) {
		return retentionPayload{}, err
	}

	if err := ctx.Err(); err != nil {
		return retentionPayload{}, err
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return retentionPayload{}, fmt.Errorf("prepare retention parent: %w", err)
	}
	temp, err := os.CreateTemp(parent, ".incoming-*")
	if err != nil {
		return retentionPayload{}, fmt.Errorf("create retention temp file: %w", err)
	}
	tempPath := temp.Name()
	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			_ = os.Remove(tempPath)
		}
	}()

	source, err := os.Open(file.PhysicalPath)
	if err != nil {
		_ = temp.Close()
		return retentionPayload{}, fmt.Errorf("open retention source %s: %w", file.PhysicalPath, err)
	}
	_, copyErr := copyWithContext(ctx, temp, source)
	closeSourceErr := source.Close()
	closeTempErr := temp.Close()
	if copyErr != nil {
		return retentionPayload{}, fmt.Errorf("copy retention payload %s: %w", file.PhysicalPath, copyErr)
	}
	if closeSourceErr != nil {
		return retentionPayload{}, fmt.Errorf("close retention source %s: %w", file.PhysicalPath, closeSourceErr)
	}
	if closeTempErr != nil {
		return retentionPayload{}, fmt.Errorf("close retention temp %s: %w", tempPath, closeTempErr)
	}
	if err := verifyRetainedPayload(tempPath, file.SizeBytes, checksum); err != nil {
		return retentionPayload{}, err
	}
	if err := os.Chmod(tempPath, 0o640); err != nil {
		return retentionPayload{}, fmt.Errorf("chmod retention payload %s: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, target); err != nil {
		if verifyErr := verifyRetainedPayload(target, file.SizeBytes, checksum); verifyErr == nil {
			return retentionPayload{Path: target, VerifiedAt: verifiedAt.UTC()}, nil
		}
		return retentionPayload{}, fmt.Errorf("publish retention payload %s: %w", target, err)
	}
	cleanupTemp = false
	if err := verifyRetainedPayload(target, file.SizeBytes, checksum); err != nil {
		return retentionPayload{}, err
	}
	return retentionPayload{Path: target, VerifiedAt: verifiedAt.UTC()}, nil
}

func retentionPayloadPath(root, checksum string) string {
	return filepath.Join(root, "main-documents", "by-sha256", strings.ToLower(strings.TrimSpace(checksum)))
}

func verifyRetainedPayload(pathValue string, sizeBytes int64, checksum string) error {
	info, err := os.Stat(pathValue)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("retention payload %s is not a regular file", pathValue)
	}
	if info.Size() != sizeBytes {
		return fmt.Errorf("retention payload %s size = %d, want %d", pathValue, info.Size(), sizeBytes)
	}
	got, err := hashFile(pathValue)
	if err != nil {
		return err
	}
	if got != strings.ToLower(strings.TrimSpace(checksum)) {
		return fmt.Errorf("retention payload %s checksum = sha256:%s, want sha256:%s", pathValue, got, checksum)
	}
	return nil
}

func copyWithContext(ctx context.Context, writer io.Writer, reader io.Reader) (int64, error) {
	buffer := make([]byte, 1024*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		nr, readErr := reader.Read(buffer)
		if nr > 0 {
			nw, writeErr := writer.Write(buffer[:nr])
			written += int64(nw)
			if writeErr != nil {
				return written, writeErr
			}
			if nw != nr {
				return written, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}
