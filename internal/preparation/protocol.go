// Package preparation provides the private, opt-in executor transport. It owns
// neither jobs nor retained artifacts, and grants no publication authority.
package preparation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/objectstore"
	"loom.local/loom/internal/scripts"
	"loom.local/loom/internal/workflows"
)

const (
	Schema             = "loom.preparation.v1"
	Profile            = "native-offline-go.v1"
	SocketPath         = "/run/loom-project-preparation/prepare.sock"
	ConfigPath         = "/etc/loom/project-preparation.json"
	UnitName           = "loom-project-preparation.service"
	SocketName         = "preparation"
	MaxControlBytes    = 64 << 10
	MaxLogBytes        = 8 << 20
	MaxOutputBytes     = 128 << 20
	MaxWorkspaceBytes  = 2 << 30
	MaxWorkspaceInodes = 65536
	MaxMemoryBytes     = 3 << 30
	MaxTasks           = 128
	WallTimeout        = 300 * time.Second
	HeaderTimeout      = 3 * time.Second
	StopTimeout        = 10 * time.Second
	MaxConnections     = 4
)

var (
	ErrBusy        = errors.New("preparation.busy")
	ErrUnavailable = errors.New("preparation.unavailable")
	ErrRefused     = errors.New("preparation.refused")
	ErrFailed      = errors.New("preparation.failed")
)

type Request struct {
	Schema            string                  `json:"schema"`
	Profile           string                  `json:"profile"`
	Platform          string                  `json:"platform"`
	ToolchainIdentity string                  `json:"toolchain_identity"`
	Source            objectstore.PackageInfo `json:"source"`
	Producer          objectstore.PackageInfo `json:"producer"`
	ProducerKind      string                  `json:"producer_kind"`
	ProducerID        string                  `json:"producer_id"`
	ProducerVersionID string                  `json:"producer_version_id"`
	ManifestJSON      string                  `json:"manifest_json"`
	ManifestHash      string                  `json:"manifest_hash"`
}

type Result struct {
	Schema            string                  `json:"schema"`
	Profile           string                  `json:"profile"`
	Platform          string                  `json:"platform"`
	ToolchainIdentity string                  `json:"toolchain_identity"`
	RequestHash       string                  `json:"request_hash"`
	Source            objectstore.PackageInfo `json:"source"`
	Producer          objectstore.PackageInfo `json:"producer"`
	Output            objectstore.PackageInfo `json:"output"`
}

type control struct {
	Schema  string  `json:"schema"`
	Status  string  `json:"status"`
	Code    string  `json:"code,omitempty"`
	Result  *Result `json:"result,omitempty"`
	Receipt string  `json:"receipt,omitempty"`
}

func digest(b []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(b)) }
func validDigest(s string) bool {
	return len(s) == 71 && strings.HasPrefix(s, "sha256:") && strings.Trim(s[7:], "0123456789abcdef") == ""
}
func validatePackage(p objectstore.PackageInfo, maxContent int64) error {
	if p.Format != objectstore.PackageFormat || !validDigest(p.SHA256) || p.SizeBytes < 1024 || p.SizeBytes > objectstore.PackageMaxArchiveBytes || p.EntryCount < 0 || p.EntryCount > objectstore.PackageMaxEntries || p.ContentBytes < 0 || p.ContentBytes > maxContent {
		return ErrRefused
	}
	return nil
}

// Validate derives the command from the exact normalized registered manifest.
// Commands remain argv data and are never evaluated by the supervisor.
func (r Request) Validate() ([]string, error) {
	if r.Schema != Schema || r.Profile != Profile || r.Platform != "x86_64-linux" && r.Platform != "aarch64-linux" || !validDigest(r.ToolchainIdentity) || digest([]byte(r.ManifestJSON)) != r.ManifestHash {
		return nil, ErrRefused
	}
	if err := validatePackage(r.Source, objectstore.PackageMaxContentBytes); err != nil {
		return nil, err
	}
	if err := validatePackage(r.Producer, objectstore.PackageMaxProducerBytes); err != nil {
		return nil, err
	}
	if len(r.ManifestJSON) > MaxControlBytes || !json.Valid([]byte(r.ManifestJSON)) {
		return nil, ErrRefused
	}
	var normalized []byte
	var command []string
	var err error
	switch r.ProducerKind {
	case "script":
		if ids.Validate(ids.ScriptPrefix, r.ProducerID) != nil || ids.Validate(ids.ScriptVersionPrefix, r.ProducerVersionID) != nil {
			return nil, ErrRefused
		}
		manifest, e := scripts.ParseManifest([]byte(r.ManifestJSON))
		if e != nil {
			return nil, ErrRefused
		}
		normalized, err = scripts.NormalizeManifestJSON(manifest)
		command = manifest.Entrypoint.Command
	case "workflow":
		if ids.Validate(ids.WorkflowPrefix, r.ProducerID) != nil || ids.Validate(ids.WorkflowVersionPrefix, r.ProducerVersionID) != nil {
			return nil, ErrRefused
		}
		manifest, e := workflows.ParseManifest([]byte(r.ManifestJSON))
		if e != nil {
			return nil, ErrRefused
		}
		normalized, err = workflows.NormalizeManifestJSON(manifest)
		command = manifest.Entrypoint.Command
	default:
		return nil, ErrRefused
	}
	if err != nil || string(normalized) != r.ManifestJSON || len(command) == 0 || len(command) > 64 {
		return nil, ErrRefused
	}
	for _, arg := range command {
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return nil, ErrRefused
		}
	}
	if command[0] == "" {
		return nil, ErrRefused
	}
	return append([]string(nil), command...), nil
}

