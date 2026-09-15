package objectstore

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func packageTestWrite(t *testing.T, root, name, body string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}
func packageTestCapture(t *testing.T, root string, opts PackageCaptureOptions) *CapturedPackage {
	t.Helper()
	f, e := OpenPackageDirectory(root)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	p, e := CapturePackage(t.Context(), f, t.TempDir(), opts)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := p.Close(); e != nil {
			t.Error(e)
		}
	})
	return p
}
func packageTestBytes(t *testing.T, p *CapturedPackage) []byte {
	t.Helper()
	b, e := os.ReadFile(p.ArchivePath)
	if e != nil {
		t.Fatal(e)
	}
	info, e := InspectPackage(t.Context(), bytes.NewReader(b))
	if e != nil || info != p.Info {
		t.Fatalf("inspect=%+v error=%v want=%+v", info, e, p.Info)
	}
	return b
}

func TestPackageCaptureCanonical(t *testing.T) {
	root := t.TempDir()
	packageTestWrite(t, root, "z.txt", "payload", 0600)
	packageTestWrite(t, root, "a/run", "#!/bin/sh\nexit 99\n", 0700)
	os.Mkdir(filepath.Join(root, "empty"), 0700)
	p := packageTestCapture(t, root, PackageCaptureOptions{Selection: []string{"z.txt", "empty", "a"}})
	first := packageTestBytes(t, p)
	os.Chmod(filepath.Join(root, "z.txt"), 0644)
	os.Chmod(filepath.Join(root, "a/run"), 0755)
	os.Chtimes(filepath.Join(root, "z.txt"), time.Unix(200, 0), time.Unix(200, 0))
	q := packageTestCapture(t, root, PackageCaptureOptions{Selection: []string{"."}})
	if !bytes.Equal(first, packageTestBytes(t, q)) {
		t.Fatal("canonical bytes changed with selection order/time/equivalent permission bits")
	}
	for _, change := range []struct {
		name  string
		apply func()
	}{{"execute", func() { os.Chmod(filepath.Join(root, "z.txt"), 0755) }}, {"bytes", func() { packageTestWrite(t, root, "z.txt", "changed", 0755) }}, {"name", func() { os.Rename(filepath.Join(root, "z.txt"), filepath.Join(root, "other.txt")) }}, {"directory", func() { os.Mkdir(filepath.Join(root, "new-empty"), 0700) }}} {
		t.Run(change.name, func(t *testing.T) {
			change.apply()
			next := packageTestCapture(t, root, PackageCaptureOptions{Selection: []string{"."}})
			if next.Info.SHA256 == q.Info.SHA256 {
				t.Fatal("identity did not change")
			}
			q = next
			packageTestBytes(t, next)
		})
	}
	t.Run("long_unicode_path", func(t *testing.T) {
		r := t.TempDir()
		name := strings.Repeat("a", 90) + "/" + strings.Repeat("é", 60) + "/leaf"
		packageTestWrite(t, r, name, "x", 0600)
		p := packageTestCapture(t, r, PackageCaptureOptions{Selection: []string{name}})
		packageTestBytes(t, p)
		if p.Info.EntryCount != 3 {
			t.Fatal(p.Info)
		}
	})
	t.Run("source_and_producer", func(t *testing.T) {
		r := t.TempDir()
		for _, n := range []string{"vendor/dep.go", "assets/logo", "node_modules/dependency", ".git/config", ".DS_Store", "run"} {
			packageTestWrite(t, r, n, n, 0600)
		}
		source := packageTestCapture(t, r, PackageCaptureOptions{Selection: []string{"."}})
		producer := packageTestCapture(t, r, PackageCaptureOptions{Selection: []string{"."}, ExcludeDirectories: []string{"node_modules", ".git"}, ExcludeFiles: []string{".DS_Store"}, MaxContentBytes: PackageMaxProducerBytes})
		packageTestBytes(t, source)
		packageTestBytes(t, producer)
		if source.Info.EntryCount <= producer.Info.EntryCount {
			t.Fatal("source dependencies dropped or producer exclusions ignored")
		}
	})
	t.Run("sealed_bytes", func(t *testing.T) {
		r := t.TempDir()
		packageTestWrite(t, r, "x", "old", 0755)
		p := packageTestCapture(t, r, PackageCaptureOptions{Selection: []string{"."}})
		b := packageTestBytes(t, p)
		packageTestWrite(t, r, "x", "new", 0600)
		if got, _ := os.ReadFile(filepath.Join(p.TreePath, "x")); string(got) != "old" {
			t.Fatal("staging aliases source")
		}
		if !bytes.Equal(b, packageTestBytes(t, p)) {
			t.Fatal("archive changed")
		}
	})
}

