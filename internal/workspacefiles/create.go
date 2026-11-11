package workspacefiles

import (
	"context"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

type CreateInput struct {
	Root      string `json:"root"`
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Text      string `json:"text,omitempty"`
}

// Create never overwrites a destination and never mutates archive custody.
// Existing watchers remain responsible for observing and processing the source.
func (s *Service) Create(ctx context.Context, input CreateInput) (Entry, error) {
	root, err := s.writableBoxRoot(ctx, input.Root)
	if err != nil {
		return Entry{}, err
	}
	relative, err := normalizePath(input.Path)
	if err != nil || relative == "." || mandatoryExcluded(relative) || rebuildable(relative) || strings.HasPrefix(path.Base(relative), ".") || len(input.Text) > 65536 || !utf8.ValidString(input.Text) || strings.ContainsRune(input.Text, 0) {
		return Entry{}, ErrInvalidPath
	}
	if !input.Directory && !strings.HasSuffix(strings.ToLower(relative), ".md") && !strings.HasSuffix(strings.ToLower(relative), ".txt") {
		return Entry{}, ErrInvalidPath
	}
	if input.Directory && input.Text != "" {
		return Entry{}, ErrInvalidPath
	}
	if err = ctx.Err(); err != nil {
		return Entry{}, err
	}
	handle, err := s.openRoot(root)
	if err != nil {
		return Entry{}, err
	}
	defer handle.Close()
	parent, err := openNonblocking(handle, path.Dir(relative))
	if err != nil {
		return Entry{}, err
	}
	defer parent.Close()
	if input.Directory {
		if err = unix.Mkdirat(int(parent.Fd()), path.Base(relative), 0755); err != nil {
			return Entry{}, err
		}
	} else {
		fd, err := unix.Openat(int(parent.Fd()), path.Base(relative), unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0644)
		if err != nil {
			return Entry{}, err
		}
		file := os.NewFile(uintptr(fd), path.Base(relative))
		_, err = file.WriteString(input.Text)
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return Entry{}, err
		}
		if closeErr != nil {
			return Entry{}, closeErr
		}
	}
	return s.Stat(ctx, input.Root, relative)
}
