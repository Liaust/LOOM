package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagecatalog"
)

type storageCloudOffloadFlags struct {
	cloudConfigPath string
	nodeID          string
	sourcePath      string
	allowNonArchive bool
}

type storageCloudOffloadSource struct {
	SourceStorageRef     string
	SourceStorageEntryID string
	SourceManifestID     string
	SourcePath           string
	SourceArea           string
	StorageClass         string
}

func newStorageCloudOffloadCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cloud-offload",
		Short: "Plan and apply explicit main-to-cloud storage offloads",
	}
	cmd.AddCommand(newStorageCloudOffloadPlanCommand(opts))
	cmd.AddCommand(newStorageCloudOffloadApplyCommand(opts))
	return cmd
}

func newStorageCloudOffloadPlanCommand(opts *options) *cobra.Command {
	var flags storageCloudOffloadFlags
	cmd := &cobra.Command{
		Use:   "plan <storage-ref-or-local-path>",
		Short: "Plan a cloud offload without copying bytes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			runtimeConfig, cloudConfig, err := loadCloudCLIConfig(opts, flags.cloudConfigPath)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			source, err := resolveStorageCloudOffloadSource(ctx, opts, correlationID, args[0], flags)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if err := requireCloudOffloadArchiveSource(source, flags.allowNonArchive); err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			result, err := cloudstorage.PlanOffload(ctx, cloudstorage.OffloadInput{
				Config:               cloudConfig,
				SourceStorageRef:     source.SourceStorageRef,
				SourceStorageEntryID: source.SourceStorageEntryID,
				SourceManifestID:     source.SourceManifestID,
				SourcePath:           source.SourcePath,
				NodeID:               firstNonEmptyCLI(flags.nodeID, runtimeConfig.NodeID),
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			return renderStorageCloudOffloadResult(cmd, opts, correlationID, result)
		},
	}
	bindStorageCloudOffloadFlags(cmd, &flags)
	return cmd
}

func newStorageCloudOffloadApplyCommand(opts *options) *cobra.Command {
	var flags storageCloudOffloadFlags
	var confirm bool
	cmd := &cobra.Command{
		Use:   "apply <storage-ref-or-local-path>",
		Short: "Upload, verify, and manifest a cloud offload",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if !confirm {
				return renderError(cmd, opts, correlationID, fmt.Errorf("pass --confirm to apply a cloud offload"))
			}
			runtimeConfig, cloudConfig, err := loadCloudCLIConfig(opts, flags.cloudConfigPath)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
			defer cancel()
			source, err := resolveStorageCloudOffloadSource(ctx, opts, correlationID, args[0], flags)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if err := requireCloudOffloadArchiveSource(source, flags.allowNonArchive); err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			result, err := cloudstorage.ApplyOffload(ctx, cloudstorage.OffloadInput{
				Config:               cloudConfig,
				SourceStorageRef:     source.SourceStorageRef,
				SourceStorageEntryID: source.SourceStorageEntryID,
				SourceManifestID:     source.SourceManifestID,
				SourcePath:           source.SourcePath,
				NodeID:               firstNonEmptyCLI(flags.nodeID, runtimeConfig.NodeID),
				Confirm:              true,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if result.Status == cloudstorage.OffloadStatusSucceeded && source.SourceStorageEntryID != "" {
				registerCloudOffloadPhysicalRef(ctx, opts, correlationID, cloudConfig, source, &result)
			}
			return renderStorageCloudOffloadResult(cmd, opts, correlationID, result)
		},
	}
	bindStorageCloudOffloadFlags(cmd, &flags)
	cmd.Flags().BoolVar(&confirm, "confirm", false, "confirm upload and remote verification")
	return cmd
}

func newStorageCloudFetchCommand(opts *options) *cobra.Command {
	var cloudConfigPath string
	var nodeID string
	var to string
	cmd := &cobra.Command{
		Use:   "cloud-fetch <cloud-offload-ref>",
		Short: "Fetch a cloud offload payload to a local path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			runtimeConfig, cloudConfig, err := loadCloudCLIConfig(opts, cloudConfigPath)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
			defer cancel()
			result, err := cloudstorage.FetchOffload(ctx, cloudstorage.OffloadFetchInput{
				Config: cloudConfig,
				NodeID: firstNonEmptyCLI(nodeID, runtimeConfig.NodeID),
				Ref:    args[0],
				To:     to,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Success(correlationID, result))
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), result.TargetPath)
				return nil
			}
			renderStorageCloudFetch(cmd, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&cloudConfigPath, "cloud-config", cloudstorage.DefaultConfigPath, "path to LOOM cloud config JSON")
	cmd.Flags().StringVar(&nodeID, "node-id", "", "offload owner node id; defaults to runtime node id")
	cmd.Flags().StringVar(&to, "to", "", "empty local target directory or missing target file")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func newStorageCloudStatusCommand(opts *options) *cobra.Command {
	var cloudConfigPath string
	var nodeID string
	var stateDir string
	cmd := &cobra.Command{
		Use:   "cloud-status <cloud-offload-ref>",
		Short: "Inspect a cloud offload manifest",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			runtimeConfig, cloudConfig, err := loadCloudCLIConfig(opts, cloudConfigPath)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			result, err := cloudstorage.StatusOffload(ctx, cloudstorage.OffloadStatusInput{
				Config:   cloudConfig,
				NodeID:   firstNonEmptyCLI(nodeID, runtimeConfig.NodeID),
				Ref:      args[0],
				StateDir: firstNonEmptyCLI(stateDir, cloudConfig.StateDir),
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Success(correlationID, result))
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), result.Status)
				return nil
			}
			renderStorageCloudStatus(cmd, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&cloudConfigPath, "cloud-config", cloudstorage.DefaultConfigPath, "path to LOOM cloud config JSON")
	cmd.Flags().StringVar(&nodeID, "node-id", "", "offload owner node id; defaults to runtime node id")
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "cloud state dir for temporary manifest downloads")
	return cmd
}

