package objectstore

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPackageExtractCanonical(t *testing.T) {
	t.Run("nested bytes modes and deterministic recapture", func(t *testing.T) {
		source := t.TempDir()
		packageTestWrite(t, source, "bin/launcher", "#!/bin/sh\nexit 0\n", 0755)
		packageTestWrite(t, source, "data/plain", "exact\x00bytes", 0644)
		p := packageTestCapture(t, source, PackageCaptureOptions{Selection: []string{"."}})
		dest := t.TempDir()
		d, err := OpenPackageDirectory(dest)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if err := ExtractPackage(context.Background(), bytes.NewReader(packageTestBytes(t, p)), d, p.Info); err != nil {
			t.Fatal(err)
		}
		for name, mode := range map[string]os.FileMode{"bin": 0755, "bin/launcher": 0755, "data/plain": 0644} {
			st, err := os.Stat(filepath.Join(dest, name))
			if err != nil || st.Mode().Perm() != mode {
				t.Fatalf("%s: %v, %v", name, st, err)
			}
		}
		q := packageTestCapture(t, dest, PackageCaptureOptions{Selection: []string{"."}})
		if p.Info != q.Info {
			t.Fatalf("extraction changed encoding: %v / %v", p.Info, q.Info)
		}
	})
	t.Run("held destination survives rename", func(t *testing.T) {
		source := t.TempDir()
		packageTestWrite(t, source, "value", "owned", 0644)
		p := packageTestCapture(t, source, PackageCaptureOptions{Selection: []string{"."}})
		parent := t.TempDir()
		dest := filepath.Join(parent, "dest")
		if err := os.Mkdir(dest, 0700); err != nil {
			t.Fatal(err)
		}
		d, err := OpenPackageDirectory(dest)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if err := os.Rename(dest, dest+"-held"); err != nil {
			t.Fatal(err)
		}
		sentinel := t.TempDir()
		if err := os.Symlink(sentinel, dest); err != nil {
			t.Fatal(err)
		}
		if err := ExtractPackage(context.Background(), bytes.NewReader(packageTestBytes(t, p)), d, p.Info); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(dest + "-held/value"); err != nil || string(b) != "owned" {
			t.Fatalf("held target: %q %v", b, err)
		}
		if _, err := os.Stat(filepath.Join(sentinel, "value")); !os.IsNotExist(err) {
			t.Fatal("followed replaced destination")
		}
	})
}

func TestPackageExtractRefusals(t *testing.T) {
	source := t.TempDir()
	packageTestWrite(t, source, "value", "original", 0644)
	p := packageTestCapture(t, source, PackageCaptureOptions{Selection: []string{"."}})
	raw := packageTestBytes(t, p)
	for name, mutate := range map[string]func([]byte, PackageInfo) ([]byte, PackageInfo){
		"changed bytes":   func(b []byte, i PackageInfo) ([]byte, PackageInfo) { b[512] ^= 1; return b, i },
		"truncated":       func(b []byte, i PackageInfo) ([]byte, PackageInfo) { return b[:len(b)-1], i },
		"trailing":        func(b []byte, i PackageInfo) ([]byte, PackageInfo) { return append(b, 0), i },
		"wrong role size": func(b []byte, i PackageInfo) ([]byte, PackageInfo) { i.ContentBytes++; return b, i },
		"wrong entries":   func(b []byte, i PackageInfo) ([]byte, PackageInfo) { i.EntryCount++; return b, i },
		"wrong hash": func(b []byte, i PackageInfo) ([]byte, PackageInfo) {
			i.SHA256 = fmt.Sprintf("sha256:%064x", 0)
			return b, i
		},
	} {
		t.Run(name, func(t *testing.T) { b, i := mutate(bytes.Clone(raw), p.Info); extractMustRefuse(t, b, i) })
	}
	for name, h := range map[string]*tar.Header{
		"parent traversal": {Name: "../escape", Mode: 0644, Typeflag: tar.TypeReg},
		"absolute":         {Name: "/escape", Mode: 0644, Typeflag: tar.TypeReg},
		"symlink":          {Name: "link", Mode: 0777, Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		"hardlink":         {Name: "link", Mode: 0644, Typeflag: tar.TypeLink, Linkname: "value"},
		"privilege mode":   {Name: "value", Mode: 04755, Typeflag: tar.TypeReg},
		"fifo":             {Name: "fifo", Mode: 0644, Typeflag: tar.TypeFifo},
	} {
		t.Run(name, func(t *testing.T) {
			b := packageTestTar(t, []*tar.Header{h}, []string{""})
			i := p.Info
			i.SizeBytes = int64(len(b))
			i.SHA256 = fmt.Sprintf("sha256:%x", sha256.Sum256(b))
			extractMustRefuse(t, b, i)
		})
	}
	t.Run("nonempty destination and no overwrite", func(t *testing.T) {
		dest := t.TempDir()
		packageTestWrite(t, dest, "value", "sentinel", 0644)
		d, _ := OpenPackageDirectory(dest)
		defer d.Close()
		if ExtractPackage(context.Background(), bytes.NewReader(raw), d, p.Info) == nil {
			t.Fatal("accepted nonempty")
		}
		b, _ := os.ReadFile(filepath.Join(dest, "value"))
		if string(b) != "sentinel" {
			t.Fatal("overwrote existing leaf")
		}
	})
	t.Run("cancellation removes private spool", func(t *testing.T) {
		dest := t.TempDir()
		d, _ := OpenPackageDirectory(dest)
		defer d.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if ExtractPackage(ctx, bytes.NewReader(raw), d, p.Info) == nil {
			t.Fatal("accepted cancellation")
		}
		names, _ := os.ReadDir(dest)
		if len(names) != 0 {
			t.Fatal("left extraction state")
		}
	})
}
func extractMustRefuse(t *testing.T, b []byte, i PackageInfo) {
	t.Helper()
	dest := t.TempDir()
	d, err := OpenPackageDirectory(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if ExtractPackage(context.Background(), bytes.NewReader(b), d, i) == nil {
		t.Fatal("accepted invalid package")
	}
	names, err := os.ReadDir(dest)
	if err != nil || len(names) != 0 {
		t.Fatalf("created entries before verification: %v %v", names, err)
	}
}
