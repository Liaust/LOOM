package filesystemmeta

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type DetectOptions struct {
	RootPath          string
	IncludeXattrNames bool
}

func DetectPath(path string, opts DetectOptions) (Observation, error) {
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return Observation{
				Kind:             ObjectKindUnknown,
				PermissionDenied: true,
				Risks:            []string{FidelityRiskUnsafePermission},
			}, nil
		}
		return Observation{}, err
	}

	mode := info.Mode()
	observation := Observation{
		Kind:              kindForMode(mode),
		SourceMode:        uint32(mode.Perm()),
		Executable:        mode.Perm()&0o111 != 0,
		LogicalSizeBytes:  maxInt64(info.Size(), 0),
		Hidden:            IsHiddenPath(path, opts.RootPath),
		GeneratedMetadata: IsGeneratedAppleMetadata(filepath.Base(path)),
		UnicodeForm:       UnicodeFormSummary(filepath.Base(path)),
		CasefoldKey:       CasefoldKey(path),
	}
	if modifiedAt := info.ModTime().UTC(); !modifiedAt.IsZero() {
		observation.SourceModifiedAt = &modifiedAt
		observation.SourceModifiedBasis = SourceTimeBasisFilesystemMtime
	}

	if mode&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err == nil {
			observation.SymlinkTarget = target
			if symlinkTargetEscapesRoot(path, target, opts.RootPath) {
				observation.Risks = appendRisk(observation.Risks, FidelityRiskExternalReference)
			}
		}
	}

	if isPackage, packageKind := IsPackageDirectory(path, info); isPackage {
		observation.Kind = ObjectKindPackage
		observation.IsPackage = true
		observation.PackageKind = packageKind
		observation.Risks = appendRisk(observation.Risks, FidelityRiskMetadataOnly)
	}

	if observation.GeneratedMetadata {
		observation.Risks = appendRisk(observation.Risks, FidelityRiskMetadataOnly)
	}
	if observation.Kind == ObjectKindSpecial {
		observation.Risks = appendRisk(observation.Risks, FidelityRiskUnsupportedObject)
	}

	if err := applyPlatformStat(path, info, &observation); err != nil {
		return Observation{}, fmt.Errorf("detect platform stat for %s: %w", path, err)
	}
	applyPlatformXattrs(path, opts, &observation)
	return observation, nil
}

func kindForMode(mode os.FileMode) string {
	switch {
	case mode&os.ModeSymlink != 0:
		return ObjectKindSymlink
	case mode.IsDir():
		return ObjectKindDirectory
	case mode.IsRegular():
		return ObjectKindRegularFile
	case mode&(os.ModeDevice|os.ModeCharDevice|os.ModeNamedPipe|os.ModeSocket|os.ModeIrregular) != 0:
		return ObjectKindSpecial
	default:
		return ObjectKindUnknown
	}
}

func symlinkTargetEscapesRoot(path, target, root string) bool {
	if target == "" {
		return false
	}
	if root = filepath.Clean(root); root == "." || root == "" {
		return filepath.IsAbs(target)
	}
	var resolved string
	if filepath.IsAbs(target) {
		resolved = filepath.Clean(target)
	} else {
		resolved = filepath.Clean(filepath.Join(filepath.Dir(path), target))
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return true
	}
	return rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator)
}

func appendRisk(risks []string, risk string) []string {
	if !ValidFidelityRisk(risk) {
		return risks
	}
	for _, existing := range risks {
		if existing == risk {
			return risks
		}
	}
	return append(risks, risk)
}

func maxInt64(value, min int64) int64 {
	if value < min {
		return min
	}
	return value
}

func platformName() string {
	return runtime.GOOS
}
