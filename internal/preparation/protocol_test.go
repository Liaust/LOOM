package preparation

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/objectstore"
	"loom.local/loom/internal/scripts"
)

func fixtureRequest(t *testing.T) Request {
	t.Helper()
	m, err := scripts.ParseManifest([]byte(`kind: loom.script
id: preparation_fixture
name: Preparation Fixture
version: 0.1.0
entrypoint: {command: ["/bin/sh", "producer.sh"]}
runtime: {type: shell}
execution: {timeout_seconds: 60, network: false}
`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := scripts.NormalizeManifestJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	info := objectstore.PackageInfo{Format: objectstore.PackageFormat, SHA256: digest(nil), SizeBytes: 1024}
	r := Request{Schema: Schema, Profile: Profile, Platform: "x86_64-linux", ToolchainIdentity: digest([]byte("fixed closure")), Source: info, Producer: info, ProducerKind: "script", ProducerID: ids.NewScriptID(), ProducerVersionID: ids.NewScriptVersionID(), ManifestJSON: string(b), ManifestHash: digest(b)}
	if _, err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestPreparationProtocol(t *testing.T) {
	t.Run("roundtrip exact request and result binding", func(t *testing.T) {
		r := fixtureRequest(t)
		var b bytes.Buffer
		if err := writeFrame(&b, r); err != nil {
			t.Fatal(err)
		}
		var got Request
		if err := readFrame(&b, &got); err != nil || got != r {
			t.Fatalf("roundtrip: %v", err)
		}
		result := resultFor(r, r.Source)
		r.Source.SHA256 = digest([]byte("changed"))
		if result.RequestHash == r.identity() {
			t.Fatal("source not bound")
		}
	})
	for name, edit := range map[string]func(*Request){
		"profile":        func(r *Request) { r.Profile = "host-shell" },
		"platform":       func(r *Request) { r.Platform = "aarch64-darwin" },
		"toolchain":      func(r *Request) { r.ToolchainIdentity = "arbitrary" },
		"source limit":   func(r *Request) { r.Source.ContentBytes = objectstore.PackageMaxContentBytes + 1 },
		"producer limit": func(r *Request) { r.Producer.ContentBytes = objectstore.PackageMaxProducerBytes + 1 },
		"manifest hash":  func(r *Request) { r.ManifestHash = digest(nil) },
		"normalization": func(r *Request) {
			r.ManifestJSON = " " + r.ManifestJSON
			r.ManifestHash = digest([]byte(r.ManifestJSON))
		},
		"wrong owner kind": func(r *Request) { r.ProducerKind = "workflow" },
	} {
		t.Run(name, func(t *testing.T) {
			r := fixtureRequest(t)
			edit(&r)
			if _, err := r.Validate(); err == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}
	for name, b := range map[string][]byte{"zero": {0, 0, 0, 0}, "oversize": {0, 1, 0, 1}, "truncated": {0, 0, 0, 4, '{'}, "unknown": framedJSON(`{"schema":"loom.preparation.v1","host_path":"/etc"}`), "trailing json": framedJSON(`{} {}`)} {
		t.Run(name, func(t *testing.T) {
			var r Request
			if readFrame(bytes.NewReader(b), &r) == nil {
				t.Fatal("accepted malformed frame")
			}
		})
	}
	t.Run("short writes", func(t *testing.T) {
		var w byteWriter
		if err := writeFrame(&w, fixtureRequest(t)); err != nil {
			t.Fatal(err)
		}
		var r Request
		if readFrame(bytes.NewReader(w.b), &r) != nil {
			t.Fatal("short writer corrupted frame")
		}
	})
}
func framedJSON(s string) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(len(s)))
	return append(b, []byte(s)...)
}

type byteWriter struct{ b []byte }

func (w *byteWriter) Write(p []byte) (int, error) { w.b = append(w.b, p[0]); return 1, nil }
func listenFixture(t *testing.T) *net.UnixListener {
	t.Helper()
	parent, err := os.MkdirTemp("", "lp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(parent) })
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(parent, "s"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}
func dialFixture(t *testing.T, l *net.UnixListener) *net.UnixConn {
	t.Helper()
	c, err := net.DialUnix("unix", nil, l.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() { c.Close() })
	return c
}
func TestPreparationSingleFlight(t *testing.T) {
	t.Run("busy before streaming including delivery cleanup", func(t *testing.T) {
		l := listenFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		entered := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		var runs atomic.Int32
		go func() {
			done <- serveOne(ctx, l, func(*net.UnixConn) error { return nil }, func(ctx context.Context, c *net.UnixConn, r Request) error {
				runs.Add(1)
				close(entered)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}()
		first := dialFixture(t, l)
		if err := writeFrame(first, fixtureRequest(t)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("not admitted")
		}
		for i := 0; i < 12; i++ {
			second := dialFixture(t, l)
			var c control
			if err := readFrame(second, &c); err != nil || c.Code != ErrBusy.Error() {
				t.Fatalf("busy without header/payload: %+v %v", c, err)
			}
			second.Close()
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if runs.Load() != 1 {
			t.Fatal("more than one runner")
		}
	})
	t.Run("idle cancellation releases bounded header handlers", func(t *testing.T) {
		l := listenFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- serveOne(ctx, l, func(*net.UnixConn) error { return nil }, func(context.Context, *net.UnixConn, Request) error { t.Error("unexpected runner"); return nil })
		}()
		for i := 0; i < MaxConnections+2; i++ {
			dialFixture(t, l)
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("handler cancellation stalled")
		}
	})
	t.Run("malformed request does not consume activation", func(t *testing.T) {
		l := listenFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- serveOne(ctx, l, func(*net.UnixConn) error { return nil }, func(context.Context, *net.UnixConn, Request) error { return nil })
		}()
		bad := dialFixture(t, l)
		bad.Write(framedJSON(`{"host_path":"/etc"}`))
		var c control
		if readFrame(bad, &c) != nil || c.Code != ErrRefused.Error() {
			t.Fatal("missing refusal")
		}
		bad.Close()
		good := dialFixture(t, l)
		writeFrame(good, fixtureRequest(t))
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
func TestPreparationInputCustody(t *testing.T) {
	t.Run("mutation after ingress cannot affect extracted bytes", func(t *testing.T) {
		source := t.TempDir()
		if err := os.WriteFile(filepath.Join(source, "value"), []byte("original"), 0644); err != nil {
			t.Fatal(err)
		}
		root, err := objectstore.OpenPackageDirectory(source)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		capture, err := objectstore.CapturePackage(context.Background(), root, t.TempDir(), objectstore.PackageCaptureOptions{Selection: []string{"."}})
		if err != nil {
			t.Fatal(err)
		}
		defer capture.Close()
		bytesIn, err := os.ReadFile(capture.ArchivePath)
		if err != nil {
			t.Fatal(err)
		}
		reader := &mutationReader{Reader: bytes.NewReader(bytesIn), mutate: func() {
			for i := range bytesIn {
				bytesIn[i] = 0
			}
		}}
		dest := t.TempDir()
		d, err := objectstore.OpenPackageDirectory(dest)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if err := objectstore.ExtractPackage(context.Background(), reader, d, capture.Info); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dest, "value"))
		if err != nil || string(b) != "original" || !reader.once {
			t.Fatalf("private copy: %q %v mutated=%v", b, err, reader.once)
		}
	})
	t.Run("entrypoint remains literal argv", func(t *testing.T) {
		r := fixtureRequest(t)
		var m map[string]any
		json.Unmarshal([]byte(r.ManifestJSON), &m)
		m["entrypoint"].(map[string]any)["command"] = []string{"/bin/sh", "-c", "printf literal; $(do-not-run)"}
		b, _ := json.Marshal(m)
		manifest, err := scripts.ParseManifest(b)
		if err != nil {
			t.Fatal(err)
		}
		b, _ = scripts.NormalizeManifestJSON(manifest)
		r.ManifestJSON = string(b)
		r.ManifestHash = digest(b)
		command, err := r.Validate()
		if err != nil || command[2] != "printf literal; $(do-not-run)" {
			t.Fatal("argv changed")
		}
	})
}

type mutationReader struct {
	io.Reader
	mutate func()
	once   bool
}

func (r *mutationReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF && !r.once {
		r.once = true
		r.mutate()
	}
	return n, err
}
