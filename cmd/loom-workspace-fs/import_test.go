//go:build darwin || linux

package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

type importTestMember struct {
	name, data, link string
	kind             byte
	mode, size       int64
}

func importTar(t *testing.T, members ...importTestMember) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, member := range members {
		kind, mode, size := member.kind, member.mode, int64(len(member.data))
		if kind == 0 {
			kind = tar.TypeReg
		}
		if mode == 0 {
			mode = 0644
		}
		if member.size != 0 {
			size = member.size
		}
		err := w.WriteHeader(&tar.Header{Name: member.name, Typeflag: kind, Mode: mode, Size: size, Linkname: member.link, Format: tar.FormatPAX})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(member.data)); err != nil {
			t.Fatal(err)
		}
	}
	// Deliberately incomplete large members exercise header limits without a
	// half-gigabyte fixture. Their headers are still a complete 512-byte block.
	_ = w.Close()
	if b.Len() < 1024 {
		b.Write(make([]byte, 1024-b.Len()))
	}
	return b.Bytes()
}

func importFixture(t *testing.T, payload []byte) (string, importRequest) {
	t.Helper()
	base := testRoot(t)
	root := filepath.Join(base, "root")
	mkdir(t, filepath.Join(root, "copies"))
	archive := filepath.Join(base, "export.tar")
	if err := os.WriteFile(archive, payload, 0600); err != nil {
		t.Fatal(err)
	}
	identity := identityAt(t, root)
	sum := sha256.Sum256(payload)
	return root, importRequest{RootIdentity: fmt.Sprintf("%d:%d", identity.device, identity.number), Archive: archive, ArchiveSHA256: hex.EncodeToString(sum[:]), ArchiveBytes: int64(len(payload)), Target: "copies/new", ID: "test-unique_01"}
}

func importJSON(t *testing.T, root string, input []byte) (int, importReply) {
	t.Helper()
	var output bytes.Buffer
	code := runImport(root, bytes.NewReader(input), &output)
	var result importReply
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("invalid import reply %q: %v", output.String(), err)
	}
	return code, result
}

func requireImportFailure(t *testing.T, root string, request importRequest) importReply {
	t.Helper()
	input, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	code, result := importJSON(t, root, input)
	if code != 1 || result.OK || result.Error == "" || result.Published || result.Uncertain || result.Stage != "" {
		t.Fatalf("unexpected refusal: code=%d %+v", code, result)
	}
	if _, err := os.Lstat(filepath.Join(root, request.Target)); !os.IsNotExist(err) {
		t.Fatalf("failed import published: %v", err)
	}
	if stages, err := filepath.Glob(filepath.Join(root, ".loom-working-copy-*.partial")); err != nil || len(stages) != 0 {
		t.Fatalf("failed import left owned stages: %v %v", stages, err)
	}
	return result
}

