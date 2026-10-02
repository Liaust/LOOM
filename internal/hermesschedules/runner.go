package hermesschedules

import (
	"bytes"
	"context"
	"io"
	"net"
	"time"
)

func SocketPath(profile string) string {
	if profile != "mina" && profile != "morathustra" {
		return ""
	}
	return "/run/loom-" + profile + "-schedules/observe.sock"
}

// HelperReader receives only sanitized IPC. The caller cannot supply a command,
// profile selector or request body. The systemd activator must be root.
func HelperReader(path string) Reader {
	return func(ctx context.Context, limit int64) ([]byte, error) {
		if path != SocketPath("mina") && path != SocketPath("morathustra") {
			return nil, ErrSource
		}
		return readSocket(ctx, path, limit, requireActivatorPeer)
	}
}

func readSocket(ctx context.Context, path string, limit int64, verify func(net.Conn) error) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxBytes {
		return nil, ErrTooLarge
	}
	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUnavailable
	}
	defer conn.Close()
	if verify(conn) != nil {
		return nil, ErrUnavailable
	}
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return nil, ErrUnavailable
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	data, err := io.ReadAll(io.LimitReader(conn, limit+1))
	if int64(len(data)) > limit {
		return nil, ErrTooLarge
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		if !time.Now().Before(deadline) {
			return nil, context.DeadlineExceeded
		}
		return nil, ErrUnavailable
	}
	return data, nil
}

type boundedOutput struct {
	buffer bytes.Buffer
	limit  int64
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if int64(b.Len())+int64(len(p)) > b.limit {
		return 0, ErrTooLarge
	}
	return b.buffer.Write(p)
}
func (b *boundedOutput) Len() int      { return b.buffer.Len() }
func (b *boundedOutput) Bytes() []byte { return b.buffer.Bytes() }
