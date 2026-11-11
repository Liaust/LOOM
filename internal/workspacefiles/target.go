package workspacefiles

import (
	"context"
	"io/fs"
	"path"
	"path/filepath"
)

// WritableTarget resolves an existing parent, not a client-supplied absolute
// path. The Notes owner independently fences the final path before publication.
func (s *Service) WritableTarget(ctx context.Context, ref, relative string) (string, error) {
	if _, err := s.writableBoxRoot(ctx, ref); err != nil {
		return "", err
	}
	logical, err := s.RetainedTarget(ref, relative)
	if err != nil {
		return "", err
	}
	parent, metadata, err := s.SourcePath(ctx, ref, path.Dir(relative))
	if err != nil {
		return "", err
	}
	if metadata.Kind != "directory" {
		return "", ErrNotDirectory
	}
	return filepath.Join(parent, filepath.Base(logical)), nil
}

// RetainedTarget validates only a logical Box path for matching an immutable
// receipt after its parent moved. It does not open a file or authorize a write.
func (s *Service) RetainedTarget(ref, relative string) (string, error) {
	root, err := s.root(ref)
	if err != nil {
		return "", err
	}
	if !root.IsWritableBox() {
		return "", fs.ErrPermission
	}
	p, err := normalizePath(relative)
	if err != nil || p == "." || mandatoryExcluded(p) || rebuildable(p) {
		return "", ErrInvalidPath
	}
	return filepath.Join(root.SourcePath, filepath.FromSlash(p)), nil
}

// InspectTarget is a logical reference for operation classification, not write
// authority. The mutation owner must independently recheck paths and permissions.
func (s *Service) InspectTarget(ref, relative string) (string, error) {
	root, err := s.root(ref)
	if err != nil {
		return "", err
	}
	p, err := normalizePath(relative)
	if err != nil || mandatoryExcluded(p) {
		return "", ErrInvalidPath
	}
	handle, err := s.openRoot(root)
	if err != nil {
		return "", err
	}
	_ = handle.Close()
	return filepath.Join(root.SourcePath, filepath.FromSlash(p)), nil
}
