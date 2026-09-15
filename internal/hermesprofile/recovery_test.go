package hermesprofile

import (
	"archive/zip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func fixture(t *testing.T) (PublishInput, Policy) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root = filepath.Join(root, ".loom-acceptance", "morathustra")
	for _, name := range []string{".hermes/memories", ".hermes/skills/created", ".hermes/sessions", ".hermes/logs", ".hermes/cron", "recovery"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{"SOUL.md": "Fixture personality", "config.yaml": "memory: {}\n", "state.db": "synthetic online snapshot", "memories/MEMORY.md": "fixture memory", "skills/created/SKILL.md": "fixture approved skill", "sessions/a.json": "fixture session"} {
		if err := os.WriteFile(filepath.Join(root, ".hermes", name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	in := PublishInput{Workspace: root, ID: "recovery-fixture", CreatedAt: time.Now().UTC().Truncate(time.Second), PrivateKey: priv, Binary: "/nix/store/fixture-hermes-agent-0.21.0/bin/hermes", Runner: fakeBackup}
	// Synthetic unit fixtures deliberately do not contain a SQLite database.
	// Production has no raw-copy fallback; the native test exercises SQLite.
	in.snapshotDatabase = func(ctx context.Context, parent, source, target *os.File, rel string) error {
		f, err := openAt(target, filepath.Base(rel), false)
		if err != nil {
			return err
		}
		f.Close()
		fd, err := unix.Openat(int(target.Fd()), filepath.Base(rel), unix.O_WRONLY|unix.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		out := os.NewFile(uintptr(fd), rel)
		defer out.Close()
		_, err = io.Copy(out, &contextReader{ctx, source})
		return err
	}
	return in, Policy{Enabled: true, Workspace: root, PublicKey: pub}
}
func fakeBackup(ctx context.Context, binary, profile, output string) (string, error) {
	f, err := os.Create(output)
	if err != nil {
		return "", err
	}
	z := zip.NewWriter(f)
	err = filepath.WalkDir(profile, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(profile, p)
		if rel == "." {
			return nil
		}
		if excluded(filepath.ToSlash(rel)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSocket != 0 {
			if _, ok := runtimeSocketMode(filepath.ToSlash(rel)); !ok {
				return errors.New("unexpected socket")
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		data, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		h := &zip.FileHeader{Name: filepath.ToSlash(rel), Method: zip.Deflate}
		info, e := d.Info()
		if e != nil {
			return e
		}
		h.SetMode(info.Mode())
		w, e := z.CreateHeader(h)
		if e != nil {
			return e
		}
		_, e = w.Write(data)
		return e
	})
	if e := z.Close(); err == nil {
		err = e
	}
	if e := f.Close(); err == nil {
		err = e
	}
	return strings.Repeat("a", 64), err
}
func TestRecoveryPublishReplayRestoreEvidence(t *testing.T) {
	in, p := fixture(t)
	ctx := context.Background()
	e, err := Publish(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(e.Path)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(e.Path, ManifestFile))
	calls := 0
	in.Runner = func(context.Context, string, string, string) (string, error) {
		calls++
		return "", errors.New("must not rerun")
	}
	again, err := Publish(ctx, in)
	if err != nil || !reflect.DeepEqual(e, again) || calls != 0 {
		t.Fatalf("replay: %v %v", again, err)
	}
	after, _ := os.Stat(e.Path)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("replay rewrote package")
	}
	got, err := Check(ctx, p, in.CreatedAt.Add(time.Minute))
	if err != nil || len(got) != 1 {
		t.Fatalf("coverage %v %v", got, err)
	}
	if _, err := Check(ctx, p, in.CreatedAt.Add(MaxAge+time.Second)); err == nil {
		t.Fatal("stale accepted")
	}
	if _, err := Check(ctx, p, in.CreatedAt.Add(-time.Second)); err == nil {
		t.Fatal("future accepted")
	}
	bad := in
	bad.CreatedAt = bad.CreatedAt.Add(time.Second)
	if _, err := Publish(ctx, bad); err == nil {
		t.Fatal("conflicting replay accepted")
	}
	restoreRoot, _ := filepath.EvalSymlinks(t.TempDir())
	restored := filepath.Join(restoreRoot, in.ID)
	os.Mkdir(restored, 0750)
	for _, name := range []string{ManifestFile, PayloadFile} {
		b, _ := os.ReadFile(filepath.Join(e.Path, name))
		if err := os.WriteFile(filepath.Join(restored, name), b, 0440); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Verify(ctx, restored, in.Workspace, p.PublicKey); err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("no manifest")
	}
}
func TestRecoveryRefusesUnsafeAndMutatingSources(t *testing.T) {
	for _, scenario := range []string{"symlink", "hardlink", "fifo", "socket", "ancestor", "database-swap", "mutation", "manifest-swap", "payload-swap", "root-swap", "directory-aba", "workspace-aba", "staging-aba", "incomplete", "interrupt", "unknown-zip", "external-provider"} {
		t.Run(scenario, func(t *testing.T) {
			in, _ := fixture(t)
			src := filepath.Join(in.Workspace, ".hermes")
			outside := filepath.Join(filepath.Dir(in.Workspace), "outside")
			os.WriteFile(outside, []byte("outside"), 0600)
			switch scenario {
			case "symlink":
				os.Symlink(outside, filepath.Join(src, "bad"))
			case "hardlink":
				os.Link(outside, filepath.Join(src, "bad"))
			case "fifo":
				unix.Mkfifo(filepath.Join(src, "bad"), 0600)
			case "socket":
				fd, e := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
				if e != nil {
					t.Fatal(e)
				}
				defer unix.Close(fd)
				socketDir, err := os.MkdirTemp("", "hermes-socket-")
				if err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(socketDir)
				socketPath := filepath.Join(socketDir, "s")
				if err := unix.Bind(fd, &unix.SockaddrUnix{Name: socketPath}); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(socketPath, filepath.Join(src, "bad")); err != nil {
					t.Fatal(err)
				}
			case "ancestor":
				os.Rename(in.Workspace, in.Workspace+"-old")
				os.Symlink(in.Workspace+"-old", in.Workspace)
			case "external-provider":
				os.WriteFile(filepath.Join(src, "config.yaml"), []byte("memory:\n  provider: honcho\n"), 0600)
			}
			original := in.Runner
			in.Runner = func(ctx context.Context, b, h, o string) (string, error) {
				sha, err := original(ctx, b, h, o)
				if err != nil {
					return "", err
				}
				switch scenario {
				case "database-swap":
					os.Rename(filepath.Join(h, "state.db"), filepath.Join(h, "old.db"))
					os.WriteFile(filepath.Join(h, "state.db"), []byte("substitute"), 0600)
				case "mutation":
					os.WriteFile(filepath.Join(h, "SOUL.md"), []byte("mutated"), 0600)
				case "root-swap":
					os.Rename(h, h+"-old")
					os.Mkdir(h, 0700)
				case "directory-aba":
					d := filepath.Join(h, "memories")
					os.Rename(d, d+"-old")
					os.Rename(d+"-old", d)
				case "workspace-aba", "staging-aba":
					d := in.Workspace
					if scenario == "staging-aba" {
						d = filepath.Join(in.Workspace, "recovery", ".staging")
					}
					os.Rename(d, d+"-old")
					os.Rename(d+"-old", d)
				case "interrupt":
					return "", context.Canceled
				case "incomplete", "unknown-zip":
					f, _ := os.Create(o)
					z := zip.NewWriter(f)
					name := "SOUL.md"
					if scenario == "unknown-zip" {
						name = "../outside"
					}
					hd := &zip.FileHeader{Name: name}
					hd.SetMode(0600)
					w, _ := z.CreateHeader(hd)
					w.Write([]byte("bad"))
					z.Close()
					f.Close()
				}
				return sha, nil
			}
			in.beforePublish = func() {
				switch scenario {
				case "manifest-swap", "payload-swap":
					entries, _ := os.ReadDir(filepath.Join(in.Workspace, "recovery", ".staging"))
					name := ManifestFile
					if scenario == "payload-swap" {
						name = PayloadFile
					}
					dest := filepath.Join(in.Workspace, "recovery", ".staging", entries[0].Name(), name)
					os.Remove(dest)
					os.Symlink(outside, dest)
				}
			}
			if _, err := Publish(context.Background(), in); err == nil {
				t.Fatal("unsafe source accepted")
			}
			if _, err := os.Lstat(filepath.Join(in.Workspace, "recovery", in.ID)); err == nil {
				t.Fatal("failed attempt published")
			}
		})
	}
}

func TestRecoveryCapturedLogAndOrdinaryBytesStayFrozen(t *testing.T) {
	for _, scenario := range []string{"unchanged", "append", "symlink", "hardlink", "fifo", "namespace", "directory-aba", "mode", "new-entry", "outside-logs", "database-content"} {
		t.Run(scenario, func(t *testing.T) {
			in, policy := fixture(t)
			log := filepath.Join(in.Workspace, ".hermes", "logs", "agent.log")
			if err := os.WriteFile(log, []byte("initial log"), 0600); err != nil {
				t.Fatal(err)
			}
			original := in.Runner
			in.Runner = func(ctx context.Context, b, h, o string) (string, error) {
				log := filepath.Join(h, "logs", "agent.log")
				// Even logs in the private capture are immutable. The live log may
				// continue to change, independently of these adversarial mutations.
				digest, err := original(ctx, b, h, o)
				if err != nil {
					return "", err
				}
				switch scenario {
				case "append":
					os.WriteFile(log, []byte("log written after archive closed"), 0600)
				case "symlink", "hardlink", "fifo", "namespace":
					os.Rename(log, log+".retained")
					switch scenario {
					case "symlink":
						os.Symlink(log+".retained", log)
					case "hardlink":
						os.Link(log+".retained", log)
					case "fifo":
						unix.Mkfifo(log, 0600)
					case "namespace":
						os.WriteFile(log, []byte("substitute"), 0600)
					}
				case "directory-aba":
					d := filepath.Dir(log)
					os.Rename(d, d+"-old")
					os.Rename(d+"-old", d)
				case "mode":
					os.Chmod(log, 0644)
				case "new-entry":
					os.WriteFile(log+".new", []byte("new entry"), 0600)
				case "outside-logs":
					os.WriteFile(filepath.Join(h, "memories", "MEMORY.md"), []byte("mutated memory"), 0600)
				case "database-content":
					os.WriteFile(filepath.Join(h, "state.db"), []byte("mutated private database"), 0600)
				}
				return digest, nil
			}
			e, err := Publish(context.Background(), in)
			if scenario != "unchanged" {
				if err == nil {
					t.Fatal("unsafe log or ordinary mutation accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, m, err := verify(context.Background(), e.Path, in.Workspace, policy.PublicKey)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, source := range m.Sources {
				if source.Path == "logs/agent.log" {
					found = source.OperationalLog && !source.OnlineDatabase && source.SHA256 != ""
				}
			}
			if !found {
				t.Fatal("missing explicit log snapshot classification")
			}
		})
	}
}
func TestRecoveryInterruptionRetryKeepsEvidence(t *testing.T) {
	in, p := fixture(t)
	original := in.Runner
	in.Runner = func(ctx context.Context, b, h, o string) (string, error) {
		original(ctx, b, h, o)
		return "", context.Canceled
	}
	if _, err := Publish(context.Background(), in); err == nil {
		t.Fatal("interruption succeeded")
	}
	if _, err := Check(context.Background(), p, in.CreatedAt); err == nil {
		t.Fatal("staging accepted")
	}
	in.Runner = original
	if _, err := Publish(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(in.Workspace, "recovery", ".staging"))
	if len(entries) != 1 {
		t.Fatalf("interrupted evidence not retained: %d", len(entries))
	}
}
func TestRecoveryAllowsWALActivity(t *testing.T) {
	in, _ := fixture(t)
	src := filepath.Join(in.Workspace, ".hermes")
	os.WriteFile(filepath.Join(src, "state.db-wal"), []byte("WAL"), 0600)
	original := in.Runner
	in.Runner = func(ctx context.Context, b, h, o string) (string, error) {
		sha, err := original(ctx, b, h, o)
		os.WriteFile(filepath.Join(src, "state.db"), []byte("later online state"), 0600)
		os.WriteFile(filepath.Join(src, "state.db-wal"), []byte("later WAL"), 0600)
		return sha, err
	}
	if _, err := Publish(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}
func TestRecoveryRejectsTamperedEvidence(t *testing.T) {
	for _, kind := range []string{"signature", "payload", "manifest-link", "extra", "mode", "wrong-key"} {
		t.Run(kind, func(t *testing.T) {
			in, p := fixture(t)
			e, err := Publish(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			os.Chmod(e.Path, 0750)
			switch kind {
			case "signature":
				f := filepath.Join(e.Path, ManifestFile)
				os.Chmod(f, 0640)
				raw, _ := os.ReadFile(f)
				raw[len(raw)-4] ^= 1
				os.WriteFile(f, raw, 0440)
				os.Chmod(f, 0440)
			case "payload":
				f := filepath.Join(e.Path, PayloadFile)
				os.Chmod(f, 0640)
				os.WriteFile(f, []byte("wrong"), 0440)
				os.Chmod(f, 0440)
			case "manifest-link":
				f := filepath.Join(e.Path, ManifestFile)
				os.Rename(f, f+"-saved")
				os.Symlink(f+"-saved", f)
			case "extra":
				os.WriteFile(filepath.Join(e.Path, "extra"), []byte("wrong"), 0440)
			case "mode":
				os.Chmod(filepath.Join(e.Path, PayloadFile), 0660)
			case "wrong-key":
				p.PublicKey = make([]byte, 32)
			}
			os.Chmod(e.Path, 0750)
			if _, err := Check(context.Background(), p, in.CreatedAt); err == nil {
				t.Fatal("tampering accepted")
			}
		})
	}
}
func TestRecoveryPolicyFailsClosed(t *testing.T) {
	for _, values := range [][2]string{{"true", ""}, {"yes", ""}, {"true", "xyz"}, {"false", "bad"}} {
		if _, err := ParsePolicy(values[0], values[1]); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
	p, err := ParsePolicy("true", hex.EncodeToString(make([]byte, 32)))
	if err != nil || !p.Enabled {
		t.Fatal(err)
	}
	if _, err := Check(context.Background(), Policy{}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

// Frozen with the unchanged f9126000 recovery implementation before W2 product edits.
// Synthetic public fixture key: Ed25519 all-zero seed; never production material.
const frozenLegacyV1Manifest = "eyJtYW5pZmVzdCI6eyJzY2hlbWEiOiJsb29tLmhlcm1lcy5yZWNvdmVyeS52MSIsImlkIjoicmV0YWluZWQtdjEiLCJ2ZXJzaW9uIjoiMC4yMS4wIiwicmV2aXNpb24iOiIyOTExMmJlZjA5OTI3NDIyOWNhZGZmNzljZGZmN2JmN2I5OWM0Yjc3IiwiYmluYXJ5IjoiL25peC9zdG9yZS9maXh0dXJlLWhlcm1lcy1hZ2VudC0wLjIxLjAvYmluL2hlcm1lcyIsImJpbmFyeV9zaGEyNTYiOiJhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhIiwid29ya3NwYWNlIjoiL3Nydi9sb29tL2FnZW50cy9tb3JhdGh1c3RyYSIsInByb2ZpbGUiOiIvc3J2L2xvb20vYWdlbnRzL21vcmF0aHVzdHJhLy5oZXJtZXMiLCJzb3VyY2Vfcm9vdCI6eyJkZXZpY2UiOjE2Nzc3MjMyLCJpbm9kZSI6MjIyMTE3NjIyLCJtb2RlIjoxNjgzMiwib3duZXIiOjUwMSwibGlua3MiOjEwLCJzaXplIjozMjAsIm1vZGlmaWVkX25zIjoxNzg5MDUwMzY2OTAyMTY4MTk0LCJjaGFuZ2VkX25zIjoxNzg5MDUwMzY2OTAyMTY4MTk0fSwiY3JlYXRlZF9hdCI6IjIwMjYtMDktMTBUMTQ6MjY6MDZaIiwic291cmNlcyI6W3sicGF0aCI6IlNPVUwubWQiLCJpZGVudGl0eSI6eyJkZXZpY2UiOjE2Nzc3MjMyLCJpbm9kZSI6MjIyMTE3NjM5LCJtb2RlIjozMzE1Miwib3duZXIiOjUwMSwibGlua3MiOjEsInNpemUiOjE5LCJtb2RpZmllZF9ucyI6MTc4OTA1MDM2NjkwMTk4MTAwMCwiY2hhbmdlZF9ucyI6MTc4OTA1MDM2NjkwNDk4ODE3Nn0sInNoYTI1NiI6IjliMDhjYzA1MzM3MGY0NTQ4YzQzZDllZDQ4MzlkNzdlODRlODNmMmEyNTBmNTc5MWE4MTEzMWM2YzZhNjVhYmEiLCJvbmxpbmVfZGF0YWJhc2UiOmZhbHNlLCJvcGVyYXRpb25hbF9sb2ciOmZhbHNlfSx7InBhdGgiOiJjb25maWcueWFtbCIsImlkZW50aXR5Ijp7ImRldmljZSI6MTY3NzcyMzIsImlub2RlIjoyMjIxMTc2NDAsIm1vZGUiOjMzMTUyLCJvd25lciI6NTAxLCJsaW5rcyI6MSwic2l6ZSI6MTEsIm1vZGlmaWVkX25zIjoxNzg5MDUwMzY2OTAyMTE3MDAwLCJjaGFuZ2VkX25zIjoxNzg5MDUwMzY2OTEyMTAyMzIwfSwic2hhMjU2IjoiODNkYTA1OGYxODAyMDcxMWE0YjRlMDYwMjRkNTFkZjNmNmNiZGQ3NGU0M2FmNGNhODI0OGY1N2FmOTUyNjBiYiIsIm9ubGluZV9kYXRhYmFzZSI6ZmFsc2UsIm9wZXJhdGlvbmFsX2xvZyI6ZmFsc2V9LHsicGF0aCI6Im1lbW9yaWVzL01FTU9SWS5tZCIsImlkZW50aXR5Ijp7ImRldmljZSI6MTY3NzcyMzIsImlub2RlIjoyMjIxMTc2NDQsIm1vZGUiOjMzMTUyLCJvd25lciI6NTAxLCJsaW5rcyI6MSwic2l6ZSI6MTQsIm1vZGlmaWVkX25zIjoxNzg5MDUwMzY2OTAyMzQ0MDAwLCJjaGFuZ2VkX25zIjoxNzg5MDUwMzY2OTE1NjU1ODkyfSwic2hhMjU2IjoiZGMxMjk2NGE4NTE1ZTAzMmViOWEzM2RjMTFmZjQ2OTQ2YWVhY2VhODMwNjkxZDQ0MDVlMTZkZWI3MGY5NjRlYyIsIm9ubGluZV9kYXRhYmFzZSI6ZmFsc2UsIm9wZXJhdGlvbmFsX2xvZyI6ZmFsc2V9LHsicGF0aCI6InNlc3Npb25zL2EuanNvbiIsImlkZW50aXR5Ijp7ImRldmljZSI6MTY3NzcyMzIsImlub2RlIjoyMjIxMTc2NDYsIm1vZGUiOjMzMTUyLCJvd25lciI6NTAxLCJsaW5rcyI6MSwic2l6ZSI6MTUsIm1vZGlmaWVkX25zIjoxNzg5MDUwMzY2OTAyNTY4MDAwLCJjaGFuZ2VkX25zIjoxNzg5MDUwMzY2OTE5Mjc4NDY0fSwic2hhMjU2IjoiY2FmOGY1ZmIyNGI1YmY0OWE2MmMxZDc4YjU1Nzc1ZjI4ZTZiY2ZhNzM5NTg2ZWYyZTQyYmFjOTBkNzIxMzM4MSIsIm9ubGluZV9kYXRhYmFzZSI6ZmFsc2UsIm9wZXJhdGlvbmFsX2xvZyI6ZmFsc2V9LHsicGF0aCI6InNraWxscy9jcmVhdGVkL1NLSUxMLm1kIiwiaWRlbnRpdHkiOnsiZGV2aWNlIjoxNjc3NzIzMiwiaW5vZGUiOjIyMjExNzY0OSwibW9kZSI6MzMxNTIsIm93bmVyIjo1MDEsImxpbmtzIjoxLCJzaXplIjoyMiwibW9kaWZpZWRfbnMiOjE3ODkwNTAzNjY5MDI0NTQwMDAsImNoYW5nZWRfbnMiOjE3ODkwNTAzNjY5MjQzODg2NzV9LCJzaGEyNTYiOiJjN2NmOWVkMjliNjEwYjM4ZjYzYTFkYWE1MWU4NTg5Mjg1NWJlMjFmMjk3N2QxMGI0ZWI4N2M1ZWE0ZDM2ODgyIiwib25saW5lX2RhdGFiYXNlIjpmYWxzZSwib3BlcmF0aW9uYWxfbG9nIjpmYWxzZX0seyJwYXRoIjoic3RhdGUuZGIiLCJpZGVudGl0eSI6eyJkZXZpY2UiOjE2Nzc3MjMyLCJpbm9kZSI6MjIyMTE3NjUwLCJtb2RlIjozMzE1Miwib3duZXIiOjUwMSwibGlua3MiOjEsInNpemUiOjI1LCJtb2RpZmllZF9ucyI6MTc4OTA1MDM2NjkyODA3OTM3MiwiY2hhbmdlZF9ucyI6MTc4OTA1MDM2NjkyODEyNjEyMn0sInNoYTI1NiI6IjY3ZDU4M2QxYjIxOGVmZDA3NTE3ZTE1ODI2OWU2N2I4MjNiOTY0MzU5ZjRhMTcwYTEyMzcxZDg2ODA2MWE3NzgiLCJvbmxpbmVfZGF0YWJhc2UiOnRydWUsIm9wZXJhdGlvbmFsX2xvZyI6ZmFsc2V9XSwiaW52ZW50b3J5IjpbeyJwYXRoIjoiU09VTC5tZCIsInNpemUiOjE5LCJtb2RlIjozODQsInNoYTI1NiI6IjliMDhjYzA1MzM3MGY0NTQ4YzQzZDllZDQ4MzlkNzdlODRlODNmMmEyNTBmNTc5MWE4MTEzMWM2YzZhNjVhYmEifSx7InBhdGgiOiJjb25maWcueWFtbCIsInNpemUiOjExLCJtb2RlIjozODQsInNoYTI1NiI6IjgzZGEwNThmMTgwMjA3MTFhNGI0ZTA2MDI0ZDUxZGYzZjZjYmRkNzRlNDNhZjRjYTgyNDhmNTdhZjk1MjYwYmIifSx7InBhdGgiOiJtZW1vcmllcy9NRU1PUlkubWQiLCJzaXplIjoxNCwibW9kZSI6Mzg0LCJzaGEyNTYiOiJkYzEyOTY0YTg1MTVlMDMyZWI5YTMzZGMxMWZmNDY5NDZhZWFjZWE4MzA2OTFkNDQwNWUxNmRlYjcwZjk2NGVjIn0seyJwYXRoIjoic2Vzc2lvbnMvYS5qc29uIiwic2l6ZSI6MTUsIm1vZGUiOjM4NCwic2hhMjU2IjoiY2FmOGY1ZmIyNGI1YmY0OWE2MmMxZDc4YjU1Nzc1ZjI4ZTZiY2ZhNzM5NTg2ZWYyZTQyYmFjOTBkNzIxMzM4MSJ9LHsicGF0aCI6InNraWxscy9jcmVhdGVkL1NLSUxMLm1kIiwic2l6ZSI6MjIsIm1vZGUiOjM4NCwic2hhMjU2IjoiYzdjZjllZDI5YjYxMGIzOGY2M2ExZGFhNTFlODU4OTI4NTViZTIxZjI5NzdkMTBiNGViODdjNWVhNGQzNjg4MiJ9LHsicGF0aCI6InN0YXRlLmRiIiwic2l6ZSI6MjUsIm1vZGUiOjM4NCwic2hhMjU2IjoiNjdkNTgzZDFiMjE4ZWZkMDc1MTdlMTU4MjY5ZTY3YjgyM2I5NjQzNTlmNGExNzBhMTIzNzFkODY4MDYxYTc3OCJ9XSwicGF5bG9hZCI6eyJwYXRoIjoicHJvZmlsZS56aXAiLCJzaXplIjo4ODAsIm1vZGUiOjI4OCwic2hhMjU2IjoiNTUzZDk1NGM1MDVmNmM2YmIzYjViNjM4NWIzN2IyMjJhYTcwZTMzYTE2Yzk4ZjRkZjkyZDc5MGFiNDE1ZDM1YyJ9fSwic2lnbmF0dXJlIjoiNmQwMGVhMGU0YTFkZWJkOTc0MTIyZTE0MzFiZWRiZWU1YmQ1ZDQ4MWE0ZDg5MTA4Yjc4NmJkM2JlNTRiYjAwODk3YzA0NjZmMGRjOThmZmFlN2MyYWM3NDE2NmQzZWYyMjkzYmQzMDY4ZWU4MTdlMjAyMmVlMzczNDM4MTBmMGIifQ=="
const frozenLegacyV1Payload = "UEsDBBQACAAIAAAAAAAAAAAAAAAAAAAAAAAHAAAAU09VTC5tZHLLrCgpLUpVKEgtKs7PS8zJLKkEBAAA//9QSwcIogCYIxkAAAATAAAAUEsDBBQACAAIAAAAAAAAAAAAAAAAAAAAAAALAAAAY29uZmlnLnlhbWzKTc3NL6q0Uqiu5QIEAAD//1BLBwgFinddEQAAAAsAAABQSwMEFAAIAAgAAAAAAAAAAAAAAAAAAAAAABIAAABtZW1vcmllcy9NRU1PUlkubWRKy6woKS1KVchNzc0vqgQEAAD//1BLBwjPHFOCFAAAAA4AAABQSwMEFAAIAAgAAAAAAAAAAAAAAAAAAAAAAA8AAABzZXNzaW9ucy9hLmpzb25Ky6woKS1KVShOLS7OzM8DBAAA//9QSwcI/vBEjRUAAAAPAAAAUEsDBBQACAAIAAAAAAAAAAAAAAAAAAAAAAAXAAAAc2tpbGxzL2NyZWF0ZWQvU0tJTEwubWRKy6woKS1KVUgsKCjKL0tNUSjOzszJAQQAAP//UEsHCCURRiYcAAAAFgAAAFBLAwQUAAgACAAAAAAAAAAAAAAAAAAAAAAACAAAAHN0YXRlLmRiKq7MK8lILclMVsjPy8nMS1UozkssKM7ILwEEAAD//1BLBwiQPW9THwAAABkAAABQSwECFAMUAAgACAAAAAAAogCYIxkAAAATAAAABwAAAAAAAAAAAAAAgIEAAAAAU09VTC5tZFBLAQIUAxQACAAIAAAAAAAFinddEQAAAAsAAAALAAAAAAAAAAAAAACAgU4AAABjb25maWcueWFtbFBLAQIUAxQACAAIAAAAAADPHFOCFAAAAA4AAAASAAAAAAAAAAAAAACAgZgAAABtZW1vcmllcy9NRU1PUlkubWRQSwECFAMUAAgACAAAAAAA/vBEjRUAAAAPAAAADwAAAAAAAAAAAAAAgIHsAAAAc2Vzc2lvbnMvYS5qc29uUEsBAhQDFAAIAAgAAAAAACURRiYcAAAAFgAAABcAAAAAAAAAAAAAAICBPgEAAHNraWxscy9jcmVhdGVkL1NLSUxMLm1kUEsBAhQDFAAIAAgAAAAAAJA9b1MfAAAAGQAAAAgAAAAAAAAAAAAAAICBnwEAAHN0YXRlLmRiUEsFBgAAAAAGAAYAZgEAAPQBAAAAAA=="
const frozenLegacyV2Manifest = "eyJtYW5pZmVzdCI6eyJzY2hlbWEiOiJsb29tLmhlcm1lcy5yZWNvdmVyeS52MiIsImlkIjoicmV0YWluZWQtdjIiLCJ2ZXJzaW9uIjoiMC4yMS4wIiwicmV2aXNpb24iOiIyOTExMmJlZjA5OTI3NDIyOWNhZGZmNzljZGZmN2JmN2I5OWM0Yjc3IiwiYmluYXJ5IjoiL25peC9zdG9yZS9maXh0dXJlLWhlcm1lcy1hZ2VudC0wLjIxLjAvYmluL2hlcm1lcyIsImJpbmFyeV9zaGEyNTYiOiJhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhIiwid29ya3NwYWNlIjoiL3Nydi9sb29tL2FnZW50cy9tb3JhdGh1c3RyYSIsInByb2ZpbGUiOiIvc3J2L2xvb20vYWdlbnRzL21vcmF0aHVzdHJhLy5oZXJtZXMiLCJzb3VyY2Vfcm9vdCI6eyJkZXZpY2UiOjE2Nzc3MjMyLCJpbm9kZSI6MjIyMTE3NjIyLCJtb2RlIjoxNjgzMiwib3duZXIiOjUwMSwibGlua3MiOjEwLCJzaXplIjozMjAsIm1vZGlmaWVkX25zIjoxNzg5MDUwMzY2OTAyMTY4MTk0LCJjaGFuZ2VkX25zIjoxNzg5MDUwMzY2OTAyMTY4MTk0fSwiY3JlYXRlZF9hdCI6IjIwMjYtMDktMTBUMTQ6MjY6MDZaIiwic291cmNlcyI6W3sicGF0aCI6IlNPVUwubWQiLCJpZGVudGl0eSI6eyJkZXZpY2UiOjE2Nzc3MjMyLCJpbm9kZSI6MjIyMTE3NjU3LCJtb2RlIjozMzE1Miwib3duZXIiOjUwMSwibGlua3MiOjEsInNpemUiOjE5LCJtb2RpZmllZF9ucyI6MTc4OTA1MDM2NjkwMTk4MTAwMCwiY2hhbmdlZF9ucyI6MTc4OTA1MDM2Njk1NDE5NzU5M30sInNoYTI1NiI6IjliMDhjYzA1MzM3MGY0NTQ4YzQzZDllZDQ4MzlkNzdlODRlODNmMmEyNTBmNTc5MWE4MTEzMWM2YzZhNjVhYmEiLCJvbmxpbmVfZGF0YWJhc2UiOmZhbHNlLCJvcGVyYXRpb25hbF9sb2ciOmZhbHNlfSx7InBhdGgiOiJjb25maWcueWFtbCIsImlkZW50aXR5Ijp7ImRldmljZSI6MTY3NzcyMzIsImlub2RlIjoyMjIxMTc2NTgsIm1vZGUiOjMzMTUyLCJvd25lciI6NTAxLCJsaW5rcyI6MSwic2l6ZSI6MTEsIm1vZGlmaWVkX25zIjoxNzg5MDUwMzY2OTAyMTE3MDAwLCJjaGFuZ2VkX25zIjoxNzg5MDUwMzY2OTU4MjgzNzUzfSwic2hhMjU2IjoiODNkYTA1OGYxODAyMDcxMWE0YjRlMDYwMjRkNTFkZjNmNmNiZGQ3NGU0M2FmNGNhODI0OGY1N2FmOTUyNjBiYiIsIm9ubGluZV9kYXRhYmFzZSI6ZmFsc2UsIm9wZXJhdGlvbmFsX2xvZyI6ZmFsc2V9LHsicGF0aCI6Im1lbW9yaWVzL01FTU9SWS5tZCIsImlkZW50aXR5Ijp7ImRldmljZSI6MTY3NzcyMzIsImlub2RlIjoyMjIxMTc2NjIsIm1vZGUiOjMzMTUyLCJvd25lciI6NTAxLCJsaW5rcyI6MSwic2l6ZSI6MTQsIm1vZGlmaWVkX25zIjoxNzg5MDUwMzY2OTAyMzQ0MDAwLCJjaGFuZ2VkX25zIjoxNzg5MDUwMzY2OTYyODQzODMzfSwic2hhMjU2IjoiZGMxMjk2NGE4NTE1ZTAzMmViOWEzM2RjMTFmZjQ2OTQ2YWVhY2VhODMwNjkxZDQ0MDVlMTZkZWI3MGY5NjRlYyIsIm9ubGluZV9kYXRhYmFzZSI6ZmFsc2UsIm9wZXJhdGlvbmFsX2xvZyI6ZmFsc2V9LHsicGF0aCI6InNlc3Npb25zL2EuanNvbiIsImlkZW50aXR5Ijp7ImRldmljZSI6MTY3NzcyMzIsImlub2RlIjoyMjIxMTc2NjQsIm1vZGUiOjMzMTUyLCJvd25lciI6NTAxLCJsaW5rcyI6MSwic2l6ZSI6MTUsIm1vZGlmaWVkX25zIjoxNzg5MDUwMzY2OTAyNTY4MDAwLCJjaGFuZ2VkX25zIjoxNzg5MDUwMzY2OTY3NDc5NzA2fSwic2hhMjU2IjoiY2FmOGY1ZmIyNGI1YmY0OWE2MmMxZDc4YjU1Nzc1ZjI4ZTZiY2ZhNzM5NTg2ZWYyZTQyYmFjOTBkNzIxMzM4MSIsIm9ubGluZV9kYXRhYmFzZSI6ZmFsc2UsIm9wZXJhdGlvbmFsX2xvZyI6ZmFsc2V9LHsicGF0aCI6InNraWxscy9jcmVhdGVkL1NLSUxMLm1kIiwiaWRlbnRpdHkiOnsiZGV2aWNlIjoxNjc3NzIzMiwiaW5vZGUiOjIyMjExNzY2NywibW9kZSI6MzMxNTIsIm93bmVyIjo1MDEsImxpbmtzIjoxLCJzaXplIjoyMiwibW9kaWZpZWRfbnMiOjE3ODkwNTAzNjY5MDI0NTQwMDAsImNoYW5nZWRfbnMiOjE3ODkwNTAzNjY5NzIyNjUxNjN9LCJzaGEyNTYiOiJjN2NmOWVkMjliNjEwYjM4ZjYzYTFkYWE1MWU4NTg5Mjg1NWJlMjFmMjk3N2QxMGI0ZWI4N2M1ZWE0ZDM2ODgyIiwib25saW5lX2RhdGFiYXNlIjpmYWxzZSwib3BlcmF0aW9uYWxfbG9nIjpmYWxzZX0seyJwYXRoIjoic3RhdGUuZGIiLCJpZGVudGl0eSI6eyJkZXZpY2UiOjE2Nzc3MjMyLCJpbm9kZSI6MjIyMTE3NjY4LCJtb2RlIjozMzE1Miwib3duZXIiOjUwMSwibGlua3MiOjEsInNpemUiOjI1LCJtb2RpZmllZF9ucyI6MTc4OTA1MDM2Njk3NzM5Mjk1NywiY2hhbmdlZF9ucyI6MTc4OTA1MDM2Njk3NzQyNjI5MH0sInNoYTI1NiI6IjY3ZDU4M2QxYjIxOGVmZDA3NTE3ZTE1ODI2OWU2N2I4MjNiOTY0MzU5ZjRhMTcwYTEyMzcxZDg2ODA2MWE3NzgiLCJvbmxpbmVfZGF0YWJhc2UiOnRydWUsIm9wZXJhdGlvbmFsX2xvZyI6ZmFsc2V9XSwiaW52ZW50b3J5IjpbeyJwYXRoIjoiU09VTC5tZCIsInNpemUiOjE5LCJtb2RlIjozODQsInNoYTI1NiI6IjliMDhjYzA1MzM3MGY0NTQ4YzQzZDllZDQ4MzlkNzdlODRlODNmMmEyNTBmNTc5MWE4MTEzMWM2YzZhNjVhYmEifSx7InBhdGgiOiJjb25maWcueWFtbCIsInNpemUiOjExLCJtb2RlIjozODQsInNoYTI1NiI6IjgzZGEwNThmMTgwMjA3MTFhNGI0ZTA2MDI0ZDUxZGYzZjZjYmRkNzRlNDNhZjRjYTgyNDhmNTdhZjk1MjYwYmIifSx7InBhdGgiOiJtZW1vcmllcy9NRU1PUlkubWQiLCJzaXplIjoxNCwibW9kZSI6Mzg0LCJzaGEyNTYiOiJkYzEyOTY0YTg1MTVlMDMyZWI5YTMzZGMxMWZmNDY5NDZhZWFjZWE4MzA2OTFkNDQwNWUxNmRlYjcwZjk2NGVjIn0seyJwYXRoIjoic2Vzc2lvbnMvYS5qc29uIiwic2l6ZSI6MTUsIm1vZGUiOjM4NCwic2hhMjU2IjoiY2FmOGY1ZmIyNGI1YmY0OWE2MmMxZDc4YjU1Nzc1ZjI4ZTZiY2ZhNzM5NTg2ZWYyZTQyYmFjOTBkNzIxMzM4MSJ9LHsicGF0aCI6InNraWxscy9jcmVhdGVkL1NLSUxMLm1kIiwic2l6ZSI6MjIsIm1vZGUiOjM4NCwic2hhMjU2IjoiYzdjZjllZDI5YjYxMGIzOGY2M2ExZGFhNTFlODU4OTI4NTViZTIxZjI5NzdkMTBiNGViODdjNWVhNGQzNjg4MiJ9LHsicGF0aCI6InN0YXRlLmRiIiwic2l6ZSI6MjUsIm1vZGUiOjM4NCwic2hhMjU2IjoiNjdkNTgzZDFiMjE4ZWZkMDc1MTdlMTU4MjY5ZTY3YjgyM2I5NjQzNTlmNGExNzBhMTIzNzFkODY4MDYxYTc3OCJ9XSwicGF5bG9hZCI6eyJwYXRoIjoicHJvZmlsZS56aXAiLCJzaXplIjo4ODAsIm1vZGUiOjI4OCwic2hhMjU2IjoiNTUzZDk1NGM1MDVmNmM2YmIzYjViNjM4NWIzN2IyMjJhYTcwZTMzYTE2Yzk4ZjRkZjkyZDc5MGFiNDE1ZDM1YyJ9LCJjYXB0dXJlIjp7InBvbGljeSI6InBlcl9maWxlX2FuZF9uYXRpdmVfc3FsaXRlX3YxIiwic3RhcnRlZF9hdCI6IjIwMjYtMDktMTBUMTQ6MjY6MDYuOTUzOTgzWiIsImNvbXBsZXRlZF9hdCI6IjIwMjYtMDktMTBUMTQ6MjY6MDYuOTg1ODU0WiIsInJlbW92ZWRfYmVmb3JlX2NhcHR1cmUiOltdfX0sInNpZ25hdHVyZSI6Ijk5NmJjOWQ4YWI3YmFkOGI0MmM1YjkxNTE5YWQ4MjVmZDU1YTRjZmQ2YjZmYmU3NTkwZTE2NzkwOTBiZDZkYzBiZDgwZWM5YmQyMGM1ODZiMTMyNzEwNTJkZWYzODUxZmRiNDhmMzZkZTA4NjY0YTJlMGFjMDU3ZWUyMTI2YTBmIn0="
const frozenLegacyV2Payload = "UEsDBBQACAAIAAAAAAAAAAAAAAAAAAAAAAAHAAAAU09VTC5tZHLLrCgpLUpVKEgtKs7PS8zJLKkEBAAA//9QSwcIogCYIxkAAAATAAAAUEsDBBQACAAIAAAAAAAAAAAAAAAAAAAAAAALAAAAY29uZmlnLnlhbWzKTc3NL6q0Uqiu5QIEAAD//1BLBwgFinddEQAAAAsAAABQSwMEFAAIAAgAAAAAAAAAAAAAAAAAAAAAABIAAABtZW1vcmllcy9NRU1PUlkubWRKy6woKS1KVchNzc0vqgQEAAD//1BLBwjPHFOCFAAAAA4AAABQSwMEFAAIAAgAAAAAAAAAAAAAAAAAAAAAAA8AAABzZXNzaW9ucy9hLmpzb25Ky6woKS1KVShOLS7OzM8DBAAA//9QSwcI/vBEjRUAAAAPAAAAUEsDBBQACAAIAAAAAAAAAAAAAAAAAAAAAAAXAAAAc2tpbGxzL2NyZWF0ZWQvU0tJTEwubWRKy6woKS1KVUgsKCjKL0tNUSjOzszJAQQAAP//UEsHCCURRiYcAAAAFgAAAFBLAwQUAAgACAAAAAAAAAAAAAAAAAAAAAAACAAAAHN0YXRlLmRiKq7MK8lILclMVsjPy8nMS1UozkssKM7ILwEEAAD//1BLBwiQPW9THwAAABkAAABQSwECFAMUAAgACAAAAAAAogCYIxkAAAATAAAABwAAAAAAAAAAAAAAgIEAAAAAU09VTC5tZFBLAQIUAxQACAAIAAAAAAAFinddEQAAAAsAAAALAAAAAAAAAAAAAACAgU4AAABjb25maWcueWFtbFBLAQIUAxQACAAIAAAAAADPHFOCFAAAAA4AAAASAAAAAAAAAAAAAACAgZgAAABtZW1vcmllcy9NRU1PUlkubWRQSwECFAMUAAgACAAAAAAA/vBEjRUAAAAPAAAADwAAAAAAAAAAAAAAgIHsAAAAc2Vzc2lvbnMvYS5qc29uUEsBAhQDFAAIAAgAAAAAACURRiYcAAAAFgAAABcAAAAAAAAAAAAAAICBPgEAAHNraWxscy9jcmVhdGVkL1NLSUxMLm1kUEsBAhQDFAAIAAgAAAAAAJA9b1MfAAAAGQAAAAgAAAAAAAAAAAAAAICBnwEAAHN0YXRlLmRiUEsFBgAAAAAGAAYAZgEAAPQBAAAAAA=="

func frozenLegacyPackage(t *testing.T, root, version string) (string, []byte, []byte) {
	t.Helper()
	manifest, payload := frozenLegacyV1Manifest, frozenLegacyV1Payload
	if version == "v2" {
		manifest, payload = frozenLegacyV2Manifest, frozenLegacyV2Payload
	}
	m, err := base64.StdEncoding.DecodeString(manifest)
	if err != nil {
		t.Fatal(err)
	}
	p, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(root, "recovery", "retained-"+version)
	if err := os.MkdirAll(pkg, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(pkg, 0750); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{ManifestFile: m, PayloadFile: p} {
		if err := os.WriteFile(filepath.Join(pkg, name), b, 0440); err != nil {
			t.Fatal(err)
		}
	}
	return pkg, m, p
}

func TestRecoveryFrozenLegacyBytesAndExactOrigin(t *testing.T) {
	in, _ := fixture(t)
	public := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			pkg, manifest, payload := frozenLegacyPackage(t, in.Workspace, version)
			before, err := Verify(context.Background(), pkg, WorkspaceRoot, public)
			if err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(filepath.Dir(in.Workspace), "mina", "recovery", before.ID)
			if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(pkg, destination); err != nil {
				t.Fatal(err)
			}
			after, err := Verify(context.Background(), destination, WorkspaceRoot, public)
			if err != nil || after.ManifestSHA256 != before.ManifestSHA256 || !reflect.DeepEqual(after.Files, before.Files) {
				t.Fatalf("relocation changed evidence: %v", err)
			}
			if _, err := Verify(context.Background(), destination, MinaWorkspaceRoot, public); err == nil {
				t.Fatal("relocation relabelled origin")
			}
			for name, want := range map[string][]byte{ManifestFile: manifest, PayloadFile: payload} {
				got, err := os.ReadFile(filepath.Join(destination, name))
				if err != nil || string(got) != string(want) {
					t.Fatal("frozen bytes changed")
				}
			}
		})
	}
}

func TestMinaRecoveryExplicitProducerReplayAndWrongOrigin(t *testing.T) {
	for _, id := range []RecoveryIdentity{MorathustraIdentity, MinaIdentity} {
		t.Run(string(id), func(t *testing.T) {
			in, p := fixture(t)
			in.Identity = id
			origin, _ := id.WorkspaceRoot()
			e, err := Publish(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(e.Path, ManifestFile))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Verify(context.Background(), e.Path, origin, p.PublicKey); err != nil {
				t.Fatal(err)
			}
			if _, err := Verify(context.Background(), e.Path, in.Workspace, p.PublicKey); err == nil {
				t.Fatal("physical location authenticated as origin")
			}
			if _, err := Publish(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			again, _ := os.ReadFile(filepath.Join(e.Path, ManifestFile))
			if string(raw) != string(again) {
				t.Fatal("replay changed signed bytes")
			}
			in.Identity = MinaIdentity
			if id == MinaIdentity {
				in.Identity = MorathustraIdentity
			}
			if _, err := Publish(context.Background(), in); err == nil {
				t.Fatal("conflicting producer replay accepted")
			}
		})
	}
}
