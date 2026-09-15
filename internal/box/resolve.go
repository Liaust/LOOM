package box

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const DefaultMainRuntimeStateRoot = "/var/lib/loom/box-state"

func Resolve(input ResolveInput) (Resolved, error) {
	ownerNode := strings.TrimSpace(input.NodeID)
	if ownerNode == "" {
		ownerNode = "unknown"
	}
	nodeRole := strings.ToLower(strings.TrimSpace(input.NodeRole))
	rootPath, pathSource, err := resolveRootPath(input)
	if err != nil {
		return Resolved{}, err
	}
	profile, profileSource, err := resolveProfile(input.ExplicitProfile, input.ConfigProfile, nodeRole)
	if err != nil {
		return Resolved{}, err
	}
	runtimeStateRoot, err := resolveRuntimeStateRoot(input.RuntimeStateRoot, profile, input.HomeDir)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{
		RootPath:         rootPath,
		PathSource:       pathSource,
		Profile:          profile,
		ProfileSource:    profileSource,
		OwnerNode:        ownerNode,
		NodeRole:         nodeRole,
		RuntimeStateRoot: runtimeStateRoot,
		LegacyStateRoot:  filepath.Join(rootPath, filepath.FromSlash(stateRelDir)),
	}, nil
}

func PathResultFromResolved(resolved Resolved) PathResult {
	return PathResult{
		RootPath:         resolved.RootPath,
		PathSource:       resolved.PathSource,
		Profile:          resolved.Profile,
		ProfileSource:    resolved.ProfileSource,
		OwnerNode:        resolved.OwnerNode,
		NodeRole:         resolved.NodeRole,
		RuntimeStateRoot: resolved.RuntimeStateRoot,
	}
}

func IsValidProfile(profile string) bool {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case ProfileWorkspace, ProfileMain:
		return true
	default:
		return false
	}
}

func SupportedProfiles() []string {
	return []string{ProfileWorkspace, ProfileMain}
}

func resolveRootPath(input ResolveInput) (string, string, error) {
	candidates := []struct {
		value  string
		source string
	}{
		{input.ExplicitPath, "flag"},
		{input.ConfiguredPath, "config"},
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.value) == "" {
			continue
		}
		path, err := normalizeRootPath(candidate.value, input.HomeDir)
		if err != nil {
			return "", "", fmt.Errorf("resolve box path from %s: %w", candidate.source, err)
		}
		return path, candidate.source, nil
	}
	home, err := resolveHome(input.HomeDir)
	if err != nil {
		return "", "", err
	}
	if strings.EqualFold(strings.TrimSpace(input.NodeRole), ProfileMain) {
		return "/srv/loom/box", "default_main", nil
	}
	return filepath.Join(home, DefaultRootDirName), "default_home", nil
}

func resolveRuntimeStateRoot(configured, profile, homeDir string) (string, error) {
	if strings.TrimSpace(configured) != "" {
		return normalizeRootPath(configured, homeDir)
	}
	if profile == ProfileMain {
		return DefaultMainRuntimeStateRoot, nil
	}
	home, err := resolveHome(homeDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "loom", "box-state"), nil
}

