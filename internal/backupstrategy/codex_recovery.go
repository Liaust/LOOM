package backupstrategy

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const CodexRecoveryPath = "/srv/loom/agents/mina/recovery-codex"

type codexCaptureManifest struct {
	Schema      string    `json:"schema"`
	ID          string    `json:"id"`
	Origin      string    `json:"origin"`
	Policy      string    `json:"capture_policy"`
	StartedAt   time.Time `json:"started_at"`
	CreatedAt   time.Time `json:"created_at"`
	PayloadSize int64     `json:"payload_size"`
	PayloadHash string    `json:"payload_sha256"`
	Entries     []struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
		Hash string `json:"sha256"`
	} `json:"entries"`
}

func codexCaptureFileAllowed(name string) bool {
	if path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.HasPrefix(name, "../") {
		return false
	}
	switch name {
	case "state_5.sqlite", "thread_history_1.sqlite", "memories_1.sqlite", "goals_1.sqlite", "history.jsonl", "session_index.jsonl", "installation_id":
		return true
	}
	return strings.HasPrefix(name, "sessions/") && strings.HasSuffix(name, ".jsonl") || strings.HasPrefix(name, "loom-jobs/") && strings.HasSuffix(name, ".json")
}

// CodexArchiveBoundary uses the ordinary authenticated root inventory. It never
// enrolls live CODEX_HOME, credentials or caches as another cloud source.
func CodexArchiveBoundary(ctx context.Context, roots []DirectArchiveRoot, exclusions []DirectArchiveExclusion, directory string, now time.Time) ([]DirectArchiveExclusion, error) {
	result := append([]DirectArchiveExclusion(nil), exclusions...)
	covered := false
	for _, root := range roots {
		rel, err := filepath.Rel(root.Path, directory)
		if err != nil {
			return nil, err
		}
		if rel == ".." || strings.HasPrefix(rel, "../") {
			nested, err := filepath.Rel(directory, root.Path)
			if err == nil && nested != ".." && !strings.HasPrefix(nested, "../") {
				return nil, fmt.Errorf("Codex recovery cannot be enrolled as a nested raw root")
			}
			continue
		}
		if _, err := os.Lstat(directory); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		covered = true
		staging := filepath.ToSlash(filepath.Join(rel, ".staging"))
		present := false
		for _, exclusion := range result {
			if exclusion.Root != root.Name {
				continue
			}
			if exclusion.RelativePath == staging {
				present = true
				continue
			}
			if strings.HasPrefix(staging, exclusion.RelativePath+"/") || strings.HasPrefix(exclusion.RelativePath, staging+"/") {
				return nil, fmt.Errorf("Codex recovery exclusion overlaps another policy")
			}
		}
		if !present {
			result = append(result, DirectArchiveExclusion{Root: root.Name, RelativePath: staging})
		}
	}
	if !covered {
		return result, nil
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("unsafe Codex recovery custody")
	}
	packages, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	fresh := false
	for _, entry := range packages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.Name() == ".staging" {
			continue
		}
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "codex-") {
			return nil, fmt.Errorf("unexpected Codex recovery entry")
		}
		created, err := verifyCodexCapture(ctx, filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		if created.After(now) {
			return nil, fmt.Errorf("future Codex capture")
		}
		if now.Sub(created) <= 25*time.Hour {
			fresh = true
		}
	}
	if !fresh {
		return nil, fmt.Errorf("Codex recovery lacks a current verified capture")
	}
	return result, nil
}

func verifyCodexCapture(ctx context.Context, directory string) (time.Time, error) {
	names, err := os.ReadDir(directory)
	if err != nil || len(names) != 2 || names[0].Name() != "codex.zip" || names[1].Name() != "manifest.json" {
		return time.Time{}, fmt.Errorf("invalid Codex capture package")
	}
	open := func(name string) (*os.File, error) {
		p := filepath.Join(directory, name)
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0440 {
			return nil, fmt.Errorf("unsafe Codex capture file")
		}
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		held, err := f.Stat()
		if err != nil || !os.SameFile(info, held) {
			f.Close()
			return nil, fmt.Errorf("Codex capture changed while opening")
		}
		return f, nil
	}
	manifestFile, err := open("manifest.json")
	if err != nil {
		return time.Time{}, err
	}
	defer manifestFile.Close()
	raw, err := io.ReadAll(io.LimitReader(manifestFile, 4<<20+1))
	if err != nil || len(raw) > 4<<20 {
		return time.Time{}, fmt.Errorf("Codex capture inventory exceeds bound")
	}
	var manifest codexCaptureManifest
	if json.Unmarshal(raw, &manifest) != nil || manifest.Schema != "loom.codex.capture.v1" || manifest.ID != filepath.Base(directory) || manifest.Origin != "/home/agents/.codex" || manifest.Policy != "sqlite_online_and_complete_file_prefixes" || manifest.StartedAt.IsZero() || manifest.CreatedAt.Before(manifest.StartedAt) || len(manifest.Entries) == 0 || len(manifest.Entries) > 16384 || manifest.PayloadSize <= 0 || manifest.PayloadSize > 8<<30 {
		return time.Time{}, fmt.Errorf("invalid Codex capture inventory")
	}
	payload, err := open("codex.zip")
	if err != nil {
		return time.Time{}, err
	}
	defer payload.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(payload, 8<<30+1))
	if err != nil || size != manifest.PayloadSize || hex.EncodeToString(hash.Sum(nil)) != manifest.PayloadHash {
		return time.Time{}, fmt.Errorf("Codex capture payload hash mismatch")
	}
	archive, err := zip.NewReader(payload, size)
	if err != nil || len(archive.File) != len(manifest.Entries) {
		return time.Time{}, fmt.Errorf("Codex capture ZIP inventory mismatch")
	}
	var total int64
	for i, entry := range manifest.Entries {
		if err := ctx.Err(); err != nil {
			return time.Time{}, err
		}
		file := archive.File[i]
		total += entry.Size
		if !codexCaptureFileAllowed(entry.Path) || i > 0 && entry.Path <= manifest.Entries[i-1].Path || entry.Size < 0 || entry.Size > 512<<20 || file.Name != entry.Path || !file.Mode().IsRegular() || file.UncompressedSize64 != uint64(entry.Size) {
			return time.Time{}, fmt.Errorf("invalid Codex captured file")
		}
		if total > 8<<30 {
			return time.Time{}, fmt.Errorf("Codex capture uncompressed size exceeds bound")
		}
		reader, err := file.Open()
		if err != nil {
			return time.Time{}, err
		}
		hash := sha256.New()
		count, copyErr := io.Copy(hash, io.LimitReader(reader, entry.Size+1))
		reader.Close()
		if copyErr != nil || count != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.Hash {
			return time.Time{}, fmt.Errorf("Codex captured file hash mismatch")
		}
	}
	return manifest.CreatedAt, nil
}
