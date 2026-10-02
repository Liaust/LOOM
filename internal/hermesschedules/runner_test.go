package hermesschedules

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureSocket(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	// Keep the pathname below the Unix socket limit on macOS.
	dir := t.TempDir()
	path := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		c, e := listener.Accept()
		if e == nil {
			defer c.Close()
			serve(c)
		}
	}()
	return path
}

func TestHelperSocketBoundsAndCancellation(t *testing.T) {
	trustFixture := func(net.Conn) error { return nil }
	path := fixtureSocket(t, func(c net.Conn) { _, _ = io.WriteString(c, "sanitized") })
	out, err := readSocket(context.Background(), path, 1024, trustFixture)
	if err != nil || string(out) != "sanitized" {
		t.Fatal(string(out), err)
	}
	path = fixtureSocket(t, func(c net.Conn) { _, _ = io.WriteString(c, strings.Repeat("x", 100)) })
	out, err = readSocket(context.Background(), path, 32, trustFixture)
	if !errors.Is(err, ErrTooLarge) || len(out) != 0 {
		t.Fatal("unbounded output", len(out), err)
	}
	path = fixtureSocket(t, func(c net.Conn) { _, _ = io.Copy(io.Discard, c) })
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(30*time.Millisecond, cancel)
	defer timer.Stop()
	started := time.Now()
	_, err = readSocket(ctx, path, 1024, trustFixture)
	if !errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
		t.Fatal("cancellation not bounded", err)
	}
	path = fixtureSocket(t, func(c net.Conn) { _, _ = io.WriteString(c, "PRIVATE") })
	out, err = readSocket(context.Background(), path, 1024, func(net.Conn) error { return ErrSource })
	if err != ErrUnavailable || len(out) != 0 {
		t.Fatal("untrusted activator accepted", err)
	}
	buffer := &boundedOutput{limit: 32}
	_, err = io.Copy(buffer, strings.NewReader(strings.Repeat("x", 100)))
	if err != ErrTooLarge || buffer.Len() > 32 {
		t.Fatal("output bound bypassed", err)
	}
}

func TestHelperSocketFixedPaths(t *testing.T) {
	for _, path := range []string{"", "/tmp/helper", SocketPath("mina") + " --home=/other", SocketPath("mina") + "/../other"} {
		if _, err := HelperReader(path)(context.Background(), 1024); err != ErrSource {
			t.Fatal("invalid socket reached IO", err)
		}
	}
	if SocketPath("unknown") != "" {
		t.Fatal("unsupported profile accepted")
	}
}
