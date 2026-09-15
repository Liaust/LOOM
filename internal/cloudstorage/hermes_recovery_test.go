package cloudstorage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"testing"

	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/hermesprofile"
)

type hermesNamespaceItem struct {
	directArchiveListItem
	UID *uint32 `json:"uid,omitempty"`
}

func TestBorgHermesFrozenRecoveryOrdinaryReadme(t *testing.T) {
	for _, workspace := range []string{hermesprofile.WorkspaceRoot, hermesprofile.MinaWorkspaceRoot} {
		t.Run(path.Base(workspace), func(t *testing.T) {
			fixture := newDirectArchiveCloudFixture(t)
			root := strings.TrimPrefix(workspace, "/") + "/recovery"
			pkg := hermesprofile.Evidence{ID: "fixture", Path: "/" + root + "/fixture", Files: []hermesprofile.File{
				{Path: "manifest.json", Mode: 0440, Size: 10, SHA256: strings.Repeat("a", 64)},
				{Path: "profile.zip", Mode: 0440, Size: 20, SHA256: strings.Repeat("b", 64)},
			}}
			for _, v2 := range []bool{false, true} {
				t.Run(fmt.Sprintf("v2=%v", v2), func(t *testing.T) {
					for _, scenario := range []string{"exact", "absent-readme", "arbitrary-size", "max-size", "unknown-file", "unknown-directory", "case-alias", "staging", "nested-readme", "symlink", "hardlink", "fifo", "socket", "directory", "writable", "execute", "setuid", "setgid", "sticky", "malformed-mode", "wrong-type", "oversized", "negative-size", "foreign-owner", "missing-owner", "missing-root-owner", "unhealthy", "duplicate", "missing-manifest", "missing-zip", "package-extra", "package-readme", "undeclared-package-file", "duplicate-package-file"} {
						t.Run(scenario, func(t *testing.T) {
							uid := uint32(501)
							foreign := uint32(502)
							makeItem := func(p, kind, mode string, size int64, sha string) hermesNamespaceItem {
								return hermesNamespaceItem{directArchiveListItem: directArchiveListItem{Path: p, Type: kind, Mode: mode, Size: size, SHA256: sha, Healthy: true}, UID: &uid}
							}
							readme := makeItem(root+"/README.md", "-", "-rw-r--r--", 57, "")
							packageItems := []hermesNamespaceItem{
								makeItem(root+"/fixture", "d", "drwxr-x---", 0, ""),
								makeItem(root+"/fixture/manifest.json", "-", "-r--r-----", 10, pkg.Files[0].SHA256),
								makeItem(root+"/fixture/profile.zip", "-", "-r--r-----", 20, pkg.Files[1].SHA256),
							}
							rootItem := makeItem(root, "d", "drwxr-xr-x", 0, "")
							extra := []hermesNamespaceItem{}
							wantOK := scenario == "exact" || scenario == "absent-readme" || scenario == "arbitrary-size" || scenario == "max-size"
							declaration := pkg
							declaration.Files = slices.Clone(pkg.Files)
							switch scenario {
							case "arbitrary-size":
								readme.Size = 987
							case "max-size":
								readme.Size = hermesprofile.RecoveryReadmeMaxBytes
							case "unknown-file":
								extra = append(extra, makeItem(root+"/notes.md", "-", "-rw-r--r--", 1, ""))
							case "unknown-directory":
								extra = append(extra, makeItem(root+"/unknown", "d", "drwxr-x---", 0, ""))
							case "case-alias":
								readme.Path = root + "/readme.md"
							case "staging":
								extra = append(extra, makeItem(root+"/.staging", "d", "drwx------", 0, ""))
							case "nested-readme":
								extra = append(extra, makeItem(root+"/README.md/extra", "-", "-rw-r--r--", 1, ""))
							case "symlink":
								readme.Type = "l"
								readme.Mode = "lrw-r--r--"
								readme.LinkTarget = "outside"
							case "hardlink":
								readme.Mode = "hrw-r--r--"
								readme.LinkTarget = "outside"
							case "fifo":
								readme.Type = "p"
								readme.Mode = "prw-r--r--"
							case "socket":
								readme.Type = "s"
								readme.Mode = "srw-r--r--"
							case "directory":
								readme.Type = "d"
								readme.Mode = "drw-r--r--"
							case "writable":
								readme.Mode = "-rw-rw-r--"
							case "execute":
								readme.Mode = "-rwxr-xr-x"
							case "setuid":
								readme.Mode = "-rwSr--r--"
							case "setgid":
								readme.Mode = "-rw-r-Sr--"
							case "sticky":
								readme.Mode = "-rw-r--r-T"
							case "malformed-mode":
								readme.Mode = "-ww-r--r--"
							case "wrong-type":
								readme.Type = "l"
							case "oversized":
								readme.Size = hermesprofile.RecoveryReadmeMaxBytes + 1
							case "negative-size":
								readme.Size = -1
							case "foreign-owner":
								readme.UID = &foreign
							case "missing-owner":
								readme.UID = nil
							case "missing-root-owner":
								rootItem.UID = nil
							case "unhealthy":
								readme.Healthy = false
							case "duplicate":
								extra = append(extra, readme)
							case "missing-manifest":
								packageItems = append(packageItems[:1], packageItems[2:]...)
							case "missing-zip":
								packageItems = packageItems[:2]
							case "package-extra":
								packageItems = append(packageItems, makeItem(root+"/fixture/extra", "-", "-r--r-----", 0, ""))
							case "package-readme":
								packageItems = append(packageItems, makeItem(root+"/fixture/README.md", "-", "-rw-r--r--", 1, ""))
							case "undeclared-package-file":
								declaration.Files[0].Path = "README.md"
							case "duplicate-package-file":
								declaration.Files[0].Path = "profile.zip"
							}
							prepared := backupstrategy.PreparedDirectArchiveManifest{Manifest: backupstrategy.DirectArchiveManifest{HermesRecovery: []hermesprofile.Evidence{declaration}}}
							if v2 {
								prepared.ManifestV2 = &backupstrategy.DirectArchiveManifestV2{HermesRecovery: []hermesprofile.Evidence{declaration}}
							}
							queries := 0
							runner := BorgCommandRunner{Config: fixture.cfg, StreamExec: func(ctx context.Context, binary string, command BorgCommand, env []string, w io.Writer) error {
								queries++
								items := slices.Clone(packageItems)
								switch command.Args[len(command.Args)-1] {
								case "pp:" + root:
									if !slices.Contains(command.Args, "{path}{type}{mode}{size}{uid}{linktarget}{health}") {
										return fmt.Errorf("ordinary README must use metadata only")
									}
									if scenario != "absent-readme" {
										items = append(items, readme)
									}
									items = append(items, extra...)
									// The verifier must not depend on Borg returning the owner first.
									items = append(items, rootItem)
								case "pp:" + root + "/fixture":
									if !slices.Contains(command.Args, "{path}{type}{mode}{size}{linktarget}{sha256}{health}") {
										return fmt.Errorf("package hashes required")
									}
								default:
									return fmt.Errorf("unbounded recovery query")
								}
								enc := json.NewEncoder(w)
								for _, item := range items {
									if err := enc.Encode(item); err != nil {
										return err
									}
								}
								return nil
							}}
							err := verifyArchivedHermesRecovery(context.Background(), runner, "fixture-archive", prepared)
							if (err == nil) != wantOK {
								t.Fatalf("acceptance=%v err=%v", wantOK, err)
							}
							if wantOK && queries != 2 {
								t.Fatalf("expected metadata + exact package hash queries, got %d", queries)
							}
						})
					}
				})
			}
		})
	}
}