func TestPackageCaptureRejectsUnsafeTree(t *testing.T) {
	for _, v := range [][]string{nil, {""}, {".", "a"}, {"a", "a"}, {"a", "a/x"}, {"../x"}, {"/absolute"}, {"a//b"}, {"a/./b"}, {"a\\b"}, {"a\x00b"}, {"a\nb"}, {strings.Repeat("x", 1025)}, {strings.Repeat("a/", 64) + "x"}, {string([]byte{255})}} {
		t.Run(fmt.Sprintf("selection_%q", v), func(t *testing.T) {
			if _, e := PackageSelection(v); e == nil {
				t.Fatal("accepted invalid selection")
			}
		})
	}
	for _, kind := range []string{"symlink_file", "symlink_directory", "fifo", "socket", "setuid", "setgid", "sticky", "missing", "file_limit", "producer_content_limit", "source_content_limit", "stage_inside_source"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			stage := t.TempDir()
			opts := PackageCaptureOptions{Selection: []string{"."}}
			switch kind {
			case "symlink_file":
				os.Symlink("outside", filepath.Join(root, "x"))
			case "symlink_directory":
				os.Symlink(t.TempDir(), filepath.Join(root, "x"))
			case "fifo":
				if e := unix.Mkfifo(filepath.Join(root, "x"), 0600); e != nil {
					t.Fatal(e)
				}
			case "socket":
				short, err := os.MkdirTemp("/tmp", "lps-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.RemoveAll(short) })
				root, err = filepath.EvalSymlinks(short)
				if err != nil {
					t.Fatal(err)
				}
				l, e := net.Listen("unix", filepath.Join(root, "x"))
				if e != nil {
					t.Fatal(e)
				}
				defer l.Close()
			case "setuid", "setgid", "sticky":
				mode := os.ModeSetuid
				if kind == "setgid" {
					mode = os.ModeSetgid
				}
				if kind == "sticky" {
					mode = os.ModeSticky
				}
				packageTestWrite(t, root, "x", "x", 0600)
				path := filepath.Join(root, "x")
				if mode == os.ModeSetgid {
					// A temp directory can inherit a group outside our memberships
					// (for example wheel under macOS /tmp). Chmod can then succeed
					// while clearing setgid, so establish a group we actually own.
					if err := os.Chown(path, -1, os.Getegid()); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Chmod(path, 0600|mode); err != nil {
					t.Fatal(err)
				}
				info, err := os.Lstat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode()&mode == 0 {
					t.Fatalf("unsafe fixture missing %s bit after chmod: %s", kind, info.Mode())
				}
			case "missing":
				opts.Selection = []string{"absent"}
			case "file_limit":
				f, e := os.Create(filepath.Join(root, "x"))
				if e != nil {
					t.Fatal(e)
				}
				f.Truncate(PackageMaxFileBytes + 1)
				f.Close()
			case "producer_content_limit":
				opts.MaxContentBytes = PackageMaxProducerBytes
				f, _ := os.Create(filepath.Join(root, "x"))
				f.Truncate(PackageMaxProducerBytes + 1)
				f.Close()
			case "source_content_limit":
				for i := 0; i < 5; i++ {
					f, _ := os.Create(filepath.Join(root, fmt.Sprint(i)))
					f.Truncate(PackageMaxFileBytes)
					f.Close()
				}
			case "stage_inside_source":
				stage = root
			}
			f, e := OpenPackageDirectory(root)
			if e != nil {
				t.Fatal(e)
			}
			defer f.Close()
			p, e := CapturePackage(t.Context(), f, stage, opts)
			if e == nil {
				p.Close()
				t.Fatal("unsafe capture succeeded")
			}
			if names, _ := os.ReadDir(stage); stage != root && len(names) != 0 {
				t.Fatal("failed capture left staging")
			}
		})
	}
	t.Run("symlink_parent", func(t *testing.T) {
		root := t.TempDir()
		os.Symlink(t.TempDir(), filepath.Join(root, "link"))
		if f, e := OpenPackageRoot(root, "link"); e == nil {
			f.Close()
			t.Fatal("followed child symlink")
		}
	})
	t.Run("trusted_anchor_alias", func(t *testing.T) {
		root := t.TempDir()
		anchor := filepath.Join(t.TempDir(), "box")
		os.Symlink(root, anchor)
		os.Mkdir(filepath.Join(root, "child"), 0700)
		f, e := OpenPackageRoot(anchor, "child")
		if e != nil {
			t.Fatal(e)
		}
		f.Close()
	})
}