func (r Request) identity() string { b, _ := json.Marshal(r); return digest(b) }
func resultFor(r Request, output objectstore.PackageInfo) Result {
	return Result{Schema: Schema, Profile: r.Profile, Platform: r.Platform, ToolchainIdentity: r.ToolchainIdentity, RequestHash: r.identity(), Source: r.Source, Producer: r.Producer, Output: output}
}

func writeFrame(w io.Writer, value any) error {
	b, err := json.Marshal(value)
	if err != nil || len(b) == 0 || len(b) > MaxControlBytes {
		return ErrRefused
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(b)))
	for _, chunk := range [][]byte{prefix[:], b} {
		for len(chunk) > 0 {
			n, err := w.Write(chunk)
			if err != nil {
				return err
			}
			if n <= 0 {
				return io.ErrShortWrite
			}
			chunk = chunk[n:]
		}
	}
	return nil
}
func readFrame(r io.Reader, value any) error {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(prefix[:])
	if n == 0 || n > MaxControlBytes {
		return ErrRefused
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return ErrRefused
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrRefused
	}
	return nil
}

// serveOne is only the bounded transport state machine. The real Linux caller
// performs activation/range/cgroup admission before supplying its listener.
// An activation permanently claims its single slot, including failed ingress.
func serveOne(ctx context.Context, listener *net.UnixListener, authorize func(*net.UnixConn) error, run func(context.Context, *net.UnixConn, Request) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	var used atomic.Bool
	var handlers sync.WaitGroup
	defer func() { cancel(); handlers.Wait() }()
	slots := make(chan struct{}, MaxConnections)
	done := make(chan error, 1)
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			select {
			case result := <-done:
				return result
			default:
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer func() { <-slots }()
			defer conn.Close()
			closeOnCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer closeOnCancel()
			_ = conn.SetDeadline(time.Now().Add(HeaderTimeout))
			refuse := func(code string) { _ = writeFrame(conn, control{Schema: Schema, Status: "failed", Code: code}) }
			if authorize(conn) != nil {
				refuse(ErrRefused.Error())
				return
			}
			if used.Load() {
				refuse(ErrBusy.Error())
				return
			}
			var req Request
			if readFrame(conn, &req) != nil {
				refuse(ErrRefused.Error())
				return
			}
			if _, err := req.Validate(); err != nil {
				refuse(ErrRefused.Error())
				return
			}
			if !used.CompareAndSwap(false, true) {
				refuse(ErrBusy.Error())
				return
			}
			runCtx, stopRun := context.WithTimeout(ctx, WallTimeout)
			defer stopRun()
			_ = conn.SetDeadline(time.Now().Add(WallTimeout))
			err := run(runCtx, conn, req)
			if err != nil {
				refuse(ErrFailed.Error())
			}
			done <- err
			_ = listener.Close()
		}()
	}
}

// IDRange entries are trusted node configuration, never client input. The node
// declares every other container allocation reservation in addition to the
// complete live passwd/group and subuid/subgid audit performed by Linux.
type IDRange struct {
	Name  string `json:"name"`
	Base  uint32 `json:"base"`
	Count uint32 `json:"count"`
}
type HostConfig struct {
	Schema          string    `json:"schema"`
	CallerUID       uint32    `json:"caller_uid"`
	UIDBase         uint32    `json:"uid_base"`
	Reservations    []IDRange `json:"reservations"`
	RuntimeManifest string    `json:"runtime_manifest"`
	Nspawn          string    `json:"nspawn"`
	Getent          string    `json:"getent"`
}

type runtimeManifest struct {
	Schema            string   `json:"schema"`
	Platform          string   `json:"platform"`
	Root              string   `json:"root"`
	Closure           []string `json:"closure"`
	ToolchainIdentity string   `json:"toolchain_identity"`
}
