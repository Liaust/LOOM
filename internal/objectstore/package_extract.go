package objectstore

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"
)

// ExtractPackage copies even a mutable input stream into a private, unlinked
// file on the destination filesystem, verifies the complete canonical encoding,
// then creates no-follow, no-overwrite entries. The caller must exclusively own
// the empty destination and discard it on failure; a partial tree is never a
// successful extraction. No source or destination pathname is reopened.
func ExtractPackage(ctx context.Context, source io.Reader, destination *os.File, expected PackageInfo) error {
	if destination == nil || source == nil || expected.Format != PackageFormat || expected.SizeBytes < 1024 || expected.SizeBytes > PackageMaxArchiveBytes {
		return fmt.Errorf("invalid package extraction input")
	}
	root, err := openPackageChild(destination, ".")
	if err != nil {
		return err
	}
	defer root.Close()
	st, err := root.Stat()
	if err != nil || !st.IsDir() {
		return fmt.Errorf("package destination must be a directory")
	}
	names, err := root.Readdirnames(1)
	if len(names) != 0 || err != io.EOF {
		return fmt.Errorf("package destination must be empty")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	name := ".loom-package-extract-" + hex.EncodeToString(random[:])
	fd, err := unix.Openat(int(root.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	archive := os.NewFile(uintptr(fd), name)
	defer archive.Close()
	if err := unix.Unlinkat(int(root.Fd()), name, 0); err != nil {
		return err
	}
	n, err := io.Copy(archive, io.LimitReader(packageContextReader{ctx, source}, expected.SizeBytes+1))
	if err != nil {
		return fmt.Errorf("package ingress: %w", err)
	}
	if n != expected.SizeBytes {
		return fmt.Errorf("package ingress length: copied %d", n)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	actual, err := InspectPackage(ctx, archive)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("package extraction identity mismatch")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	tr := tar.NewReader(packageContextReader{ctx, archive})
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(header.Name, "/")
		// The private archive was fully verified. Recheck path/kind here too,
		// so no future parser extension silently widens this writing boundary.
		if err := packagePath(name); err != nil {
			return err
		}
		parent, err := extractParent(root, path.Dir(name))
		if err != nil {
			return err
		}
		err = extractEntry(ctx, parent, path.Base(name), header, tr)
		parent.Close()
		if err != nil {
			return err
		}
	}
}

func extractParent(root *os.File, name string) (*os.File, error) {
	parent, err := openPackageChild(root, ".")
	if err != nil {
		return nil, err
	}
	if name == "." {
		return parent, nil
	}
	for _, part := range strings.Split(name, "/") {
		next, err := openPackageChild(parent, part)
		parent.Close()
		if err != nil {
			return nil, err
		}
		st, err := next.Stat()
		if err != nil || !st.IsDir() {
			next.Close()
			return nil, fmt.Errorf("package extraction parent is not a directory")
		}
		parent = next
	}
	return parent, nil
}

func extractEntry(ctx context.Context, parent *os.File, name string, h *tar.Header, r io.Reader) error {
	if h.Typeflag == tar.TypeDir && h.Mode == 0755 && h.Size == 0 {
		if err := unix.Mkdirat(int(parent.Fd()), name, 0700); err != nil {
			return err
		}
		f, err := openPackageChild(parent, name)
		if err != nil {
			return err
		}
		defer f.Close()
		return f.Chmod(0755)
	}
	if h.Typeflag != tar.TypeReg || h.Mode != 0644 && h.Mode != 0755 {
		return fmt.Errorf("unsupported extraction entry")
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	n, err := io.Copy(f, packageContextReader{ctx, r})
	if err != nil {
		return fmt.Errorf("package extraction content: %w", err)
	}
	if n != h.Size {
		return fmt.Errorf("package extraction content length: %d", n)
	}
	return f.Chmod(os.FileMode(h.Mode))
}
