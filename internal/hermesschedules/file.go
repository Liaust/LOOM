package hermesschedules

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// FileReader selects exactly <profileHome>/cron/jobs.json. It does not discover
// profiles or read config/credentials. The caller supplies a trusted absolute
// profile home for Source.Profile, on the selected host. A missing store is
// unavailable, never evidence of an empty schedule inventory.
//
// Reads are byte-bounded and check cancellation between file operations. Like
// ordinary local file I/O, a kernel/filesystem stall cannot be preempted by a Go
// context. Use a context-aware transport Reader for remote observation.
func FileReader(profileHome string) Reader {
	return func(ctx context.Context, limit int64) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(profileHome) || limit <= 0 || limit > MaxBytes {
			return nil, ErrSource
		}
		// O_NONBLOCK prevents a FIFO substituted for jobs.json from hanging open;
		// O_NOFOLLOW rejects a final symlink. The parent profile path is trusted.
		f, err := os.OpenFile(filepath.Join(profileHome, "cron", "jobs.json"), os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
		if err != nil {
			return nil, ErrUnavailable
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return nil, ErrUnavailable
		}
		if info.Size() > limit {
			return nil, ErrTooLarge
		}
		data, err := io.ReadAll(io.LimitReader(contextReader{ctx, f}, limit+1))
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, ErrUnavailable
		}
		if int64(len(data)) > limit {
			return nil, ErrTooLarge
		}
		return data, nil
	}
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
