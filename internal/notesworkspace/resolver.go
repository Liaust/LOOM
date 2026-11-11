package notesworkspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/knowledge"
)

// KnowledgeResolver consumes existing declaration roots. WithSelection holds the
// runtime's selection fence throughout its callback, or returns ErrMembership.
// The runtime must advance the generation on withdrawal/re-enrollment. This seam
// deliberately does not invent a second source-root registry.
type KnowledgeResolver struct {
	Knowledge     *knowledge.Service
	NodeKey       string
	WithSelection func(context.Context, string, func(generation string) error) error
	// ObserveRoot runs under canonical source admission, for exact watch reuse.
	ObserveRoot func(Source, knowledge.SourceRoot)
}

func (r KnowledgeResolver) WithSource(ctx context.Context, id, relative string, fn func(Source) error) error {
	return r.WithPaths(ctx, id, []string{relative}, fn)
}

func (r KnowledgeResolver) WithRoot(ctx context.Context, id string, fn func(Source) error) error {
	if r.Knowledge == nil || r.WithSelection == nil {
		return ErrMembership
	}
	return r.WithSelection(ctx, id, func(generation string) error {
		if generation == "" {
			return ErrMembership
		}
		return r.Knowledge.WithNotesWorkspaceRoot(ctx, id, r.NodeKey, func(root knowledge.SourceRoot, ownerGeneration string) error {
			src := Source{CollectionID: id, Generation: identity([]string{generation, ownerGeneration}), Path: root.SourcePath}
			if r.ObserveRoot != nil {
				r.ObserveRoot(src, root)
			}
			return fn(src)
		})
	})
}

func (r KnowledgeResolver) WithPaths(ctx context.Context, id string, relatives []string, fn func(Source) error) error {
	return r.withPaths(ctx, id, relatives, false, fn)
}

func (r KnowledgeResolver) WithDirectoryPaths(ctx context.Context, id string, relatives []string, fn func(Source) error) error {
	return r.withPaths(ctx, id, relatives, true, fn)
}

func (r KnowledgeResolver) WithReadDirectoryPaths(ctx context.Context, id string, relatives []string, fn func(Source) error) error {
	if r.Knowledge == nil || r.WithSelection == nil {
		return ErrMembership
	}
	return r.WithSelection(ctx, id, func(generation string) error {
		if generation == "" {
			return ErrMembership
		}
		return r.Knowledge.WithReadableNotesDirectoryPaths(ctx, id, r.NodeKey, relatives, func(root knowledge.SourceRoot, ownerGeneration string) error {
			src := Source{CollectionID: id, Generation: identity([]string{generation, ownerGeneration}), Path: root.SourcePath}
			if r.ObserveRoot != nil {
				r.ObserveRoot(src, root)
			}
			return fn(src)
		})
	})
}

func (r KnowledgeResolver) withPaths(ctx context.Context, id string, relatives []string, directory bool, fn func(Source) error) error {
	if r.Knowledge == nil || r.WithSelection == nil {
		return ErrMembership
	}
	return r.WithSelection(ctx, id, func(generation string) error {
		if generation == "" {
			return ErrMembership
		}
		callback := func(root knowledge.SourceRoot, ownerGeneration string) error {
			src := Source{CollectionID: id, Generation: identity([]string{generation, ownerGeneration}), Path: root.SourcePath}
			if r.ObserveRoot != nil {
				r.ObserveRoot(src, root)
			}
			return fn(src)
		}
		if directory {
			return r.Knowledge.WithWritableNotesDirectoryPaths(ctx, id, r.NodeKey, relatives, callback)
		}
		if len(relatives) == 1 && IsReferencePath(relatives[0]) {
			return r.Knowledge.WithReferenceNotesSource(ctx, id, r.NodeKey, relatives[0], callback)
		}
		return r.Knowledge.WithWritableNotesSourcePaths(ctx, id, r.NodeKey, relatives, callback)
	})
}

// ObserveSourcePath inspects metadata under the same no-follow parent fence as
// canonical Read. It does not admit membership or read payload bytes.
func ObserveSourcePath(root, relative string) (os.FileInfo, error) {
	if relative == "" {
		return observeSourceDirectory(root, "")
	}
	if !observationRelative(relative) {
		return nil, ErrUnsafePath
	}
	parent, name, err := openSourceParent(root, relative)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	fd, err := unix.Openat(parent.fd(), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if err := parent.check(); err != nil {
		return nil, err
	}
	return info, nil
}
func observationRelative(relative string) bool {
	return relative == "" || !filepath.IsAbs(relative) && filepath.Clean(relative) == relative && !strings.ContainsAny(relative, "\\\x00") && relative != ".." && !strings.HasPrefix(relative, "../")
}
func observeSourceDirectory(root, relative string) (os.FileInfo, error) {
	if !observationRelative(relative) {
		return nil, ErrUnsafePath
	}
	parent, _, err := openSourceParent(root, filepath.ToSlash(filepath.Join(relative, "probe.md")))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if err := parent.check(); err != nil {
		return nil, err
	}
	return parent.current().Stat()
}

// ReadSourceDirectory retains and rechecks every parent identity around listing.
// Enumeration cannot follow a concurrently substituted parent symlink.
func ReadSourceDirectory(root, relative string) ([]os.DirEntry, error) {
	if !observationRelative(relative) {
		return nil, ErrUnsafePath
	}
	parent, _, err := openSourceParent(root, filepath.ToSlash(filepath.Join(relative, "probe.md")))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if err := parent.check(); err != nil {
		return nil, err
	}
	entries, err := parent.current().ReadDir(-1)
	if err != nil {
		return nil, err
	}
	if err := parent.check(); err != nil {
		return nil, err
	}
	return entries, nil
}
