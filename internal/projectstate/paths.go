package projectstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type OSPathResolver struct{}

func (OSPathResolver) ResolveWithin(root, relative string) (ResolvedPath, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	relative = filepath.Clean(strings.TrimSpace(relative))
	if root == "" || !filepath.IsAbs(root) {
		return ResolvedPath{}, fmt.Errorf("%w: registered root must be absolute", ErrPathInvalid)
	}
	if relative == "" || relative == "." {
		relative = "."
	} else if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ResolvedPath{}, fmt.Errorf("%w: relative source is unsafe", ErrPathInvalid)
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ResolvedPath{Path: root}, nil
		}
		return ResolvedPath{}, fmt.Errorf("resolve registered root: %w", err)
	}
	rootInfo, err := os.Stat(resolvedRoot)
	if err != nil {
		return ResolvedPath{}, fmt.Errorf("stat registered root: %w", err)
	}
	if !rootInfo.IsDir() {
		return ResolvedPath{}, fmt.Errorf("%w: registered root is not a directory", ErrPathInvalid)
	}

	target := filepath.Join(root, relative)
	resolvedTarget, exists, err := resolveExistingOrParent(target)
	if err != nil {
		return ResolvedPath{}, err
	}
	if !pathWithin(resolvedRoot, resolvedTarget) {
		return ResolvedPath{}, fmt.Errorf("%w: %s", ErrPathEscape, relative)
	}
	if !exists {
		return ResolvedPath{Path: target}, nil
	}
	info, err := os.Stat(resolvedTarget)
	if err != nil {
		return ResolvedPath{}, fmt.Errorf("stat resolved path: %w", err)
	}
	return ResolvedPath{Path: resolvedTarget, Exists: true, Directory: info.IsDir()}, nil
}

func resolveExistingOrParent(target string) (string, bool, error) {
	resolved, err := filepath.EvalSymlinks(target)
	if err == nil {
		return resolved, true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("resolve project repository path: %w", err)
	}
	parent := filepath.Dir(target)
	for parent != filepath.Dir(parent) {
		resolvedParent, parentErr := filepath.EvalSymlinks(parent)
		if parentErr == nil {
			return resolvedParent, false, nil
		}
		if !errors.Is(parentErr, os.ErrNotExist) {
			return "", false, fmt.Errorf("resolve project repository parent: %w", parentErr)
		}
		parent = filepath.Dir(parent)
	}
	return "", false, fmt.Errorf("resolve project repository path: %w", os.ErrNotExist)
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
