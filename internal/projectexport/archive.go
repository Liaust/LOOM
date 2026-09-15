package projectexport

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/filepolicy"
)

func WriteArchive(ctx context.Context, writer io.Writer, plan Plan) (Summary, error) {
	mode, err := ParseMode(string(plan.Summary.Mode))
	if err != nil {
		return Summary{}, err
	}
	if err := validateUniqueArchivePaths(plan.Entries); err != nil {
		return Summary{}, err
	}
	resolver, err := filepolicy.NewResolver(plan.ProjectRoot, filepolicy.ProfileFaithful, resolverOptionsForMode(mode))
	if err != nil {
		return Summary{}, fmt.Errorf("re-resolve project export policy: %w", err)
	}
	if resolver.Fingerprint() != plan.Summary.PolicyFingerprint {
		return Summary{}, fmt.Errorf("project export policy changed after planning; re-plan before writing the archive")
	}
	hash := sha256.New()
	counting := &countingWriter{writer: io.MultiWriter(writer, hash)}
	tarWriter := tar.NewWriter(counting)
	for _, entry := range plan.Entries {
		if err := ctx.Err(); err != nil {
			_ = tarWriter.Close()
			return Summary{}, err
		}
		name, err := safeArchiveName(entry.RelativePath)
		if err != nil {
			_ = tarWriter.Close()
			return Summary{}, err
		}
		header := &tar.Header{Name: name, Mode: entry.Mode, ModTime: time.Unix(0, 0).UTC(), AccessTime: time.Time{}, ChangeTime: time.Time{}, Uid: 0, Gid: 0, Format: tar.FormatPAX}
		switch entry.Kind {
		case "directory":
			header.Typeflag = tar.TypeDir
			header.Name += "/"
		case "symlink":
			if err := validateArchiveSymlink(name, entry.LinkTarget); err != nil {
				_ = tarWriter.Close()
				return Summary{}, err
			}
			header.Typeflag = tar.TypeSymlink
			header.Linkname = entry.LinkTarget
		case "file":
			header.Typeflag = tar.TypeReg
			header.Size = entry.Size
		default:
			_ = tarWriter.Close()
			return Summary{}, fmt.Errorf("unsupported planned export entry kind %q", entry.Kind)
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			_ = tarWriter.Close()
			return Summary{}, fmt.Errorf("write export header %q: %w", name, err)
		}
		if entry.Kind != "file" {
			continue
		}
		if entry.Generated != nil {
			entryHash := sha256.Sum256(entry.Generated)
			if "sha256:"+hex.EncodeToString(entryHash[:]) != entry.ContentHash {
				_ = tarWriter.Close()
				return Summary{}, fmt.Errorf("generated export entry %q changed after planning", name)
			}
			if _, err := tarWriter.Write(entry.Generated); err != nil {
				_ = tarWriter.Close()
				return Summary{}, err
			}
			continue
		}
		pathValue := filepath.Join(plan.ProjectRoot, filepath.FromSlash(entry.RelativePath))
		if _, err := safeRelativePath(plan.ProjectRoot, pathValue); err != nil {
			_ = tarWriter.Close()
			return Summary{}, err
		}
		info, err := os.Lstat(pathValue)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != entry.Size || int64(info.Mode().Perm()) != entry.Mode {
			_ = tarWriter.Close()
			return Summary{}, fmt.Errorf("project export entry %q changed after planning", name)
		}
		resolvedPath, err := filepath.EvalSymlinks(pathValue)
		if err != nil {
			_ = tarWriter.Close()
			return Summary{}, fmt.Errorf("resolve export entry %q: %w", name, err)
		}
		if _, err := safeRelativePath(plan.ProjectRoot, resolvedPath); err != nil {
			_ = tarWriter.Close()
			return Summary{}, err
		}
		file, err := os.Open(resolvedPath)
		if err != nil {
			_ = tarWriter.Close()
			return Summary{}, fmt.Errorf("open export entry %q: %w", name, err)
		}
		entryHash := sha256.New()
		_, copyErr := io.CopyN(io.MultiWriter(tarWriter, entryHash), file, entry.Size)
		closeErr := file.Close()
		if copyErr != nil {
			_ = tarWriter.Close()
			return Summary{}, fmt.Errorf("write export entry %q: %w", name, copyErr)
		}
		if closeErr != nil {
			_ = tarWriter.Close()
			return Summary{}, closeErr
		}
		if "sha256:"+hex.EncodeToString(entryHash.Sum(nil)) != entry.ContentHash {
			_ = tarWriter.Close()
			return Summary{}, fmt.Errorf("project export entry %q changed after planning", name)
		}
	}
	if err := tarWriter.Close(); err != nil {
		return Summary{}, fmt.Errorf("finish project export archive: %w", err)
	}
	if plan.MaxArchiveBytes > 0 && counting.count > plan.MaxArchiveBytes {
		return Summary{}, fmt.Errorf("project export archive exceeds %d bytes", plan.MaxArchiveBytes)
	}
	result := plan.Summary
	result.ArchiveBytes = counting.count
	result.ArchiveChecksum = "sha256:" + hex.EncodeToString(hash.Sum(nil))
	return result, nil
}

