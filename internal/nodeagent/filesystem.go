package nodeagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/filesystemconnector"
)

type filesystemRootsResult struct {
	SafeRoots       []filesystemconnector.SafeRoot        `json:"safe_roots"`
	PublicSummaries []filesystemconnector.SafeRootSummary `json:"public_summaries"`
}

func newFilesystemCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "filesystem",
		Short: "Manage local filesystem connector configuration",
	}
	cmd.AddCommand(newFilesystemRootsCommand(opts))
	return cmd
}

func newFilesystemRootsCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "roots",
		Short: "Manage filesystem connector safe roots",
	}
	cmd.AddCommand(newFilesystemRootsListCommand(opts))
	cmd.AddCommand(newFilesystemRootsAddCommand(opts))
	return cmd
}

func newFilesystemRootsListCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List local filesystem connector safe roots",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, config, _, err := opts.loadAll()
			if err != nil {
				return err
			}
			result := filesystemRootsResult{
				SafeRoots:       config.Filesystem.SafeRoots,
				PublicSummaries: filesystemconnector.PublicSummaries(config.Filesystem),
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), result))
			}
			if len(config.Filesystem.SafeRoots) == 0 {
				_, err = fmt.Fprintln(opts.out, "no filesystem safe roots configured")
				return err
			}
			for _, root := range config.Filesystem.SafeRoots {
				_, err := fmt.Fprintf(
					opts.out,
					"root=%s path=%s list=%t metadata=%t ingest=%t private_backup_only=%t max_file_bytes=%d\n",
					root.RootKey,
					root.AbsolutePath,
					root.AllowList,
					root.AllowMetadata,
					root.AllowIngest,
					root.PrivateBackupOnly,
					root.MaxFileBytes,
				)
				if err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newFilesystemRootsAddCommand(opts *rootOptions) *cobra.Command {
	var root filesystemconnector.SafeRoot
	var rawPath string
	var rawMetadata string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add or replace a filesystem connector safe root",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, config, _, err := opts.loadAll()
			if err != nil {
				return err
			}
			var releaseConfig func() error
			config, releaseConfig, err = lockWatchedRootConfig(cmd.Context(), store)
			if err != nil {
				return err
			}
			defer func() { _ = releaseConfig() }()

			if strings.TrimSpace(rawPath) == "" {
				return errors.New("--path is required")
			}
			absolutePath, err := canonicalDirectoryPath(rawPath)
			if err != nil {
				return err
			}
			root.AbsolutePath = absolutePath
			if strings.TrimSpace(rawMetadata) != "" {
				metadata := json.RawMessage(strings.TrimSpace(rawMetadata))
				if !json.Valid(metadata) {
					return errors.New("--metadata must be valid JSON")
				}
				if len(metadata) == 0 || metadata[0] != '{' {
					return errors.New("--metadata must be a JSON object")
				}
				root.Metadata = metadata
			}
			if root.PrivateBackupOnly {
				root.AllowList = false
				root.AllowMetadata = false
				root.AllowIngest = false
			}
			root = filesystemconnector.NormalizeSafeRoot(root)
			if err := filesystemconnector.ValidateSafeRoot(root); err != nil {
				return err
			}
			config.Filesystem = filesystemconnector.UpsertSafeRoot(config.Filesystem, root)
			if err := store.SaveConfig(config); err != nil {
				return err
			}
			result := filesystemRootsResult{
				SafeRoots:       config.Filesystem.SafeRoots,
				PublicSummaries: filesystemconnector.PublicSummaries(config.Filesystem),
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), result))
			}
			_, err = fmt.Fprintf(opts.out, "filesystem safe root %s configured at %s\n", root.RootKey, root.AbsolutePath)
			return err
		},
	}
	cmd.Flags().StringVar(&root.RootKey, "key", "", "Safe root key")
	cmd.Flags().StringVar(&rawPath, "path", "", "Absolute or relative directory path to expose as a safe root")
	cmd.Flags().StringVar(&root.DisplayName, "display-name", "", "Human-readable safe root name")
	cmd.Flags().BoolVar(&root.AllowList, "allow-list", true, "Allow safe_list on this root")
	cmd.Flags().BoolVar(&root.AllowMetadata, "allow-metadata", true, "Allow read_metadata on this root")
	cmd.Flags().BoolVar(&root.AllowIngest, "allow-ingest", true, "Allow ingest_file on this root")
	cmd.Flags().Int64Var(&root.MaxFileBytes, "max-file-bytes", filesystemconnector.DefaultMaxFileBytes, "Maximum file size ingest_file may read from this root")
	cmd.Flags().BoolVar(&root.IncludeHiddenDefault, "include-hidden-default", false, "Include hidden entries unless a call overrides it")
	cmd.Flags().BoolVar(&root.PrivateBackupOnly, "private-backup-only", false, "Mark this root as private backup-only and exclude it from connector advertisement")
	cmd.Flags().StringVar(&rawMetadata, "metadata", "", "Optional JSON object metadata for local root configuration")
	_ = cmd.MarkFlagRequired("key")
	_ = cmd.MarkFlagRequired("path")
	return cmd
}

func canonicalDirectoryPath(rawPath string) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return "", errors.New("path is required")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(absolutePath); err == nil {
		absolutePath = resolved
	}
	stat, err := os.Stat(absolutePath)
	if err != nil {
		return "", fmt.Errorf("inspect filesystem safe root path: %w", err)
	}
	if !stat.IsDir() {
		return "", fmt.Errorf("filesystem safe root path must be a directory: %s", absolutePath)
	}
	return filepath.Clean(absolutePath), nil
}
