package workspacefiles

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/notesworkspace"
)

// MutationSource is filesystem authority only. It creates no knowledge root,
// consumer selection, indexing request or device-download rule.
func (s *Service) MutationSource(ctx context.Context, sourcePath, collection string) (notesworkspace.Source, error) {
	root, err := s.mutationRoot(ctx, sourcePath)
	if err != nil {
		return notesworkspace.Source{}, fs.ErrPermission
	}
	if sourcePath == "" {
		sourcePath = root.SourcePath
	}
	relative, err := filepath.Rel(root.SourcePath, sourcePath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return notesworkspace.Source{}, ErrInvalidPath
	}
	handle, err := s.openRoot(root)
	if err != nil {
		return notesworkspace.Source{}, err
	}
	defer handle.Close()
	directory, err := openNonblocking(handle, filepath.ToSlash(relative))
	if err != nil {
		return notesworkspace.Source{}, err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		return notesworkspace.Source{}, ErrNotDirectory
	}
	var stat unix.Stat_t
	if err = unix.Fstat(int(directory.Fd()), &stat); err != nil {
		return notesworkspace.Source{}, err
	}
	nodeIdentity := root.Node
	if root.Ref != "box" {
		nodeIdentity += ":" + root.Ref
	}
	identity := fmt.Sprintf("%s:%s:%d:%d", nodeIdentity, sourcePath, stat.Dev, stat.Ino)
	if collection == "" {
		collection = fmt.Sprintf("filesystem_box_%x", sha256.Sum256([]byte(identity)))
	}
	generation := notesworkspace.FilesystemGeneration(collection, nodeIdentity, sourcePath, uint64(stat.Dev), uint64(stat.Ino))
	return notesworkspace.Source{CollectionID: collection, Path: sourcePath, Generation: generation}, nil
}

func (s *Service) writableBoxRoot(ctx context.Context, ref string) (Root, error) {
	root, err := s.root(ref)
	if err != nil {
		return Root{}, err
	}
	if !root.IsWritableBox() {
		return Root{}, fs.ErrPermission
	}
	if selection, scoped := boxregistry.FromContext(ctx); scoped && !selection.Contains(root.BoxID) {
		return Root{}, fs.ErrPermission
	}
	return root, nil
}

// An absolute source is bound to exactly one configured writable Box. Never
// retry a failed selected source against the legacy Personal root.
func (s *Service) mutationRoot(ctx context.Context, sourcePath string) (Root, error) {
	selection, scoped := boxregistry.FromContext(ctx)
	var found Root
	for _, root := range s.Roots {
		if !root.IsWritableBox() || root.SourcePath == "" || scoped && !selection.Contains(root.BoxID) {
			continue
		}
		if sourcePath == "" {
			if !scoped && root.Ref != "box" {
				continue
			}
		} else {
			if !filepath.IsAbs(sourcePath) || filepath.Clean(sourcePath) != sourcePath {
				return Root{}, ErrInvalidPath
			}
			rel, err := filepath.Rel(root.SourcePath, sourcePath)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
				continue
			}
		}
		if found.Ref != "" {
			return Root{}, fs.ErrPermission
		}
		found = root
	}
	if found.Ref == "" {
		return Root{}, fs.ErrPermission
	}
	return found, nil
}

type MutationResolver struct {
	Files  *Service
	Source notesworkspace.Source
}

func (r MutationResolver) WithSource(ctx context.Context, id, relative string, fn func(notesworkspace.Source) error) error {
	return r.WithPaths(ctx, id, []string{relative}, fn)
}
func (r MutationResolver) WithDirectoryPaths(ctx context.Context, id string, relatives []string, fn func(notesworkspace.Source) error) error {
	return r.WithPaths(ctx, id, relatives, fn)
}
func (r MutationResolver) WithReadDirectoryPaths(ctx context.Context, id string, relatives []string, fn func(notesworkspace.Source) error) error {
	return r.WithPaths(ctx, id, relatives, fn)
}
func (r MutationResolver) WithPaths(ctx context.Context, id string, relatives []string, fn func(notesworkspace.Source) error) error {
	if r.Files == nil || id != r.Source.CollectionID {
		return notesworkspace.ErrMembership
	}
	current, err := r.Files.MutationSource(ctx, r.Source.Path, id)
	if err != nil || current != r.Source {
		return notesworkspace.ErrMembership
	}
	root, err := r.Files.mutationRoot(ctx, current.Path)
	if err != nil {
		return err
	}
	for _, relative := range relatives {
		if relative == "" {
			continue
		}
		absolute := filepath.Join(current.Path, filepath.FromSlash(relative))
		boxRelative, e := filepath.Rel(root.SourcePath, absolute)
		if e != nil {
			return e
		}
		if _, e = r.Files.RetainedTarget(root.Ref, filepath.ToSlash(boxRelative)); e != nil {
			return e
		}
	}
	if err := fn(current); err != nil {
		return err
	}
	after, err := r.Files.MutationSource(ctx, current.Path, id)
	if err != nil || after != current {
		return notesworkspace.ErrMembership
	}
	return nil
}