func bindStorageCloudOffloadFlags(cmd *cobra.Command, flags *storageCloudOffloadFlags) {
	cmd.Flags().StringVar(&flags.cloudConfigPath, "cloud-config", cloudstorage.DefaultConfigPath, "path to LOOM cloud config JSON")
	cmd.Flags().StringVar(&flags.nodeID, "node-id", "", "offload owner node id; defaults to runtime node id")
	cmd.Flags().StringVar(&flags.sourcePath, "source-path", "", "explicit local source path to upload for a storage ref")
	cmd.Flags().BoolVar(&flags.allowNonArchive, "allow-non-archive", false, "allow offloading a source that is not resolved as main Archive material")
}

func resolveStorageCloudOffloadSource(ctx context.Context, opts *options, correlationID, ref string, flags storageCloudOffloadFlags) (storageCloudOffloadSource, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return storageCloudOffloadSource{}, fmt.Errorf("source ref is required")
	}
	if strings.TrimSpace(flags.sourcePath) != "" {
		sourcePath := filepath.Clean(strings.TrimSpace(flags.sourcePath))
		return storageCloudOffloadSource{SourceStorageRef: ref, SourcePath: sourcePath}, nil
	}
	if info, err := os.Stat(ref); err == nil {
		if info.IsDir() || info.Mode().IsRegular() {
			return storageCloudOffloadSource{SourceStorageRef: ref, SourcePath: filepath.Clean(ref)}, nil
		}
	}

	commandCtx, err := resolveCommandContext(opts)
	if err != nil {
		return storageCloudOffloadSource{}, loomerrors.Wrap("transport.unavailable", "runtime", "", "Could not resolve storage ref; pass a local path or --source-path.", err)
	}
	if strings.HasPrefix(ref, ids.StorageEntryPrefix+"_") {
		envelope, err := commandCtx.Client.InspectStorageEntry(ctx, commandCtx.CorrelationID, ref)
		if err != nil {
			return storageCloudOffloadSource{}, loomerrors.Wrap("storage.inspect_failed", "storage", ref, "Could not inspect storage entry.", err)
		}
		return sourceFromStorageDetail(ref, envelope.Data)
	}
	envelope, err := commandCtx.Client.ResolveStoragePath(ctx, firstNonEmptyCLI(commandCtx.CorrelationID, correlationID), ref)
	if err != nil {
		return storageCloudOffloadSource{}, loomerrors.Wrap("storage.resolve_failed", "storage", ref, "Could not resolve storage view path.", err)
	}
	if envelope.Data.EntryDetail == nil {
		return storageCloudOffloadSource{}, fmt.Errorf("storage ref %q did not include an entry detail", ref)
	}
	return sourceFromStorageDetail(ref, *envelope.Data.EntryDetail)
}

