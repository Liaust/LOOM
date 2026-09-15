package hermesprofile

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/agentpack"
)

func writeRecoveryReadme(t *testing.T, workspace string) string {
	t.Helper()
	name := filepath.Join(workspace, "recovery", RecoveryReadmeFile)
	if err := os.WriteFile(name, []byte("Ordinary editable workspace contract.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, 0644); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestRecoveryCheckRealScaffoldReadmeAndExactPackages(t *testing.T) {
	in, policy := fixture(t)
	pack, err := agentpack.LoadFromPath("../../ai-loom-pack")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(filepath.Dir(in.Workspace), "scaffold")
	plan, err := agentpack.PlanWorkspace(pack, agentpack.WorkspaceOptions{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentpack.ApplyWorkspace(plan, true); err != nil {
		t.Fatal(err)
	}
	// The installed boundary is deliberately read-only; only this disposable
	// scaffold needs permission restoration for testing.T cleanup.
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(root, "skills"), 0700)
		_ = os.Chmod(filepath.Join(root, "skills", "installed"), 0700)
	})
	err = filepath.WalkDir(filepath.Join(in.Workspace, ".hermes"), func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(filepath.Join(in.Workspace, ".hermes"), p)
		if e != nil {
			return e
		}
		dest := filepath.Join(root, ".hermes", rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0700)
		}
		if rel == "SOUL.md" {
			return nil
		}
		data, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		return os.WriteFile(dest, data, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	in.Workspace, policy.Workspace = root, root
	readme := filepath.Join(root, "recovery", RecoveryReadmeFile)
	before, err := os.ReadFile(readme)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(readme)
	if err != nil || info.Mode() != 0644 {
		t.Fatalf("real scaffold mode: %v %v", info, err)
	}
	if _, err := Check(context.Background(), policy, in.CreatedAt); err == nil {
		t.Fatal("README satisfied freshness without a package")
	}
	first, err := Publish(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.ID = "recovery-second"
	second, err := Publish(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	// Staging remains excluded even with malformed content.
	if err := os.WriteFile(filepath.Join(root, "recovery", ".staging", "not-evidence"), []byte("untrusted"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Check(context.Background(), policy, in.CreatedAt.Add(time.Minute))
	if err != nil || !reflect.DeepEqual(got, []Evidence{first, second}) {
		t.Fatalf("exact package discovery: %v %v", got, err)
	}
	if _, err := Check(context.Background(), policy, in.CreatedAt.Add(MaxAge+time.Second)); err == nil {
		t.Fatal("README masked stale packages")
	}
	if _, err := Check(context.Background(), policy, in.CreatedAt.Add(-time.Second)); err == nil {
		t.Fatal("README masked future packages")
	}
	after, err := os.ReadFile(readme)
	if err != nil || string(before) != string(after) {
		t.Fatal("README was changed")
	}
	// The bytes are not a pin or signature: ordinary edits between checks pass.
	writeRecoveryReadme(t, root)
	if _, err := Check(context.Background(), policy, in.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Path, RecoveryReadmeFile), before, 0440); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(context.Background(), policy, in.CreatedAt); err == nil {
		t.Fatal("README admitted inside authenticated package")
	}
}

func TestRecoveryCheckReadmeUnsafeEntries(t *testing.T) {
	for _, scenario := range []string{"unknown-file", "case-alias", "unknown-directory", "symlink", "hardlink", "fifo", "socket", "directory", "writable", "executable", "private-mode", "setuid", "oversized", "staging-symlink"} {
		t.Run(scenario, func(t *testing.T) {
			in, p := fixture(t)
			if _, err := Publish(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(in.Workspace, "recovery", RecoveryReadmeFile)
			outside := filepath.Join(filepath.Dir(in.Workspace), "outside")
			if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
				t.Fatal(err)
			}
			var err error
			switch scenario {
			case "unknown-file":
				err = os.WriteFile(filepath.Join(in.Workspace, "recovery", "notes.md"), nil, 0644)
			case "case-alias":
				err = os.WriteFile(filepath.Join(in.Workspace, "recovery", "readme.md"), nil, 0644)
			case "unknown-directory":
				err = os.Mkdir(filepath.Join(in.Workspace, "recovery", "unknown"), 0750)
			case "symlink":
				err = os.Symlink(outside, name)
			case "hardlink":
				err = os.Link(outside, name)
			case "fifo":
				err = unix.Mkfifo(name, 0644)
			case "socket":
				var fd int
				fd, err = unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
				if err == nil {
					defer unix.Close(fd)
					var short string
					short, err = os.MkdirTemp("", "hr-socket-")
					if err == nil {
						t.Cleanup(func() { _ = os.RemoveAll(short) })
						socketPath := filepath.Join(short, "s")
						err = unix.Bind(fd, &unix.SockaddrUnix{Name: socketPath})
						if err == nil {
							err = os.Rename(socketPath, name)
						}
					}
				}
			case "directory":
				err = os.Mkdir(name, 0750)
			case "staging-symlink":
				staging := filepath.Join(in.Workspace, "recovery", ".staging")
				err = os.Rename(staging, filepath.Join(in.Workspace, "saved-staging"))
				if err == nil {
					err = os.Symlink(outside, staging)
				}
			default:
				writeRecoveryReadme(t, in.Workspace)
				switch scenario {
				case "writable":
					err = os.Chmod(name, 0664)
				case "executable":
					err = os.Chmod(name, 0755)
				case "private-mode":
					err = os.Chmod(name, 0600)
				case "setuid":
					err = os.Chmod(name, os.ModeSetuid|0644)
				case "oversized":
					err = os.Truncate(name, RecoveryReadmeMaxBytes+1)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Check(context.Background(), p, in.CreatedAt); err == nil {
				t.Fatal("unsafe entry accepted")
			}
		})
	}
}

type recoveryMutationContext struct {
	context.Context
	once   sync.Once
	mutate func()
}

func (c *recoveryMutationContext) Err() error { c.once.Do(c.mutate); return c.Context.Err() }

func TestRecoveryCheckReadmeSubstitutionDuringPackageVerification(t *testing.T) {
	for _, scenario := range []string{"replace", "symlink", "hardlink", "bytes", "mode", "remove", "aba", "root-aba", "insert-unknown", "insert-readme"} {
		t.Run(scenario, func(t *testing.T) {
			in, p := fixture(t)
			if _, err := Publish(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(in.Workspace, "recovery", RecoveryReadmeFile)
			if scenario != "insert-readme" {
				writeRecoveryReadme(t, in.Workspace)
			}
			mutated := false
			// Check holds README before Verify reads the package payload. Its context
			// read supplies a deterministic mutation point, without sleeps or globals.
			ctx := &recoveryMutationContext{Context: context.Background(), mutate: func() {
				mutated = true
				var err error
				switch scenario {
				case "replace", "symlink", "hardlink", "aba":
					saved := filepath.Join(in.Workspace, "saved-readme")
					err = os.Rename(name, saved)
					if err == nil {
						switch scenario {
						case "replace":
							writeRecoveryReadme(t, in.Workspace)
						case "symlink":
							err = os.Symlink(saved, name)
						case "hardlink":
							err = os.Link(saved, name)
						case "aba":
							err = os.Rename(saved, name)
						}
					}
				case "bytes":
					err = os.WriteFile(name, []byte("mutated ordinary contract"), 0644)
				case "mode":
					err = os.Chmod(name, 0664)
				case "remove":
					err = os.Rename(name, filepath.Join(in.Workspace, "saved-readme"))
				case "root-aba":
					root := filepath.Dir(name)
					saved := root + "-saved"
					err = os.Rename(root, saved)
					if err == nil {
						err = os.Rename(saved, root)
					}
				case "insert-unknown":
					err = os.WriteFile(filepath.Join(filepath.Dir(name), "rogue"), nil, 0644)
				case "insert-readme":
					writeRecoveryReadme(t, in.Workspace)
				}
				if err != nil {
					t.Fatal(err)
				}
			}}
			_, err := Check(ctx, p, in.CreatedAt)
			if !mutated || err == nil || (!strings.Contains(err.Error(), "README") && !strings.Contains(err.Error(), "namespace")) {
				t.Fatalf("mutation not refused by discovery: mutated=%v err=%v", mutated, err)
			}
		})
	}
}

func TestRecoveryReadmeOwnerAndSizeBounds(t *testing.T) {
	info := Identity{Mode: unix.S_IFREG | 0644, Owner: 501, Links: 1, Size: RecoveryReadmeMaxBytes}
	if !validRecoveryReadme(info, 501) {
		t.Fatal("valid boundary refused")
	}
	if validRecoveryReadme(info, 502) {
		t.Fatal("foreign owner accepted")
	}
	info.Size = -1
	if validRecoveryReadme(info, 501) {
		t.Fatal("negative size accepted")
	}
}

func TestRecoveryReadmeSubstitutedBeforeOpen(t *testing.T) {
	in, _ := fixture(t)
	name := writeRecoveryReadme(t, in.Workspace)
	chain, err := openCustody(filepath.Dir(name))
	if err != nil {
		t.Fatal(err)
	}
	defer chain.close()
	initial, err := statAt(chain.leaf(), RecoveryReadmeFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(name, filepath.Join(in.Workspace, "saved-readme")); err != nil {
		t.Fatal(err)
	}
	writeRecoveryReadme(t, in.Workspace)
	held, err := openRecoveryReadme(chain.leaf(), initial, initial.Owner)
	if held != nil {
		held.file.Close()
	}
	if err == nil {
		t.Fatal("pre-open substitution accepted")
	}
}

// Both identities use the same synthetic signing key. Authentication alone
// therefore cannot grant the retained origin permission.
func TestMinaRecoveryPolicyCurrentAndRetainedEvidence(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		for _, scenario := range []string{"transition", "old-stale", "no-current", "current-stale", "undeclared-old", "future-old", "future-current", "tampered-old", "unsigned-workspace", "unsigned-profile", "signed-profile-conflict", "other-origin", "missing-retained", "duplicate-package", "wrong-binding", "rogue-sibling"} {
			t.Run(version+"/"+scenario, func(t *testing.T) {
				old, _ := fixture(t)
				current, _ := fixture(t)
				old.Identity = MorathustraIdentity
				current.Identity = MinaIdentity
				old.ID = "retained"
				current.ID = "current"
				old.CreatedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
				current.CreatedAt = old.CreatedAt
				current.PrivateKey = old.PrivateKey
				now := old.CreatedAt.Add(time.Minute)
				switch scenario {
				case "old-stale":
					old.CreatedAt = old.CreatedAt.Add(-2 * MaxAge)
				case "current-stale":
					current.CreatedAt = current.CreatedAt.Add(-2 * MaxAge)
				case "future-old":
					old.CreatedAt = now.Add(time.Second)
				case "future-current":
					current.CreatedAt = now.Add(time.Second)
				}
				e, err := Publish(context.Background(), old)
				if err != nil {
					t.Fatal(err)
				}
				if version == "v1" {
					rewriteManifest(t, old, e.Path, func(m *Manifest) { m.Schema = Schema; m.Capture = nil })
				}
				dest := filepath.Join(current.Workspace, "recovery", old.ID)
				if err := os.Rename(e.Path, dest); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(filepath.Join(dest, ManifestFile))
				if err != nil {
					t.Fatal(err)
				}
				if scenario != "no-current" {
					if _, err := Publish(context.Background(), current); err != nil {
						t.Fatal(err)
					}
				}
				p := Policy{Enabled: true, Workspace: current.Workspace, Identity: MinaIdentity, PublicKey: old.PrivateKey.Public().(ed25519.PublicKey), RetainedMorathustra: []string{old.ID}}
				switch scenario {
				case "undeclared-old":
					p.RetainedMorathustra = nil
				case "missing-retained":
					p.RetainedMorathustra = append(p.RetainedMorathustra, "missing")
				case "wrong-binding":
					p.RetainedMorathustra = append(p.RetainedMorathustra, current.ID)
				case "tampered-old":
					path := filepath.Join(dest, PayloadFile)
					if err := os.Chmod(path, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("tampered"), 0440); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(path, 0440); err != nil {
						t.Fatal(err)
					}
				case "unsigned-workspace", "unsigned-profile":
					path := filepath.Join(dest, ManifestFile)
					var env envelope
					var m Manifest
					if err := json.Unmarshal(before, &env); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(env.Manifest, &m); err != nil {
						t.Fatal(err)
					}
					if scenario == "unsigned-workspace" {
						m.Workspace = MinaWorkspaceRoot
					} else {
						m.Profile = MinaWorkspaceRoot + "/.hermes"
					}
					env.Manifest, err = json.Marshal(m)
					if err != nil {
						t.Fatal(err)
					}
					raw, err := json.Marshal(env)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(path, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, raw, 0440); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(path, 0440); err != nil {
						t.Fatal(err)
					}
				case "signed-profile-conflict":
					rewriteManifest(t, old, dest, func(m *Manifest) { m.Profile = MinaWorkspaceRoot + "/.hermes" })

				case "other-origin":
					rewriteManifest(t, old, dest, func(m *Manifest) { m.Workspace = "/srv/loom/agents/rogue"; m.Profile = m.Workspace + "/.hermes" })
				case "duplicate-package":
					duplicate := filepath.Join(current.Workspace, "recovery", "duplicate")
					if err := os.Mkdir(duplicate, 0750); err != nil {
						t.Fatal(err)
					}
					for _, name := range []string{ManifestFile, PayloadFile} {
						b, err := os.ReadFile(filepath.Join(dest, name))
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(duplicate, name), b, 0440); err != nil {
							t.Fatal(err)
						}
					}
					p.RetainedMorathustra = append(p.RetainedMorathustra, "duplicate")
				case "rogue-sibling":
					if err := os.WriteFile(filepath.Join(current.Workspace, "recovery", "rogue"), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
				got, err := Check(context.Background(), p, now)
				valid := scenario == "transition" || scenario == "old-stale"
				if (err == nil) != valid {
					t.Fatalf("evidence=%v err=%v", got, err)
				}
				if valid {
					if len(got) != 2 {
						t.Fatal("retained package omitted")
					}
					after, err := os.ReadFile(filepath.Join(dest, ManifestFile))
					if err != nil || !bytes.Equal(before, after) {
						t.Fatal("retained signed bytes rewritten")
					}
				}
			})
		}
	}
}

func TestMinaRecoveryFrozenTransition(t *testing.T) {
	in, _ := fixture(t)
	in.Identity = MinaIdentity
	in.PrivateKey = ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	in.ID = "mina-current"
	// The unchanged base's original v1 and v2 bytes coexist with a new producer.
	for _, version := range []string{"v1", "v2"} {
		frozenLegacyPackage(t, in.Workspace, version)
	}
	if _, err := Publish(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	p := Policy{Enabled: true, Workspace: in.Workspace, Identity: MinaIdentity, PublicKey: in.PrivateKey.Public().(ed25519.PublicKey), RetainedMorathustra: []string{"retained-v1", "retained-v2"}}
	got, err := Check(context.Background(), p, time.Now().UTC())
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestMinaRecoveryPolicySubstitutedCustody(t *testing.T) {
	for _, scenario := range []string{"ancestor", "manifest", "payload"} {
		t.Run(scenario, func(t *testing.T) {
			in, _ := fixture(t)
			in.Identity = MinaIdentity
			e, err := Publish(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			p := Policy{Enabled: true, Workspace: in.Workspace, Identity: MinaIdentity, PublicKey: in.PrivateKey.Public().(ed25519.PublicKey)}
			changed := false
			ctx := &recoveryMutationContext{Context: context.Background(), mutate: func() {
				changed = true
				path := in.Workspace
				if scenario != "ancestor" {
					name := ManifestFile
					if scenario == "payload" {
						name = PayloadFile
					}
					path = filepath.Join(e.Path, name)
				}
				if err := os.Rename(path, path+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+"-saved", path); err != nil {
					t.Fatal(err)
				}
			}}
			if _, err := Check(ctx, p, time.Now().UTC()); err == nil || !changed {
				t.Fatalf("substitution accepted: changed=%v err=%v", changed, err)
			}
		})
	}
}

func TestRecoveryDisabledLegacyExclusionLocation(t *testing.T) {
	// An old disabled caller carries a physical exclusion path, not permission
	// to open a profile. Preserve this use without granting an enabled producer.
	p := Policy{Workspace: "/absent/legacy/exclusion-only"}
	got, err := Check(context.Background(), p, time.Now().UTC())
	if err != nil || len(got) != 0 {
		t.Fatalf("disabled legacy gate: %v %v", got, err)
	}
	p.Enabled = true
	if _, err := Check(context.Background(), p, time.Now().UTC()); err == nil {
		t.Fatal("disabled compatibility authorized enabled evidence")
	}
	p.Enabled = false
	p.Identity = MinaIdentity
	if _, err := Check(context.Background(), p, time.Now().UTC()); err == nil {
		t.Fatal("legacy disabled location masked explicit identity conflict")
	}
}
