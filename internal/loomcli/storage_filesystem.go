package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/db"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/filesystemlayout"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagedoctor"
	"loom.local/loom/internal/storagemigration"
)

const (
	maxFilesystemManifestBytes      = 64 << 20
	maxFilesystemManifestBatchBytes = 8 << 20
	maxFilesystemManifestIndexBytes = 4 << 20
	maxFilesystemManifestRecords    = 2000
)

type filesystemManifestArtifact struct {
	Path     string
	Manifest *storagemigration.Manifest
	Set      *storagemigration.ManifestSet
}

type filesystemManifestSummary struct {
	SchemaVersion     string                         `json:"schema_version"`
	ManifestPath      string                         `json:"manifest_path,omitempty"`
	ManifestHash      string                         `json:"manifest_hash,omitempty"`
	ManifestSetHash   string                         `json:"manifest_set_hash,omitempty"`
	GeneratedAt       time.Time                      `json:"generated_at"`
	NodeID            string                         `json:"node_id"`
	LayoutFingerprint string                         `json:"layout_fingerprint"`
	Roots             []storagemigration.RootMapping `json:"roots"`
	BatchCount        int                            `json:"batch_count"`
	ActionCount       int                            `json:"action_count"`
	ConflictCount     int                            `json:"conflict_count"`
	ReviewRequired    bool                           `json:"review_required"`
}

func newStorageFilesystemCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "filesystem", Short: "Plan and verify canonical storage paths"}
	cmd.AddCommand(newStorageFilesystemStatusCommand(opts))
	cmd.AddCommand(newStorageFilesystemMigrateCommand(opts))
	cmd.AddCommand(newStorageFilesystemVerifyCommand(opts))
	return cmd
}

func newStorageFilesystemStatusCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show bounded canonical physical-root and catalog health",
		RunE: func(cmd *cobra.Command, _ []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetStorageFilesystemStatus(ctx, commandCtx.CorrelationID)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "storage", "filesystem", "Could not read canonical filesystem status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
				return nil
			}
			renderStorageFilesystemStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
}

func renderStorageFilesystemStatus(cmd *cobra.Command, status storagedoctor.FilesystemStatus) {
	fmt.Fprintf(cmd.OutOrStdout(), "Canonical filesystem: %s\n", status.Status)
	for _, root := range status.Roots {
		fmt.Fprintf(cmd.OutOrStdout(), "  %-14s %-7s %s\n", root.Key, root.Status, root.Path)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Catalog sample: returned=%d limit=%d truncated=%t available=%d pending=%d failed=%d deleted=%d\n",
		status.Catalog.Returned, status.Catalog.QueryLimit, status.Catalog.Truncated,
		status.Catalog.Available, status.Catalog.Pending, status.Catalog.Failed, status.Catalog.Deleted)
	fmt.Fprintln(cmd.OutOrStdout(), "Generated export: retired (compatibility diagnostic only)")
}

