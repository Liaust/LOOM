package agentpack

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const maxWorkspaceFileSize = 1024 * 1024

// openWorkspaceDirectory holds each parent while checking the next component.
// OpenRoot confines resolution; matching the opened identity to Lstat prevents
// a symlink substituted between inspection and opening from being accepted.
func openWorkspaceDirectory(path string) (*os.Root, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// macOS exposes the process temporary directory through /var -> /private/var.
	// Resolve that OS-selected anchor only, never a user-supplied child symlink.
	temp := filepath.Clean(os.TempDir())
	if relative, err := filepath.Rel(temp, absolute); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		if resolved, err := filepath.EvalSymlinks(temp); err == nil {
			absolute = filepath.Join(resolved, relative)
		}
	}
	volume := filepath.VolumeName(absolute) + string(filepath.Separator)
	root, err := os.OpenRoot(volume)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(absolute, volume), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		info, err := root.Lstat(part)
		if err != nil {
			root.Close()
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, fmt.Errorf("workspace path contains a non-directory or symlink: %s", path)
		}
		next, err := root.OpenRoot(part)
		if err != nil {
			root.Close()
			return nil, err
		}
		current, currentErr := root.Lstat(part)
		root.Close()
		opened, err := next.Stat(".")
		if err != nil || currentErr != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, current) || !os.SameFile(info, opened) {
			next.Close()
			return nil, fmt.Errorf("workspace directory changed while opening: %s", path)
		}
		root = next
	}
	return root, nil
}

func readWorkspaceFile(path string) ([]byte, error) {
	parent, err := openWorkspaceDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	name := filepath.Base(path)
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxWorkspaceFileSize {
		return nil, fmt.Errorf("workspace file must be regular and at most %d bytes: %s", maxWorkspaceFileSize, path)
	}
	// A replaced FIFO must not block the reader before its identity is checked.
	file, err := parent.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	current, currentErr := parent.Lstat(name)
	if err != nil || currentErr != nil || !current.Mode().IsRegular() || !opened.Mode().IsRegular() || !os.SameFile(info, current) || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("workspace file changed while opening: %s", path)
	}
	payload, err := io.ReadAll(io.LimitReader(file, maxWorkspaceFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > maxWorkspaceFileSize {
		return nil, fmt.Errorf("workspace file exceeds size limit: %s", path)
	}
	return payload, nil
}

// Validate physical entries before any catalogue-driven reader sees them.
func inspectPackRegularTree(path string) error {
	root, err := openWorkspaceDirectory(path)
	if err != nil {
		return err
	}
	root.Close()
	return filepath.WalkDir(path, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return fmt.Errorf("pack contains non-regular entry: %s", path)
		}
		return nil
	})
}
