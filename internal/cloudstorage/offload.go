package cloudstorage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	OffloadStatusPlanned              = "planned"
	OffloadStatusSucceeded            = "succeeded"
	OffloadStatusFailed               = "failed"
	OffloadStatusConfirmationRequired = "confirmation_required"
)

type OffloadInput struct {
	Config               Config
	Driver               Driver
	SourceStorageRef     string
	SourceStorageEntryID string
	SourceManifestID     string
	SourcePath           string
	NodeID               string
	Confirm              bool
	Now                  func() time.Time
}

type OffloadResult struct {
	Status                    string            `json:"status"`
	SourceStorageRef          string            `json:"source_storage_ref"`
	SourceStorageEntryID      string            `json:"source_storage_entry_id,omitempty"`
	SourceManifestID          string            `json:"source_manifest_id,omitempty"`
	SourcePath                string            `json:"source_path"`
	SourceIsDir               bool              `json:"source_is_dir"`
	SourceName                string            `json:"source_name"`
	NodeID                    string            `json:"node_id"`
	OffloadID                 string            `json:"offload_id"`
	RemotePrefix              string            `json:"remote_prefix"`
	RemoteURI                 string            `json:"remote_uri"`
	PayloadRemotePrefix       string            `json:"payload_remote_prefix"`
	PayloadRemoteURI          string            `json:"payload_remote_uri"`
	ManifestRemotePath        string            `json:"manifest_remote_path"`
	ManifestRemoteURI         string            `json:"manifest_remote_uri"`
	LocalManifestPath         string            `json:"local_manifest_path,omitempty"`
	CatalogPhysicalRefID      string            `json:"catalog_physical_ref_id,omitempty"`
	CatalogRegistrationStatus string            `json:"catalog_registration_status,omitempty"`
	CatalogRegistrationError  string            `json:"catalog_registration_error,omitempty"`
	Custody                   string            `json:"custody"`
	LocalSourceAction         string            `json:"local_source_action"`
	FileCount                 int64             `json:"file_count"`
	TotalBytes                int64             `json:"total_bytes"`
	Checks                    map[string]string `json:"checks"`
	Manifest                  *OffloadManifest  `json:"manifest,omitempty"`
	Error                     string            `json:"error,omitempty"`
}

type OffloadFetchInput struct {
	Config Config
	Driver Driver
	NodeID string
	Ref    string
	To     string
	Now    func() time.Time
}

type OffloadFetchResult struct {
	Status              string            `json:"status"`
	Ref                 string            `json:"ref"`
	RemotePrefix        string            `json:"remote_prefix"`
	RemoteURI           string            `json:"remote_uri"`
	PayloadRemotePrefix string            `json:"payload_remote_prefix"`
	PayloadRemoteURI    string            `json:"payload_remote_uri"`
	ManifestPath        string            `json:"manifest_path"`
	TargetPath          string            `json:"target_path"`
	Copy                CopyResult        `json:"copy"`
	Manifest            OffloadManifest   `json:"manifest"`
	Checks              map[string]string `json:"checks"`
	Error               string            `json:"error,omitempty"`
}

type OffloadStatusInput struct {
	Config   Config
	Driver   Driver
	NodeID   string
	Ref      string
	StateDir string
	Now      func() time.Time
}

type OffloadStatusResult struct {
	Status       string            `json:"status"`
	Ref          string            `json:"ref"`
	RemotePrefix string            `json:"remote_prefix"`
	RemoteURI    string            `json:"remote_uri"`
	ManifestPath string            `json:"manifest_path"`
	Manifest     OffloadManifest   `json:"manifest,omitempty"`
	CheckedAt    time.Time         `json:"checked_at"`
	Checks       map[string]string `json:"checks"`
	Errors       []string          `json:"errors,omitempty"`
}

func PlanOffload(ctx context.Context, input OffloadInput) (OffloadResult, error) {
	_ = ctx
	result, _, err := buildOffloadPlan(input)
	if err != nil {
		return OffloadResult{}, err
	}
	result.Status = OffloadStatusPlanned
	return result, nil
}