func packageTestTar(t *testing.T, headers []*tar.Header, bodies []string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for i, h := range headers {
		if e := w.WriteHeader(h); e != nil {
			t.Fatal(e)
		}
		if _, e := io.WriteString(w, bodies[i]); e != nil {
			t.Fatal(e)
		}
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func TestPackageArchiveValidation(t *testing.T) {
	root := t.TempDir()
	packageTestWrite(t, root, "a", "payload", 0600)
	good := packageTestBytes(t, packageTestCapture(t, root, PackageCaptureOptions{Selection: []string{"."}}))
	for _, kind := range []string{"trailing_byte", "trailing_archive", "truncated", "wrong_uid", "wrong_time", "wrong_mode", "link", "hardlink", "fifo_header", "character_device", "block_device", "global_pax", "unknown_pax", "sparse_pax", "duplicate", "missing_parent", "prefix_collision", "wrong_format", "wrong_schema", "empty_name", "file_limit", "size_short", "content_limit", "entry_limit", "archive_limit"} {
		t.Run(kind, func(t *testing.T) {
			h := packageHeader("a", 0644, 1, false)
			headers := []*tar.Header{h}
			bodies := []string{"x"}
			var raw []byte
			switch kind {
			case "trailing_byte":
				raw = append(append([]byte{}, good...), 1)
			case "trailing_archive":
				raw = append(append([]byte{}, good...), good...)
			case "truncated":
				raw = good[:len(good)-600]
			case "wrong_uid":
				h.Uid = 42
			case "wrong_time":
				h.ModTime = time.Unix(2, 0)
			case "wrong_mode":
				h.Mode = 0600
			case "link":
				h.Typeflag = tar.TypeSymlink
				h.Linkname = "outside"
				h.Size = 0
				bodies[0] = ""
			case "hardlink", "fifo_header", "character_device", "block_device":
				switch kind {
				case "hardlink":
					h.Typeflag = tar.TypeLink
					h.Linkname = "a"
				case "fifo_header":
					h.Typeflag = tar.TypeFifo
				case "character_device":
					h.Typeflag = tar.TypeChar
					h.Devmajor = 1
					h.Devminor = 3
				case "block_device":
					h.Typeflag = tar.TypeBlock
					h.Devmajor = 8
					h.Devminor = 0
				}
				h.Size = 0
				bodies[0] = ""
			case "global_pax":
				*h = tar.Header{Typeflag: tar.TypeXGlobalHeader, Format: tar.FormatPAX, PAXRecords: map[string]string{"comment": "global"}}
				bodies[0] = ""
			case "unknown_pax":
				h.PAXRecords["comment"] = "hidden"
			case "sparse_pax":
				h.PAXRecords["ZZZ.sparse.name"] = "a"
			case "duplicate":
				headers = append(headers, packageHeader("a", 0644, 1, false))
				bodies = append(bodies, "y")
			case "missing_parent":
				h.Name = "absent/a"
			case "prefix_collision":
				headers = append(headers, packageHeader("a/b", 0644, 1, false))
				bodies = append(bodies, "y")
			case "wrong_format":
				h.Format = tar.FormatUSTAR
				h.PAXRecords = nil
			case "wrong_schema":
				h.PAXRecords[packageSchemaKey] = "future"
			case "empty_name":
				h.Name = "."
			case "file_limit":
				var b bytes.Buffer
				w := tar.NewWriter(&b)
				h.Size = PackageMaxFileBytes + 1
				if e := w.WriteHeader(h); e != nil {
					t.Fatal(e)
				}
				raw = b.Bytes()
			case "size_short":
				raw = good[:1537]
			case "content_limit":
				headers = nil
				bodies = nil
				for i := 0; i < 5; i++ {
					headers = append(headers, packageHeader(fmt.Sprint(i), 0644, PackageMaxFileBytes, false))
				}
				// Use a synthetic tar stream: no large allocation or disk capture.
				pr, pw := io.Pipe()
				go func() {
					tw := tar.NewWriter(pw)
					for _, hdr := range headers {
						if e := tw.WriteHeader(hdr); e != nil {
							break
						}
						if _, e := io.CopyN(tw, packageZeroReader{}, hdr.Size); e != nil {
							break
						}
					}
					tw.Close()
					pw.Close()
				}()
				_, e := InspectPackage(t.Context(), pr)
				pr.Close()
				if e == nil {
					t.Fatal("content bound accepted")
				}
				return
			case "entry_limit":
				headers = nil
				bodies = nil
				for i := 0; i <= PackageMaxEntries; i++ {
					headers = append(headers, packageHeader(fmt.Sprintf("%05d", i), 0755, 0, true))
					bodies = append(bodies, "")
				}
			case "archive_limit":
				w := packageCountWriter{w: io.Discard, n: PackageMaxArchiveBytes, limit: PackageMaxArchiveBytes}
				if _, e := w.Write([]byte{1}); e == nil {
					t.Fatal("archive limit accepted")
				}
				return
			}
			if raw == nil {
				raw = packageTestTar(t, headers, bodies)
			}
			if kind == "sparse_pax" {
				raw = bytes.ReplaceAll(raw, []byte("ZZZ.sparse.name"), []byte("GNU.sparse.name"))
			}
			if _, e := InspectPackage(t.Context(), bytes.NewReader(raw)); e == nil {
				t.Fatal("invalid archive accepted")
			}
		})
	}
}

type packageZeroReader struct{}

func (packageZeroReader) Read(b []byte) (int, error) { clear(b); return len(b), nil }

func TestPackageCaptureMutationAndCancellation(t *testing.T) {
	for _, kind := range []string{"replace", "symlink", "grow", "shrink", "mode", "add", "remove", "parent", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			stage := t.TempDir()
			packageTestWrite(t, root, "dir/x", "original", 0600)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			opts := PackageCaptureOptions{Selection: []string{"."}, beforeCopy: func(name string) {
				if name != "dir/x" {
					return
				}
				p := filepath.Join(root, name)
				switch kind {
				case "replace":
					os.Rename(p, p+".old")
					packageTestWrite(t, root, name, "original", 0600)
				case "symlink":
					os.Remove(p)
					os.Symlink("elsewhere", p)
				case "grow":
					packageTestWrite(t, root, name, "original plus", 0600)
				case "shrink":
					packageTestWrite(t, root, name, "o", 0600)
				case "mode":
					os.Chmod(p, 0755)
				case "parent":
					os.Rename(filepath.Join(root, "dir"), filepath.Join(root, "old"))
					os.Symlink(filepath.Join(root, "old"), filepath.Join(root, "dir"))
				case "cancel":
					cancel()
				}
			}, beforeRecheck: func() {
				switch kind {
				case "add":
					packageTestWrite(t, root, "added", "x", 0600)
				case "remove":
					os.Remove(filepath.Join(root, "dir/x"))
				}
			}}
			f, e := OpenPackageDirectory(root)
			if e != nil {
				t.Fatal(e)
			}
			defer f.Close()
			p, e := CapturePackage(ctx, f, stage, opts)
			if e == nil {
				p.Close()
				t.Fatal("mutation/cancellation accepted")
			}
			if names, _ := os.ReadDir(stage); len(names) != 0 {
				t.Fatal("staging leaked")
			}
		})
	}
}

// The plan retains the original named checks and also specifies this regex gate.
// This umbrella repeats their bounded cases; the full streaming size boundary is
// independently exercised by TestPackageArchiveValidation in focused/race gates.
func TestRetainedPackageRegression(t *testing.T) {
	t.Run("canonical", TestPackageCaptureCanonical)
	t.Run("unsafe", TestPackageCaptureRejectsUnsafeTree)
	t.Run("mutation", TestPackageCaptureMutationAndCancellation)
	t.Run("digest", func(t *testing.T) {
		r := t.TempDir()
		packageTestWrite(t, r, "x", "x", 0600)
		p := packageTestCapture(t, r, PackageCaptureOptions{Selection: []string{"."}})
		b := packageTestBytes(t, p)
		if p.Info.SHA256 != fmt.Sprintf("sha256:%x", sha256.Sum256(b)) {
			t.Fatal("digest mismatch")
		}
	})
}