func newStorageFilesystemMigrateCommand(opts *options) *cobra.Command {
	var dryRun, apply, yes bool
	var manifestPath, rollbackFrom string
	var rootSpecs []string
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Build or apply a reviewed canonical-filesystem manifest",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rollbackMode := strings.TrimSpace(rollbackFrom) != ""
			modeCount := 0
			for _, selected := range []bool{dryRun, apply, rollbackMode} {
				if selected {
					modeCount++
				}
			}
			if modeCount != 1 {
				return fmt.Errorf("choose exactly one of --dry-run, --apply, or --rollback-from")
			}
			if apply && (!yes || strings.TrimSpace(manifestPath) == "") {
				return fmt.Errorf("--apply requires --manifest <path> and --yes")
			}
			if rollbackMode {
				if strings.TrimSpace(manifestPath) == "" {
					return fmt.Errorf("--rollback-from requires a separate --manifest <path> destination")
				}
				if yes || len(rootSpecs) != 0 {
					return fmt.Errorf("--rollback-from is non-mutating and does not accept --yes or --root")
				}
				summary, err := writeFilesystemRollbackArtifact(rollbackFrom, manifestPath, time.Now().UTC())
				if err != nil {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
				}
				return renderStorageFilesystemManifest(cmd, opts, summary)
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			sqlDB, err := db.OpenSQL(ctx, commandCtx.Config.DBURL)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("storage.filesystem.database_unavailable", "storage", "filesystem", "Could not open the storage catalog.", err))
			}
			defer sqlDB.Close()
			catalog := storagecatalog.NewService(sqlDB)
			fingerprint := storagemigration.LayoutFingerprint(commandCtx.Config.NodeID, commandCtx.Filesystem.SafeFields())
			if apply {
				artifact, err := readFilesystemManifestArtifact(manifestPath)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
				result, err := applyFilesystemManifestArtifact(ctx, catalog, artifact, commandCtx.Config.NodeID, fingerprint)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
				return renderStorageFilesystemApply(cmd, opts, result)
			}
			details, err := listFilesystemMigrationDetails(ctx, catalog)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, err)
			}
			roots, err := filesystemMigrationRoots(commandCtx.Filesystem, rootSpecs)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, err)
			}
			manifest, err := storagemigration.Plan(storagemigration.PlanInput{Context: ctx, NodeID: commandCtx.Config.NodeID, LayoutFingerprint: fingerprint, Roots: roots, Details: details, Now: time.Now().UTC()})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, err)
			}
			summary := filesystemManifestSummary{
				SchemaVersion:     manifest.SchemaVersion,
				ManifestHash:      manifest.ManifestHash,
				GeneratedAt:       manifest.GeneratedAt,
				NodeID:            manifest.NodeID,
				LayoutFingerprint: manifest.LayoutFingerprint,
				Roots:             manifest.Roots,
				BatchCount:        1,
				ActionCount:       len(manifest.Actions),
				ConflictCount:     len(manifest.Conflicts),
				ReviewRequired:    true,
			}
			if strings.TrimSpace(manifestPath) != "" {
				set, err := writeFilesystemManifestArtifact(manifestPath, manifest)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
				summary.SchemaVersion = set.SchemaVersion
				summary.ManifestPath = filepath.Clean(manifestPath)
				summary.ManifestHash = ""
				summary.ManifestSetHash = set.ManifestSetHash
				summary.BatchCount = len(set.Batches)
			}
			return renderStorageFilesystemManifest(cmd, opts, summary)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "inventory paths and emit a non-mutating migration manifest")
	cmd.Flags().BoolVar(&apply, "apply", false, "apply catalog rebinds from a reviewed manifest")
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "manifest file to create for dry-run or read for apply")
	cmd.Flags().StringVar(&rollbackFrom, "rollback-from", "", "reviewed forward manifest artifact to invert without mutation")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm application of an already reviewed manifest")
	cmd.Flags().StringArrayVar(&rootSpecs, "root", nil, "explicit migration root as name=old:new; replaces auto-detected roots (repeatable)")
	return cmd
}

func newStorageFilesystemVerifyCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Verify available catalog refs against canonical filesystem roots",
		RunE: func(cmd *cobra.Command, _ []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			sqlDB, err := db.OpenSQL(ctx, commandCtx.Config.DBURL)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, err)
			}
			defer sqlDB.Close()
			catalog := storagecatalog.NewService(sqlDB)
			details, err := listFilesystemMigrationDetails(ctx, catalog)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, err)
			}
			layout := commandCtx.Filesystem
			result := storagemigration.Verify(storagemigration.VerifyInput{Details: details, CanonicalRoots: []string{layout.ImportsRoot.String(), layout.ArchiveRoot.String()}, ClassRoots: map[string][]string{
				storagecatalog.PhysicalRefClassCanonicalCustody: {layout.ImportsRoot.String(), layout.ArchiveRoot.String()},
				storagecatalog.PhysicalRefClassRetentionCopy:    {layout.StorageRetentionRoot.String(), commandCtx.Config.ObjectStore},
				storagecatalog.PhysicalRefClassArchiveCopy:      {layout.ArchiveRoot.String()},
				storagecatalog.PhysicalRefClassBackupArtifact:   {layout.UserBackupsRoot.String()},
			}})
			return renderStorageFilesystemVerify(cmd, opts, result)
		},
	}
}

func listFilesystemMigrationDetails(ctx context.Context, catalog storagecatalog.Service) ([]storagecatalog.EntryDetail, error) {
	entries, err := catalog.ListAllEntries(ctx, storagecatalog.ListFilter{IncludeDeleted: true})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.StorageEntryID)
	}
	byID, err := catalog.ListEntryDetails(ctx, ids)
	if err != nil {
		return nil, err
	}
	details := make([]storagecatalog.EntryDetail, 0, len(byID))
	for _, detail := range byID {
		details = append(details, detail)
	}
	sort.Slice(details, func(i, j int) bool { return details[i].Entry.StorageEntryID < details[j].Entry.StorageEntryID })
	return details, nil
}