func ApplyOffload(ctx context.Context, input OffloadInput) (OffloadResult, error) {
	result, manifest, err := buildOffloadPlan(input)
	if err != nil {
		return OffloadResult{}, err
	}
	if !input.Confirm {
		result.Status = OffloadStatusConfirmationRequired
		return result, nil
	}
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return OffloadResult{}, err
	}
	driver, err := normalizeDriver(cfg, input.Driver)
	if err != nil {
		return OffloadResult{}, err
	}

	copyOptions := CopyOptions{Checksum: true, SingleFile: !manifest.SourceIsDir}
	if _, err := driver.CopyToRemote(ctx, manifest.SourcePath, manifest.PayloadRemotePrefix, copyOptions); err != nil {
		result.Status = OffloadStatusFailed
		result.Checks["remote_copy"] = OffloadStatusFailed
		result.Error = err.Error()
		return result, nil
	}
	result.Checks["remote_copy"] = OffloadStatusSucceeded

	check, err := driver.Check(ctx, manifest.SourcePath, manifest.PayloadRemotePrefix)
	if err != nil || !check.Matched {
		result.Status = OffloadStatusFailed
		result.Checks["remote_check"] = OffloadStatusFailed
		if err != nil {
			result.Error = err.Error()
		} else {
			result.Error = "remote check failed"
		}
		return result, nil
	}
	result.Checks["remote_check"] = OffloadStatusSucceeded

	completed := normalizeNow(input.Now)()
	manifest.CompletedAt = completed
	manifest.Custody = OffloadCustodyReplicatedToCloud
	manifest.Checks = cloneStringMap(result.Checks)
	localManifestPath := filepath.Join(cfg.StateDir, "offload", "manifests", manifest.OffloadID+".json")
	if err := WriteOffloadManifest(localManifestPath, manifest); err != nil {
		return OffloadResult{}, err
	}
	if _, err := driver.CopyToRemote(ctx, localManifestPath, manifest.ManifestRemotePath, CopyOptions{SingleFile: true}); err != nil {
		result.Status = OffloadStatusFailed
		result.Checks["offload_manifest"] = OffloadStatusFailed
		result.Error = err.Error()
		return result, nil
	}
	result.Checks["offload_manifest"] = OffloadStatusSucceeded
	manifest.Checks = cloneStringMap(result.Checks)
	if err := WriteOffloadManifest(localManifestPath, manifest); err != nil {
		return OffloadResult{}, err
	}
	if _, err := driver.CopyToRemote(ctx, localManifestPath, manifest.ManifestRemotePath, CopyOptions{SingleFile: true}); err != nil {
		result.Status = OffloadStatusFailed
		result.Checks["offload_manifest_refresh"] = OffloadStatusFailed
		result.Error = err.Error()
		return result, nil
	}
	result.Status = OffloadStatusSucceeded
	result.Custody = manifest.Custody
	result.LocalManifestPath = localManifestPath
	result.Manifest = &manifest
	return result, nil
}

