package hermesprofile

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func zipContents(t *testing.T, pkg string) map[string][]byte {
	t.Helper()
	z, err := zip.OpenReader(filepath.Join(pkg, PayloadFile))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	m := map[string][]byte{}
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		m[f.Name], err = io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func replaceFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.CreateTemp(filepath.Dir(path), ".replace-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(data); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err = os.Rename(f.Name(), path); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureLiveChangesAfterCaptureDoNotInvalidate(t *testing.T) {
	in, p := fixture(t)
	home := filepath.Join(in.Workspace, ".hermes")
	for _, d := range []string{"state", "cron"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"state/gateway.heartbeat", "state/.gateway_1234abcd.tmp", "cron/ticker_heartbeat", "cron/ticker_last_success", "cron/.hb_1234abcd.tmp", "cron/.tick.lock", "cron/jobs.json"} {
		if err := os.WriteFile(filepath.Join(home, n), []byte("before"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	original := in.Runner
	in.Runner = func(ctx context.Context, b, h, o string) (string, error) {
		if h == home || filepath.Base(h) != ".capture" {
			t.Fatal("backup received live home")
		}
		for _, n := range []string{"state/gateway.heartbeat", "cron/ticker_heartbeat", "cron/ticker_last_success", "memories/MEMORY.md", "skills/created/SKILL.md", "cron/jobs.json"} {
			replaceFixture(t, filepath.Join(home, n), []byte("after"))
		}
		if err := os.WriteFile(filepath.Join(home, "new-session.json"), []byte("new"), 0600); err != nil {
			t.Fatal(err)
		}
		return original(ctx, b, h, o)
	}
	e, err := Publish(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	_, m, err := verify(context.Background(), e.Path, in.Workspace, p.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if m.Schema != CaptureSchema || m.Capture == nil || m.Capture.Policy != capturePolicy {
		t.Fatal("missing capture truth")
	}
	contents := zipContents(t, e.Path)
	if string(contents["memories/MEMORY.md"]) != "fixture memory" || string(contents["cron/jobs.json"]) != "before" {
		t.Fatal("capture changed with live source")
	}
	for name := range contents {
		if ephemeral(name) || name == "new-session.json" {
			t.Fatalf("unexpected captured entry %q", name)
		}
	}
	if _, err := os.Lstat(filepath.Join(home, ".backup.lock")); !os.IsNotExist(err) {
		t.Fatal("live backup lock created")
	}
	if names, err := os.ReadDir(e.Path); err != nil || len(names) != 2 {
		t.Fatal("private capture leaked into package")
	}
}

func TestCapturePerFileRetryAndBusyRefusal(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry", true: "busy"}[busy], func(t *testing.T) {
			in, _ := fixture(t)
			count := 0
			in.beforeCaptureCopy = func(rel string) {
				if rel == "memories/MEMORY.md" {
					count++
					if busy || count == 1 {
						replaceFixture(t, filepath.Join(in.Workspace, ".hermes", rel), []byte("new stable memory"))
					}
				}
			}
			e, err := Publish(context.Background(), in)
			if busy {
				if err == nil || count != 3 || !strings.Contains(err.Error(), "file_busy") {
					t.Fatalf("busy: count=%d err=%v", count, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if count != 2 || string(zipContents(t, e.Path)["memories/MEMORY.md"]) != "new stable memory" {
				t.Fatal("wrong retry snapshot")
			}
		})
	}
}

func TestCaptureDisappearanceIsRecordedAndRequiredEntriesRefuse(t *testing.T) {
	for _, name := range []string{"z-temporary.md", "state.db"} {
		t.Run(name, func(t *testing.T) {
			in, p := fixture(t)
			home := filepath.Join(in.Workspace, ".hermes")
			if name == "z-temporary.md" {
				os.WriteFile(filepath.Join(home, name), []byte("gone"), 0600)
			}
			in.beforeCaptureCopy = func(rel string) {
				if rel == "SOUL.md" {
					if err := os.Remove(filepath.Join(home, name)); err != nil {
						t.Fatal(err)
					}
				}
			}
			e, err := Publish(context.Background(), in)
			if name == "state.db" {
				if err == nil {
					t.Fatal("missing required DB accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, m, err := verify(context.Background(), e.Path, in.Workspace, p.PublicKey)
			if err != nil || !reflect.DeepEqual(m.Capture.Omitted, []string{name}) {
				t.Fatalf("omission: %v %v", m.Capture, err)
			}
		})
	}
}

func TestCaptureEphemeralBoundary(t *testing.T) {
	for _, n := range []string{"state/gateway.heartbeat", "state/.gateway_a1b2c3d4.tmp", "cron/.hb_a1b2c3d4.tmp", "state/gateway.loop-tick.123.sock"} {
		if !ephemeral(n) {
			t.Fatalf("runtime not excluded %s", n)
		}
	}
	for _, n := range []string{"cron/jobs.json", "state/decisions.json", "memories/gateway.heartbeat", "state/.gateway_short.tmp", "skills/created/.hb_a1b2c3d4.tmp", "state/gateway.loop-tick.0.sock"} {
		if ephemeral(n) {
			t.Fatalf("durable path excluded %s", n)
		}
	}
}

func TestCaptureDatabaseInsideLogsUsesNativeSnapshot(t *testing.T) {
	in, p := fixture(t)
	if err := os.WriteFile(filepath.Join(in.Workspace, ".hermes", "logs", "history.db"), []byte("fixture SQLite"), 0600); err != nil {
		t.Fatal(err)
	}
	original := in.snapshotDatabase
	called := false
	in.snapshotDatabase = func(ctx context.Context, parent, source, target *os.File, rel string) error {
		if rel == "logs/history.db" {
			called = true
		}
		return original(ctx, parent, source, target, rel)
	}
	e, err := Publish(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	_, m, err := verify(context.Background(), e.Path, in.Workspace, p.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("log-directory database was raw copied")
	}
	for _, s := range m.Sources {
		if s.Path == "logs/history.db" && (!s.OnlineDatabase || s.OperationalLog) {
			t.Fatal("wrong frozen database classification")
		}
	}
}

func TestCaptureCleanupPrevalidatesOwnedSet(t *testing.T) {
	for _, kind := range []string{"complete", "replacement", "symlink", "unexpected", "root-replaced"} {
		t.Run(kind, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			parent, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			c, err := newProfileCapture(parent, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.close()
			for _, n := range []string{"a", "b"} {
				if err := c.createOwned(c.root, n, 0600); err != nil {
					t.Fatal(err)
				}
			}
			capturePath := filepath.Join(root, ".capture")
			switch kind {
			case "replacement", "symlink":
				if err := os.Rename(filepath.Join(capturePath, "b"), filepath.Join(root, "retained")); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					err = os.Symlink(filepath.Join(root, "retained"), filepath.Join(capturePath, "b"))
				} else {
					err = os.WriteFile(filepath.Join(capturePath, "b"), []byte("replacement"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "unexpected":
				if err := os.WriteFile(filepath.Join(capturePath, "other"), []byte("retain"), 0600); err != nil {
					t.Fatal(err)
				}
			case "root-replaced":
				if err := os.Rename(capturePath, capturePath+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(capturePath, 0700); err != nil {
					t.Fatal(err)
				}
				capturePath += "-old"
			}
			err = c.remove()
			if kind == "complete" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(capturePath); !os.IsNotExist(err) {
					t.Fatal("capture not removed")
				}
				return
			}
			if err == nil {
				t.Fatal("changed capture cleaned")
			}
			if _, err := os.Stat(filepath.Join(capturePath, "a")); err != nil {
				t.Fatal("cleanup removed earlier entry before full validation")
			}
		})
	}
}

func TestCaptureNativeDatabaseFailureAndCancellationDoNotPublish(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "database", true: "cancelled"}[cancelled], func(t *testing.T) {
			in, _ := fixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			in.snapshotDatabase = func(context.Context, *os.File, *os.File, *os.File, string) error {
				if cancelled {
					cancel()
					return ctx.Err()
				}
				return errors.New("private database failure")
			}
			called := false
			in.Runner = func(context.Context, string, string, string) (string, error) { called = true; return "", nil }
			if _, err := Publish(ctx, in); err == nil || strings.Contains(err.Error(), "private database") || called {
				t.Fatalf("failure truth: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(in.Workspace, "recovery", in.ID)); !os.IsNotExist(err) {
				t.Fatal("failed capture published")
			}
		})
	}
}

func rewriteManifest(t *testing.T, in PublishInput, pkg string, mutate func(*Manifest)) {
	t.Helper()
	file := filepath.Join(pkg, ManifestFile)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err = json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err = json.Unmarshal(env.Manifest, &m); err != nil {
		t.Fatal(err)
	}
	mutate(&m)
	env.Manifest, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	env.Signature = hex.EncodeToString(ed25519.Sign(in.PrivateKey, env.Manifest))
	raw, err = json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(file, 0600)
	if err = os.WriteFile(file, raw, 0440); err != nil {
		t.Fatal(err)
	}
	os.Chmod(file, 0440)
}

func TestCaptureLegacyVerificationAndReceiptValidation(t *testing.T) {
	for _, kind := range []string{"legacy", "legacy-with-capture", "missing", "policy", "inverted", "overlong", "omitted-existing", "omitted-required"} {
		t.Run(kind, func(t *testing.T) {
			in, p := fixture(t)
			e, err := Publish(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			rewriteManifest(t, in, e.Path, func(m *Manifest) {
				switch kind {
				case "legacy":
					m.Schema = Schema
					m.Capture = nil
				case "legacy-with-capture":
					m.Schema = Schema
				case "missing":
					m.Capture = nil
				case "policy":
					m.Capture.Policy = "best-effort"
				case "inverted":
					m.Capture.CompletedAt = m.Capture.StartedAt.Add(-time.Second)
				case "overlong":
					m.Capture.CompletedAt = m.Capture.StartedAt.Add(time.Hour)
				case "omitted-existing":
					m.Capture.Omitted = []string{"memories/MEMORY.md"}
				case "omitted-required":
					m.Capture.Omitted = []string{"state.db"}
				}
			})
			_, err = Verify(context.Background(), e.Path, in.Workspace, p.PublicKey)
			if (err == nil) != (kind == "legacy") {
				t.Fatalf("%s: %v", kind, err)
			}
			if kind == "legacy" {
				raw, _ := os.ReadFile(filepath.Join(e.Path, ManifestFile))
				if bytes.Contains(raw, []byte(`"capture"`)) {
					t.Fatal("legacy encoding changed")
				}
			}
		})
	}
}

// This fixture starts only a tiny SQLite/file writer, never an agent, gateway,
// provider, production database, Borg repository, or cloud transfer.
func TestCaptureNativeHermesLive(t *testing.T) {
	binary := os.Getenv("LOOM_TEST_HERMES_BINARY")
	if binary == "" {
		t.Skip("set LOOM_TEST_HERMES_BINARY to the locally built pinned package")
	}
	python := os.Getenv("LOOM_TEST_HERMES_PYTHON")
	if python == "" {
		t.Fatal("LOOM_TEST_HERMES_PYTHON required")
	}
	in, p := fixture(t)
	in.Binary = binary
	in.Runner = RunFixtureBackup
	in.snapshotDatabase = nil
	home := filepath.Join(in.Workspace, ".hermes")
	if err := os.Remove(filepath.Join(home, "state.db")); err != nil {
		t.Fatal(err)
	}
	writer := exec.Command(python, "-u", "-c", `import pathlib, sqlite3, sys, time
from utils import atomic_write_text
home = pathlib.Path(sys.argv[1])
(home / 'state').mkdir(exist_ok=True)
db = sqlite3.connect(home / 'state.db')
db.execute('pragma journal_mode=wal')
db.execute('create table fixture(n integer primary key, text text)')
db.execute("insert into fixture values(0, 'committed before capture')")
db.commit()
for n in range(1, 10000):
    db.execute('insert into fixture values(?, ?)', (n, 'committed conversation'))
    db.commit()
    atomic_write_text(home / 'state/gateway.heartbeat', str(n), tmp_prefix='.gateway_')
    atomic_write_text(home / 'cron/ticker_heartbeat', str(n), tmp_prefix='.hb_')
    atomic_write_text(home / 'cron/ticker_last_success', str(n), tmp_prefix='.hb_')
    atomic_write_text(home / 'memories/MEMORY.md', 'memory version ' + str(n))
    atomic_write_text(home / 'skills/created/SKILL.md', 'skill version ' + str(n))
    if n == 1:
        print('ready', flush=True)
    time.sleep(.03)
`, home)
	writer.Env = append(os.Environ(), "HERMES_HOME="+home, "PYTHONDONTWRITEBYTECODE=1")
	stdout, err := writer.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	writer.Stderr = os.Stderr
	if err = writer.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Process.Kill(); writer.Wait() })
	ready := make(chan error, 1)
	go func() {
		b := make([]byte, 6)
		_, err := io.ReadFull(stdout, b)
		if err == nil && string(b) != "ready\n" {
			err = errors.New("writer not ready")
		}
		ready <- err
	}()
	select {
	case err = <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("writer readiness timeout")
	}
	e, err := Publish(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Verify(context.Background(), e.Path, in.Workspace, p.PublicKey); err != nil {
		t.Fatal(err)
	}
	contents := zipContents(t, e.Path)
	if !bytes.HasPrefix(contents["memories/MEMORY.md"], []byte("memory version")) || !bytes.HasPrefix(contents["skills/created/SKILL.md"], []byte("skill version")) {
		t.Fatal("durable agent edits absent")
	}
	for n := range contents {
		if ephemeral(n) {
			t.Fatal("runtime liveness archived")
		}
	}
	restored := filepath.Join(in.Workspace, "native-import", ".hermes")
	check := exec.Command(python, "-c", `import pathlib,sqlite3,sys,zipfile
from types import SimpleNamespace
from hermes_cli.backup import run_import
run_import(SimpleNamespace(zipfile=sys.argv[1],force=True))
home=pathlib.Path(sys.argv[2])
c=sqlite3.connect('file:'+str(home/'state.db')+'?mode=ro',uri=True)
assert c.execute('pragma integrity_check').fetchone()[0]=='ok'
assert c.execute('select text from fixture where n=0').fetchone()[0]=='committed before capture'
assert c.execute('select count(*) from fixture').fetchone()[0]>=2
with zipfile.ZipFile(sys.argv[1]) as z:
    for p in ('SOUL.md','config.yaml','memories/MEMORY.md','skills/created/SKILL.md','sessions/a.json'):
        assert (home/p).read_bytes()==z.read(p)
`, filepath.Join(e.Path, PayloadFile), restored)
	check.Env = []string{"HOME=" + filepath.Dir(restored), "HERMES_HOME=" + restored, "HERMES_MANAGED=true", "PYTHONDONTWRITEBYTECODE=1", "PATH=/usr/bin:/bin"}
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("restored SQLite: %v %s", err, out)
	}
	t.Logf("native capture verified: %d entries, ZIP %d bytes", len(contents), e.Files[1].Size)
}
