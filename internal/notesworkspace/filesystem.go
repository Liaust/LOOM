package notesworkspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type sourceDirectory struct {
	dirs  []*os.File
	names []string
}

func (d *sourceDirectory) current() *os.File { return d.dirs[len(d.dirs)-1] }
func (d *sourceDirectory) fd() int           { return int(d.current().Fd()) }
func (d *sourceDirectory) Close() {
	for i := len(d.dirs) - 1; i >= 0; i-- {
		d.dirs[i].Close()
	}
}
func (d *sourceDirectory) check() error {
	for i, name := range d.names {
		fd, err := unix.Openat(int(d.dirs[i].Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return ErrUnsafePath
		}
		file := os.NewFile(uintptr(fd), name)
		actual, e := file.Stat()
		file.Close()
		expected, e2 := d.dirs[i+1].Stat()
		if e != nil || e2 != nil || !os.SameFile(actual, expected) {
			return ErrUnsafePath
		}
	}
	return nil
}
func openSourceParent(root, relative string) (*sourceDirectory, string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, "", ErrUnsafePath
	}
	full := filepath.Join(root, filepath.FromSlash(relative))
	parent := filepath.Dir(full)
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", err
	}
	d := &sourceDirectory{dirs: []*os.File{os.NewFile(uintptr(fd), "/")}}
	for _, part := range strings.Split(strings.TrimPrefix(parent, "/"), "/") {
		if part == "" {
			continue
		}
		if err := checkNameAlias(d.current(), part); err != nil {
			d.Close()
			return nil, "", err
		}
		next, err := unix.Openat(d.fd(), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			d.Close()
			return nil, "", err
		}
		d.names = append(d.names, part)
		d.dirs = append(d.dirs, os.NewFile(uintptr(next), part))
	}
	name := filepath.Base(full)
	if err := checkNameAlias(d.current(), name); err != nil {
		d.Close()
		return nil, "", err
	}
	return d, name, nil
}
func checkNameAlias(parent *os.File, name string) error {
	fd, err := unix.Openat(int(parent.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), ".")
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return err
	}
	key := pathKey(name)
	for _, e := range entries {
		if e.Name() != name && pathKey(e.Name()) == key {
			return fmt.Errorf("source path case/Unicode collision")
		}
	}
	return nil
}
func readAt(parent *os.File, name string) ([]byte, bool, error) {
	b, exists, err := readBytesAt(parent, name, MaxContentBytes)
	if err == nil {
		err = validateContent(b)
	}
	return b, exists, err
}

func readBytesAt(parent *os.File, name string, limit int64) ([]byte, bool, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil {
		return nil, false, err
	}
	if !before.Mode().IsRegular() || before.Size() > limit || stat.Nlink != 1 {
		return nil, false, ErrReferenceOnly
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, false, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, false, fmt.Errorf("source changed during read")
	}
	if int64(len(b)) > limit {
		return nil, false, ErrReferenceOnly
	}
	return b, true, nil
}
func readSource(d *sourceDirectory, name string) ([]byte, bool, error) {
	if err := d.check(); err != nil {
		return nil, false, err
	}
	return readAt(d.current(), name)
}
func writeAt(parent *os.File, name string, content []byte) error {
	return writeAtMode(parent, name, content, 0600)
}

func stageProposed(journal *os.File, op Operation) error {
	mode := uint32(0600)
	if !op.Base.Exists {
		// The private journal inherits the source directory's default ACL.
		// New notes use normal file creation permissions, filtered by that ACL
		// or the process umask; replacements copy the existing file's metadata.
		mode = 0666
	}
	return writeAtMode(journal, "proposed", op.Request.Content, mode)
}

