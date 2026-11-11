package workspacenotes

import (
	"context"
	"io/fs"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/workspacefiles"
)

func (s Service) checkRoot(ctx context.Context, ref string) error {
	selection, scoped := boxregistry.FromContext(ctx)
	if !scoped || selection.AllBoxes {
		return nil
	}
	if s.Files != nil {
		for _, root := range s.Files.Roots {
			if root.Ref == ref {
				if root.BoxID != "" {
					if selection.Contains(root.BoxID) {
						return nil
					}
					return fs.ErrPermission
				}
				break
			}
		}
	}
	if s.RootBoxID != nil {
		id, err := s.RootBoxID(ctx, ref)
		if err != nil {
			return err
		}
		if id != "" && selection.Contains(id) {
			return nil
		}
		return fs.ErrPermission
	}
	if s.Files != nil {
		for _, root := range s.Files.Roots {
			if root.Ref == ref {
				return fs.ErrPermission
			}
		}
	}
	return workspacefiles.ErrUnknownRoot
}

func (s Service) sameBoxRoots(left, right string) bool {
	if left == right {
		return true
	}
	var leftBox, rightBox string
	for _, root := range s.Files.Roots {
		if root.Ref == left {
			leftBox = root.BoxID
		}
		if root.Ref == right {
			rightBox = root.BoxID
		}
	}
	return leftBox != "" && leftBox == rightBox
}
