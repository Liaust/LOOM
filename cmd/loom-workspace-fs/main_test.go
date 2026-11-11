//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func testRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func mkdir(t *testing.T, name string) {
	t.Helper()
	if err := os.MkdirAll(name, 0750); err != nil {
		t.Fatal(err)
	}
}

func identityAt(t *testing.T, name string) inode {
	t.Helper()
	var stat unix.Stat_t
	if err := unix.Lstat(name, &stat); err != nil {
		t.Fatal(err)
	}
	return inode{uint64(stat.Dev), uint64(stat.Ino)}
}

func invoke(t *testing.T, args ...string) (int, reply) {
	t.Helper()
	var output bytes.Buffer
	code := run(args, &output)
	var result reply
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON result %q: %v", output.String(), err)
	}
	return code, result
}

func TestMovePopulatedDirectoryAndReplay(t *testing.T) {
	root := testRoot(t)
	from, to := "box/source", "box/destination/moved"
	mkdir(t, filepath.Join(root, from, "empty"))
	mkdir(t, filepath.Join(root, "box/destination"))
	file := filepath.Join(root, from, ".pending")
	if err := os.WriteFile(file, []byte("local pending edit"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".pending", filepath.Join(root, from, "link")); err != nil {
		t.Fatal(err)
	}
	expected, child := identityAt(t, filepath.Join(root, from)), identityAt(t, file)
	args := []string{"move", root, from, to, fmt.Sprintf("%d:%d", expected.device, expected.number)}
	for _, replayed := range []bool{false, true} {
		code, result := invoke(t, args...)
		if code != 0 || !result.OK || result.Replayed != replayed || result.Error != "" {
			t.Fatalf("move (replay %t): code=%d result=%+v", replayed, code, result)
		}
		if _, err := os.Lstat(filepath.Join(root, from)); !os.IsNotExist(err) {
			t.Fatalf("source still present: %v", err)
		}
		if got := identityAt(t, filepath.Join(root, to)); got != expected {
			t.Fatalf("directory inode changed: %v != %v", got, expected)
		}
		if got := identityAt(t, filepath.Join(root, to, ".pending")); got != child {
			t.Fatal("child inode changed")
		}
		if content, err := os.ReadFile(filepath.Join(root, to, ".pending")); err != nil || string(content) != "local pending edit" {
			t.Fatalf("child bytes changed: %q %v", content, err)
		}
		if link, err := os.Readlink(filepath.Join(root, to, "link")); err != nil || link != ".pending" {
			t.Fatalf("child symlink changed: %q %v", link, err)
		}
		if info, err := os.Stat(filepath.Join(root, to, "empty")); err != nil || !info.IsDir() {
			t.Fatalf("empty child lost: %v", err)
		}
	}
}

func TestInvalidArgumentsAndPaths(t *testing.T) {
	root := testRoot(t)
	mkdir(t, filepath.Join(root, "source"))
	expected := identityAt(t, filepath.Join(root, "source"))
	id := fmt.Sprintf("%d:%d", expected.device, expected.number)
	cases := [][]string{nil, {"move"}, {"copy", root, "source", "target", id}, {"move", root, "source", "target", id, "extra"}}
	for _, bad := range []string{"", "1", "1:2:3", "-1:2", "+1:2", "1:-2", "1:", " 1:2", "1:18446744073709551616"} {
		cases = append(cases, []string{"move", root, "source", "target", bad})
	}
	for _, bad := range []string{"", ".", "..", "../other", "/absolute", "box/../target", "box//target", "box/target/", ".hidden", "box/.hidden/target", "box\\target", "box/\x00target"} {
		cases = append(cases, []string{"move", root, bad, "target", id}, []string{"move", root, "source", bad, id})
	}
	for _, bad := range []string{"", ".", "relative", root + "/", root + "/../other", root + "\x00"} {
		cases = append(cases, []string{"move", bad, "source", "target", id})
	}
	for _, to := range []string{"source", "source/child"} {
		cases = append(cases, []string{"move", root, "source", to, id})
	}
	cases = append(cases, []string{"move", root, "source/child", "source", id})
	for _, args := range cases {
		code, result := invoke(t, args...)
		if code == 0 || result.OK || result.Error == "" {
			t.Fatalf("accepted invalid arguments %q: %+v", args, result)
		}
	}
	if got := identityAt(t, filepath.Join(root, "source")); got != expected {
		t.Fatal("invalid request changed source")
	}
}

func TestMoveRefusesOccupiedDestination(t *testing.T) {
	for _, kind := range []string{"empty", "populated", "file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := testRoot(t)
			source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
			mkdir(t, source)
			switch kind {
			case "empty", "populated":
				mkdir(t, target)
				if kind == "populated" {
					mkdir(t, filepath.Join(target, "child"))
				}
			case "file":
				if err := os.WriteFile(target, []byte("unrelated"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("source", target); err != nil {
					t.Fatal(err)
				}
			}
			expected, destination := identityAt(t, source), identityAt(t, target)
			if _, err := moveDirectory(root, "source", "target", expected); err == nil {
				t.Fatal("accepted occupied destination")
			}
			if identityAt(t, source) != expected || identityAt(t, target) != destination {
				t.Fatal("replaced an existing entry")
			}
		})
	}
}

func TestRenamePrimitiveDoesNotReplaceEmptyDirectory(t *testing.T) {
	root := testRoot(t)
	mkdir(t, filepath.Join(root, "source"))
	mkdir(t, filepath.Join(root, "target"))
	source, target := identityAt(t, filepath.Join(root, "source")), identityAt(t, filepath.Join(root, "target"))
	fd, err := unix.Open(root, directoryFlags, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := renameNoReplace(fd, "source", fd, "target"); err == nil {
		t.Fatal("kernel rename replaced an empty destination")
	}
	if identityAt(t, filepath.Join(root, "source")) != source || identityAt(t, filepath.Join(root, "target")) != target {
		t.Fatal("kernel rename changed occupied paths")
	}
}

func TestIdentityAndDirectoryRequired(t *testing.T) {
	for _, kind := range []string{"inode", "device", "file", "symlink", "missing", "wrong-replay", "file-replay"} {
		t.Run(kind, func(t *testing.T) {
			root := testRoot(t)
			source := filepath.Join(root, "source")
			mkdir(t, source)
			expected := identityAt(t, source)
			switch kind {
			case "inode":
				expected.number++
			case "device":
				expected.device++
			case "file", "symlink":
				if err := os.Remove(source); err != nil {
					t.Fatal(err)
				}
				var err error
				if kind == "file" {
					err = os.WriteFile(source, []byte("keep"), 0600)
				} else {
					err = os.Symlink("elsewhere", source)
				}
				if err != nil {
					t.Fatal(err)
				}
				expected = identityAt(t, source)
			case "missing", "wrong-replay", "file-replay":
				if err := os.Rename(source, filepath.Join(root, "retained")); err != nil {
					t.Fatal(err)
				}
				if kind == "wrong-replay" {
					mkdir(t, filepath.Join(root, "target"))
				} else if kind == "file-replay" {
					if err := os.WriteFile(filepath.Join(root, "target"), nil, 0600); err != nil {
						t.Fatal(err)
					}
					expected = identityAt(t, filepath.Join(root, "target"))
				}
			}
			if _, err := moveDirectory(root, "source", "target", expected); err == nil {
				t.Fatal("accepted invalid source or replay")
			}
		})
	}
}

func TestSymlinkedRootAndParentsRejected(t *testing.T) {
	for _, part := range []string{"root", "ancestor", "source-parent", "target-parent"} {
		t.Run(part, func(t *testing.T) {
			base := testRoot(t)
			root := filepath.Join(base, "real", "root")
			mkdir(t, filepath.Join(root, "from", "source"))
			mkdir(t, filepath.Join(root, "to"))
			expected := identityAt(t, filepath.Join(root, "from/source"))
			alias := filepath.Join(base, "alias")
			linkTarget := root
			if part == "ancestor" {
				linkTarget = filepath.Dir(root)
			}
			if part == "source-parent" {
				linkTarget = filepath.Join(root, "from")
				alias = filepath.Join(root, "alias")
			}
			if part == "target-parent" {
				linkTarget = filepath.Join(root, "to")
				alias = filepath.Join(root, "alias")
			}
			if err := os.Symlink(linkTarget, alias); err != nil {
				t.Fatal(err)
			}
			from, to := "from/source", "to/target"
			switch part {
			case "root":
				root = alias
			case "ancestor":
				root = filepath.Join(alias, "root")
			case "source-parent":
				from = "alias/source"
			case "target-parent":
				to = "alias/target"
			}
			if _, err := moveDirectory(root, from, to, expected); err == nil {
				t.Fatal("followed parent symlink")
			}
		})
	}
}

func TestPinnedParentRecheck(t *testing.T) {
	for _, replace := range []string{"root", "parent"} {
		t.Run(replace, func(t *testing.T) {
			root := filepath.Join(testRoot(t), "root")
			mkdir(t, filepath.Join(root, "box", "parent"))
			chain, _, err := openParent(root, "box/parent/source")
			if err != nil {
				t.Fatal(err)
			}
			defer chain.close()
			if err := chain.check(); err != nil {
				t.Fatal(err)
			}
			target := root
			if replace == "parent" {
				target = filepath.Join(root, "box", "parent")
			}
			if err := os.Rename(target, target+"-retained"); err != nil {
				t.Fatal(err)
			}
			mkdir(t, target)
			if err := chain.check(); err == nil {
				t.Fatal("accepted a replaced pinned parent")
			}
		})
	}
}