func writeAtMode(parent *os.File, name string, content []byte, mode uint32) error {
	existing, exists, err := readAt(parent, name)
	if err != nil {
		return err
	}
	if exists {
		if !bytes.Equal(existing, content) {
			return ErrIntentMismatch
		}
		return nil
	}
	// Only complete, fsynced staging records receive the known name. Interrupted
	// temporary writes remain in the private journal; restart can stage afresh.
	id, err := newFileID()
	if err != nil {
		return err
	}
	temporary := ".staged-" + id
	fd, err := unix.Openat(int(parent.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temporary)
	_, err = file.Write(content)
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	err = renameNoReplace(int(parent.Fd()), temporary, int(parent.Fd()), name)
	if errors.Is(err, unix.EEXIST) {
		existing, _, err = readAt(parent, name)
		if err == nil && !bytes.Equal(existing, content) {
			err = ErrIntentMismatch
		}
	}
	return err
}

func openJournal(parent *sourceDirectory, op Operation) (*os.File, error) {
	name := ".loom-notes-" + identity(op.Request.OperationID)
	if op.Journal != name {
		return nil, ErrIntentMismatch
	}
	if err := parent.check(); err != nil {
		return nil, err
	}
	if err := unix.Mkdirat(parent.fd(), name, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
		return nil, err
	}
	fd, err := unix.Openat(parent.fd(), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), name)
	info, err := dir.Stat()
	if err != nil || info.Mode().Perm() != 0700 {
		dir.Close()
		return nil, ErrUnsafePath
	}
	if err = writeAt(dir, "intent", []byte(identity(op.Request))); err != nil {
		dir.Close()
		return nil, err
	}
	if err = dir.Sync(); err == nil {
		err = parent.current().Sync()
	}
	if err != nil {
		dir.Close()
		return nil, err
	}
	return dir, nil
}
func finishConflict(ctx context.Context, store Store, parent *sourceDirectory, name string, journal *os.File, op Operation, observed []byte, exists bool) (Operation, error) {
	op.State, op.Reason, op.Observed, op.ObservedExists = Pending, "source diverged from exact base", observed, exists
	// Commit variant custody before attempting restoration. A competing new file
	// wins the path; no variant is overwritten or deleted to resolve the conflict.
	if err := store.SaveOperation(ctx, op); err != nil {
		return op, err
	}
	if journal != nil {
		if err := parent.check(); err != nil {
			return op, err
		}
		err := renameNoReplace(int(journal.Fd()), "displaced", parent.fd(), name)
		if err != nil && !errors.Is(err, unix.EEXIST) && !errors.Is(err, unix.ENOENT) {
			return op, err
		}
		if err = parent.current().Sync(); err != nil {
			return op, err
		}
		if err = journal.Sync(); err != nil {
			return op, err
		}
	}
	op.State = Conflict
	return op, nil
}