func filesystemMigrationRoots(layout filesystemlayout.Layout, specs []string) ([]storagemigration.RootMapping, error) {
	if len(specs) > 0 {
		explicit := make([]storagemigration.RootMapping, 0, len(specs))
		for _, spec := range specs {
			mapping, err := parseFilesystemRootSpec(spec)
			if err != nil {
				return nil, err
			}
			explicit = append(explicit, mapping)
		}
		return storagemigration.ValidateRootMappings(explicit)
	}

	var candidates []storagemigration.RootMapping
	addDefault := func(name, oldRoot, newRoot string) {
		if oldRoot != "" && filepath.Clean(oldRoot) != filepath.Clean(newRoot) {
			candidates = append(candidates, storagemigration.RootMapping{Name: name, OldRoot: filepath.Clean(oldRoot), NewRoot: filepath.Clean(newRoot)})
		}
	}
	addDefault("lane_accepted", filepath.Join(layout.DataRoot.String(), "lane", "accepted"), layout.ImportsRoot.String())
	addDefault("main_documents", layout.DeprecatedMainDocumentsRoot.String(), filepath.Join(layout.BoxRoot.String(), "Documents"))
	addDefault("archive_custody", filepath.Join(layout.DataRoot.String(), "storage-archive", "objects"), layout.ArchiveRoot.String())
	if _, err := storagemigration.ValidateRootMappings(candidates); err != nil {
		return nil, err
	}
	selected := make([]storagemigration.RootMapping, 0, len(candidates))
	for _, mapping := range candidates {
		if _, err := os.Stat(mapping.OldRoot); err == nil {
			selected = append(selected, mapping)
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no migration roots exist; provide --root name=old:new for an explicit plan")
	}
	return storagemigration.ValidateRootMappings(selected)
}

func parseFilesystemRootSpec(spec string) (storagemigration.RootMapping, error) {
	nameAndPaths := strings.SplitN(strings.TrimSpace(spec), "=", 2)
	if len(nameAndPaths) != 2 {
		return storagemigration.RootMapping{}, fmt.Errorf("invalid --root %q: want name=old:new", spec)
	}
	paths := strings.SplitN(nameAndPaths[1], ":", 2)
	if len(paths) != 2 {
		return storagemigration.RootMapping{}, fmt.Errorf("invalid --root %q: want name=old:new", spec)
	}
	mapping := storagemigration.RootMapping{Name: strings.TrimSpace(nameAndPaths[0]), OldRoot: strings.TrimSpace(paths[0]), NewRoot: strings.TrimSpace(paths[1])}
	validated, err := storagemigration.ValidateRootMappings([]storagemigration.RootMapping{mapping})
	if err != nil {
		return storagemigration.RootMapping{}, fmt.Errorf("invalid --root %q: %w", spec, err)
	}
	return validated[0], nil
}

func writeFilesystemManifest(pathValue string, manifest storagemigration.Manifest) error {
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if len(payload) > maxFilesystemManifestBytes {
		return fmt.Errorf("single manifest requires %d bytes, exceeding the symmetric %d-byte read/write limit; use a manifest artifact", len(payload), maxFilesystemManifestBytes)
	}
	file, err := os.OpenFile(filepath.Clean(pathValue), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create manifest without overwrite: %w", err)
	}
	if _, err = file.Write(payload); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func writeFilesystemManifestArtifact(pathValue string, manifest storagemigration.Manifest) (storagemigration.ManifestSet, error) {
	set, batches, err := storagemigration.SplitManifest(manifest, maxFilesystemManifestRecords, maxFilesystemManifestBatchBytes)
	if err != nil {
		return storagemigration.ManifestSet{}, err
	}
	if err := writeFilesystemManifestSetArtifact(pathValue, set, batches); err != nil {
		return storagemigration.ManifestSet{}, err
	}
	return set, nil
}

func writeFilesystemManifestSetArtifact(pathValue string, set storagemigration.ManifestSet, batches []storagemigration.Manifest) error {
	if len(batches) != len(set.Batches) {
		return fmt.Errorf("manifest set has %d descriptors for %d children", len(set.Batches), len(batches))
	}
	indexPayload, err := json.MarshalIndent(set, "", "  ")
	if err != nil {
		return err
	}
	if len(indexPayload)+1 > maxFilesystemManifestIndexBytes {
		return fmt.Errorf("manifest set index requires %d bytes, exceeding the symmetric %d-byte read/write limit", len(indexPayload)+1, maxFilesystemManifestIndexBytes)
	}
	target := filepath.Clean(pathValue)
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("create manifest artifact without overwrite: %s already exists", target)
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(target)
	temp, err := os.MkdirTemp(parent, "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return fmt.Errorf("create manifest artifact: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(temp)
		}
	}()
	for index, batch := range batches {
		payload, err := json.MarshalIndent(batch, "", "  ")
		if err != nil {
			return err
		}
		if int64(len(payload)+1) != set.Batches[index].EncodedBytes || len(payload)+1 > maxFilesystemManifestBatchBytes {
			return fmt.Errorf("manifest batch %s encoded size is not bounded by its descriptor", set.Batches[index].File)
		}
		if err := writeJSONExclusive(filepath.Join(temp, set.Batches[index].File), batch); err != nil {
			return err
		}
	}
	if err := writeJSONExclusive(filepath.Join(temp, "index.json"), set); err != nil {
		return err
	}
	if err := os.Rename(temp, target); err != nil {
		return fmt.Errorf("publish manifest artifact: %w", err)
	}
	keep = true
	return nil
}

func writeJSONExclusive(pathValue string, value any) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	file, err := os.OpenFile(pathValue, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func readFilesystemManifest(pathValue string) (storagemigration.Manifest, error) {
	clean := filepath.Clean(pathValue)
	info, err := os.Lstat(clean)
	if err != nil {
		return storagemigration.Manifest{}, err
	}
	if !info.Mode().IsRegular() {
		return storagemigration.Manifest{}, fmt.Errorf("manifest must be a regular file, not a symlink or special file")
	}
	file, err := os.Open(clean)
	if err != nil {
		return storagemigration.Manifest{}, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxFilesystemManifestBytes+1))
	if err != nil {
		return storagemigration.Manifest{}, err
	}
	if len(payload) > maxFilesystemManifestBytes {
		return storagemigration.Manifest{}, fmt.Errorf("manifest exceeds %d bytes", maxFilesystemManifestBytes)
	}
	var manifest storagemigration.Manifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return storagemigration.Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	return manifest, nil
}

func readFilesystemManifestArtifact(pathValue string) (filesystemManifestArtifact, error) {
	clean := filepath.Clean(pathValue)
	info, err := os.Lstat(clean)
	if err != nil {
		return filesystemManifestArtifact{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return filesystemManifestArtifact{}, fmt.Errorf("manifest artifact must not be a symlink")
	}
	if !info.IsDir() {
		manifest, err := readFilesystemManifest(clean)
		if err != nil {
			return filesystemManifestArtifact{}, err
		}
		return filesystemManifestArtifact{Path: clean, Manifest: &manifest}, nil
	}
	var set storagemigration.ManifestSet
	if err := readBoundedJSON(filepath.Join(clean, "index.json"), maxFilesystemManifestIndexBytes, &set); err != nil {
		return filesystemManifestArtifact{}, fmt.Errorf("read manifest set index: %w", err)
	}
	if set.SchemaVersion != storagemigration.ManifestSetSchemaVersion {
		return filesystemManifestArtifact{}, fmt.Errorf("unsupported manifest set schema %q", set.SchemaVersion)
	}
	if err := storagemigration.ValidateManifestSetHash(set); err != nil {
		return filesystemManifestArtifact{}, err
	}
	if err := validateFilesystemManifestSetIndex(set); err != nil {
		return filesystemManifestArtifact{}, err
	}
	return filesystemManifestArtifact{Path: clean, Set: &set}, nil
}

func writeFilesystemRollbackArtifact(sourcePath, destinationPath string, generatedAt time.Time) (filesystemManifestSummary, error) {
	artifact, err := readFilesystemManifestArtifact(sourcePath)
	if err != nil {
		return filesystemManifestSummary{}, err
	}
	if artifact.Manifest != nil {
		rollback, err := storagemigration.RollbackManifest(*artifact.Manifest, generatedAt)
		if err != nil {
			return filesystemManifestSummary{}, err
		}
		if err := writeFilesystemManifest(destinationPath, rollback); err != nil {
			return filesystemManifestSummary{}, err
		}
		return filesystemManifestSummary{
			SchemaVersion:     rollback.SchemaVersion,
			ManifestPath:      filepath.Clean(destinationPath),
			ManifestHash:      rollback.ManifestHash,
			GeneratedAt:       rollback.GeneratedAt,
			NodeID:            rollback.NodeID,
			LayoutFingerprint: rollback.LayoutFingerprint,
			Roots:             rollback.Roots,
			BatchCount:        1,
			ActionCount:       len(rollback.Actions),
			ConflictCount:     0,
			ReviewRequired:    true,
		}, nil
	}
	if artifact.Set == nil {
		return filesystemManifestSummary{}, fmt.Errorf("forward manifest artifact is empty")
	}
	forwardSet := *artifact.Set
	if err := storagemigration.ValidateManifestSetHash(forwardSet); err != nil {
		return filesystemManifestSummary{}, err
	}
	if err := validateFilesystemManifestSetIndex(forwardSet); err != nil {
		return filesystemManifestSummary{}, err
	}
	if forwardSet.ConflictCount != 0 {
		return filesystemManifestSummary{}, fmt.Errorf("forward manifest set has %d open conflicts", forwardSet.ConflictCount)
	}
	if strings.TrimSpace(forwardSet.Review.ReviewedBy) == "" || forwardSet.Review.ReviewedAt == nil || forwardSet.Review.ReviewedAt.IsZero() {
		return filesystemManifestSummary{}, fmt.Errorf("forward manifest set is not reviewed")
	}
	if err := storagemigration.ValidateRootInventory(forwardSet.Roots, forwardSet.RootInventory, true); err != nil {
		return filesystemManifestSummary{}, err
	}
	rollbackBatches := make([]storagemigration.Manifest, 0, len(forwardSet.Batches))
	rollbackDescriptors := make([]storagemigration.ManifestBatch, 0, len(forwardSet.Batches))
	for _, descriptor := range forwardSet.Batches {
		forwardBatch, err := loadFilesystemManifestBatch(artifact.Path, forwardSet, descriptor)
		if err != nil {
			return filesystemManifestSummary{}, err
		}
		forwardBatch.Review = forwardSet.Review
		rollbackBatch, err := storagemigration.RollbackManifest(forwardBatch, generatedAt)
		if err != nil {
			return filesystemManifestSummary{}, err
		}
		payload, err := json.MarshalIndent(rollbackBatch, "", "  ")
		if err != nil {
			return filesystemManifestSummary{}, err
		}
		encodedBytes := int64(len(payload) + 1)
		if encodedBytes > maxFilesystemManifestBatchBytes {
			return filesystemManifestSummary{}, fmt.Errorf("rollback manifest batch %s requires %d bytes, exceeding %d", descriptor.File, encodedBytes, maxFilesystemManifestBatchBytes)
		}
		rollbackBatches = append(rollbackBatches, rollbackBatch)
		rollbackDescriptors = append(rollbackDescriptors, storagemigration.ManifestBatch{
			File:          descriptor.File,
			ManifestHash:  rollbackBatch.ManifestHash,
			EncodedBytes:  encodedBytes,
			ActionCount:   len(rollbackBatch.Actions),
			ConflictCount: 0,
		})
	}
	if len(rollbackBatches) == 0 {
		return filesystemManifestSummary{}, fmt.Errorf("forward manifest set has no children")
	}
	rollbackSet := storagemigration.ManifestSet{
		SchemaVersion:     storagemigration.ManifestSetSchemaVersion,
		GeneratedAt:       generatedAt.UTC(),
		NodeID:            forwardSet.NodeID,
		LayoutFingerprint: forwardSet.LayoutFingerprint,
		Roots:             append([]storagemigration.RootMapping(nil), rollbackBatches[0].Roots...),
		RootInventory:     append([]storagemigration.RootInventoryEvidence(nil), rollbackBatches[0].RootInventory...),
		Batches:           rollbackDescriptors,
		ActionCount:       forwardSet.ActionCount,
		ConflictCount:     0,
		Review:            storagemigration.Review{},
	}
	if err := storagemigration.RefreshManifestSetHash(&rollbackSet); err != nil {
		return filesystemManifestSummary{}, err
	}
	if err := writeFilesystemManifestSetArtifact(destinationPath, rollbackSet, rollbackBatches); err != nil {
		return filesystemManifestSummary{}, err
	}
	return filesystemManifestSummary{
		SchemaVersion:     rollbackSet.SchemaVersion,
		ManifestPath:      filepath.Clean(destinationPath),
		ManifestSetHash:   rollbackSet.ManifestSetHash,
		GeneratedAt:       rollbackSet.GeneratedAt,
		NodeID:            rollbackSet.NodeID,
		LayoutFingerprint: rollbackSet.LayoutFingerprint,
		Roots:             rollbackSet.Roots,
		BatchCount:        len(rollbackSet.Batches),
		ActionCount:       rollbackSet.ActionCount,
		ConflictCount:     0,
		ReviewRequired:    true,
	}, nil
}

func readBoundedJSON(pathValue string, maxBytes int64, target any) error {
	info, err := os.Lstat(pathValue)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("artifact member must be a regular file, not a symlink or special file")
	}
	if info.Size() > maxBytes {
		return fmt.Errorf("artifact member exceeds %d bytes", maxBytes)
	}
	file, err := os.Open(pathValue)
	if err != nil {
		return err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return err
	}
	if int64(len(payload)) > maxBytes {
		return fmt.Errorf("artifact member exceeds %d bytes", maxBytes)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("decode artifact member: %w", err)
	}
	return nil
}

func validateFilesystemManifestSetIndex(set storagemigration.ManifestSet) error {
	if err := storagemigration.ValidateRootInventory(set.Roots, set.RootInventory, false); err != nil {
		return err
	}
	if len(set.Batches) == 0 {
		return fmt.Errorf("manifest set has no batches")
	}
	actions, conflicts := 0, 0
	seen := map[string]struct{}{}
	for index, batch := range set.Batches {
		wantFile := fmt.Sprintf("batch-%06d.json", index+1)
		if batch.File != wantFile {
			return fmt.Errorf("manifest batch %d file is %q, want deterministic %q", index+1, batch.File, wantFile)
		}
		if _, exists := seen[batch.File]; exists {
			return fmt.Errorf("duplicate manifest batch file %q", batch.File)
		}
		seen[batch.File] = struct{}{}
		if strings.TrimSpace(batch.ManifestHash) == "" || batch.EncodedBytes <= 0 || batch.EncodedBytes > maxFilesystemManifestBatchBytes {
			return fmt.Errorf("manifest batch %s has invalid hash or encoded size", batch.File)
		}
		records := batch.ActionCount + batch.ConflictCount
		emptySetBatch := len(set.Batches) == 1 && set.ActionCount == 0 && set.ConflictCount == 0 && records == 0
		if batch.ActionCount < 0 || batch.ConflictCount < 0 || (!emptySetBatch && records <= 0) || records > maxFilesystemManifestRecords {
			return fmt.Errorf("manifest batch %s has invalid record counts", batch.File)
		}
		actions += batch.ActionCount
		conflicts += batch.ConflictCount
	}
	if actions != set.ActionCount || conflicts != set.ConflictCount {
		return fmt.Errorf("manifest set totals do not match batch descriptors")
	}
	return nil
}

func loadFilesystemManifestBatch(artifactPath string, set storagemigration.ManifestSet, descriptor storagemigration.ManifestBatch) (storagemigration.Manifest, error) {
	if filepath.IsAbs(descriptor.File) || filepath.Base(descriptor.File) != descriptor.File || descriptor.File == "." {
		return storagemigration.Manifest{}, fmt.Errorf("unsafe manifest batch path %q", descriptor.File)
	}
	if descriptor.EncodedBytes <= 0 || descriptor.EncodedBytes > maxFilesystemManifestBatchBytes {
		return storagemigration.Manifest{}, fmt.Errorf("manifest batch %s has invalid encoded size %d", descriptor.File, descriptor.EncodedBytes)
	}
	batchPath := filepath.Join(artifactPath, descriptor.File)
	var batch storagemigration.Manifest
	if err := readBoundedJSON(batchPath, descriptor.EncodedBytes, &batch); err != nil {
		return storagemigration.Manifest{}, err
	}
	info, err := os.Lstat(batchPath)
	if err != nil || info.Size() != descriptor.EncodedBytes {
		return storagemigration.Manifest{}, fmt.Errorf("manifest batch %s size does not match index", descriptor.File)
	}
	if err := storagemigration.ValidateManifestHash(batch); err != nil {
		return storagemigration.Manifest{}, err
	}
	if err := storagemigration.ValidateManifestSemantics(batch); err != nil {
		return storagemigration.Manifest{}, err
	}
	if batch.ManifestHash != descriptor.ManifestHash || len(batch.Actions) != descriptor.ActionCount || len(batch.Conflicts) != descriptor.ConflictCount {
		return storagemigration.Manifest{}, fmt.Errorf("manifest batch %s does not match index", descriptor.File)
	}
	if batch.NodeID != set.NodeID || batch.LayoutFingerprint != set.LayoutFingerprint || !reflect.DeepEqual(batch.Roots, set.Roots) {
		return storagemigration.Manifest{}, fmt.Errorf("manifest batch %s has different node/layout roots", descriptor.File)
	}
	if !reflect.DeepEqual(batch.RootInventory, set.RootInventory) {
		return storagemigration.Manifest{}, fmt.Errorf("manifest batch %s has different root inventory", descriptor.File)
	}
	return batch, nil
}

func applyFilesystemManifestArtifact(ctx context.Context, catalog storagemigration.CatalogRebinder, artifact filesystemManifestArtifact, nodeID, fingerprint string) (storagemigration.ApplyResult, error) {
	if artifact.Manifest != nil {
		return storagemigration.Apply(ctx, catalog, storagemigration.ApplyInput{Manifest: *artifact.Manifest, NodeID: nodeID, LayoutFingerprint: fingerprint, LoadedFromFile: true})
	}
	if artifact.Set == nil {
		return storagemigration.ApplyResult{}, fmt.Errorf("manifest artifact is empty")
	}
	batchCatalog, ok := catalog.(storagemigration.CatalogBatchRebinder)
	if !ok {
		return storagemigration.ApplyResult{}, fmt.Errorf("storage catalog does not support atomic manifest-set rebind")
	}
	set := *artifact.Set
	if strings.TrimSpace(set.Review.ReviewedBy) == "" || set.Review.ReviewedAt == nil || set.Review.ReviewedAt.IsZero() {
		return storagemigration.ApplyResult{}, fmt.Errorf("manifest set is not reviewed")
	}
	if set.ConflictCount > 0 {
		return storagemigration.ApplyResult{}, fmt.Errorf("manifest set has %d open conflicts", set.ConflictCount)
	}
	if strings.TrimSpace(nodeID) != set.NodeID || strings.TrimSpace(fingerprint) != set.LayoutFingerprint {
		return storagemigration.ApplyResult{}, fmt.Errorf("manifest set node/layout does not match this runtime")
	}
	if err := storagemigration.ValidateRootInventory(set.Roots, set.RootInventory, true); err != nil {
		return storagemigration.ApplyResult{}, err
	}
	if err := storagemigration.VerifyReviewedRootMove(ctx, set.Roots, set.RootInventory); err != nil {
		return storagemigration.ApplyResult{}, fmt.Errorf("root-move preflight: %w", err)
	}
	loadBatch := func(descriptor storagemigration.ManifestBatch) (storagemigration.Manifest, error) {
		return loadFilesystemManifestBatch(artifact.Path, set, descriptor)
	}
	// Validate the complete artifact, including filesystem evidence and
	// cross-batch uniqueness, before the one catalog transaction.
	previousStorageEntryID := ""
	verifiedDestinations := 0
	for _, descriptor := range set.Batches {
		batch, err := loadBatch(descriptor)
		if err != nil {
			return storagemigration.ApplyResult{}, fmt.Errorf("preflight %s: %w", descriptor.File, err)
		}
		batch.Review = set.Review
		prepared, err := storagemigration.PreflightApplyActions(ctx, storagemigration.ApplyInput{Manifest: batch, NodeID: nodeID, LayoutFingerprint: fingerprint, LoadedFromFile: true})
		if err != nil {
			return storagemigration.ApplyResult{}, fmt.Errorf("preflight %s: %w", descriptor.File, err)
		}
		verifiedDestinations += prepared.VerifiedDestinations
		batchEntryID := ""
		for _, action := range batch.Actions {
			if batchEntryID != "" && action.StorageEntryID < batchEntryID {
				return storagemigration.ApplyResult{}, fmt.Errorf("preflight %s: actions are not deterministically grouped by storage entry", descriptor.File)
			}
			if batchEntryID == "" && previousStorageEntryID != "" && action.StorageEntryID <= previousStorageEntryID {
				return storagemigration.ApplyResult{}, fmt.Errorf("preflight %s: storage entry %s overlaps an earlier batch", descriptor.File, action.StorageEntryID)
			}
			batchEntryID = action.StorageEntryID
		}
		if batchEntryID != "" {
			previousStorageEntryID = batchEntryID
		}
	}
	// Repeat the complete evidence pass immediately before opening the catalog
	// transaction. Slice 10 still stops writers because an external filesystem
	// rename and a PostgreSQL commit cannot be one atomic primitive.
	for _, descriptor := range set.Batches {
		batch, err := loadBatch(descriptor)
		if err != nil {
			return storagemigration.ApplyResult{}, fmt.Errorf("repeat preflight %s: %w", descriptor.File, err)
		}
		batch.Review = set.Review
		if _, err := storagemigration.RecheckApplyActionEvidence(ctx, storagemigration.ApplyInput{Manifest: batch, NodeID: nodeID, LayoutFingerprint: fingerprint, LoadedFromFile: true}); err != nil {
			return storagemigration.ApplyResult{}, fmt.Errorf("repeat preflight %s: %w", descriptor.File, err)
		}
	}
	if err := storagemigration.VerifyReviewedRootMove(ctx, set.Roots, set.RootInventory); err != nil {
		return storagemigration.ApplyResult{}, fmt.Errorf("repeat root-move preflight: %w", err)
	}
	if set.ActionCount == 0 {
		return storagemigration.ApplyResult{VerifiedDestinations: verifiedDestinations, BatchesApplied: len(set.Batches)}, nil
	}
	loadCatalogBatch := func(index int) (storagecatalog.RebindPathsInput, error) {
		if index < 0 || index >= len(set.Batches) {
			return storagecatalog.RebindPathsInput{}, fmt.Errorf("manifest batch index %d is out of range", index)
		}
		descriptor := set.Batches[index]
		batch, err := loadBatch(descriptor)
		if err != nil {
			return storagecatalog.RebindPathsInput{}, fmt.Errorf("reload %s: %w", descriptor.File, err)
		}
		batch.Review = set.Review
		return storagemigration.PrepareCatalogRebind(storagemigration.ApplyInput{Manifest: batch, NodeID: nodeID, LayoutFingerprint: fingerprint, LoadedFromFile: true})
	}
	catalogResult, err := batchCatalog.RebindPathBatches(ctx, len(set.Batches), loadCatalogBatch)
	if err != nil {
		return storagemigration.ApplyResult{}, err
	}
	return storagemigration.ApplyResult{Catalog: catalogResult, VerifiedDestinations: verifiedDestinations, BatchesApplied: len(set.Batches)}, nil
}

func renderStorageFilesystemManifest(cmd *cobra.Command, opts *options, summary filesystemManifestSummary) error {
	if opts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Migration actions: %d\nConflicts: %d\nManifest batches: %d\n", summary.ActionCount, summary.ConflictCount, summary.BatchCount)
	if summary.ManifestPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Manifest artifact: %s\nManifest set hash: %s\n", summary.ManifestPath, summary.ManifestSetHash)
	}
	return nil
}
func renderStorageFilesystemApply(cmd *cobra.Command, opts *options, result storagemigration.ApplyResult) error {
	if opts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Manifest batches applied: %d\nVerified destinations: %d\nPhysical refs updated: %d\nEntry paths updated: %d\nAlready applied: %d\n", result.BatchesApplied, result.VerifiedDestinations, result.Catalog.PhysicalRefsUpdated, result.Catalog.EntriesUpdated, result.Catalog.AlreadyApplied)
	return nil
}

func renderStorageFilesystemVerify(cmd *cobra.Command, opts *options, result storagemigration.VerifyResult) error {
	if opts.jsonOutput {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Filesystem refs checked: %d\n", result.CheckedAvailableRefs)
		fmt.Fprintf(cmd.OutOrStdout(), "Findings: %d\n", len(result.Findings))
		for _, finding := range result.Findings {
			fmt.Fprintf(cmd.OutOrStdout(), "- %s %s: %s\n", finding.Code, finding.Path, finding.Detail)
		}
	}
	if !result.OK {
		return fmt.Errorf("canonical filesystem verification found %d problems", len(result.Findings))
	}
	return nil
}
