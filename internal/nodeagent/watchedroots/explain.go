package watchedroots

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/filesystemmeta"
)

type ExplainResult struct {
	RootKey        string          `json:"root_key"`
	Path           string          `json:"path"`
	Safe           bool            `json:"safe"`
	Exists         bool            `json:"exists"`
	KnownFromState bool            `json:"known_from_state"`
	Classification Classification  `json:"classification"`
	Observation    PathObservation `json:"observation"`
	State          *PathState      `json:"state,omitempty"`
	Policies       Policies        `json:"policies"`
	CheckedAt      time.Time       `json:"checked_at"`
}

func ExplainPath(store Store, root ValidatedRoot, rawPath string) (ExplainResult, error) {
	relativePath, _, err := filesystemconnector.NormalizeRelativePath(rawPath)
	if err != nil {
		return ExplainResult{}, err
	}
	obs := ObservePath(root, relativePath)
	classification := ClassifyPath(obs, root)
	state, stateErr := store.LoadPathState(root.Config.RootKey, relativePath)
	var statePtr *PathState
	if stateErr == nil {
		statePtr = &state
	} else if !IsNotExist(stateErr) {
		return ExplainResult{}, stateErr
	}
	return ExplainResult{
		RootKey:        root.Config.RootKey,
		Path:           relativePath,
		Safe:           obs.Safe,
		Exists:         obs.Exists,
		KnownFromState: statePtr != nil,
		Classification: classification,
		Observation:    obs,
		State:          statePtr,
		Policies:       classification.Policies,
		CheckedAt:      time.Now().UTC(),
	}, nil
}

func ObservePath(root ValidatedRoot, relativePath string) PathObservation {
	relativePath, hidden, err := filesystemconnector.NormalizeRelativePath(relativePath)
	obs := PathObservation{
		RelativePath: relativePath,
		Hidden:       hidden || hasHiddenSegment(relativePath),
		Safe:         false,
		Kind:         PathKindMissing,
	}
	if err != nil {
		obs.ErrorCode = ReasonSkippedPathEscape
		obs.Error = err.Error()
		return obs
	}
	targetPath := filepath.Join(root.RootPath, filepath.FromSlash(relativePath))
	cleanTarget := filepath.Clean(targetPath)
	if !filesystemconnector.IsWithin(root.RootPath, cleanTarget) {
		obs.ErrorCode = ReasonSkippedPathEscape
		obs.Error = "path escapes watched root"
		return obs
	}
	obs.Safe = true
	info, err := os.Lstat(cleanTarget)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			obs.ErrorCode = ReasonSkippedPermissionDenied
			obs.Error = "permission denied"
		}
		return obs
	}
	obs.Exists = true
	obs.Mode = info.Mode().String()
	obs.ModifiedAt = info.ModTime().UTC()
	obs.SizeBytes = info.Size()
	if fidelity, err := filesystemmeta.DetectPath(cleanTarget, filesystemmeta.DetectOptions{
		RootPath:          root.RootPath,
		IncludeXattrNames: root.Config.FidelityPolicy.ObserveXattrs,
	}); err == nil {
		fidelity.CasefoldKey = filesystemmeta.CasefoldKey(relativePath)
		obs.Fidelity = &fidelity
		obs.Hidden = obs.Hidden || fidelity.Hidden
		obs.Kind = pathKindFromFidelity(fidelity)
		obs.Symlink = fidelity.Kind == filesystemmeta.ObjectKindSymlink
	}
	if info.Mode()&os.ModeSymlink != 0 {
		obs.Kind = PathKindSymlink
		obs.Symlink = true
		if root.Config.Scan.FollowSymlinks {
			if resolved, err := filepath.EvalSymlinks(cleanTarget); err != nil {
				obs.Safe = false
				obs.ErrorCode = ReasonSkippedPathEscape
				obs.Error = "symlink could not be safely resolved"
			} else if !filesystemconnector.IsWithin(root.RootPath, resolved) {
				obs.Safe = false
				obs.ErrorCode = ReasonSkippedPathEscape
				obs.Error = "symlink escapes watched root"
			}
		}
		return obs
	}
	if resolved, err := filepath.EvalSymlinks(cleanTarget); err == nil && !filesystemconnector.IsWithin(root.RootPath, resolved) {
		obs.Safe = false
		obs.ErrorCode = ReasonSkippedPathEscape
		obs.Error = "path escapes watched root"
		return obs
	}
	switch {
	case info.Mode().IsRegular():
		obs.Kind = PathKindFile
	case info.IsDir():
		obs.Kind = PathKindDirectory
	default:
		obs.Kind = PathKindOther
	}
	return obs
}

func pathKindFromFidelity(fidelity filesystemmeta.Observation) string {
	switch fidelity.Kind {
	case filesystemmeta.ObjectKindRegularFile, filesystemmeta.ObjectKindHardLink:
		return PathKindFile
	case filesystemmeta.ObjectKindDirectory, filesystemmeta.ObjectKindPackage:
		return PathKindDirectory
	case filesystemmeta.ObjectKindSymlink:
		return PathKindSymlink
	case filesystemmeta.ObjectKindSpecial, filesystemmeta.ObjectKindUnknown:
		return PathKindOther
	default:
		return PathKindOther
	}
}

func hasHiddenSegment(relativePath string) bool {
	relativePath = strings.TrimSpace(filepath.ToSlash(relativePath))
	if relativePath == "" || relativePath == "." {
		return false
	}
	for _, part := range strings.Split(relativePath, "/") {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return true
		}
	}
	return false
}