func FetchOffload(ctx context.Context, input OffloadFetchInput) (OffloadFetchResult, error) {
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return OffloadFetchResult{}, err
	}
	driver, err := normalizeDriver(cfg, input.Driver)
	if err != nil {
		return OffloadFetchResult{}, err
	}
	ref, err := ResolveOffloadRef(cfg, input.NodeID, input.Ref)
	if err != nil {
		return OffloadFetchResult{}, err
	}
	target := filepath.Clean(strings.TrimSpace(input.To))
	if target == "." || target == "" {
		return OffloadFetchResult{}, fmt.Errorf("target path is required")
	}
	manifestPath := filepath.Join(cfg.StateDir, "temp", "offload-fetch", safeRemoteSegment(path.Base(ref.RemotePrefix)), "offload-manifest.json")
	_ = os.Remove(manifestPath)
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o750); err != nil {
		return OffloadFetchResult{}, err
	}
	result := OffloadFetchResult{
		Status:       OffloadStatusFailed,
		Ref:          input.Ref,
		RemotePrefix: ref.RemotePrefix,
		RemoteURI:    ref.RemoteURI,
		ManifestPath: manifestPath,
		Checks:       map[string]string{},
	}
	if _, err := driver.CopyFromRemote(ctx, path.Join(ref.RemotePrefix, "offload-manifest.json"), manifestPath, CopyOptions{SingleFile: true}); err != nil {
		result.Checks["offload_manifest"] = OffloadStatusFailed
		result.Error = err.Error()
		return result, nil
	}
	manifest, err := ReadOffloadManifest(manifestPath)
	if err != nil {
		result.Checks["offload_manifest"] = OffloadStatusFailed
		result.Error = err.Error()
		return result, nil
	}
	result.Manifest = manifest
	result.PayloadRemotePrefix = manifest.PayloadRemotePrefix
	result.PayloadRemoteURI = manifest.PayloadRemoteURI
	result.Checks["offload_manifest"] = OffloadStatusSucceeded

	targetPath := target
	copyOptions := CopyOptions{Checksum: true, SingleFile: !manifest.SourceIsDir}
	if manifest.SourceIsDir {
		if err := requireEmptyOrMissingDir(targetPath); err != nil {
			return OffloadFetchResult{}, err
		}
	} else {
		if isDirectory(targetPath) {
			targetPath = filepath.Join(targetPath, manifest.SourceName)
		}
		if err := requireMissingFileTarget(targetPath); err != nil {
			return OffloadFetchResult{}, err
		}
	}
	copyResult, err := driver.CopyFromRemote(ctx, manifest.PayloadRemotePrefix, targetPath, copyOptions)
	result.Copy = copyResult
	result.TargetPath = targetPath
	if err != nil {
		result.Checks["remote_fetch"] = OffloadStatusFailed
		result.Error = err.Error()
		return result, nil
	}
	result.Checks["remote_fetch"] = OffloadStatusSucceeded

	fileCount, totalBytes, err := inventoryPath(targetPath)
	if err != nil {
		result.Checks["fetched_inventory"] = OffloadStatusFailed
		result.Error = err.Error()
		return result, nil
	}
	if fileCount != manifest.FileCount || totalBytes != manifest.TotalBytes {
		result.Checks["fetched_inventory"] = OffloadStatusFailed
		result.Error = fmt.Sprintf("fetched inventory mismatch: files=%d/%d bytes=%d/%d", fileCount, manifest.FileCount, totalBytes, manifest.TotalBytes)
		return result, nil
	}
	result.Checks["fetched_inventory"] = OffloadStatusSucceeded
	result.Status = OffloadStatusSucceeded
	return result, nil
}