// ResolveRuntimeState selects one effective read/write root until an explicit
// migration is applied. Legacy-only installations remain entirely on the
// legacy root so mutations cannot split state across two trees. When both
// roots exist their complete typed manifests must agree before canonical wins.
func ResolveRuntimeState(resolved Resolved) (RuntimeStateResolution, error) {
	writeRoot := strings.TrimSpace(resolved.RuntimeStateRoot)
	if writeRoot == "" {
		if resolved.Profile == ProfileMain {
			writeRoot = DefaultMainRuntimeStateRoot
		} else {
			writeRoot = filepath.Join(filepath.Dir(resolved.RootPath), ".loom-box-state")
		}
	}
	writeRoot = filepath.Clean(writeRoot)
	legacyRoot := strings.TrimSpace(resolved.LegacyStateRoot)
	if legacyRoot == "" {
		legacyRoot = filepath.Join(resolved.RootPath, filepath.FromSlash(stateRelDir))
	}
	legacyRoot = filepath.Clean(legacyRoot)
	if writeRoot == legacyRoot {
		// The staged main profile deliberately points the configured and legacy
		// names at one tree before cutover. Resolve that path identity before any
		// stat, walk, or digest so the same potentially large tree is never
		// inventoried twice or mistaken for a dual-root migration.
		return RuntimeStateResolution{ReadRoot: writeRoot, WriteRoot: writeRoot, Source: "shared"}, nil
	}
	newExists, err := directoryExists(writeRoot)
	if err != nil {
		return RuntimeStateResolution{}, err
	}
	legacyExists, err := directoryExists(legacyRoot)
	if err != nil {
		return RuntimeStateResolution{}, err
	}
	switch {
	case newExists && legacyExists:
		newDigest, err := runtimeStateDigest(writeRoot)
		if err != nil {
			return RuntimeStateResolution{}, err
		}
		legacyDigest, err := runtimeStateDigest(legacyRoot)
		if err != nil {
			return RuntimeStateResolution{}, err
		}
		if newDigest != legacyDigest {
			return RuntimeStateResolution{}, fmt.Errorf("Box runtime state is divergent between %s and %s; refusing to merge records", writeRoot, legacyRoot)
		}
		return RuntimeStateResolution{ReadRoot: writeRoot, WriteRoot: writeRoot, Source: "canonical"}, nil
	case newExists:
		return RuntimeStateResolution{ReadRoot: writeRoot, WriteRoot: writeRoot, Source: "canonical"}, nil
	case legacyExists:
		return RuntimeStateResolution{ReadRoot: legacyRoot, WriteRoot: legacyRoot, Source: "legacy", MigrationRequired: true}, nil
	default:
		return RuntimeStateResolution{ReadRoot: writeRoot, WriteRoot: writeRoot, Source: "canonical_empty"}, nil
	}
}

func directoryExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("runtime state root %s exists but is not a directory", path)
	}
	return true, nil
}

func runtimeStateDigest(root string) (string, error) {
	entries := []string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		kind := "other"
		value := ""
		size := int64(0)
		switch {
		case info.IsDir():
			kind = "dir"
		case info.Mode().IsRegular():
			kind = "file"
			size = info.Size()
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			h := sha256.New()
			_, copyErr := io.Copy(h, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			value = hex.EncodeToString(h.Sum(nil))
		case info.Mode()&os.ModeSymlink != 0:
			kind = "symlink"
			value, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		entries = append(entries, filepath.ToSlash(rel)+"\x00"+kind+"\x00"+fmt.Sprint(size)+"\x00"+value)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(entries)
	h := sha256.New()
	for _, entry := range entries {
		_, _ = io.WriteString(h, entry+"\n")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func resolveProfile(explicit, configured, nodeRole string) (string, string, error) {
	candidates := []struct {
		value  string
		source string
	}{
		{explicit, "flag"},
		{configured, "config"},
	}
	for _, candidate := range candidates {
		value := strings.ToLower(strings.TrimSpace(candidate.value))
		if value == "" {
			continue
		}
		if !IsValidProfile(value) {
			return "", "", fmt.Errorf("unsupported box profile %q", candidate.value)
		}
		return value, candidate.source, nil
	}
	if strings.EqualFold(strings.TrimSpace(nodeRole), ProfileMain) {
		return ProfileMain, "node_role", nil
	}
	return ProfileWorkspace, "default_workspace", nil
}

func normalizeRootPath(value string, homeDir string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("path is empty")
	}
	if strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("path contains NUL")
	}
	if strings.HasPrefix(value, "~") {
		home, err := resolveHome(homeDir)
		if err != nil {
			return "", err
		}
		switch {
		case value == "~":
			value = home
		case strings.HasPrefix(value, "~/"):
			value = filepath.Join(home, value[2:])
		default:
			return "", fmt.Errorf("only ~ and ~/ paths are supported")
		}
	}
	if !filepath.IsAbs(value) {
		absolute, err := filepath.Abs(value)
		if err != nil {
			return "", err
		}
		value = absolute
	}
	return filepath.Clean(value), nil
}

func resolveHome(homeDir string) (string, error) {
	if strings.TrimSpace(homeDir) != "" {
		return filepath.Clean(homeDir), nil
	}
	if envHome := strings.TrimSpace(os.Getenv("HOME")); envHome != "" {
		return filepath.Clean(envHome), nil
	}
	detected, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(detected) == "" {
		return "", fmt.Errorf("could not determine home directory for default LOOM Box path")
	}
	return filepath.Clean(detected), nil
}
