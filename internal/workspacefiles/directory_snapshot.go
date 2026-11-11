package workspacefiles

import (
	"context"
	"fmt"
)

// WithSnapshotDirectory pins the admitted directory for the bounded snapshot.
// The callback owns no write authority and must not retain the path afterward.
func (s *Service) WithSnapshotDirectory(ctx context.Context, ref, relative string, fn func(string, Entry) error) error {
	if ref != "box" && ref != "storage" {
		return ErrUnknownRoot
	}
	p, err := normalizePath(relative)
	if err != nil || p == "." || mandatoryExcluded(p) || rebuildable(p) {
		return ErrInvalidPath
	}
	root, err := s.root(ref)
	if err != nil {
		return err
	}
	lease, err := s.newDirectoryLease(root, ListInput{Root: ref, Path: p}, s.now())
	if err != nil {
		return err
	}
	defer lease.close()
	if !lease.current() {
		return ErrRootChanged
	}
	if err := fn(root.SourcePath+"/"+p, entry(p, lease.info)); err != nil {
		return err
	}
	if !lease.current() {
		return fmt.Errorf("snapshot directory changed; no copy was published")
	}
	return nil
}