func ExportToFile(ctx context.Context, projectRoot, outputPath string, overwrite bool, options PlanOptions) (Summary, error) {
	outputPath = filepath.Clean(strings.TrimSpace(outputPath))
	if outputPath == "." || outputPath == "" {
		return Summary{}, fmt.Errorf("project export output path is required")
	}
	if _, err := os.Lstat(outputPath); err == nil && !overwrite {
		return Summary{}, fmt.Errorf("project export output %q already exists; review it and pass --overwrite to replace it", outputPath)
	} else if err != nil && !os.IsNotExist(err) {
		return Summary{}, fmt.Errorf("inspect project export output %q: %w", outputPath, err)
	}
	plan, err := PlanProject(ctx, projectRoot, options)
	if err != nil {
		return Summary{}, err
	}
	partial, err := os.CreateTemp(filepath.Dir(outputPath), "."+filepath.Base(outputPath)+".partial-*")
	if err != nil {
		return Summary{}, fmt.Errorf("create partial project export: %w", err)
	}
	partialPath := partial.Name()
	keep := false
	defer func() {
		_ = partial.Close()
		if !keep {
			_ = os.Remove(partialPath)
		}
	}()
	result, err := WriteArchive(ctx, partial, plan)
	if err != nil {
		return Summary{}, err
	}
	if err := partial.Sync(); err != nil {
		return Summary{}, err
	}
	if err := partial.Close(); err != nil {
		return Summary{}, err
	}
	if err := os.Rename(partialPath, outputPath); err != nil {
		return Summary{}, fmt.Errorf("install project export %q: %w", outputPath, err)
	}
	keep = true
	absolute, _ := filepath.Abs(outputPath)
	result.OutputPath = absolute
	return result, nil
}

func safeRelativePath(root, pathValue string) (string, error) {
	relative, err := filepath.Rel(root, pathValue)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("project export path %q escapes root %q", pathValue, root)
	}
	return safeArchiveName(filepath.ToSlash(relative))
}

func safeArchiveName(name string) (string, error) {
	if filepath.IsAbs(name) || strings.Contains(name, "\\") {
		return "", fmt.Errorf("unsafe project export archive path %q", name)
	}
	cleaned := filepath.ToSlash(filepath.Clean(name))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("unsafe project export archive path %q", name)
	}
	return cleaned, nil
}

func validateUniqueArchivePaths(entries []Entry) error {
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name, err := safeArchiveName(entry.RelativePath)
		if err != nil {
			return err
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate project export archive path %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validateArchiveSymlink(name, target string) error {
	if strings.TrimSpace(target) == "" || filepath.IsAbs(target) || strings.Contains(target, "\\") {
		return fmt.Errorf("unsafe project export symlink %q target %q", name, target)
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") || strings.HasPrefix(resolved, "/") {
		return fmt.Errorf("unsafe project export symlink %q target %q", name, target)
	}
	return nil
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (writer *countingWriter) Write(payload []byte) (int, error) {
	n, err := writer.writer.Write(payload)
	writer.count += int64(n)
	return n, err
}