func TestImportGitFilesExecutableAndEmptyDirectories(t *testing.T) {
	payload := importTar(t,
		importTestMember{name: "repo/.git/HEAD", data: "ref: refs/heads/main\n"},
		importTestMember{name: "repo/.git/config", data: "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n[remote \"origin\"]\n\turl = https://example.invalid/repo.git\n"},
		importTestMember{name: "repo/.git/objects/empty/", kind: tar.TypeDir},
		importTestMember{name: "repo/.git/", kind: tar.TypeDir}, // Explicit after implicit parent.
		importTestMember{name: "repo/run", data: "#!/bin/sh\nexit 0\n", mode: 06755},
		importTestMember{name: "file.txt", data: "exact contents\n"},
		importTestMember{name: "empty/", kind: tar.TypeDir, mode: 02755},
	)
	root, request := importFixture(t, payload)
	input, _ := json.Marshal(request)
	code, result := importJSON(t, root, input)
	if code != 0 || !result.OK || result.Replayed || !result.Published || result.Uncertain || result.Stage != "" || result.Error != "" || result.SymlinkPolicy != "reject" || result.Members != 7 {
		t.Fatalf("import: code=%d %+v", code, result)
	}
	target := filepath.Join(root, request.Target)
	identity := identityAt(t, target)
	if result.DirectoryIdentity != fmt.Sprintf("%d:%d", identity.device, identity.number) {
		t.Fatalf("wrong published identity: %+v", result)
	}
	wantBytes := int64(0)
	for name, want := range map[string]string{
		"file.txt": "exact contents\n", "repo/run": "#!/bin/sh\nexit 0\n", "repo/.git/HEAD": "ref: refs/heads/main\n",
		"repo/.git/config": "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n[remote \"origin\"]\n\turl = https://example.invalid/repo.git\n",
	} {
		got, err := os.ReadFile(filepath.Join(target, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
		wantBytes += int64(len(want))
	}
	if result.PayloadBytes != wantBytes {
		t.Fatalf("payload count: %d != %d", result.PayloadBytes, wantBytes)
	}
	for _, name := range []string{"empty", "repo/.git/objects/empty"} {
		entries, err := os.ReadDir(filepath.Join(target, name))
		if err != nil || len(entries) != 0 {
			t.Fatalf("empty directory lost: %s %v", name, err)
		}
	}
	var stat unix.Stat_t
	if err := unix.Lstat(filepath.Join(target, "repo/run"), &stat); err != nil || stat.Mode&07777 != 0755 {
		t.Fatalf("execute/special bits: %o %v", stat.Mode, err)
	}
	if got, err := os.ReadFile(request.Archive); err != nil || !bytes.Equal(got, payload) {
		t.Fatal("source archive changed")
	}
	var receipt importReceipt
	receiptPath := filepath.Join(target, importReceiptName)
	data, err := os.ReadFile(receiptPath)
	if err != nil || json.Unmarshal(data, &receipt) != nil || receipt.importRequest != request || receipt.DirectoryIdentity != result.DirectoryIdentity {
		t.Fatalf("wrong receipt: %q %v", data, err)
	}
	if err := unix.Lstat(receiptPath, &stat); err != nil || stat.Mode&07777 != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		t.Fatalf("unsafe receipt: %+v %v", stat, err)
	}
	// A lost reply must not replace or even re-inspect the user's edited files.
	if err := os.WriteFile(filepath.Join(target, "file.txt"), []byte("user edited"), 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(target, "repo/run")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/outside", filepath.Join(target, "new-user-link")); err != nil {
		t.Fatal(err)
	}
	code, replay := importJSON(t, root, input)
	if code != 0 || !replay.OK || !replay.Replayed || !replay.Published || replay.Uncertain || replay.DirectoryIdentity != result.DirectoryIdentity || replay.Members != result.Members || replay.PayloadBytes != result.PayloadBytes || identityAt(t, target) != identity {
		t.Fatalf("replay did not verify the native receipt: %+v", replay)
	}
	if got, err := os.ReadFile(filepath.Join(target, "file.txt")); err != nil || string(got) != "user edited" {
		t.Fatal("replay changed user's edit")
	}
	if _, err := os.Lstat(filepath.Join(target, "repo/run")); !os.IsNotExist(err) {
		t.Fatal("replay recreated deleted user file")
	}
	if link, err := os.Readlink(filepath.Join(target, "new-user-link")); err != nil || link != "/outside" {
		t.Fatal("replay touched user's symlink")
	}
}

func TestImportRejectsMaliciousMembers(t *testing.T) {
	cases := map[string][]importTestMember{}
	for _, name := range []string{"../escape", "/absolute", "a/../../escape", "a\\escape", "a//b", "a/./b", "./a", "a/../b", "C:/escape", ".", ".."} {
		cases[name] = []importTestMember{{name: name, data: "bad"}}
	}
	cases["duplicate-file"] = []importTestMember{{name: "a", data: "first"}, {name: "a", data: "second"}}
	cases["duplicate-dir"] = []importTestMember{{name: "a/", kind: tar.TypeDir}, {name: "a", kind: tar.TypeDir}}
	cases["parent-file"] = []importTestMember{{name: "a", data: "file"}, {name: "a/b", data: "bad"}}
	cases["dir-file-alias"] = []importTestMember{{name: "a/b", data: "file"}, {name: "a", data: "bad"}}
	cases["case-alias"] = []importTestMember{{name: "A", data: "file"}, {name: "a", data: "bad"}}
	cases["case-parent-alias"] = []importTestMember{{name: "A/one", data: "file"}, {name: "a/two", data: "bad"}}
	for _, name := range []string{importReceiptName, ".LOOM-working-copy.json", importReceiptName + "/child"} {
		cases[name] = []importTestMember{{name: name, data: "forged"}}
	}
	cases["symlink-parent"] = []importTestMember{{name: "a", kind: tar.TypeSymlink, link: "../outside"}, {name: "a/b", data: "bad"}}
	cases["safe-symlink-held"] = []importTestMember{{name: "a", data: "file"}, {name: "b", kind: tar.TypeSymlink, link: "a"}}
	for _, kind := range []byte{tar.TypeLink, tar.TypeChar, tar.TypeBlock, tar.TypeFifo, 'X'} {
		link := ""
		if kind == tar.TypeLink {
			link = "a"
		}
		cases[fmt.Sprintf("special-%d", kind)] = []importTestMember{{name: "special", kind: kind, link: link}}
	}
	for _, name := range []string{"repo/.git", "repo/.git/commondir", "repo/.git/gitdir", "repo/.git/config.worktree", "repo/.git/worktrees/x/gitdir", "repo/.git/modules/sub/config", "repo/.git/objects/info/alternates", "repo/.git/objects/info/http-alternates", "repo/.GIT/commondir"} {
		cases[name] = []importTestMember{{name: name, data: "/external\n"}}
	}
	for _, value := range []string{"[core]\nworktree = /outside", "[include]\npath = /outside", "[core]\nhooksPath = /outside", "[core]\nfsmonitor = command", "[core]\nwork\\\ntree = /outside"} {
		cases[value] = []importTestMember{{name: ".git/config", data: value}}
	}
	cases["oversize-payload"] = []importTestMember{{name: "large", size: importMaxBytes + 1}}
	for name, members := range cases {
		t.Run(name, func(t *testing.T) {
			root, request := importFixture(t, importTar(t, members...))
			requireImportFailure(t, root, request)
		})
	}
}

func TestImportReplayRequiresExactOwnedReceipt(t *testing.T) {
	for _, kind := range []string{"missing", "symlink", "hardlink", "mode", "malformed", "extra", "inode", "root", "id", "hash", "bytes", "archive", "target"} {
		t.Run(kind, func(t *testing.T) {
			root, request := importFixture(t, importTar(t, importTestMember{name: "file", data: "keep"}))
			input, _ := json.Marshal(request)
			if code, result := importJSON(t, root, input); code != 0 || !result.OK {
				t.Fatalf("initial import: %+v", result)
			}
			target := filepath.Join(root, request.Target)
			receiptPath := filepath.Join(target, importReceiptName)
			data, err := os.ReadFile(receiptPath)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing", "symlink":
				if err := os.Rename(receiptPath, receiptPath+".saved"); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					if err := os.Symlink(receiptPath+".saved", receiptPath); err != nil {
						t.Fatal(err)
					}
				}
			case "hardlink":
				if err := os.Link(receiptPath, receiptPath+".link"); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(receiptPath, 0644); err != nil {
					t.Fatal(err)
				}
			case "malformed", "extra":
				if kind == "malformed" {
					data = []byte("{}\n")
				} else {
					data = append(data, []byte("{}")...)
				}
				if err := os.WriteFile(receiptPath, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "inode":
				if err := os.Rename(target, target+"-retained"); err != nil {
					t.Fatal(err)
				}
				mkdir(t, target)
				if err := os.WriteFile(receiptPath, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "root":
				var receipt importReceipt
				if err := json.Unmarshal(data, &receipt); err != nil {
					t.Fatal(err)
				}
				receipt.RootIdentity = "0:0"
				data, _ = json.Marshal(receipt)
				if err := os.WriteFile(receiptPath, append(data, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			case "id":
				request.ID = "different-id"
			case "hash":
				request.ArchiveSHA256 = strings.Repeat("0", 64)
			case "bytes":
				request.ArchiveBytes += 512
			case "archive":
				request.Archive += ".other"
			case "target":
				request.Target = "copies/renamed"
				if err := os.Rename(target, filepath.Join(root, request.Target)); err != nil {
					t.Fatal(err)
				}
				target = filepath.Join(root, request.Target)
			}
			before := identityAt(t, target)
			input, _ = json.Marshal(request)
			code, result := importJSON(t, root, input)
			if code != 1 || result.OK || result.Replayed || result.Published || result.Uncertain || result.Stage != "" || !strings.Contains(result.Error, "collision") {
				t.Fatalf("accepted mismatching replay: %+v", result)
			}
			if identityAt(t, target) != before {
				t.Fatal("replay changed destination inode")
			}
			if entries, err := filepath.Glob(filepath.Join(root, ".loom-working-copy-*.partial")); err != nil || len(entries) != 0 {
				t.Fatal("replay extracted another stage")
			}
		})
	}
}

func TestImportEmptyArchiveUnicodeAndPAXNames(t *testing.T) {
	for _, members := range [][]importTestMember{nil, {{name: "caf\u00e9.txt", data: "unicode"}, {name: strings.Repeat("long", 40) + "/file", data: "PAX"}}} {
		root, request := importFixture(t, importTar(t, members...))
		input, _ := json.Marshal(request)
		code, result := importJSON(t, root, input)
		if code != 0 || !result.OK || result.Uncertain || result.Members != len(members) {
			t.Fatalf("valid tar rejected: %+v", result)
		}
	}
}

func TestImportArchiveVerification(t *testing.T) {
	valid := importTar(t, importTestMember{name: "file", data: "content"})
	for _, kind := range []string{"hash", "short", "long", "junk", "concatenated", "not-tar", "truncated", "sparse-pax", "archive-limit", "member-limit", "member-boundary"} {
		t.Run(kind, func(t *testing.T) {
			payload := append([]byte(nil), valid...)
			switch kind {
			case "junk":
				payload = append(payload, append([]byte{1}, make([]byte, 511)...)...)
			case "concatenated":
				payload = append(payload, valid...)
			case "not-tar":
				payload = bytes.Repeat([]byte{'z'}, 1024)
			case "truncated":
				payload = importTar(t, importTestMember{name: "file", size: 4096, data: "short"})
			case "sparse-pax":
				var b bytes.Buffer
				w := tar.NewWriter(&b)
				if err := w.WriteHeader(&tar.Header{Name: "sparse", Typeflag: tar.TypeReg, Mode: 0600, PAXRecords: map[string]string{"SCHILY.realsize": "999"}}); err != nil {
					t.Fatal(err)
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				payload = b.Bytes()
			case "member-limit", "member-boundary":
				count := importMaxMembers
				if kind == "member-limit" {
					count++
				}
				members := make([]importTestMember, count)
				for i := range members {
					members[i] = importTestMember{name: fmt.Sprintf("dir-%05d/", i), kind: tar.TypeDir}
				}
				payload = importTar(t, members...)
			}
			root, request := importFixture(t, payload)
			switch kind {
			case "hash":
				request.ArchiveSHA256 = strings.Repeat("0", 64)
			case "short":
				request.ArchiveBytes -= 512
			case "long":
				request.ArchiveBytes += 512
			case "archive-limit":
				f, err := os.OpenFile(request.Archive, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				err = f.Truncate(importMaxBytes + 512)
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
				request.ArchiveBytes = importMaxBytes + 512
			}
			if kind == "member-boundary" {
				input, _ := json.Marshal(request)
				code, result := importJSON(t, root, input)
				if code != 0 || !result.OK || result.Members != importMaxMembers {
					t.Fatalf("refused exactly 10000 snapshot members plus receipt: %+v", result)
				}
			} else {
				requireImportFailure(t, root, request)
			}
		})
	}
}

func TestImportRejectsUnsafeSourceAndDestination(t *testing.T) {
	for _, kind := range []string{"source-symlink", "source-hardlink", "source-dir", "source-fifo", "source-parent-symlink", "root-symlink", "root-ancestor-symlink", "target-parent-symlink", "wrong-root", "collision-file", "collision-dir", "collision-symlink", "existing-stage"} {
		t.Run(kind, func(t *testing.T) {
			root, request := importFixture(t, importTar(t, importTestMember{name: "file", data: "content"}))
			saved := ""
			switch kind {
			case "source-symlink", "source-dir", "source-fifo":
				if err := os.Rename(request.Archive, request.Archive+".saved"); err != nil {
					t.Fatal(err)
				}
				var err error
				switch kind {
				case "source-symlink":
					err = os.Symlink(request.Archive+".saved", request.Archive)
				case "source-dir":
					err = os.Mkdir(request.Archive, 0700)
				case "source-fifo":
					err = unix.Mkfifo(request.Archive, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "source-hardlink":
				if err := os.Link(request.Archive, request.Archive+".link"); err != nil {
					t.Fatal(err)
				}
			case "source-parent-symlink", "root-ancestor-symlink":
				base := filepath.Dir(root)
				alias := base + "-alias"
				if err := os.Symlink(base, alias); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Remove(alias) })
				if kind == "source-parent-symlink" {
					request.Archive = filepath.Join(alias, "export.tar")
				} else {
					root = filepath.Join(alias, "root")
				}
			case "root-symlink":
				alias := root + "-alias"
				if err := os.Symlink(root, alias); err != nil {
					t.Fatal(err)
				}
				root = alias
			case "target-parent-symlink":
				if err := os.Symlink(filepath.Dir(root), filepath.Join(root, "alias")); err != nil {
					t.Fatal(err)
				}
				request.Target = "alias/copy"
			case "wrong-root":
				id := identityAt(t, root)
				request.RootIdentity = fmt.Sprintf("%d:%d", id.device, id.number+1)
			case "collision-file", "collision-dir", "collision-symlink", "existing-stage":
				saved = filepath.Join(root, request.Target)
				if kind == "existing-stage" {
					saved = filepath.Join(root, ".loom-working-copy-"+request.ID+".partial")
				}
				var err error
				switch kind {
				case "collision-file":
					err = os.WriteFile(saved, []byte("keep"), 0600)
				case "collision-symlink":
					err = os.Symlink("../elsewhere", saved)
				default:
					err = os.Mkdir(saved, 0700)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			var savedID inode
			if saved != "" {
				savedID = identityAt(t, saved)
			}
			input, _ := json.Marshal(request)
			code, result := importJSON(t, root, input)
			wantUncertain := kind == "existing-stage"
			wantStage := ""
			if wantUncertain {
				wantStage = ".loom-working-copy-" + request.ID + ".partial"
			}
			if code != 1 || result.OK || result.Published || result.Uncertain != wantUncertain || result.Stage != wantStage {
				t.Fatalf("accepted unsafe path: %+v", result)
			}
			if saved != "" && identityAt(t, saved) != savedID {
				t.Fatal("changed unowned existing entry")
			}
		})
	}
}

func TestImportRequestAndDispatch(t *testing.T) {
	root, valid := importFixture(t, importTar(t, importTestMember{name: "file", data: "ok"}))
	input, _ := json.Marshal(valid)
	for _, bad := range [][]byte{nil, []byte("null"), []byte("{}"), append(append([]byte(nil), input...), []byte(" {}")...), []byte(strings.TrimSuffix(string(input), "}") + `,"unknown":true}`), append(append([]byte(nil), input...), bytes.Repeat([]byte(" "), 64<<10)...)} {
		if code, result := importJSON(t, root, bad); code != 1 || result.OK || result.Published {
			t.Fatalf("accepted invalid JSON: %q %+v", bad, result)
		}
	}
	for _, field := range []string{"id", "root", "archive", "target", "hash", "bytes"} {
		for _, value := range []string{"", "../escape", ".hidden", "a//b", "a\\b", "a/../b", "/absolute"} {
			request, rootArg := valid, root
			switch field {
			case "id":
				request.ID = value
			case "root":
				rootArg = value
			case "archive":
				request.Archive = value
			case "target":
				request.Target = value
			case "hash":
				request.ArchiveSHA256 = value
			case "bytes":
				request.ArchiveBytes = 0
			}
			data, _ := json.Marshal(request)
			if code, result := importJSON(t, rootArg, data); code != 1 || result.OK {
				t.Fatalf("accepted invalid %s=%q: %+v", field, value, result)
			}
		}
	}
	f, err := os.CreateTemp(t.TempDir(), "request")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(input); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = original }()
	var output bytes.Buffer
	if code := run([]string{"import", root}, &output); code != 0 {
		t.Fatalf("dispatch: %s", output.String())
	}
}

func TestImportUnencodableNamesAndSparseType(t *testing.T) {
	for _, name := range []string{"a/", "a\x00b", string([]byte{0xff}), strings.Repeat("x/", 64) + "file"} {
		if _, err := importMemberName(&tar.Header{Name: name, Typeflag: tar.TypeReg}); err == nil {
			t.Fatalf("accepted unsafe member %q", name)
		}
	}
	_, stage := newImportTestStage(t)
	if err := stage.member(tar.NewReader(bytes.NewReader(nil)), &tar.Header{Name: "sparse", Typeflag: tar.TypeGNUSparse}, "sparse"); err == nil {
		t.Fatal("accepted old GNU sparse type")
	}
}

func newImportTestStage(t *testing.T) (string, *importStage) {
	t.Helper()
	root := filepath.Join(testRoot(t), "root")
	mkdir(t, root)
	chain, _, err := openParent(root, "unused")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(chain.close)
	name := ".loom-working-copy-test.partial"
	if err := unix.Mkdirat(chain.fd(), name, 0700); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Openat(chain.fd(), name, directoryFlags, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		t.Fatal(err)
	}
	return root, &importStage{root: chain, fd: fd, name: name, nodes: map[string]*importNode{"": {identity: importIdentity(&st), dir: true}}}
}

func TestImportPinnedStageAndMemberDrift(t *testing.T) {
	for _, kind := range []string{"root", "stage", "member-parent", "member-file", "unknown-member"} {
		t.Run(kind, func(t *testing.T) {
			root, stage := newImportTestStage(t)
			payload := importTar(t, importTestMember{name: "dir/file", data: "keep"})
			tr := tar.NewReader(bytes.NewReader(payload))
			h, err := tr.Next()
			if err != nil {
				t.Fatal(err)
			}
			if err := stage.member(tr, h, h.Name); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, stage.name)
			swapped := ""
			switch kind {
			case "root":
				swapped = root
			case "stage":
				swapped = path
			case "member-parent":
				swapped = filepath.Join(path, "dir")
			case "member-file":
				swapped = filepath.Join(path, "dir/file")
			case "unknown-member":
				if err := os.WriteFile(filepath.Join(path, "unknown"), []byte("untouched"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if swapped != "" {
				if err := os.Rename(swapped, swapped+"-retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(swapped+"-retained", swapped); err != nil {
					t.Fatal(err)
				}
			}
			if err := stage.finish(false); err == nil {
				t.Fatal("accepted drift before publication")
			}
			if err := stage.finish(true); err == nil {
				t.Fatal("claimed cleanup of swapped/unknown stage")
			}
			if swapped != "" {
				if _, err := os.Readlink(swapped); err != nil {
					t.Fatalf("deleted swapped directory entry: %v", err)
				}
			} else if got, err := os.ReadFile(filepath.Join(path, "unknown")); err != nil || string(got) != "untouched" {
				t.Fatal("deleted unknown staged data")
			}
		})
	}
}