func sourceFromStorageDetail(ref string, detail storagecatalog.EntryDetail) (storageCloudOffloadSource, error) {
	localPath := ""
	for _, physicalRef := range detail.PhysicalRefs {
		if path := localPathFromPhysicalRef(physicalRef); path != "" {
			localPath = path
			break
		}
	}
	if localPath == "" {
		return storageCloudOffloadSource{}, fmt.Errorf("storage ref %q has no local/archive physical path to offload", ref)
	}
	sourceManifestID := ""
	if detail.Entry.ArchiveManifestID != nil {
		sourceManifestID = *detail.Entry.ArchiveManifestID
	}
	return storageCloudOffloadSource{
		SourceStorageRef:     ref,
		SourceStorageEntryID: detail.Entry.StorageEntryID,
		SourceManifestID:     sourceManifestID,
		SourcePath:           localPath,
		SourceArea:           detail.Entry.SourceArea,
		StorageClass:         detail.Entry.StorageClass,
	}, nil
}

func localPathFromPhysicalRef(ref storagecatalog.PhysicalRef) string {
	switch ref.RefKind {
	case storagecatalog.PhysicalRefKindArchiveFile,
		storagecatalog.PhysicalRefKindBackupArtifact,
		storagecatalog.PhysicalRefKindLocalPath,
		storagecatalog.PhysicalRefKindDropzoneFile,
		storagecatalog.PhysicalRefKindLaneFile,
		storagecatalog.PhysicalRefKindObjectBlob:
	default:
		return ""
	}
	uri := strings.TrimSpace(ref.URI)
	if strings.HasPrefix(uri, "file://") {
		parsed, err := url.Parse(uri)
		if err != nil {
			return ""
		}
		return parsed.Path
	}
	if filepath.IsAbs(uri) {
		return uri
	}
	return ""
}

func requireCloudOffloadArchiveSource(source storageCloudOffloadSource, allowNonArchive bool) error {
	if allowNonArchive {
		return nil
	}
	if source.SourceArea == storagecatalog.SourceAreaMainArchive || source.StorageClass == storagecatalog.StorageClassArchiveEntry {
		return nil
	}
	sourceRef := strings.Trim(strings.ToLower(source.SourceStorageRef), "/")
	if strings.HasPrefix(sourceRef, "main/archive/") || sourceRef == "main/archive" {
		return nil
	}
	sourcePath := filepath.ToSlash(strings.ToLower(source.SourcePath))
	if strings.Contains(sourcePath, "/storage-archive/") || strings.Contains(sourcePath, "/main/archive/") {
		return nil
	}
	return fmt.Errorf("cloud offload currently accepts main Archive material; pass --allow-non-archive for an explicit test or exceptional source")
}

func registerCloudOffloadPhysicalRef(ctx context.Context, opts *options, correlationID string, cfg cloudstorage.Config, source storageCloudOffloadSource, result *cloudstorage.OffloadResult) {
	if result == nil || source.SourceStorageEntryID == "" {
		return
	}
	commandCtx, err := resolveCommandContext(opts)
	if err != nil {
		result.CatalogRegistrationStatus = "failed"
		result.CatalogRegistrationError = err.Error()
		return
	}
	metadata := map[string]any{
		"schema_version":       "loom.cloud.physical_ref.v0.6.3",
		"physical_backend":     storagecatalog.PhysicalBackendCloud,
		"cloud_provider":       cfg.Provider,
		"cloud_remote_uri":     result.RemoteURI,
		"cloud_payload_uri":    result.PayloadRemoteURI,
		"cloud_manifest_uri":   result.ManifestRemoteURI,
		"cloud_offload_id":     result.OffloadID,
		"custody_state":        storagecatalog.CustodyStateReplicatedToCloud,
		"local_source_action":  result.LocalSourceAction,
		"file_count":           result.FileCount,
		"total_bytes":          result.TotalBytes,
		"source_storage_ref":   result.SourceStorageRef,
		"source_manifest_id":   result.SourceManifestID,
		"manifest_remote_path": result.ManifestRemotePath,
		"payload_remote_path":  result.PayloadRemotePrefix,
	}
	rawMetadata, err := json.Marshal(metadata)
	if err != nil {
		result.CatalogRegistrationStatus = "failed"
		result.CatalogRegistrationError = err.Error()
		return
	}
	envelope, err := commandCtx.Client.RegisterStoragePhysicalRef(ctx, firstNonEmptyCLI(commandCtx.CorrelationID, correlationID), source.SourceStorageEntryID, storagecatalog.RegisterPhysicalRefInput{
		StorageEntryID: source.SourceStorageEntryID,
		RefKind:        storagecatalog.PhysicalRefKindCloudObject,
		URI:            result.PayloadRemoteURI,
		NodeKey:        "cloud:" + cfg.Provider,
		Status:         storagecatalog.PhysicalRefStatusAvailable,
		Metadata:       rawMetadata,
	})
	if err != nil {
		result.CatalogRegistrationStatus = "failed"
		result.CatalogRegistrationError = err.Error()
		return
	}
	result.CatalogRegistrationStatus = "succeeded"
	result.CatalogPhysicalRefID = envelope.Data.StoragePhysicalRefID
}

