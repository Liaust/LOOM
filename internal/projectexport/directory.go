package projectexport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"loom.local/loom/internal/filepolicy"
)

const DirectorySchemaVersion = "loom.directory_export.v1"

// Directory snapshots share the project export policy/tar writer but have no
// project registration semantics. The bound root confines every payload read.
func PlanDirectory(ctx context.Context, directory string) (plan Plan, err error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return plan, fmt.Errorf("exact directory required")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return plan, fmt.Errorf("snapshot source must be a real directory")
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return plan, err
	}
	bound, err := os.OpenRoot(directory)
	if err != nil {
		return plan, err
	}
	defer func() {
		if err != nil {
			_ = bound.Close()
		}
	}()
	resolver, err := filepolicy.NewResolver(directory, filepolicy.ProfileManaged, directoryResolverOptions())
	if err != nil {
		return plan, err
	}
	plan = Plan{ProjectRoot: directory, MaxArchiveBytes: DefaultMaxBytes, boundRoot: bound, profile: filepolicy.ProfileManaged,
		Summary: Summary{SchemaVersion: DirectorySchemaVersion, Mode: ModePortable, ProjectSlug: "directory-snapshot", PolicyVersion: filepolicy.BuiltInPolicyVersion, PolicyFingerprint: resolver.Fingerprint()}}
	seen := 0
	err = fs.WalkDir(bound.FS(), ".", func(relative string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		seen++
		if seen > 10000 {
			return fmt.Errorf("working copy exceeds 10,000 entries; choose a smaller folder")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		resolution, err := resolver.ResolvePath(filepath.Join(directory, filepath.FromSlash(relative)))
		if err != nil {
			return err
		}
		item := Entry{RelativePath: relative, Mode: int64(info.Mode().Perm()), Size: entrySize(info), Decision: resolution.Decision, Revision: directoryRevision(info)}
		if !item.Decision.Included {
			plan.Summary.Ignored.Count++
			plan.Summary.Ignored.Bytes += item.Size
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case info.IsDir():
			item.Kind = "directory"
		case info.Mode().IsRegular():
			item.Kind = "file"
			if path.Base(relative) == ".git" || info.Size() > 0 && (strings.HasSuffix(relative, "/.git/commondir") || strings.HasSuffix(relative, "/.git/objects/info/alternates")) {
				return fmt.Errorf("external Git metadata requires a Git clone, not a directory snapshot")
			}
			if plan.Summary.Included.Bytes+info.Size() > DefaultMaxBytes {
				return fmt.Errorf("working copy exceeds 512 MiB; choose a smaller folder")
			}
			file, err := bound.Open(relative)
			if err != nil {
				return err
			}
			hash := sha256.New()
			opened, statErr := file.Stat()
			if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || directoryRevision(info) != directoryRevision(opened) {
				file.Close()
				return fmt.Errorf("snapshot source changed: %s", relative)
			}
			n, copyErr := io.Copy(hash, io.LimitReader(file, info.Size()+1))
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if n != info.Size() {
				return fmt.Errorf("snapshot source changed: %s", relative)
			}
			item.ContentHash = "sha256:" + hex.EncodeToString(hash.Sum(nil))
		default:
			return fmt.Errorf("working copies do not follow links or special files: %s", relative)
		}
		plan.Entries = append(plan.Entries, item)
		plan.Summary.Included.Count++
		plan.Summary.Included.Bytes += item.Size
		return nil
	})
	if err != nil {
		return plan, err
	}
	sort.Slice(plan.Entries, func(i, j int) bool { return plan.Entries[i].RelativePath < plan.Entries[j].RelativePath })
	err = validateUniqueArchivePaths(plan.Entries)
	return plan, err
}

func directoryResolverOptions() filepolicy.ResolverOptions {
	options := resolverOptionsForMode(ModePortable)
	for _, name := range []string{".env", ".env.*", ".secrets", "credentials", "private_no_index", ".obsidian", ".hermes", ".codex", ".loom-notes-*", ".loom-working-copy.json", "dist", "build", "target"} {
		options.ContractExcludes = append(options.ContractExcludes, name, name+"/**", "**/"+name, "**/"+name+"/**")
	}
	return options
}

func directoryRevision(info fs.FileInfo) string {
	return fmt.Sprintf("%d:%d:%d", info.Mode(), info.Size(), info.ModTime().UnixNano())
}

func (p Plan) DirectoryDigest(sourceRevision string) string {
	raw, _ := json.Marshal([]any{p.ProjectRoot, sourceRevision, p.Summary, p.Entries})
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (p Plan) CheckDirectory(ctx context.Context) error {
	if p.boundRoot == nil {
		return fmt.Errorf("directory snapshot has no pinned source")
	}
	for _, item := range p.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := p.boundRoot.Lstat(item.RelativePath)
		if err != nil || directoryRevision(info) != item.Revision {
			return fmt.Errorf("snapshot source changed: %s", item.RelativePath)
		}
	}
	return nil
}