func StatusOffload(ctx context.Context, input OffloadStatusInput) (OffloadStatusResult, error) {
	now := normalizeNow(input.Now)
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return OffloadStatusResult{}, err
	}
	driver, err := normalizeDriver(cfg, input.Driver)
	if err != nil {
		return OffloadStatusResult{}, err
	}
	ref, err := ResolveOffloadRef(cfg, input.NodeID, input.Ref)
	if err != nil {
		return OffloadStatusResult{}, err
	}
	stateDir := firstNonEmpty(input.StateDir, cfg.StateDir)
	manifestPath := filepath.Join(stateDir, "temp", "offload-status", safeRemoteSegment(path.Base(ref.RemotePrefix)), "offload-manifest.json")
	_ = os.Remove(manifestPath)
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o750); err != nil {
		return OffloadStatusResult{}, err
	}
	result := OffloadStatusResult{
		Status:       OffloadStatusFailed,
		Ref:          input.Ref,
		RemotePrefix: ref.RemotePrefix,
		RemoteURI:    ref.RemoteURI,
		ManifestPath: manifestPath,
		CheckedAt:    now(),
		Checks:       map[string]string{},
	}
	if _, err := driver.CopyFromRemote(ctx, path.Join(ref.RemotePrefix, "offload-manifest.json"), manifestPath, CopyOptions{SingleFile: true}); err != nil {
		result.Checks["offload_manifest"] = OffloadStatusFailed
		result.Errors = append(result.Errors, err.Error())
		return result, nil
	}
	manifest, err := ReadOffloadManifest(manifestPath)
	if err != nil {
		result.Checks["offload_manifest"] = OffloadStatusFailed
		result.Errors = append(result.Errors, err.Error())
		return result, nil
	}
	result.Manifest = manifest
	result.Checks["offload_manifest"] = OffloadStatusSucceeded
	if cleanRemotePrefix(manifest.RemotePrefix) != cleanRemotePrefix(ref.RemotePrefix) {
		result.Checks["remote_prefix_match"] = OffloadStatusFailed
		result.Errors = append(result.Errors, "offload manifest remote_prefix does not match requested offload")
		return result, nil
	}
	result.Checks["remote_prefix_match"] = OffloadStatusSucceeded
	result.Status = OffloadStatusSucceeded
	return result, nil
}

type OffloadRef struct {
	RemotePrefix string `json:"remote_prefix"`
	RemoteURI    string `json:"remote_uri"`
}

func ResolveOffloadRef(cfg Config, nodeID, ref string) (OffloadRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return OffloadRef{}, fmt.Errorf("cloud offload ref is required")
	}
	prefix := ref
	remotePrefix := cfg.RemoteName + ":"
	if strings.HasPrefix(prefix, remotePrefix) {
		prefix = strings.TrimPrefix(prefix, remotePrefix)
	}
	prefix = strings.Trim(prefix, "/")
	remoteRoot := cleanRemotePrefix(cfg.RemoteRoot)
	if strings.HasPrefix(prefix, remoteRoot+"/") {
		prefix = strings.TrimPrefix(prefix, remoteRoot+"/")
	}
	fullOffloadRoot := cleanRemotePrefix(cfg.Roots.FullOffload)
	switch {
	case strings.HasPrefix(prefix, fullOffloadRoot+"/"):
		// Already rooted at full-offload.
	case strings.Contains(prefix, "/"):
		prefix = path.Join(fullOffloadRoot, prefix)
	default:
		return OffloadRef{}, fmt.Errorf("cloud offload ref must be a remote prefix or URI; got %q", ref)
	}
	if err := ValidateRemotePrefix("offload_ref", prefix); err != nil {
		return OffloadRef{}, err
	}
	return OffloadRef{RemotePrefix: cleanRemotePrefix(prefix), RemoteURI: cfg.RemoteURI(prefix)}, nil
}