func TestBorgHermesFrozenRecoveryExactMultiplePackages(t *testing.T) {
	fixture := newDirectArchiveCloudFixture(t)
	root := strings.TrimPrefix(hermesprofile.WorkspaceRoot, "/") + "/recovery"
	packages := []hermesprofile.Evidence{}
	items := []directArchiveListItem{{Path: root, Type: "d", Mode: "drwxr-xr-x", Healthy: true}}
	for _, id := range []string{"first", "second"} {
		pkg := hermesprofile.Evidence{ID: id, Path: "/" + root + "/" + id, Files: []hermesprofile.File{{Path: "manifest.json", Mode: 0440, Size: 1, SHA256: strings.Repeat("a", 64)}, {Path: "profile.zip", Mode: 0440, Size: 2, SHA256: strings.Repeat("b", 64)}}}
		packages = append(packages, pkg)
		items = append(items, directArchiveListItem{Path: root + "/" + id, Type: "d", Mode: "drwxr-x---", Healthy: true})
		for _, f := range pkg.Files {
			items = append(items, directArchiveListItem{Path: root + "/" + id + "/" + f.Path, Type: "-", Mode: "-r--r-----", Size: f.Size, SHA256: f.SHA256, Healthy: true})
		}
	}
	queries := 0
	runner := BorgCommandRunner{Config: fixture.cfg, StreamExec: func(ctx context.Context, binary string, command BorgCommand, env []string, w io.Writer) error {
		queries++
		prefix := strings.TrimPrefix(command.Args[len(command.Args)-1], "pp:")
		if prefix != root && path.Dir(prefix) != root {
			return fmt.Errorf("unbounded query")
		}
		enc := json.NewEncoder(w)
		for _, item := range items {
			if item.Path == prefix || strings.HasPrefix(item.Path, prefix+"/") {
				if err := enc.Encode(item); err != nil {
					return err
				}
			}
		}
		return nil
	}}
	prepared := backupstrategy.PreparedDirectArchiveManifest{Manifest: backupstrategy.DirectArchiveManifest{HermesRecovery: packages}}
	if err := verifyArchivedHermesRecovery(context.Background(), runner, "fixture-archive", prepared); err != nil {
		t.Fatal(err)
	}
	if queries != 3 {
		t.Fatalf("expected complete namespace and both exact packages: %d", queries)
	}
}

