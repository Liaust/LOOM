package agentpack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func exportTestGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-C", root}, args...)...)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid"}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func exportTestWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func exportTestFixture(t *testing.T) (string, WorkspaceExportOptions) {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "source")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join("..", "..")
	paths := []string{"ai-loom-pack/manifest.yaml", workspaceExportSkin}
	for _, path := range workspaceExportSources {
		paths = append(paths, "ai-loom-pack/templates/mina/"+path)
	}
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Join(repo, path))
		if err != nil {
			t.Fatal(err)
		}
		exportTestWrite(t, filepath.Join(root, path), data)
	}
	exportTestGit(t, root, "init", "--quiet")
	exportTestGit(t, root, "add", "--", "ai-loom-pack/manifest.yaml", "ai-loom-pack/templates/mina", workspaceExportSkin)
	exportTestGit(t, root, "commit", "--quiet", "-m", "synthetic portable sources")
	return root, WorkspaceExportOptions{Template: "mina", SourceRoot: root, Path: filepath.Join(parent, "export")}
}

func exportTestTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = fmt.Sprintf("%o:%s", info.Mode().Perm(), exportDigest(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestWorkspaceExportSourceBindingAndReconstruction(t *testing.T) {
	root, options := exportTestFixture(t)
	// Deliberately unselected hostile material must remain outside read authority.
	exportTestWrite(t, filepath.Join(root, ".hermes/state.db"), []byte("private unselected data"))
	if err := syscall.Mkfifo(filepath.Join(root, "ai-loom-pack/templates/mina/unselected-fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanWorkspaceExport(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(options.Path); !os.IsNotExist(err) {
		t.Fatal("planning wrote destination")
	}
	if len(plan.Manifest.Files) != 22 || len(plan.payloads) != 23 {
		t.Fatal("wrong census")
	}

	// Independent reviewed source census, including both newly admitted protocols.
	expectedSources := map[string]bool{}
	for _, path := range minaSourceFiles {
		switch path {
		case "investigations/README.md", "recovery/README.md", "skills/installed/README.md", "tmp/README.md":
			continue
		}
		expectedSources[path] = true
	}
	if len(expectedSources) != 19 {
		t.Fatal("instruction source census changed")
	}
	for _, entry := range plan.Manifest.Files {
		if entry.Origin != "source" || entry.Path == "skins/mina-matrix-teal.yaml" {
			continue
		}
		if !expectedSources[entry.Path] {
			t.Fatalf("unreviewed or duplicate instruction %s", entry.Path)
		}
		delete(expectedSources, entry.Path)
	}
	if len(expectedSources) != 0 || plan.Manifest.PackVersion != "0.6.4" {
		t.Fatal("missing reviewed source or patch version")
	}
	for _, path := range []string{"protocols/DEVICE-AND-TOOL-ROUTING.md", "protocols/MAC-COMPUTER-USE.md"} {
		if len(plan.payloads[path]) == 0 {
			t.Fatalf("new protocol missing: %s", path)
		}
	}
	revision := exportTestGit(t, root, "rev-parse", "HEAD")
	if plan.Manifest.SourceRevision != revision {
		t.Fatal("wrong revision")
	}
	serialized, _ := json.Marshal(plan)
	if bytes.Contains(serialized, []byte(root)) || bytes.Contains(serialized, []byte("private unselected data")) {
		t.Fatal("receipt leaked host path/content")
	}
	canonical := plan.Manifest
	id := canonical.ExportID
	canonical.ExportID = ""
	data, _ := json.Marshal(canonical)
	if exportDigest(data) != id {
		t.Fatal("non-deterministic identity")
	}
	replay, err := PlanWorkspaceExport(options)
	if err != nil || !reflect.DeepEqual(plan.Manifest, replay.Manifest) {
		t.Fatalf("unstable plan: %v", err)
	}
	result, err := ApplyWorkspaceExport(plan, true)
	if err != nil || !result.Applied {
		t.Fatalf("apply: %v", err)
	}
	tree := exportTestTree(t, options.Path)
	if len(tree) != 23 {
		t.Fatalf("tree: %v", tree)
	}
	for _, file := range plan.Manifest.Files {
		data, err := os.ReadFile(filepath.Join(options.Path, file.Path))
		if err != nil {
			t.Fatal(err)
		}
		if len(data) != file.Bytes || exportDigest(data) != file.SHA256 {
			t.Fatal("manifest payload mismatch")
		}
		if file.Origin == "source" {
			source, _ := os.ReadFile(filepath.Join(root, file.SourcePath))
			if !bytes.Equal(data, source) {
				t.Fatal("source changed")
			}
			a, _ := os.Stat(filepath.Join(root, file.SourcePath))
			b, _ := os.Stat(filepath.Join(options.Path, file.Path))
			if os.SameFile(a, b) || !exportSingleFile(b) {
				t.Fatal("output is linked")
			}
		} else if file.Origin != "generated" || file.SourcePath != "" {
			t.Fatal("wrong generated provenance")
		}
	}
	if plan.payloads[".hermes/SOUL.md"] == nil || exportDigest(plan.payloads["skins/mina-matrix-teal.yaml"]) != workspaceExportSkinHash {
		t.Fatal("soul or skin missing")
	}
	// Reconstruct only the explicit portable source inventory using local Git.
	exportTestGit(t, options.Path, "init", "--quiet")
	inventory := []string{"add", "--", workspaceExportManifest}
	for _, file := range plan.Manifest.Files {
		inventory = append(inventory, file.Path)
	}
	exportTestGit(t, options.Path, inventory...)
	exportTestGit(t, options.Path, "commit", "--quiet", "-m", "portable instructions only")
	restored := filepath.Join(t.TempDir(), "restored")
	if err := os.Mkdir(restored, 0700); err != nil {
		t.Fatal(err)
	}
	// checkout-index writes an exact local committed index into the disposable root.
	exportTestGit(t, options.Path, "checkout-index", "--all", "--prefix="+restored+string(filepath.Separator))
	for path, want := range plan.payloads {
		data, err := os.ReadFile(filepath.Join(restored, path))
		if err != nil || !bytes.Equal(want, data) {
			t.Fatalf("reconstruction %s: %v", path, err)
		}
	}
	// Ignored additions do not become allowed inputs to a future export.
	exportTestWrite(t, filepath.Join(options.Path, ".hermes/credentials.json"), []byte("untouched local fixture"))
	if got := exportTestGit(t, options.Path, "status", "--porcelain"); got != "" {
		t.Fatalf("defensive ignore failed: %s", got)
	}
	before := exportTestTree(t, options.Path)
	if _, err = PlanWorkspaceExport(options); err == nil {
		t.Fatal("existing Git destination accepted")
	}
	if !reflect.DeepEqual(before, exportTestTree(t, options.Path)) {
		t.Fatal("dirty destination changed")
	}
	// A second valid source revision produces distinct identity and changed bytes.
	source := filepath.Join(root, "ai-loom-pack/templates/mina/WORKFLOW.md")
	data, _ = os.ReadFile(source)
	exportTestWrite(t, source, append(data, []byte("\nReviewed portable fixture addition.\n")...))
	exportTestGit(t, root, "add", "--", "ai-loom-pack/templates/mina/WORKFLOW.md")
	exportTestGit(t, root, "commit", "--quiet", "-m", "second fixture revision")
	options.Path = filepath.Join(filepath.Dir(options.Path), "export-two")
	second, err := PlanWorkspaceExport(options)
	if err != nil {
		t.Fatal(err)
	}
	if second.Manifest.ExportID == id {
		t.Fatal("changed source reused export identity")
	}
	if result, err = ApplyWorkspaceExport(second, true); err != nil || !result.Applied {
		t.Fatal(err)
	}
	if bytes.Equal(second.payloads["WORKFLOW.md"], plan.payloads["WORKFLOW.md"]) {
		t.Fatal("second export not changed")
	}
}

func TestWorkspaceExportRefusesUnsafeAndUnreviewedInputs(t *testing.T) {
	cases := map[string]func(*testing.T, string, *WorkspaceExportOptions){
		"missing-template":    func(t *testing.T, r string, o *WorkspaceExportOptions) { o.Template = "" },
		"other-template":      func(t *testing.T, r string, o *WorkspaceExportOptions) { o.Template = "morathustra" },
		"missing-source":      func(t *testing.T, r string, o *WorkspaceExportOptions) { o.SourceRoot = "" },
		"missing-destination": func(t *testing.T, r string, o *WorkspaceExportOptions) { o.Path = "" },
		"missing-parent": func(t *testing.T, r string, o *WorkspaceExportOptions) {
			o.Path = filepath.Join(o.Path, "missing", "export")
		},
		"nested-checkout": func(t *testing.T, r string, o *WorkspaceExportOptions) {
			o.SourceRoot = filepath.Join(r, "ai-loom-pack")
		},
		"existing-empty": func(t *testing.T, r string, o *WorkspaceExportOptions) {
			if err := os.Mkdir(o.Path, 0700); err != nil {
				t.Fatal(err)
			}
		},
		"existing-file": func(t *testing.T, r string, o *WorkspaceExportOptions) {
			exportTestWrite(t, o.Path, []byte("preserve"))
		},
		"existing-symlink": func(t *testing.T, r string, o *WorkspaceExportOptions) {
			if err := os.Symlink(r, o.Path); err != nil {
				t.Fatal(err)
			}
		},
		"parent-symlink": func(t *testing.T, r string, o *WorkspaceExportOptions) {
			link := filepath.Join(filepath.Dir(r), "link")
			if err := os.Symlink(filepath.Dir(r), link); err != nil {
				t.Fatal(err)
			}
			o.Path = filepath.Join(link, "out")
		},
		"source-symlink": func(t *testing.T, r string, o *WorkspaceExportOptions) {
			link := r + "-link"
			if err := os.Symlink(r, link); err != nil {
				t.Fatal(err)
			}
			o.SourceRoot = link
		},
		"source-file-symlink": func(t *testing.T, r string, o *WorkspaceExportOptions) {
			p := filepath.Join(r, "ai-loom-pack/templates/mina/WORKFLOW.md")
			if err := os.Rename(p, p+"-original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(p+"-original", p); err != nil {
				t.Fatal(err)
			}
		},
		"source-hardlink": func(t *testing.T, r string, o *WorkspaceExportOptions) {
			p := filepath.Join(r, "ai-loom-pack/templates/mina/WORKFLOW.md")
			if err := os.Link(p, p+"-link"); err != nil {
				t.Fatal(err)
			}
		},
		"source-fifo": func(t *testing.T, r string, o *WorkspaceExportOptions) {
			p := filepath.Join(r, "ai-loom-pack/templates/mina/WORKFLOW.md")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(p, 0600); err != nil {
				t.Fatal(err)
			}
		},
		"oversize": func(t *testing.T, r string, o *WorkspaceExportOptions) {
			exportTestWrite(t, filepath.Join(r, "ai-loom-pack/templates/mina/WORKFLOW.md"), bytes.Repeat([]byte("x"), maxWorkspaceFileSize+1))
		},
		"dirty-selected": func(t *testing.T, r string, o *WorkspaceExportOptions) {
			exportTestWrite(t, filepath.Join(r, "ai-loom-pack/templates/mina/WORKFLOW.md"), []byte("changed portable text"))
		},
		"staged-selected": func(t *testing.T, r string, o *WorkspaceExportOptions) {
			exportTestWrite(t, filepath.Join(r, "ai-loom-pack/templates/mina/WORKFLOW.md"), []byte("changed portable text"))
			exportTestGit(t, r, "add", "--", "ai-loom-pack/templates/mina/WORKFLOW.md")
		},
		"missing-selected": func(t *testing.T, r string, o *WorkspaceExportOptions) {
			if err := os.Remove(filepath.Join(r, "ai-loom-pack/templates/mina/WORKFLOW.md")); err != nil {
				t.Fatal(err)
			}
		},
		"untracked-selected": func(t *testing.T, r string, o *WorkspaceExportOptions) {
			exportTestGit(t, r, "rm", "--cached", "ai-loom-pack/templates/mina/WORKFLOW.md")
			exportTestGit(t, r, "commit", "--quiet", "-m", "remove fixture")
		},
		"wrong-skin": func(t *testing.T, r string, o *WorkspaceExportOptions) {
			exportTestWrite(t, filepath.Join(r, workspaceExportSkin), []byte("changed skin"))
		},
		"alternates": func(t *testing.T, r string, o *WorkspaceExportOptions) {
			exportTestWrite(t, filepath.Join(r, ".git/objects/info/alternates"), []byte("/not-an-authorized-source\n"))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r, o := exportTestFixture(t)
			change(t, r, &o)
			if _, err := PlanWorkspaceExport(o); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
	for name, change := range map[string]func(string) string{
		"path-escape": func(s string) string { return strings.Replace(s, "      - WORKFLOW.md", "      - ../escape.md", 1) },
		"duplicate": func(s string) string {
			return strings.Replace(s, "      - WORKFLOW.md", "      - WORKFLOW.md\n      - WORKFLOW.md", 1)
		},
		"new-routing": func(s string) string {
			return strings.Replace(s, "      - WORKFLOW.md", "      - WORKFLOW.md\n      - protocols/MAC.md", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, o := exportTestFixture(t)
			path := filepath.Join(r, "ai-loom-pack/manifest.yaml")
			data, _ := os.ReadFile(path)
			before, mina, ok := strings.Cut(string(data), "  - name: mina\n")
			if !ok {
				t.Fatal("missing MINA fixture template")
			}
			changed := before + "  - name: mina\n" + change(mina)
			if changed == string(data) {
				t.Fatal("fixture mutation did not apply")
			}
			exportTestWrite(t, path, []byte(changed))
			exportTestGit(t, r, "add", "--", "ai-loom-pack/manifest.yaml")
			exportTestGit(t, r, "commit", "--quiet", "-m", "unreviewed declaration")
			if _, err := PlanWorkspaceExport(o); err == nil {
				t.Fatal("unreviewed declaration accepted")
			}
		})
	}
	for _, content := range []string{"password=DO_NOT_LEAK_FIXTURE", "source at /srv/private-fixture/path", "\x00binary", "# Empty soul\n"} {
		t.Run(fmt.Sprintf("redaction-%x", exportDigest([]byte(content))[:8]), func(t *testing.T) {
			r, o := exportTestFixture(t)
			path := "ai-loom-pack/templates/mina/.hermes/SOUL.md"
			exportTestWrite(t, filepath.Join(r, path), []byte(content))
			exportTestGit(t, r, "add", "--", path)
			exportTestGit(t, r, "commit", "--quiet", "-m", "invalid content fixture")
			_, err := PlanWorkspaceExport(o)
			if err == nil || strings.Contains(err.Error(), content) {
				t.Fatalf("refusal or redaction failed: %v", err)
			}
		})
	}
}

func TestWorkspaceExportRevalidationAndPartialInventory(t *testing.T) {
	for _, kind := range []string{"no-confirmation", "forged", "manifest-tamper", "payload-tamper", "source-replace", "source-ancestor", "destination-ancestor", "head-change", "destination-created"} {
		t.Run(kind, func(t *testing.T) {
			r, o := exportTestFixture(t)
			plan, err := PlanWorkspaceExport(o)
			if err != nil {
				t.Fatal(err)
			}
			confirmed := true
			switch kind {
			case "no-confirmation":
				confirmed = false
			case "forged":
				data, _ := json.Marshal(plan)
				var forged WorkspaceExportPlan
				if err := json.Unmarshal(data, &forged); err != nil {
					t.Fatal(err)
				}
				plan = forged
			case "manifest-tamper":
				plan.Manifest.Files[0].Path = "../escape"
			case "payload-tamper":
				plan.payloads["README.md"] = []byte("different")
			case "source-replace":
				p := filepath.Join(r, "ai-loom-pack/templates/mina/WORKFLOW.md")
				data, _ := os.ReadFile(p)
				if err := os.Rename(p, p+".old"); err != nil {
					t.Fatal(err)
				}
				exportTestWrite(t, p, data)
			case "source-ancestor":
				p := filepath.Join(r, "ai-loom-pack/templates/mina/protocols")
				if err := os.Rename(p, p+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(p, 0700); err != nil {
					t.Fatal(err)
				}
			case "destination-ancestor":
				parent := filepath.Dir(o.Path)
				if err := os.Rename(parent, parent+".moved"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.RemoveAll(parent + ".moved") })
				if err := os.Mkdir(parent, 0700); err != nil {
					t.Fatal(err)
				}
			case "head-change":
				exportTestGit(t, r, "commit", "--quiet", "--allow-empty", "-m", "new head")
			case "destination-created":
				exportTestWrite(t, filepath.Join(o.Path, "user.txt"), []byte("preserve"))
			}
			result, err := ApplyWorkspaceExport(plan, confirmed)
			if err == nil || result.Applied || len(result.Created) != 0 {
				t.Fatalf("unsafe apply accepted: %+v %v", result, err)
			}
			if kind != "destination-created" {
				if _, err := os.Lstat(o.Path); !os.IsNotExist(err) {
					t.Fatal("refusal wrote output")
				}
			}
		})
	}
	for _, stop := range []string{".", ".hermes", ".hermes/SOUL.md", "WORKFLOW.md"} {
		t.Run("partial-"+stop, func(t *testing.T) {
			_, o := exportTestFixture(t)
			plan, err := PlanWorkspaceExport(o)
			if err != nil {
				t.Fatal(err)
			}
			result, err := applyWorkspaceExport(plan, true, func(path string) error {
				if path == stop {
					return fmt.Errorf("injected publication failure")
				}
				return nil
			})
			if err == nil || result.Applied || len(result.Created) == 0 {
				t.Fatal("lost partial result")
			}
			last := result.Created[len(result.Created)-1]
			if last.Path != stop || last.Kind == "file" && last.Complete {
				t.Fatalf("misleading partial census: %+v", result.Created)
			}
			for _, created := range result.Created {
				if _, err := os.Lstat(filepath.Join(o.Path, created.Path)); err != nil {
					t.Fatalf("claimed creation absent: %v", err)
				}
			}
			if _, err := PlanWorkspaceExport(o); err == nil {
				t.Fatal("partial destination reused")
			}
		})
	}
}

func TestWorkspaceExportIgnoresInheritedGitSourceAndConfig(t *testing.T) {
	r, o := exportTestFixture(t)
	for name, value := range map[string]string{"GIT_DIR": "/invalid-git-source", "GIT_WORK_TREE": "/invalid-work-tree", "GIT_OBJECT_DIRECTORY": "/invalid-objects", "GIT_ALTERNATE_OBJECT_DIRECTORIES": "/invalid-alternates", "GIT_INDEX_FILE": "/invalid-index", "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.hooksPath", "GIT_CONFIG_VALUE_0": "/invalid-hooks"} {
		t.Setenv(name, value)
	}
	plan, err := PlanWorkspaceExport(o)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Manifest.SourceRevision != exportTestGit(t, r, "rev-parse", "HEAD") {
		t.Fatal("inherited Git source used")
	}
}

// Bounded adversarial review fixtures: swap each publication boundary after its
// first successful creation, and prove the result never claims complete output.
func TestWorkspaceExportPublicationSubstitutionAndUnknownEntry(t *testing.T) {
	for _, attack := range []string{"root", "directory", "file-link", "source", "unknown-entry", "head"} {
		t.Run(attack, func(t *testing.T) {
			root, options := exportTestFixture(t)
			plan, err := PlanWorkspaceExport(options)
			if err != nil {
				t.Fatal(err)
			}
			attacked := false
			result, err := applyWorkspaceExport(plan, true, func(path string) error {
				if attacked {
					return nil
				}
				switch {
				case attack == "root" && path == ".":
					attacked = true
					if err := os.Rename(options.Path, options.Path+"-created"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(options.Path, 0700); err != nil {
						t.Fatal(err)
					}
					exportTestWrite(t, filepath.Join(options.Path, "unknown.txt"), []byte("preserve replacement"))
				case attack == "directory" && path == ".hermes":
					attacked = true
					p := filepath.Join(options.Path, path)
					if err := os.Rename(p, p+"-created"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(p, 0700); err != nil {
						t.Fatal(err)
					}
				case attack == "file-link" && path == ".hermes/SOUL.md":
					attacked = true
					p := filepath.Join(options.Path, path)
					if err := os.Remove(p); err != nil {
						t.Fatal(err)
					}
					if err := os.Link(filepath.Join(root, "ai-loom-pack/templates/mina/.hermes/SOUL.md"), p); err != nil {
						t.Fatal(err)
					}
				case attack == "source" && path == ".":
					attacked = true
					p := filepath.Join(root, "ai-loom-pack/templates/mina/WORKFLOW.md")
					data, _ := os.ReadFile(p)
					if err := os.Rename(p, p+"-original"); err != nil {
						t.Fatal(err)
					}
					exportTestWrite(t, p, data)
				case attack == "unknown-entry" && path == ".":
					attacked = true
					exportTestWrite(t, filepath.Join(options.Path, "private.txt"), []byte("DO_NOT_READ_UNKNOWN"))
				case attack == "head" && path == ".":
					attacked = true
					exportTestGit(t, root, "commit", "--quiet", "--allow-empty", "-m", "concurrent source head")
				}
				return nil
			})
			if !attacked || err == nil || result.Applied || len(result.Created) == 0 {
				t.Fatalf("publication attack accepted: %+v %v", result, err)
			}
			if attack == "unknown-entry" {
				data, readErr := os.ReadFile(filepath.Join(options.Path, "private.txt"))
				if readErr != nil || string(data) != "DO_NOT_READ_UNKNOWN" || strings.Contains(err.Error(), "DO_NOT_READ_UNKNOWN") {
					t.Fatal("unknown data was lost or exposed")
				}
			}
		})
	}
}

func TestWorkspaceExportGitContractAndBounds(t *testing.T) {
	for _, attack := range []string{"protocol-new-file", "protocol-duplicate", "tracked-symlink", "tracked-executable", "total-bound", "manifest-hardlink", "object-directory-symlink", "manifest-inclusive-bound"} {
		t.Run(attack, func(t *testing.T) {
			root, options := exportTestFixture(t)
			switch attack {
			case "protocol-new-file", "protocol-duplicate":
				path := "ai-loom-pack/templates/mina/protocols/GITHUB.md"
				data, _ := os.ReadFile(filepath.Join(root, path))
				addition := "protocols/MAC.md"
				if attack == "protocol-duplicate" {
					addition = "WORKFLOW.md"
				}
				data = bytes.Replace(data, []byte("The projection is versioned source"), []byte("- `"+addition+"`.\n\nThe projection is versioned source"), 1)
				exportTestWrite(t, filepath.Join(root, path), data)
				exportTestGit(t, root, "add", "--", path)
				exportTestGit(t, root, "commit", "--quiet", "-m", "unreviewed protocol")
			case "tracked-symlink":
				path := "ai-loom-pack/templates/mina/WORKFLOW.md"
				p := filepath.Join(root, path)
				data, _ := os.ReadFile(p)
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("TOOLING.md", p); err != nil {
					t.Fatal(err)
				}
				exportTestGit(t, root, "add", "--", path)
				exportTestGit(t, root, "commit", "--quiet", "-m", "symlink source")
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				exportTestWrite(t, p, data)
			case "tracked-executable":
				path := "ai-loom-pack/templates/mina/WORKFLOW.md"
				exportTestGit(t, root, "update-index", "--chmod=+x", "--", path)
				exportTestGit(t, root, "commit", "--quiet", "-m", "executable source")
			case "total-bound":
				for _, name := range []string{"WORKFLOW.md", "TOOLING.md"} {
					exportTestWrite(t, filepath.Join(root, "ai-loom-pack/templates/mina", name), bytes.Repeat([]byte("x"), maxWorkspaceFileSize))
				}
			case "manifest-inclusive-bound":
				plan, err := PlanWorkspaceExport(options)
				if err != nil {
					t.Fatal(err)
				}
				other := 0
				for _, entry := range plan.Manifest.Files {
					if entry.Path != "WORKFLOW.md" && entry.Path != "TOOLING.md" {
						other += entry.Bytes
					}
				}
				// Both payloads fit individual bounds and the payload census fits
				// total, but adding the manifest must refuse before publication.
				exportTestWrite(t, filepath.Join(root, "ai-loom-pack/templates/mina/WORKFLOW.md"), bytes.Repeat([]byte("x"), maxWorkspaceFileSize))
				exportTestWrite(t, filepath.Join(root, "ai-loom-pack/templates/mina/TOOLING.md"), bytes.Repeat([]byte("x"), workspaceExportMaxBytes-maxWorkspaceFileSize-other-1))
				exportTestGit(t, root, "add", "--", "ai-loom-pack/templates/mina/WORKFLOW.md", "ai-loom-pack/templates/mina/TOOLING.md")
				exportTestGit(t, root, "commit", "--quiet", "-m", "manifest-inclusive bound")
			case "manifest-hardlink":
				p := filepath.Join(root, "ai-loom-pack/manifest.yaml")
				if err := os.Link(p, p+"-link"); err != nil {
					t.Fatal(err)
				}
			case "object-directory-symlink":
				p := filepath.Join(root, ".git/objects")
				if err := os.Rename(p, p+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(p+"-original", p); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := PlanWorkspaceExport(options); err == nil {
				t.Fatal("unreviewed Git source or bound accepted")
			}
			if _, err := os.Stat(options.Path); !os.IsNotExist(err) {
				t.Fatal("refusal created destination")
			}
		})
	}
}
