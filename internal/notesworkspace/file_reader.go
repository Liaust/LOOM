package notesworkspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/knowledge"
)

type FileReader struct{ NodeKey string }

func (r FileReader) ObserveNotesFile(ctx context.Context, node, root, relative string) (knowledge.FileSourceObservation, error) {
	if node != r.NodeKey {
		return knowledge.FileSourceObservation{}, knowledge.ErrFileSourceUnavailable
	}
	if err := ctx.Err(); err != nil {
		return knowledge.FileSourceObservation{}, err
	}
	// A missing file is withdrawal only when its owning root remains readable.
	if _, err := ObserveSourcePath(root, ""); err != nil {
		return knowledge.FileSourceObservation{}, fmt.Errorf("%w: owning root is unreadable", knowledge.ErrFileSourceUnavailable)
	}
	info, err := ObserveSourcePath(root, relative)
	if errors.Is(err, os.ErrNotExist) {
		return knowledge.FileSourceObservation{Presence: "missing"}, nil
	}
	if err != nil {
		return knowledge.FileSourceObservation{}, fmt.Errorf("%w: cannot inspect file", knowledge.ErrFileSourceUnavailable)
	}
	if !info.Mode().IsRegular() {
		return knowledge.FileSourceObservation{}, knowledge.ErrFileSourceNotRegular
	}
	mtime := info.ModTime().UTC()
	return knowledge.FileSourceObservation{Presence: "present", Size: info.Size(), ModifiedAt: &mtime}, nil
}

func (r FileReader) ReadNotesFile(ctx context.Context, node, root, relative string, offset int64, limit int) (knowledge.NativeFileRead, error) {
	if node != r.NodeKey {
		return knowledge.NativeFileRead{}, knowledge.ErrFileSourceUnavailable
	}
	if !observationRelative(relative) || relative == "" || offset < 0 || limit <= 0 || limit > knowledge.MaxFileReadBytes {
		return knowledge.NativeFileRead{}, ErrUnsafePath
	}
	if err := ctx.Err(); err != nil {
		return knowledge.NativeFileRead{}, err
	}
	if _, err := ObserveSourcePath(root, ""); err != nil {
		return knowledge.NativeFileRead{}, knowledge.ErrFileSourceUnavailable
	}
	parent, name, err := openSourceParent(root, relative)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return knowledge.NativeFileRead{}, knowledge.ErrFileSourceMissing
		}
		return knowledge.NativeFileRead{}, err
	}
	defer parent.Close()
	fd, err := unix.Openat(parent.fd(), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return knowledge.NativeFileRead{}, knowledge.ErrFileSourceMissing
		}
		return knowledge.NativeFileRead{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return knowledge.NativeFileRead{}, err
	}
	if !before.Mode().IsRegular() {
		return knowledge.NativeFileRead{}, fmt.Errorf("Notes source is not a regular file")
	}
	if _, err = file.Seek(offset, io.SeekStart); err != nil {
		return knowledge.NativeFileRead{}, err
	}
	b, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return knowledge.NativeFileRead{}, err
	}
	after, err := file.Stat()
	if err != nil {
		return knowledge.NativeFileRead{}, err
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return knowledge.NativeFileRead{}, knowledge.ErrFileSourceChanged
	}
	if err = parent.check(); err != nil {
		return knowledge.NativeFileRead{}, err
	}
	var named unix.Stat_t
	if err = unix.Fstatat(parent.fd(), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return knowledge.NativeFileRead{}, knowledge.ErrFileSourceChanged
	}
	var opened unix.Stat_t
	if err = unix.Fstat(fd, &opened); err != nil || named.Dev != opened.Dev || named.Ino != opened.Ino {
		return knowledge.NativeFileRead{}, knowledge.ErrFileSourceChanged
	}
	if err = ctx.Err(); err != nil {
		return knowledge.NativeFileRead{}, err
	}
	return knowledge.FileTextRange(b, offset, limit)
}
