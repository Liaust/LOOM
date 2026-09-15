package hermesprofile

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const capturePolicy = "per_file_and_native_sqlite_v1"

// Capture describes an interval, not a transaction across all Hermes files.
// Sources in a v2 manifest describe the private captured inventory.
type Capture struct {
	Policy      string    `json:"policy"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	Omitted     []string  `json:"removed_before_capture"`
}

type databaseSnapshot func(context.Context, *os.File, *os.File, *os.File, string) error

type profileCapture struct {
	root       *os.File
	parent     *os.File
	initial    Identity
	dirs       []heldDir
	files      []sourceFile
	receipt    Capture
	entries    int
	bytes      int64
	database   databaseSnapshot
	beforeCopy func(string) // deterministic fixture boundary, never configured
}

func captureError(kind, relative string) error {
	h := sha256.Sum256([]byte(relative))
	return fmt.Errorf("Hermes capture %s (entry=%x)", kind, h[:8])
}

// Exact runtime liveness state is not recoverable user data. Do not exclude
// state/ or cron/: both also contain durable agent state and job definitions.
func ephemeral(name string) bool {
	switch name {
	case "gateway.sock", "gateway.lock", "gateway_state.json", "processes.json",
		"state/gateway.heartbeat", "cron/ticker_heartbeat", "cron/ticker_last_success",
		"cron/.tick.lock", "cron/.jobs.lock":
		return true
	}
	if _, ok := runtimeSocketMode(name); ok {
		return true
	}
	// tempfile names used by the pinned heartbeat writers, only in their
	// exact runtime directories. Durable files in those directories remain.
	for _, prefix := range []string{"state/.gateway_", "cron/.hb_"} {
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".tmp") {
			token := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".tmp")
			if len(token) == 8 && strings.Trim(token, "abcdefghijklmnopqrstuvwxyz0123456789_") == "" {
				return true
			}
		}
	}
	return false
}

func newProfileCapture(parent *os.File, database databaseSnapshot) (*profileCapture, error) {
	if err := unix.Mkdirat(int(parent.Fd()), ".capture", 0700); err != nil {
		return nil, fmt.Errorf("create private capture: %w", err)
	}
	f, err := openAt(parent, ".capture", true)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0700); err != nil {
		f.Close()
		return nil, err
	}
	initial, err := statFD(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &profileCapture{root: f, parent: parent, initial: initial, database: database,
		receipt: Capture{Policy: capturePolicy, StartedAt: time.Now().UTC(), Omitted: []string{}}}, nil
}

func (c *profileCapture) close() {
	for _, f := range c.files {
		f.fd.Close()
	}
	for i := len(c.dirs) - 1; i >= 0; i-- {
		c.dirs[i].file.Close()
	}
	c.root.Close()
}

func captureBinding(f, parent *os.File, name string, initial Identity) error {
	held, err := statFD(f)
	if err != nil || !sameObject(held, initial) {
		return captureError("directory_changed", name)
	}
	if parent != nil {
		named, err := statAt(parent, name)
		if err != nil || !sameObject(named, held) {
			return captureError("directory_replaced", name)
		}
	}
	return nil
}

func (c *profileCapture) walk(ctx context.Context, src, dst, srcParent *os.File, name, prefix string, initial Identity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := captureBinding(src, srcParent, name, initial); err != nil {
		return err
	}
	ns, err := names(src)
	if err != nil {
		return captureError("enumeration_failed", prefix)
	}
	sort.Strings(ns)
	for _, n := range ns {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.entries++
		if c.entries > maxEntries {
			return fmt.Errorf("Hermes capture entry limit exceeded")
		}
		rel := path.Join(prefix, n)
		if !portable(rel) || stringsExternal(rel) || n == "profiles" {
			return captureError("unsafe_path", rel)
		}
		if excluded(rel) || ephemeral(rel) {
			continue
		}
		info, err := statAt(src, n)
		if errors.Is(err, unix.ENOENT) {
			c.receipt.Omitted = append(c.receipt.Omitted, rel)
			continue
		}
		if err != nil {
			return captureError("stat_failed", rel)
		}
		if directory(info) {
			f, err := openAt(src, n, true)
			if errors.Is(err, unix.ENOENT) {
				c.receipt.Omitted = append(c.receipt.Omitted, rel)
				continue
			}
			if err != nil {
				return captureError("directory_open_failed", rel)
			}
			if err = unix.Mkdirat(int(dst.Fd()), n, 0700); err != nil {
				f.Close()
				return err
			}
			d, err := openAt(dst, n, true)
			if err != nil {
				f.Close()
				return err
			}
			if err = d.Chmod(0700); err != nil {
				f.Close()
				d.Close()
				return err
			}
			di, err := statFD(d)
			if err != nil {
				f.Close()
				d.Close()
				return err
			}
			c.dirs = append(c.dirs, heldDir{file: d, parent: dst, name: n, initial: di})
			err = c.walk(ctx, f, d, src, n, rel, info)
			f.Close()
			if err != nil {
				return err
			}
			continue
		}
		if !regular(info) || info.Mode&07000 != 0 {
			return captureError("unsafe_entry", rel)
		}
		if err := c.copyFile(ctx, src, dst, n, rel); err != nil {
			return err
		}
	}
	return captureBinding(src, srcParent, name, initial)
}

func (c *profileCapture) copyFile(ctx context.Context, parent, dst *os.File, name, rel string) error {
	// Replacements before opening select the new version; changes while copying
	// retry only this file. No source is locked, chmodded, renamed or deleted.
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := openAt(parent, name, false)
		if errors.Is(err, unix.ENOENT) {
			c.receipt.Omitted = append(c.receipt.Omitted, rel)
			return nil
		}
		if err != nil {
			return captureError("file_open_failed", rel)
		}
		before, err := statFD(f)
		if err != nil || !regular(before) || before.Mode&07000 != 0 || before.Size < 0 || before.Size > maxBytes {
			f.Close()
			return captureError("unsafe_file", rel)
		}
		fd, err := unix.Openat(int(dst.Fd()), name, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0600)
		if err != nil {
			f.Close()
			return err
		}
		out := os.NewFile(uintptr(fd), name)
		owned, err := statFD(out)
		if err != nil {
			f.Close()
			out.Close()
			return err
		}
		c.files = append(c.files, sourceFile{parent: dst, name: name, initial: owned, fd: out})
		if c.beforeCopy != nil {
			c.beforeCopy(rel)
		}
		db := strings.HasSuffix(name, ".db")
		var copied int64
		if db {
			err = c.copyDatabase(ctx, parent, f, dst, rel, before)
			if err == nil {
				var s Identity
				s, err = statFD(out)
				copied = s.Size
			}
		} else {
			copied, err = io.Copy(out, io.LimitReader(&contextReader{ctx, f}, maxBytes+1))
		}
		after, statErr := statFD(f)
		named, nameErr := statAt(parent, name)
		f.Close()
		stable := statErr == nil && nameErr == nil && sameObject(after, before) && named == after
		if !db {
			stable = stable && before == after && copied == before.Size
		}
		if err == nil && !stable && !db {
			if err = removeCapturedFile(c.files[len(c.files)-1]); err != nil {
				return err
			}
			out.Close()
			c.files = c.files[:len(c.files)-1]
			continue
		}
		if err != nil || !stable {
			return captureError("copy_unstable_or_failed", rel)
		}
		c.bytes += copied
		if copied < 0 || copied > maxBytes || c.bytes > maxBytes {
			return fmt.Errorf("Hermes capture byte limit exceeded")
		}
		mode := before.Mode & 0777
		if db {
			mode = 0600
		}
		if err = out.Chmod(os.FileMode(mode)); err != nil {
			return err
		}
		if !db {
			tv := unix.NsecToTimeval(before.Modified)
			if err = unix.Futimes(int(out.Fd()), []unix.Timeval{tv, tv}); err != nil {
				return err
			}
		}
		if err = out.Sync(); err != nil {
			return err
		}
		return nil
	}
	return captureError("file_busy", rel)
}

func (c *profileCapture) copyDatabase(ctx context.Context, parent, source, dst *os.File, name string, initial Identity) error {
	monitor, err := newSourceMonitor()
	if err != nil {
		return err
	}
	defer monitor.close()
	if err = monitor.add(source, initial, sourceWatch{kind: watchDatabase}); err != nil {
		return err
	}
	if err = c.database(ctx, parent, source, dst, name); err != nil {
		return err
	}
	_, err = monitor.check(&sourceTree{})
	return err
}

func removeCapturedFile(f sourceFile) error {
	if err := capturedFileBinding(f); err != nil {
		return err
	}
	return unix.Unlinkat(int(f.parent.Fd()), f.name, 0)
}

func capturedFileBinding(f sourceFile) error {
	held, err := statFD(f.fd)
	named, e := statAt(f.parent, f.name)
	if err != nil || e != nil || !regular(held) || held.Owner != f.initial.Owner || held.Device != f.initial.Device || held.Inode != f.initial.Inode || held != named {
		return fmt.Errorf("private capture cleanup identity changed")
	}
	return nil
}

// Cleanup owns only descriptors created by this capture, never a live tree.
// Validate the complete exact set before removing any entry.
func (c *profileCapture) remove() error {
	if err := captureBinding(c.root, c.parent, ".capture", c.initial); err != nil {
		return err
	}
	for _, d := range c.dirs {
		if err := captureBinding(d.file, d.parent, d.name, d.initial); err != nil {
			return err
		}
	}
	expected := map[*os.File]map[string]bool{c.root: {}}
	for _, d := range c.dirs {
		if expected[d.parent] == nil {
			expected[d.parent] = map[string]bool{}
		}
		expected[d.parent][d.name] = true
		if expected[d.file] == nil {
			expected[d.file] = map[string]bool{}
		}
	}
	for _, f := range c.files {
		expected[f.parent][f.name] = true
	}
	for dir, entries := range expected {
		ns, err := names(dir)
		if err != nil || len(ns) != len(entries) {
			return fmt.Errorf("private capture cleanup namespace changed")
		}
		for _, n := range ns {
			if !entries[n] {
				return fmt.Errorf("private capture cleanup entry changed")
			}
		}
	}
	for _, f := range c.files {
		if err := capturedFileBinding(f); err != nil {
			return err
		}
	}
	for _, f := range c.files {
		if err := removeCapturedFile(f); err != nil {
			return err
		}
	}
	for i := len(c.dirs) - 1; i >= 0; i-- {
		d := c.dirs[i]
		if err := captureBinding(d.file, d.parent, d.name, d.initial); err != nil {
			return err
		}
		if err := unix.Unlinkat(int(d.parent.Fd()), d.name, unix.AT_REMOVEDIR); err != nil {
			return err
		}
	}
	if err := captureBinding(c.root, c.parent, ".capture", c.initial); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(c.parent.Fd()), ".capture", unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return c.parent.Sync()
}

func (c *profileCapture) prepareCommand() error {
	// Native backup consults managed config, whose initialization requires
	// these directories and creates logs/curator. Empty scaffolding adds no
	// payload file and is owned solely by this disposable capture.
	for _, rel := range []string{"cron", "sessions", "logs", "memories", "logs/curator"} {
		parent := c.root
		for _, name := range strings.Split(rel, "/") {
			var existing *os.File
			for _, d := range c.dirs {
				if d.parent == parent && d.name == name {
					existing = d.file
					break
				}
			}
			if existing != nil {
				parent = existing
				continue
			}
			if err := unix.Mkdirat(int(parent.Fd()), name, 0700); err != nil {
				return err
			}
			f, err := openAt(parent, name, true)
			if err != nil {
				return err
			}
			id, err := statFD(f)
			if err != nil {
				f.Close()
				return err
			}
			c.dirs = append(c.dirs, heldDir{file: f, parent: parent, name: name, initial: id})
			parent = f
		}
	}
	// The native backup lock belongs only to the private captured home.
	if err := c.createOwned(c.root, ".backup.lock", 0600); err != nil {
		return err
	}
	sort.Strings(c.receipt.Omitted)
	c.receipt.CompletedAt = time.Now().UTC()
	return nil
}

func (c *profileCapture) createOwned(parent *os.File, name string, mode uint32) error {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, mode)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	id, err := statFD(f)
	if err != nil {
		f.Close()
		return err
	}
	c.files = append(c.files, sourceFile{parent: parent, name: name, initial: id, fd: f})
	if err := f.Chmod(os.FileMode(mode)); err != nil {
		return err
	}
	return f.Sync()
}

func validCapture(m Manifest) bool {
	if m.Schema == Schema {
		return m.Capture == nil
	}
	c := m.Capture
	if m.Schema != CaptureSchema || c == nil || c.Policy != capturePolicy || c.StartedAt.Location() != time.UTC || c.CompletedAt.Location() != time.UTC || c.StartedAt.Before(m.CreatedAt) || c.CompletedAt.Before(c.StartedAt) || c.CompletedAt.Sub(c.StartedAt) > 5*time.Minute || c.Omitted == nil {
		return false
	}
	entries := map[string]bool{}
	for _, f := range m.Inventory {
		if ephemeral(f.Path) {
			return false
		}
		entries[f.Path] = true
	}
	for i, p := range c.Omitted {
		if !portable(p) || excluded(p) || ephemeral(p) || stringsExternal(p) || entries[p] || p == "SOUL.md" || p == "config.yaml" || p == "state.db" || i > 0 && c.Omitted[i-1] >= p {
			return false
		}
	}
	return len(c.Omitted) <= maxEntries
}
