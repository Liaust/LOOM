// Package hermesprofile publishes and authenticates bounded native Hermes
// recovery packages. It never reads live database bytes or parses SQLite itself.
package hermesprofile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const maxEntries = 8192
const maxBytes = int64(8 << 30)

type Identity struct {
	Device   uint64 `json:"device"`
	Inode    uint64 `json:"inode"`
	Mode     uint32 `json:"mode"`
	Owner    uint32 `json:"owner"`
	Links    uint64 `json:"links"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified_ns"`
	Changed  int64  `json:"changed_ns"`
}

func identity(s unix.Stat_t) Identity {
	return Identity{uint64(s.Dev), s.Ino, uint32(s.Mode), s.Uid, uint64(s.Nlink), s.Size, s.Mtim.Nano(), s.Ctim.Nano()}
}
func statFD(f *os.File) (Identity, error) {
	var s unix.Stat_t
	err := unix.Fstat(int(f.Fd()), &s)
	return identity(s), err
}
func statAt(f *os.File, n string) (Identity, error) {
	var s unix.Stat_t
	err := unix.Fstatat(int(f.Fd()), n, &s, unix.AT_SYMLINK_NOFOLLOW)
	return identity(s), err
}
func sameObject(a, b Identity) bool {
	return a.Device == b.Device && a.Inode == b.Inode && a.Mode == b.Mode && a.Owner == b.Owner && (directory(a) || a.Links == b.Links)
}
func regular(s Identity) bool   { return s.Mode&unix.S_IFMT == unix.S_IFREG && s.Links == 1 }
func directory(s Identity) bool { return s.Mode&unix.S_IFMT == unix.S_IFDIR }
func openAt(parent *os.File, name string, dir bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if dir {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	s, err := statFD(f)
	if err != nil || (dir && !directory(s)) || (!dir && !regular(s)) {
		f.Close()
		return nil, fmt.Errorf("unsafe recovery entry")
	}
	return f, nil
}

type heldDir struct {
	file    *os.File
	parent  *os.File
	name    string
	initial Identity
}
type custody struct{ dirs []heldDir }

func openCustody(path string) (*custody, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return nil, fmt.Errorf("exact absolute recovery path required")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "/")
	s, err := statFD(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	c := &custody{dirs: []heldDir{{file: f, initial: s}}}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, name := range parts {
		if name == "" {
			continue
		}
		if i == len(parts)-1 {
			_, err = c.child(name)
		} else {
			_, err = c.pathChild(name)
		}
		if err != nil {
			c.close()
			return nil, err
		}
	}
	return c, nil
}
func (c *custody) leaf() *os.File { return c.dirs[len(c.dirs)-1].file }
func (c *custody) pathChild(name string) (*os.File, error) {
	p := c.leaf()
	before, err := statAt(p, name)
	if err != nil {
		return nil, err
	}
	f, err := openDirectoryPathAt(p, name)
	if err != nil {
		return nil, err
	}
	after, err := statFD(f)
	if err != nil || !sameObject(before, after) {
		f.Close()
		return nil, fmt.Errorf("directory substituted")
	}
	c.dirs = append(c.dirs, heldDir{file: f, parent: p, name: name, initial: after})
	return f, nil
}
func (c *custody) child(name string) (*os.File, error) {
	p := c.leaf()
	before, err := statAt(p, name)
	if err != nil {
		return nil, err
	}
	f, err := openAt(p, name, true)
	if err != nil {
		return nil, err
	}
	after, err := statFD(f)
	if err != nil || !sameObject(before, after) {
		f.Close()
		return nil, fmt.Errorf("directory substituted")
	}
	c.dirs = append(c.dirs, heldDir{file: f, parent: p, name: name, initial: after})
	return f, nil
}
func (c *custody) revalidate() error {
	for _, h := range c.dirs {
		now, err := statFD(h.file)
		if err != nil || !sameObject(h.initial, now) {
			return fmt.Errorf("held directory changed")
		}
		if h.parent != nil {
			named, e := statAt(h.parent, h.name)
			if e != nil || !sameObject(now, named) {
				return fmt.Errorf("directory namespace substituted")
			}
		}
	}
	return nil
}
func (c *custody) close() {
	for i := len(c.dirs) - 1; i >= 0; i-- {
		c.dirs[i].file.Close()
	}
}
func names(f *os.File) ([]string, error) {
	// A fresh directory descriptor has an independent enumeration offset.
	fresh, err := openAt(f, ".", true)
	if err != nil {
		return nil, err
	}
	defer fresh.Close()
	ns, err := fresh.Readdirnames(maxEntries + 1)
	if err == io.EOF {
		err = nil
	}
	if len(ns) > maxEntries {
		return nil, fmt.Errorf("recovery inventory limit exceeded")
	}
	return ns, err
}
func hashFile(ctx context.Context, f *os.File) (string, int64, error) {
	before, err := statFD(f)
	if err != nil || !regular(before) || before.Size < 0 || before.Size > maxBytes {
		return "", 0, fmt.Errorf("invalid bounded regular file")
	}
	if _, err = f.Seek(0, 0); err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(&contextReader{ctx, f}, maxBytes+1))
	if err != nil {
		return "", 0, err
	}
	after, e := statFD(f)
	if e != nil || before != after || n != before.Size {
		return "", 0, fmt.Errorf("file changed during read")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
func readBounded(f *os.File, max int64) ([]byte, error) {
	s, err := statFD(f)
	if err != nil || !regular(s) || s.Size > max {
		return nil, fmt.Errorf("invalid bounded file")
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	after, e := statFD(f)
	if e != nil || s != after || int64(len(b)) > max {
		return nil, fmt.Errorf("file changed")
	}
	return b, err
}
func writeNew(parent *os.File, name string, data []byte, mode uint32) error {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, mode)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Chmod(os.FileMode(mode)); err != nil {
		return err
	}
	return f.Sync()
}