func buildOffloadPlan(input OffloadInput) (OffloadResult, OffloadManifest, error) {
	now := normalizeNow(input.Now)
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return OffloadResult{}, OffloadManifest{}, err
	}
	if !cfg.Enabled {
		return OffloadResult{}, OffloadManifest{}, fmt.Errorf("cloud storage is disabled")
	}
	sourcePath := filepath.Clean(strings.TrimSpace(input.SourcePath))
	if sourcePath == "." || sourcePath == "" {
		return OffloadResult{}, OffloadManifest{}, fmt.Errorf("source path is required")
	}
	absSource, err := filepath.Abs(sourcePath)
	if err != nil {
		return OffloadResult{}, OffloadManifest{}, err
	}
	info, err := os.Stat(absSource)
	if err != nil {
		return OffloadResult{}, OffloadManifest{}, err
	}
	fileCount, totalBytes, err := inventoryPath(absSource)
	if err != nil {
		return OffloadResult{}, OffloadManifest{}, err
	}
	created := now()
	sourceName := filepath.Base(absSource)
	sourceRef := firstNonEmpty(input.SourceStorageRef, sourceName)
	nodeID := safeRemoteSegment(firstNonEmpty(input.NodeID, "main"))
	offloadID := newCloudOffloadID(created, sourceRef)
	remotePrefix := path.Join(cfg.Roots.FullOffload, nodeID, safeRemoteSegment(sourceRef), offloadID)
	payloadRemotePrefix := path.Join(remotePrefix, "payload")
	if !info.IsDir() {
		payloadRemotePrefix = path.Join(payloadRemotePrefix, safeRemoteSegment(sourceName))
	}
	manifestRemotePath := path.Join(remotePrefix, "offload-manifest.json")
	checks := map[string]string{"source_inventory": OffloadStatusSucceeded}
	manifest := OffloadManifest{
		SchemaVersion:        OffloadManifestSchema,
		OffloadID:            offloadID,
		SourceStorageRef:     sourceRef,
		SourceStorageEntryID: strings.TrimSpace(input.SourceStorageEntryID),
		SourceManifestID:     strings.TrimSpace(input.SourceManifestID),
		SourcePath:           absSource,
		SourceIsDir:          info.IsDir(),
		SourceName:           sourceName,
		NodeID:               nodeID,
		RemoteURI:            cfg.RemoteURI(remotePrefix),
		RemotePrefix:         remotePrefix,
		PayloadRemoteURI:     cfg.RemoteURI(payloadRemotePrefix),
		PayloadRemotePrefix:  payloadRemotePrefix,
		ManifestRemoteURI:    cfg.RemoteURI(manifestRemotePath),
		ManifestRemotePath:   manifestRemotePath,
		CreatedAt:            created,
		Driver:               cfg.Driver,
		Custody:              OffloadCustodyLocalPrimary,
		LocalSourceAction:    OffloadLocalSourceRetained,
		FileCount:            fileCount,
		TotalBytes:           totalBytes,
		Checks:               cloneStringMap(checks),
	}
	result := OffloadResult{
		Status:               OffloadStatusPlanned,
		SourceStorageRef:     manifest.SourceStorageRef,
		SourceStorageEntryID: manifest.SourceStorageEntryID,
		SourceManifestID:     manifest.SourceManifestID,
		SourcePath:           manifest.SourcePath,
		SourceIsDir:          manifest.SourceIsDir,
		SourceName:           manifest.SourceName,
		NodeID:               manifest.NodeID,
		OffloadID:            manifest.OffloadID,
		RemotePrefix:         manifest.RemotePrefix,
		RemoteURI:            manifest.RemoteURI,
		PayloadRemotePrefix:  manifest.PayloadRemotePrefix,
		PayloadRemoteURI:     manifest.PayloadRemoteURI,
		ManifestRemotePath:   manifest.ManifestRemotePath,
		ManifestRemoteURI:    manifest.ManifestRemoteURI,
		Custody:              manifest.Custody,
		LocalSourceAction:    manifest.LocalSourceAction,
		FileCount:            manifest.FileCount,
		TotalBytes:           manifest.TotalBytes,
		Checks:               checks,
		Manifest:             &manifest,
	}
	return result, manifest, nil
}

func inventoryPath(root string) (int64, int64, error) {
	info, err := os.Stat(root)
	if err != nil {
		return 0, 0, err
	}
	if !info.IsDir() {
		return 1, info.Size(), nil
	}
	var fileCount int64
	var totalBytes int64
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		fileCount++
		totalBytes += info.Size()
		return nil
	})
	return fileCount, totalBytes, err
}

func requireMissingFileTarget(target string) error {
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("target file path is required")
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("target file already exists: %s", target)
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(filepath.Dir(target), 0o750)
}

func newCloudOffloadID(now time.Time, sourceRef string) string {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "cloud_offload_" + now.UTC().Format("20060102T150405Z") + "_" + safeRemoteSegment(sourceRef)
	}
	return "cloud_offload_" + now.UTC().Format("20060102T150405Z") + "_" + hex.EncodeToString(random)
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