// restoreDisplaced makes one context-independent cleanup attempt while the caller
// still owns the source callback and pinned parent descriptors. No-replace keeps
// any racing canonical entry; a failed path check leaves custody in the journal.
func restoreDisplaced(parent *sourceDirectory, name string, journal *os.File) error {
	if err := parent.check(); err != nil {
		return err
	}
	if err := checkNameAlias(parent.current(), name); err != nil {
		return err
	}
	err := renameNoReplace(int(journal.Fd()), "displaced", parent.fd(), name)
	if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.Join(parent.current().Sync(), journal.Sync())
}
func publish(ctx context.Context, store Store, parent *sourceDirectory, name string, op Operation) (result Operation, publishErr error) {
	if err := ctx.Err(); err != nil {
		return op, err
	}
	current, exists, err := readSource(parent, name)
	if err != nil {
		return op, err
	}
	// A saved journal means a previous attempt may already have displaced bytes.
	// Inspect it before a byte-equal replay can acknowledge publication.
	var journal *os.File
	// Register before opening the journal so cleanup runs before Close on every
	// unsuccessful return, including sync/store errors and canceled requests.
	defer func() {
		if journal == nil {
			return
		}
		if publishErr != nil || result.State != Accepted {
			if err := restoreDisplaced(parent, name, journal); err != nil {
				publishErr = errors.Join(publishErr, fmt.Errorf("restore displaced source: %w", err))
			}
		}
		journal.Close()
	}()
	if op.Journal != "" {
		journal, err = openJournal(parent, op)
		if err != nil {
			return op, err
		}
	}
	if journal == nil {
		if exists && bytes.Equal(current, op.Request.Content) {
			op.State, op.Reason = Accepted, "already equals proposal"
			return op, nil
		}
		if exists != op.Base.Exists || !bytes.Equal(current, op.Base.Content) {
			return finishConflict(ctx, store, parent, name, nil, op, current, exists)
		}
		op.Journal = ".loom-notes-" + identity(op.Request.OperationID)
		if err := store.SaveOperation(ctx, op); err != nil {
			return op, err
		}
		journal, err = openJournal(parent, op)
		if err != nil {
			return op, err
		}
	}
	displaced, detached, err := readAt(journal, "displaced")
	if err != nil {
		return op, err
	}

	if !detached {
		if exists && bytes.Equal(current, op.Request.Content) {
			op.State, op.Reason = Accepted, "already equals proposal"
			return op, nil
		}
		if exists != op.Base.Exists || !bytes.Equal(current, op.Base.Content) {
			return finishConflict(ctx, store, parent, name, nil, op, current, exists)
		}
		if err := stageProposed(journal, op); err != nil {
			return op, err
		}
		if err := journal.Sync(); err != nil {
			return op, err
		}
		if op.Base.Exists {
			if err := ctx.Err(); err != nil {
				return op, err
			}
			if err := parent.check(); err != nil {
				return op, err
			}
			if err := renameNoReplace(parent.fd(), name, int(journal.Fd()), "displaced"); err != nil {
				return op, err
			}
			if err := parent.current().Sync(); err != nil {
				return op, err
			}
			if err := journal.Sync(); err != nil {
				return op, err
			}
			displaced, detached, err = readAt(journal, "displaced")
			if err != nil {
				// Cleanup returns even a raced non-text inode only to a vacant path.
				return op, err
			}
		}
	}
	if detached {
		op.RetainedHash = digest(displaced)
		if !op.Base.Exists || !bytes.Equal(displaced, op.Base.Content) {
			return finishConflict(ctx, store, parent, name, journal, op, displaced, true)
		}
		op.Observed, op.ObservedExists = displaced, true
		if err := store.SaveOperation(ctx, op); err != nil {
			return op, err
		}
	}
	// Recovery may arrive after rename but before database acknowledgement.
	current, exists, err = readSource(parent, name)
	if err != nil {
		return op, err
	}
	if exists {
		if bytes.Equal(current, op.Request.Content) {
			op.State, op.Reason = Accepted, "published"
			return op, nil
		}
		return finishConflict(ctx, store, parent, name, nil, op, current, true)
	}
	if err := ctx.Err(); err != nil {
		return op, err
	}
	if err := parent.check(); err != nil {
		return op, err
	}
	if err := checkNameAlias(parent.current(), name); err != nil {
		return op, err
	}
	// Re-stage from durable bytes if a crash occurred before initial staging.
	if err := stageProposed(journal, op); err != nil {
		return op, err
	}
	if detached {
		if err := preserveReplacementMetadata(journal); err != nil {
			return op, err
		}
	}
	if err := journal.Sync(); err != nil {
		return op, err
	}
	if err := ctx.Err(); err != nil {
		return op, err
	}
	if err := parent.check(); err != nil {
		return op, err
	}
	err = renameNoReplace(int(journal.Fd()), "proposed", parent.fd(), name)
	if errors.Is(err, unix.EEXIST) {
		current, exists, readErr := readSource(parent, name)
		if readErr != nil {
			return op, readErr
		}
		return finishConflict(ctx, store, parent, name, nil, op, current, exists)
	}
	if err != nil {
		return op, err
	}
	if err := parent.current().Sync(); err != nil {
		return op, err
	}
	if err := journal.Sync(); err != nil {
		return op, err
	}
	// A writer may have replaced our new path. Preserve it and report a conflict.
	current, exists, err = readSource(parent, name)
	if err != nil {
		return op, err
	}
	if !exists || !bytes.Equal(current, op.Request.Content) {
		return finishConflict(ctx, store, parent, name, nil, op, current, exists)
	}
	op.State, op.Reason = Accepted, "published"
	return op, nil
}
