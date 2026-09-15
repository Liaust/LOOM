package projectexport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/projectcontracts"
)

func PlanProject(ctx context.Context, projectRoot string, options PlanOptions) (Plan, error) {
	mode, err := ParseMode(string(options.Mode))
	if err != nil {
		return Plan{}, err
	}
	loaded, err := projectcontracts.LoadProject(projectRoot)
	if err != nil {
		return Plan{}, fmt.Errorf("load project for export: %w", err)
	}
	if options.MaxEntries <= 0 {
		options.MaxEntries = DefaultMaxEntries
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = DefaultMaxBytes
	}
	resolverOptions := resolverOptionsForMode(mode)
	resolver, err := filepolicy.NewResolver(loaded.RootPath, filepolicy.ProfileFaithful, resolverOptions)
	if err != nil {
		return Plan{}, fmt.Errorf("resolve project export policy: %w", err)
	}
	refs := options.RegistrationReferences
	if refs.ProjectRef == "" {
		refs.ProjectRef = loaded.Contract.Project.Slug
	}
	plan := Plan{
		ProjectRoot:     resolver.Root(),
		ContractPath:    filepath.Join(resolver.Root(), filepath.FromSlash(contractRelativePath(loaded))),
		MaxArchiveBytes: options.MaxBytes,
		Summary:         Summary{SchemaVersion: SchemaVersion, ProjectSlug: loaded.Contract.Project.Slug, Mode: mode, PolicyVersion: filepolicy.BuiltInPolicyVersion, PolicyFingerprint: resolver.Fingerprint(), PolicyHashes: map[string]string{}},
	}
	for _, policyFile := range resolver.PolicyFiles() {
		plan.Summary.PolicyHashes[policyFile.RelativePath] = policyFile.ContentHash
	}
	seen := 0
	err = filepath.WalkDir(resolver.Root(), func(pathValue string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if pathValue == resolver.Root() {
			return nil
		}
		seen++
		if seen > options.MaxEntries {
			return fmt.Errorf("project export exceeds %d entries", options.MaxEntries)
		}
		relative, err := safeRelativePath(resolver.Root(), pathValue)
		if err != nil {
			return err
		}
		info, err := os.Lstat(pathValue)
		if err != nil {
			return fmt.Errorf("inspect export entry %q: %w", relative, err)
		}
		resolution, err := resolver.ResolvePath(pathValue)
		if err != nil {
			return err
		}
		decision := resolution.Decision
		if mode == ModeArchival && relative == ArchiveManifestPath {
			decision.Included = false
			decision.RuleCategory = filepolicy.RuleCategoryContract
			decision.Pattern = "reserved-generated-archival-manifest"
		}
		if mode == ModeHuman && relative == "AGENTS.md" && info.Mode().IsRegular() {
			content, err := os.ReadFile(pathValue)
			if err != nil {
				return fmt.Errorf("read root AGENTS.md: %w", err)
			}
			if strings.Contains(string(content), ManagedAgentsMarker) {
				decision.Included = false
				decision.RuleCategory = filepolicy.RuleCategoryContract
				decision.Pattern = "managed-root-agents-marker"
			}
		}
		entry := Entry{RelativePath: relative, Mode: int64(info.Mode().Perm()), Size: entrySize(info), Decision: decision}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			entry.Kind = "symlink"
			entry.LinkTarget, err = portableSymlinkTarget(resolver.Root(), pathValue)
			if err != nil {
				return err
			}
		case info.IsDir():
			entry.Kind = "directory"
		case info.Mode().IsRegular():
			entry.Kind = "file"
		default:
			return fmt.Errorf("unsupported project export entry %q with mode %s", relative, info.Mode())
		}
		if !decision.Included {
			plan.Ignored = append(plan.Ignored, entry)
			plan.Summary.Ignored.Count++
			plan.Summary.Ignored.Bytes += entry.Size
			if mode == ModeArchival && relative == ArchiveManifestPath && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Kind == "file" {
			entry.ContentHash, err = hashProjectFile(pathValue)
			if err != nil {
				return fmt.Errorf("hash export entry %q: %w", relative, err)
			}
		}
		if plan.Summary.Included.Bytes+entry.Size > options.MaxBytes {
			return fmt.Errorf("project export exceeds %d included bytes", options.MaxBytes)
		}
		plan.Entries = append(plan.Entries, entry)
		plan.Summary.Included.Count++
		plan.Summary.Included.Bytes += entry.Size
		return nil
	})
	if err != nil {
		return Plan{}, fmt.Errorf("plan project export: %w", err)
	}
	if mode == ModeArchival {
		manifest, err := buildArchivalManifest(refs)
		if err != nil {
			return Plan{}, err
		}
		decision := filepolicy.Decision{Path: ArchiveManifestPath, Included: true, Profile: filepolicy.ProfileFaithful, RuleCategory: filepolicy.RuleCategoryContract, Pattern: "archival-registration-manifest", PolicyVersion: filepolicy.BuiltInPolicyVersion}
		manifestSum := sha256.Sum256(manifest)
		entry := Entry{RelativePath: ArchiveManifestPath, Kind: "file", Mode: 0o644, Size: int64(len(manifest)), ContentHash: "sha256:" + hex.EncodeToString(manifestSum[:]), Decision: decision, Generated: manifest}
		if plan.Summary.Included.Bytes+entry.Size > options.MaxBytes {
			return Plan{}, fmt.Errorf("project export exceeds %d included bytes", options.MaxBytes)
		}
		plan.Entries = append(plan.Entries, entry)
		plan.Summary.Included.Count++
		plan.Summary.Included.Bytes += entry.Size
	}
	sort.Slice(plan.Entries, func(i, j int) bool { return plan.Entries[i].RelativePath < plan.Entries[j].RelativePath })
	sort.Slice(plan.Ignored, func(i, j int) bool { return plan.Ignored[i].RelativePath < plan.Ignored[j].RelativePath })
	if err := validateUniqueArchivePaths(plan.Entries); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func resolverOptionsForMode(mode Mode) filepolicy.ResolverOptions {
	options := filepolicy.ResolverOptions{DiscoverUserRules: true}
	if mode == ModeHuman {
		options.ContractExcludes = []string{".loom", ".loom/**", ".loom-acceptance", ".loom-acceptance/**"}
	} else {
		options.ContractIncludes = []string{"AGENTS.md", ".loom", ".loom/**", ".loomignore"}
	}
	return options
}

func portableSymlinkTarget(root, pathValue string) (string, error) {
	resolved, err := filepath.EvalSymlinks(pathValue)
	if err != nil {
		return "", fmt.Errorf("resolve project export symlink %q: %w", pathValue, err)
	}
	if _, err := safeRelativePath(root, resolved); err != nil {
		return "", err
	}
	relative, err := filepath.Rel(filepath.Dir(pathValue), resolved)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(relative), nil
}

func hashProjectFile(pathValue string) (string, error) {
	file, err := os.Open(pathValue)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func entrySize(info os.FileInfo) int64 {
	if info.Mode().IsRegular() {
		return info.Size()
	}
	return 0
}

func contractRelativePath(loaded projectcontracts.LoadedProject) string {
	if loaded.Layout == projectcontracts.ProjectLayoutLegacy {
		return projectcontracts.LegacyRootContractPath
	}
	return projectcontracts.CanonicalRootContractPath
}