func TestMinaBorgFrozenRetainedAndCurrentNamespaces(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		for _, scenario := range []string{"exact", "mixed-namespace", "duplicate", "old-root-query"} {
			t.Run(fmt.Sprintf("v2=%v/%s", v2, scenario), func(t *testing.T) {
				fixture := newDirectArchiveCloudFixture(t)
				root := strings.TrimPrefix(hermesprofile.MinaWorkspaceRoot, "/") + "/recovery"
				packages := []hermesprofile.Evidence{}
				items := []directArchiveListItem{{Path: root, Type: "d", Mode: "drwxr-xr-x", Healthy: true}}
				for _, id := range []string{"mina-current", "retained-v1", "retained-v2"} {
					pkg := hermesprofile.Evidence{ID: id, Path: "/" + root + "/" + id, Files: []hermesprofile.File{{Path: "manifest.json", Mode: 0440, Size: 1, SHA256: strings.Repeat("a", 64)}, {Path: "profile.zip", Mode: 0440, Size: 2, SHA256: strings.Repeat("b", 64)}}}
					packages = append(packages, pkg)
					items = append(items, directArchiveListItem{Path: root + "/" + id, Type: "d", Mode: "drwxr-x---", Healthy: true})
					for _, f := range pkg.Files {
						items = append(items, directArchiveListItem{Path: root + "/" + id + "/" + f.Path, Type: "-", Mode: "-r--r-----", Size: f.Size, SHA256: f.SHA256, Healthy: true})
					}
				}
				if scenario == "mixed-namespace" {
					packages[1].Path = hermesprofile.WorkspaceRoot + "/recovery/retained-v1"
				}
				if scenario == "duplicate" {
					packages = append(packages, packages[1])
				}
				queries := 0
				runner := BorgCommandRunner{Config: fixture.cfg, StreamExec: func(ctx context.Context, binary string, command BorgCommand, env []string, w io.Writer) error {
					queries++
					prefix := strings.TrimPrefix(command.Args[len(command.Args)-1], "pp:")
					if prefix != root && path.Dir(prefix) != root {
						return fmt.Errorf("query escaped selected physical namespace")
					}
					encoder := json.NewEncoder(w)
					for _, item := range items {
						if item.Path == prefix || strings.HasPrefix(item.Path, prefix+"/") {
							if scenario == "old-root-query" {
								item.Path = strings.Replace(item.Path, "/mina/", "/morathustra/", 1)
							}
							if err := encoder.Encode(item); err != nil {
								return err
							}
						}
					}
					return nil
				}}
				prepared := backupstrategy.PreparedDirectArchiveManifest{Manifest: backupstrategy.DirectArchiveManifest{HermesRecovery: packages}}
				if v2 {
					prepared.ManifestV2 = &backupstrategy.DirectArchiveManifestV2{HermesRecovery: packages}
				}
				err := verifyArchivedHermesRecovery(context.Background(), runner, "immutable-original-archive-name", prepared)
				if (err == nil) != (scenario == "exact") {
					t.Fatalf("%v", err)
				}
				if scenario == "exact" && queries != 4 {
					t.Fatalf("expected namespace and all three packages, got %d", queries)
				}
			})
		}
	}
}