func renderStorageCloudOffloadResult(cmd *cobra.Command, opts *options, correlationID string, result cloudstorage.OffloadResult) error {
	if opts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Success(correlationID, result))
	}
	if opts.plainOutput {
		fmt.Fprintln(cmd.OutOrStdout(), result.Status)
		return nil
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Cloud offload: %s\n", result.Status)
	fmt.Fprintf(out, "Source: %s\n", result.SourceStorageRef)
	fmt.Fprintf(out, "Path: %s\n", result.SourcePath)
	fmt.Fprintf(out, "Remote: %s\n", result.RemoteURI)
	fmt.Fprintf(out, "Payload: %s\n", result.PayloadRemoteURI)
	fmt.Fprintf(out, "Manifest: %s\n", result.ManifestRemoteURI)
	fmt.Fprintf(out, "Custody: %s  local_action=%s\n", result.Custody, result.LocalSourceAction)
	fmt.Fprintf(out, "Files: %d bytes=%d\n", result.FileCount, result.TotalBytes)
	if result.LocalManifestPath != "" {
		fmt.Fprintf(out, "Local manifest: %s\n", result.LocalManifestPath)
	}
	if result.CatalogRegistrationStatus != "" {
		fmt.Fprintf(out, "Catalog registration: %s", result.CatalogRegistrationStatus)
		if result.CatalogPhysicalRefID != "" {
			fmt.Fprintf(out, " %s", result.CatalogPhysicalRefID)
		}
		fmt.Fprintln(out)
	}
	if result.CatalogRegistrationError != "" {
		fmt.Fprintf(out, "Catalog registration error: %s\n", result.CatalogRegistrationError)
	}
	if result.Error != "" {
		fmt.Fprintf(out, "Error: %s\n", result.Error)
	}
	renderStringMap(cmd, "Checks", result.Checks)
	return nil
}

func renderStorageCloudFetch(cmd *cobra.Command, result cloudstorage.OffloadFetchResult) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Cloud offload fetch: %s\n", result.Status)
	fmt.Fprintf(out, "Remote: %s\n", result.RemoteURI)
	fmt.Fprintf(out, "Target: %s\n", result.TargetPath)
	fmt.Fprintf(out, "Manifest: %s\n", result.ManifestPath)
	if result.Error != "" {
		fmt.Fprintf(out, "Error: %s\n", result.Error)
	}
	renderStringMap(cmd, "Checks", result.Checks)
}

func renderStorageCloudStatus(cmd *cobra.Command, result cloudstorage.OffloadStatusResult) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Cloud offload status: %s\n", result.Status)
	fmt.Fprintf(out, "Remote: %s\n", result.RemoteURI)
	fmt.Fprintf(out, "Manifest: %s\n", result.ManifestPath)
	if result.Manifest.OffloadID != "" {
		writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "FIELD\tVALUE")
		fmt.Fprintf(writer, "offload_id\t%s\n", result.Manifest.OffloadID)
		fmt.Fprintf(writer, "source\t%s\n", result.Manifest.SourceStorageRef)
		fmt.Fprintf(writer, "custody\t%s\n", result.Manifest.Custody)
		fmt.Fprintf(writer, "local_action\t%s\n", result.Manifest.LocalSourceAction)
		fmt.Fprintf(writer, "payload\t%s\n", result.Manifest.PayloadRemoteURI)
		fmt.Fprintf(writer, "files\t%d\n", result.Manifest.FileCount)
		fmt.Fprintf(writer, "bytes\t%d\n", result.Manifest.TotalBytes)
		_ = writer.Flush()
	}
	renderStringMap(cmd, "Checks", result.Checks)
	for _, err := range result.Errors {
		fmt.Fprintf(out, "Error: %s\n", err)
	}
}
