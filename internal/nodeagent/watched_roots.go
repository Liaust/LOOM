package nodeagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/filesystemconnector"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectquiescence"
	"loom.local/loom/internal/projects"
)

type watchedRootAddResult struct {
	Worker     noderuntime.WorkerInstance `json:"worker"`
	Config     watchedroots.RootConfig    `json:"config"`
	ConfigHash string                     `json:"config_hash"`
	Paths      watchedroots.RootPaths     `json:"paths"`
}

type watchedRootStatusResult struct {
	Roots []watchedRootStatus `json:"roots"`
}

type watchedRootStatus struct {
	RootKey        string                        `json:"root_key"`
	WorkerKey      string                        `json:"worker_key"`
	Status         string                        `json:"status"`
	Config         watchedroots.RootConfig       `json:"config"`
	ConfigHash     string                        `json:"config_hash,omitempty"`
	Worker         noderuntime.WorkerInstance    `json:"worker"`
	Health         *noderuntime.WorkerHealth     `json:"health,omitempty"`
	Checkpoint     *noderuntime.WorkerCheckpoint `json:"checkpoint,omitempty"`
	Summary        *watchedroots.RootSummary     `json:"summary,omitempty"`
	RootCheckpoint *watchedroots.RootCheckpoint  `json:"root_checkpoint,omitempty"`
	DirtyHints     int                           `json:"dirty_hints"`
	Findings       int                           `json:"findings"`
	BackupStatus   *LocalWatchedRootBackupStatus `json:"backup_status,omitempty"`
	PathCounts     map[string]int                `json:"path_counts,omitempty"`
	Paths          watchedroots.RootPaths        `json:"paths"`
	Error          string                        `json:"error,omitempty"`
}

type watchedRootDisableResult struct {
	RootKey   string                     `json:"root_key"`
	WorkerKey string                     `json:"worker_key"`
	Changed   bool                       `json:"changed"`
	Reason    string                     `json:"reason"`
	Worker    noderuntime.WorkerInstance `json:"worker"`
}

func newWatchedRootsCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watched-roots",
		Short: "Manage local watched-root scanner configuration",
	}
	cmd.AddCommand(newWatchedRootsAddCommand(opts))
	cmd.AddCommand(newWatchedRootsDisableCommand(opts))
	cmd.AddCommand(newWatchedRootsApplyPlanCommand(opts))
	cmd.AddCommand(newWatchedRootsListCommand(opts))
	cmd.AddCommand(newWatchedRootsStatusCommand(opts))
	cmd.AddCommand(newWatchedRootsRunCommand(opts))
	cmd.AddCommand(newWatchedRootsExplainCommand(opts))
	cmd.AddCommand(newWatchedRootsBackupsCommand(opts))
	return cmd
}

func newWatchedRootsDisableCommand(opts *rootOptions) *cobra.Command {
	var yes bool
	var reason string
	cmd := &cobra.Command{
		Use:   "disable <root-key>",
		Short: "Disable a watched-root worker while preserving its evidence",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return errors.New("watched-roots disable requires --yes")
			}
			reason = strings.TrimSpace(reason)
			if reason == "" {
				return errors.New("watched-roots disable requires --reason")
			}
			store, _, _, err := opts.loadAll()
			if err != nil {
				return err
			}
			var releaseConfig func() error
			_, releaseConfig, err = lockWatchedRootConfig(cmd.Context(), store)
			if err != nil {
				return err
			}
			defer func() {
				if releaseConfig != nil {
					_ = releaseConfig()
				}
			}()
			rootKey := filesystemconnector.NormalizeRootKey(args[0])
			instance, rootConfig, err := loadWatchedRootInstance(store.DataDir, rootKey)
			if err != nil {
				return err
			}
			if rootConfig.RootKey != rootKey {
				return fmt.Errorf("watched-root identity mismatch: requested %q, worker contains %q", rootKey, rootConfig.RootKey)
			}
			changed := instance.Enabled
			if changed {
				instance.Enabled = false
				if err := noderuntime.NewStore(store.DataDir).SaveInstance(instance); err != nil {
					return err
				}
			}
			result := watchedRootDisableResult{
				RootKey:   rootKey,
				WorkerKey: instance.WorkerKey,
				Changed:   changed,
				Reason:    reason,
				Worker:    instance,
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), result))
			}
			_, err = fmt.Fprintf(opts.out, "root=%s worker=%s enabled=false changed=%t reason=%q\n", rootKey, instance.WorkerKey, changed, reason)
			return err
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm disabling this watched-root worker")
	cmd.Flags().StringVar(&reason, "reason", "", "Operator reason recorded in the command result")
	return cmd
}

func newWatchedRootsAddCommand(opts *rootOptions) *cobra.Command {
	var safeRootKey string
	var rawPath string
	var displayName string
	var includes []string
	var excludes []string
	var syncMode string
	var indexMode string
	var deleteMode string
	var projectRef string
	var scopeRef string
	var syncMaxFileBytes int64
	var indexMaxTextBytes int64
	var logicalNameStrategy string
	var backupMode string
	var backupMaxFileBytes int64
	var backupMaxBatchBytes int64
	var backupMaxPendingItems int
	var backupMaxPendingBytes int64
	var backupIncludeDeletions bool
	var backupOnLimit string
	cmd := &cobra.Command{
		Use:   "add <root-key>",
		Short: "Add or replace a local watched root",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(safeRootKey) == "" {
				return errors.New("--safe-root is required")
			}
			if strings.TrimSpace(rawPath) == "" {
				return errors.New("--path is required")
			}
			store, config, _, err := opts.loadAll()
			if err != nil {
				return err
			}
			var releaseConfig func() error
			config, releaseConfig, err = lockWatchedRootConfig(cmd.Context(), store)
			if err != nil {
				return err
			}
			defer func() {
				if releaseConfig != nil {
					_ = releaseConfig()
				}
			}()
			safeRoot, ok := filesystemconnector.FindSafeRoot(config.Filesystem, safeRootKey)
			if !ok {
				return fmt.Errorf("filesystem safe root %q is not configured", safeRootKey)
			}
			rootRelativePath, err := watchedroots.NormalizeRootPathInput(rawPath, safeRoot)
			if err != nil {
				return err
			}
			rootConfig := watchedroots.RootConfig{
				RootKey:          args[0],
				DisplayName:      displayName,
				SafeRootKey:      safeRootKey,
				RootRelativePath: rootRelativePath,
				Include:          includes,
				Exclude:          excludes,
				BackupPolicy: watchedroots.BackupPolicy{
					Mode:                   backupMode,
					MaxFileBytes:           backupMaxFileBytes,
					MaxBatchBytes:          backupMaxBatchBytes,
					MaxPendingItems:        backupMaxPendingItems,
					MaxPendingBytes:        backupMaxPendingBytes,
					IncludeDeletionMarkers: &backupIncludeDeletions,
					OnLimit:                backupOnLimit,
				},
				SyncPolicy: watchedroots.SyncPolicy{
					Mode:                syncMode,
					ProjectRef:          projectRef,
					ScopeRef:            scopeRef,
					MaxFileBytes:        syncMaxFileBytes,
					LogicalNameStrategy: logicalNameStrategy,
				},
				IndexPolicy: watchedroots.IndexPolicy{
					Mode:         indexMode,
					MaxTextBytes: indexMaxTextBytes,
				},
				DeletePolicy: watchedroots.DeletePolicy{
					Mode: deleteMode,
				},
			}
			validated, err := watchedroots.ValidateRootConfig(rootConfig, config.Filesystem)
			if err != nil {
				return err
			}
			result, err := applyWatchedRootValidated(store, config, validated, "")
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), result))
			}
			_, err = fmt.Fprintf(opts.out, "watched_root=%s worker=%s safe_root=%s path=%s\n",
				validated.Config.RootKey,
				result.Worker.WorkerKey,
				validated.Config.SafeRootKey,
				validated.Config.RootRelativePath,
			)
			return err
		},
	}
	cmd.Flags().StringVar(&safeRootKey, "safe-root", "", "Filesystem safe root key")
	cmd.Flags().StringVar(&rawPath, "path", ".", "Relative path inside the safe root, or an absolute path inside it")
	cmd.Flags().StringVar(&displayName, "display-name", "", "Human-readable watched-root name")
	cmd.Flags().StringArrayVar(&includes, "include", nil, "Include glob; repeat for multiple patterns")
	cmd.Flags().StringArrayVar(&excludes, "exclude", nil, "Exclude glob; repeat for multiple patterns")
	cmd.Flags().StringVar(&backupMode, "backup-mode", watchedroots.BackupModeNone, "Backup mode: none, metadata_only, private_raw, raw_snapshot, or incremental_raw")
	cmd.Flags().Int64Var(&backupMaxFileBytes, "backup-max-file-bytes", 0, "Maximum watched-root file size queued into local backup")
	cmd.Flags().Int64Var(&backupMaxBatchBytes, "backup-max-batch-bytes", 0, "Maximum local watched-root backup batch bytes")
	cmd.Flags().IntVar(&backupMaxPendingItems, "backup-max-pending-items", 0, "Maximum pending local watched-root backup outbox items")
	cmd.Flags().Int64Var(&backupMaxPendingBytes, "backup-max-pending-bytes", 0, "Maximum pending local watched-root backup bytes")
	cmd.Flags().BoolVar(&backupIncludeDeletions, "backup-include-deletions", true, "Queue watched-root backup deletion markers")
	cmd.Flags().StringVar(&backupOnLimit, "backup-on-limit", watchedroots.BackupOnLimitDegradeAndRequireManualAction, "Backup queue limit behavior")
	cmd.Flags().StringVar(&syncMode, "sync-mode", watchedroots.SyncModeNone, "Sync mode: none or selected_files")
	cmd.Flags().StringVar(&indexMode, "index-mode", watchedroots.IndexModeNone, "Index mode: none, metadata_only, or markdown_text")
	cmd.Flags().StringVar(&deleteMode, "delete-mode", watchedroots.DeleteModeLocalStateOnly, "Delete mode: local_state_only, tombstone, or ignore")
	cmd.Flags().StringVar(&projectRef, "project", "", "Project ref used by selected file sync")
	cmd.Flags().StringVar(&scopeRef, "scope", "", "Scope ref used by selected file sync")
	cmd.Flags().Int64Var(&syncMaxFileBytes, "sync-max-file-bytes", 0, "Maximum watched-root file size queued into local sync")
	cmd.Flags().Int64Var(&indexMaxTextBytes, "index-max-text-bytes", 0, "Maximum text bytes made available for indexing")
	cmd.Flags().StringVar(&logicalNameStrategy, "sync-name-strategy", watchedroots.SyncLogicalNameRelativePath, "Logical name strategy: relative_path or basename")
	return cmd
}

func applyWatchedRootValidated(store Store, config Config, validated watchedroots.ValidatedRoot, localRootKey string) (watchedRootAddResult, error) {
	runtimeStore := noderuntime.NewStore(store.DataDir)
	if err := runtimeStore.EnsureDefaultInstances(noderuntime.DefaultInstanceInput{
		HeartbeatIntervalSeconds: config.HeartbeatIntervalSeconds,
		PollIntervalSeconds:      config.PollIntervalSeconds,
	}); err != nil {
		return watchedRootAddResult{}, err
	}
	configJSON, err := json.Marshal(validated.Config)
	if err != nil {
		return watchedRootAddResult{}, err
	}
	workerKey := noderuntime.WatchedRootWorkerKey(validated.Config.RootKey)
	instance := noderuntime.WorkerInstance{
		WorkerKey:           workerKey,
		Kind:                noderuntime.KindWatchedRoot,
		DisplayName:         validated.Config.DisplayName,
		Enabled:             true,
		IntervalSeconds:     watchedRootWorkerIntervalSeconds(validated.Config),
		LeaseTimeoutSeconds: 120,
		LocalRootKey:        filesystemconnector.NormalizeRootKey(localRootKey),
		ConfigHash:          validated.ConfigHash,
		ConfigJSON:          configJSON,
		CreatedAt:           time.Now().UTC(),
	}
	if existing, err := runtimeStore.LoadInstance(workerKey); err == nil {
		instance.CreatedAt = existing.CreatedAt
		if instance.LocalRootKey == "" {
			instance.LocalRootKey = existing.LocalRootKey
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return watchedRootAddResult{}, err
	}
	if err := runtimeStore.SaveInstance(instance); err != nil {
		return watchedRootAddResult{}, err
	}
	watchedStore := watchedroots.NewStore(store.DataDir)
	if err := watchedStore.EnsureRoot(validated.Config.RootKey); err != nil {
		return watchedRootAddResult{}, err
	}
	return watchedRootAddResult{
		Worker:     instance,
		Config:     validated.Config,
		ConfigHash: validated.ConfigHash,
		Paths:      watchedStore.RootPaths(validated.Config.RootKey),
	}, nil
}

func watchedRootWorkerIntervalSeconds(config watchedroots.RootConfig) int {
	if strings.HasPrefix(config.RootKey, "loom_box__") {
		return 60
	}
	return watchedroots.DefaultWorkerIntervalSeconds()
}

// Caller holds this exact worker's execution lock after validating the entire
// adoption set. Preserve scheduling/creation fields of the checked instance.
func applyProjectWatchInstanceWhileLocked(store Store, instance noderuntime.WorkerInstance) error {
	if err := noderuntime.NewStore(store.DataDir).SaveInstanceWhileLocked(instance); err != nil {
		return err
	}
	var cfg watchedroots.RootConfig
	if err := json.Unmarshal(instance.ConfigJSON, &cfg); err != nil {
		return err
	}
	return watchedroots.NewStore(store.DataDir).EnsureRoot(cfg.RootKey)
}

type watchedRootApplyPlanResult struct {
	ProjectRoot string                        `json:"project_root,omitempty"`
	Applied     []watchedRootApplyPlanApplied `json:"applied"`
	Skipped     []watchedRootApplyPlanSkipped `json:"skipped,omitempty"`
	Runs        []watchedroots.ScanResult     `json:"runs,omitempty"`
}

type watchedRootApplyPlanApplied struct {
	RootKey          string `json:"root_key"`
	BackendRootKey   string `json:"backend_root_key"`
	WorkerKey        string `json:"worker_key"`
	SafeRootKey      string `json:"safe_root_key"`
	RootRelativePath string `json:"root_relative_path"`
	ConfigHash       string `json:"config_hash"`
}

type watchedRootApplyPlanSkipped struct {
	BackendRootKey string `json:"backend_root_key,omitempty"`
	OwnerNode      string `json:"owner_node,omitempty"`
	Reason         string `json:"reason"`
}

func newWatchedRootsApplyPlanCommand(opts *rootOptions) *cobra.Command {
	var projectRoot string
	var rootFilters []string
	var skipSafeRoot bool
	var runOnce bool
	var flush bool
	cmd := &cobra.Command{
		Use:   "apply-plan <plan-file>",
		Short: "Apply project watched-root desired state from a watch-plan JSON file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, config, state, err := opts.loadAll()
			if err != nil {
				return err
			}
			var releaseConfig func() error
			config, releaseConfig, err = lockWatchedRootConfig(cmd.Context(), store)
			if err != nil {
				return err
			}
			defer func() {
				if releaseConfig != nil {
					_ = releaseConfig()
				}
			}()
			raw, err := readWatchedRootApplyPlanFile(args[0])
			if err != nil {
				return err
			}
			planRoot, items, err := decodeWatchedRootApplyPlan(raw)
			if err != nil {
				return err
			}
			if strings.TrimSpace(projectRoot) == "" {
				projectRoot = planRoot
			}
			filterSet := watchedRootApplyPlanFilterSet(rootFilters)
			result := watchedRootApplyPlanResult{ProjectRoot: projectRoot}
			appliedValidated := []watchedroots.ValidatedRoot{}
			for _, item := range items {
				if !watchedRootApplyPlanTargetsNode(item, config.NodeKey) {
					result.Skipped = append(result.Skipped, watchedRootApplyPlanSkipped{
						BackendRootKey: item.BackendRootKey,
						OwnerNode:      item.OwnerNode,
						Reason:         "different_owner_node",
					})
					continue
				}
				if len(filterSet) > 0 && !watchedRootApplyPlanMatchesFilter(item, filterSet) {
					result.Skipped = append(result.Skipped, watchedRootApplyPlanSkipped{
						BackendRootKey: item.BackendRootKey,
						OwnerNode:      item.OwnerNode,
						Reason:         "filtered",
					})
					continue
				}
				rootConfig, err := watchedRootConfigFromProjectPlanItem(item)
				if err != nil {
					return err
				}
				updatedConfig, err := ensureWatchedRootPlanSafeRoot(store, config, rootConfig, item, projectRoot, skipSafeRoot)
				if err != nil {
					return err
				}
				config = updatedConfig
				validated, err := watchedroots.ValidateRootConfig(rootConfig, config.Filesystem)
				if err != nil {
					return err
				}
				applied, err := applyWatchedRootValidated(store, config, validated, item.Key)
				if err != nil {
					return err
				}
				result.Applied = append(result.Applied, watchedRootApplyPlanApplied{
					RootKey:          applied.Config.RootKey,
					BackendRootKey:   firstNonEmptyWatchedRootString(item.BackendRootKey, applied.Config.RootKey),
					WorkerKey:        applied.Worker.WorkerKey,
					SafeRootKey:      applied.Config.SafeRootKey,
					RootRelativePath: applied.Config.RootRelativePath,
					ConfigHash:       applied.ConfigHash,
				})
				appliedValidated = append(appliedValidated, validated)
			}
			if err = releaseConfig(); err != nil {
				return err
			}
			releaseConfig = nil

			if runOnce {
				for _, validated := range appliedValidated {
					runtimeStore := noderuntime.NewStore(store.DataDir)
					release, err := runtimeStore.AcquireWorkerExecutionLock(cmd.Context(), noderuntime.WatchedRootWorkerKey(validated.Config.RootKey))
					if err != nil {
						return err
					}
					instance, rootConfig, err := loadWatchedRootInstance(store.DataDir, validated.Config.RootKey)
					if err != nil {
						_ = release()
						return err
					}
					fenced, err := runtimeStore.ProjectArchiveFenceActive(projectquiescence.TargetKindWatchedRoot, instance.WorkerKey)
					if err != nil || fenced || !instance.Enabled {
						_ = release()
						if err != nil {
							return err
						}
						return fmt.Errorf("watched root %q is disabled or project-archive fenced", rootConfig.RootKey)
					}
					runRoot, err := watchedroots.ValidateRootConfigForRun(rootConfig, config.Filesystem)
					if err != nil {
						_ = release()
						return err
					}
					runResult, err := runWatchedRootReconcileAndPlan(cmd.Context(), store, config, state, instance, runRoot, watchedroots.ScanModeFull, true, flush, true, correlation.Normalize(opts.correlationID))
					releaseErr := release()
					if err != nil {
						return err
					}
					if releaseErr != nil {
						return releaseErr
					}
					result.Runs = append(result.Runs, runResult)
				}
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), result))
			}
			if len(result.Applied) == 0 {
				if _, err := fmt.Fprintln(opts.out, "no watched roots applied"); err != nil {
					return err
				}
			}
			for _, applied := range result.Applied {
				if _, err := fmt.Fprintf(opts.out, "applied root=%s worker=%s safe_root=%s path=%s\n",
					applied.BackendRootKey,
					applied.WorkerKey,
					applied.SafeRootKey,
					applied.RootRelativePath,
				); err != nil {
					return err
				}
			}
			for _, skipped := range result.Skipped {
				if _, err := fmt.Fprintf(opts.out, "skipped root=%s owner=%s reason=%s\n", skipped.BackendRootKey, skipped.OwnerNode, skipped.Reason); err != nil {
					return err
				}
			}
			for _, run := range result.Runs {
				if _, err := fmt.Fprintf(opts.out, "ran root=%s status=%s included=%d changed=%d deleted=%d\n",
					run.RootKey,
					run.Status,
					run.Counts.Included,
					run.Counts.Changed,
					run.Counts.Deleted,
				); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectRoot, "project-root", "", "Local project root used to add or update safe_root=project")
	cmd.Flags().StringArrayVar(&rootFilters, "root", nil, "Apply only this backend/local root key; repeat for multiple roots")
	cmd.Flags().BoolVar(&skipSafeRoot, "skip-safe-root", false, "Fail if a safe root is missing instead of creating safe_root=project from --project-root")
	cmd.Flags().BoolVar(&runOnce, "run-once", false, "Run applied roots once after configuration")
	cmd.Flags().BoolVar(&flush, "flush", false, "Flush sync/backup outputs when --run-once is set")
	return cmd
}

func readWatchedRootApplyPlanFile(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "-" {
		return io.ReadAll(os.Stdin)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read watched-root plan: %w", err)
	}
	return raw, nil
}

func decodeWatchedRootApplyPlan(raw []byte) (string, []projectcontracts.ProjectWatchedRootItem, error) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return "", nil, errors.New("watched-root plan is empty")
	}
	var direct []projectcontracts.ProjectWatchedRootItem
	if err := json.Unmarshal(raw, &direct); err == nil && len(direct) > 0 {
		return "", direct, nil
	}
	var directRegistrations []projects.ProjectWatchedRootRegistration
	if err := json.Unmarshal(raw, &directRegistrations); err == nil && len(directRegistrations) > 0 {
		return "", watchedRootItemsFromRegistrations(directRegistrations), nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Data) > 0 {
		root, items, err := decodeWatchedRootApplyPlan(envelope.Data)
		if err == nil {
			return root, items, nil
		}
	}
	var object struct {
		ProjectRoot  string                                    `json:"project_root"`
		WatchedRoots []projectcontracts.ProjectWatchedRootItem `json:"watched_roots"`
	}
	if err := json.Unmarshal(raw, &object); err == nil && len(object.WatchedRoots) > 0 {
		return object.ProjectRoot, object.WatchedRoots, nil
	}
	var registrationObject struct {
		ProjectRoot  string                                    `json:"project_root"`
		WatchedRoots []projects.ProjectWatchedRootRegistration `json:"watched_roots"`
	}
	if err := json.Unmarshal(raw, &registrationObject); err == nil && len(registrationObject.WatchedRoots) > 0 {
		return registrationObject.ProjectRoot, watchedRootItemsFromRegistrations(registrationObject.WatchedRoots), nil
	}
	var single projectcontracts.ProjectWatchedRootItem
	if err := json.Unmarshal(raw, &single); err == nil && len(single.ConfigJSON) > 0 {
		return "", []projectcontracts.ProjectWatchedRootItem{single}, nil
	}
	var registration projects.ProjectWatchedRootRegistration
	if err := json.Unmarshal(raw, &registration); err == nil && len(registration.ConfigJSON) > 0 {
		return "", watchedRootItemsFromRegistrations([]projects.ProjectWatchedRootRegistration{registration}), nil
	}
	return "", nil, errors.New("watched-root plan must contain watched_roots or a watched-root item array")
}

func watchedRootItemsFromRegistrations(registrations []projects.ProjectWatchedRootRegistration) []projectcontracts.ProjectWatchedRootItem {
	items := make([]projectcontracts.ProjectWatchedRootItem, 0, len(registrations))
	for _, registration := range registrations {
		var sourceKinds []string
		_ = json.Unmarshal(registration.SourceKinds, &sourceKinds)
		var commands []projectcontracts.ProjectWatchedRootCommand
		_ = json.Unmarshal(registration.CommandJSON, &commands)
		var metadata map[string]any
		_ = json.Unmarshal(registration.Metadata, &metadata)
		items = append(items, projectcontracts.ProjectWatchedRootItem{
			Key:              registration.LocalRootKey,
			BackendRootKey:   registration.BackendRootKey,
			WorkerKey:        registration.WorkerKey,
			SourceKinds:      sourceKinds,
			OwnerNode:        registration.OwnerNodeKey,
			SafeRootKey:      registration.SafeRootKey,
			RootRelativePath: registration.RootRelativePath,
			DisplayName:      registration.DisplayName,
			SyncMode:         registration.SyncMode,
			BackupMode:       registration.BackupMode,
			IndexMode:        registration.IndexMode,
			DeleteMode:       registration.DeleteMode,
			ConfigHash:       registration.ConfigHash,
			ConfigJSON:       registration.ConfigJSON,
			AgentCommands:    commands,
			ActivationStatus: registration.ActivationStatus,
			Metadata:         metadata,
		})
	}
	return items
}

func watchedRootConfigFromProjectPlanItem(item projectcontracts.ProjectWatchedRootItem) (watchedroots.RootConfig, error) {
	if len(item.ConfigJSON) == 0 {
		return watchedroots.RootConfig{}, fmt.Errorf("watched-root %q has no config_json", item.BackendRootKey)
	}
	var rootConfig watchedroots.RootConfig
	if err := json.Unmarshal(item.ConfigJSON, &rootConfig); err != nil {
		return watchedroots.RootConfig{}, fmt.Errorf("decode watched-root %q config_json: %w", item.BackendRootKey, err)
	}
	rootConfig = watchedroots.NormalizeRootConfig(rootConfig)
	if rootConfig.RootKey == "" && item.BackendRootKey != "" {
		rootConfig.RootKey = item.BackendRootKey
	}
	return rootConfig, nil
}

func ensureWatchedRootPlanSafeRoot(store Store, config Config, rootConfig watchedroots.RootConfig, item projectcontracts.ProjectWatchedRootItem, projectRoot string, skipSafeRoot bool) (Config, error) {
	safeRootKey := filesystemconnector.NormalizeRootKey(rootConfig.SafeRootKey)
	if safeRootKey == "" {
		return Config{}, errors.New("watched-root config is missing safe_root_key")
	}
	existingSafeRoot, exists := filesystemconnector.FindSafeRoot(config.Filesystem, safeRootKey)
	backupSafeRoot, isBackupSafeRoot, err := watchedRootBackupContractSafeRoot(rootConfig, item)
	if err != nil {
		return Config{}, err
	}
	if isBackupSafeRoot {
		if skipSafeRoot && !exists {
			return Config{}, fmt.Errorf("filesystem safe root %q is not configured", safeRootKey)
		}
		if exists {
			if filepath.Clean(existingSafeRoot.AbsolutePath) != filepath.Clean(backupSafeRoot.AbsolutePath) {
				return Config{}, fmt.Errorf("filesystem safe root %q already points at %s, not backup contract target %s", safeRootKey, existingSafeRoot.AbsolutePath, backupSafeRoot.AbsolutePath)
			}
			if !existingSafeRoot.PrivateBackupOnly {
				return Config{}, fmt.Errorf("filesystem safe root %q already exists and is not private backup-only", safeRootKey)
			}
			if backupSafeRoot.MaxFileBytes > existingSafeRoot.MaxFileBytes {
				existingSafeRoot.MaxFileBytes = backupSafeRoot.MaxFileBytes
			}
			existingSafeRoot.DisplayName = backupSafeRoot.DisplayName
			existingSafeRoot.AllowList = true
			existingSafeRoot.AllowMetadata = true
			existingSafeRoot.AllowIngest = true
			existingSafeRoot.PrivateBackupOnly = true
			existingSafeRoot.Metadata = backupSafeRoot.Metadata
			existingSafeRoot = filesystemconnector.NormalizeSafeRoot(existingSafeRoot)
			if err := filesystemconnector.ValidateSafeRoot(existingSafeRoot); err != nil {
				return Config{}, err
			}
			config.Filesystem = filesystemconnector.UpsertSafeRoot(config.Filesystem, existingSafeRoot)
			if err := store.SaveConfig(config); err != nil {
				return Config{}, err
			}
			return config, nil
		}
		config.Filesystem = filesystemconnector.UpsertSafeRoot(config.Filesystem, backupSafeRoot)
		if err := store.SaveConfig(config); err != nil {
			return Config{}, err
		}
		return config, nil
	}
	if skipSafeRoot && !exists {
		return Config{}, fmt.Errorf("filesystem safe root %q is not configured", safeRootKey)
	}
	if safeRootKey != "project" {
		if !exists {
			return Config{}, fmt.Errorf("filesystem safe root %q is not configured", safeRootKey)
		}
		if safeRootKey == "loom_box" && strings.TrimSpace(projectRoot) != "" {
			absolutePath, err := canonicalDirectoryPath(projectRoot)
			if err != nil {
				return Config{}, err
			}
			existingSafeRoot.AbsolutePath = absolutePath
			existingSafeRoot.Metadata, _ = json.Marshal(map[string]any{
				"source":        "box.watch_plan.apply",
				"box_root_path": absolutePath,
			})
		}
		requiredMax := watchedRootRequiredSafeRootMaxFileBytes(rootConfig)
		if requiredMax > existingSafeRoot.MaxFileBytes {
			existingSafeRoot.MaxFileBytes = requiredMax
		}
		existingSafeRoot = filesystemconnector.NormalizeSafeRoot(existingSafeRoot)
		if err := filesystemconnector.ValidateSafeRoot(existingSafeRoot); err != nil {
			return Config{}, err
		}
		config.Filesystem = filesystemconnector.UpsertSafeRoot(config.Filesystem, existingSafeRoot)
		if err := store.SaveConfig(config); err != nil {
			return Config{}, err
		}
		return config, nil
	}
	if strings.TrimSpace(projectRoot) == "" {
		if exists {
			requiredMax := watchedRootRequiredSafeRootMaxFileBytes(rootConfig)
			if requiredMax > existingSafeRoot.MaxFileBytes {
				existingSafeRoot.MaxFileBytes = requiredMax
				existingSafeRoot = filesystemconnector.NormalizeSafeRoot(existingSafeRoot)
				if err := filesystemconnector.ValidateSafeRoot(existingSafeRoot); err != nil {
					return Config{}, err
				}
				config.Filesystem = filesystemconnector.UpsertSafeRoot(config.Filesystem, existingSafeRoot)
				if err := store.SaveConfig(config); err != nil {
					return Config{}, err
				}
			}
			return config, nil
		}
		return Config{}, errors.New("safe_root=project is missing; pass --project-root to configure it")
	}
	absolutePath, err := canonicalDirectoryPath(projectRoot)
	if err != nil {
		return Config{}, err
	}
	metadata, _ := json.Marshal(map[string]any{"source": "project.watch_plan.apply", "project_root": absolutePath})
	maxFileBytes := watchedRootRequiredSafeRootMaxFileBytes(rootConfig)
	if exists && existingSafeRoot.MaxFileBytes > maxFileBytes {
		maxFileBytes = existingSafeRoot.MaxFileBytes
	}
	safeRoot := filesystemconnector.SafeRoot{
		RootKey:       safeRootKey,
		DisplayName:   "project",
		AbsolutePath:  absolutePath,
		AllowList:     true,
		AllowMetadata: true,
		AllowIngest:   true,
		MaxFileBytes:  maxFileBytes,
		Metadata:      metadata,
	}
	safeRoot = filesystemconnector.NormalizeSafeRoot(safeRoot)
	if err := filesystemconnector.ValidateSafeRoot(safeRoot); err != nil {
		return Config{}, err
	}
	config.Filesystem = filesystemconnector.UpsertSafeRoot(config.Filesystem, safeRoot)
	if err := store.SaveConfig(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func watchedRootBackupContractSafeRoot(rootConfig watchedroots.RootConfig, item projectcontracts.ProjectWatchedRootItem) (filesystemconnector.SafeRoot, bool, error) {
	if !backupcontracts.IsBackupRootItem(item) {
		return filesystemconnector.SafeRoot{}, false, nil
	}
	if rootConfig.SyncPolicy.Mode != watchedroots.SyncModeNone {
		return filesystemconnector.SafeRoot{}, false, errors.New("backup contract watched roots cannot enable sync")
	}
	if rootConfig.IndexPolicy.Mode != watchedroots.IndexModeNone {
		return filesystemconnector.SafeRoot{}, false, errors.New("backup contract watched roots cannot enable indexing")
	}
	targetScope, _ := item.Metadata["target_scope"].(string)
	if targetScope != backupcontracts.TargetScopeOwnerNodeAbsolute {
		return filesystemconnector.SafeRoot{}, false, nil
	}
	targetPath, _ := item.Metadata["safe_root_absolute_path"].(string)
	if strings.TrimSpace(targetPath) == "" {
		targetPath, _ = item.Metadata["target_path"].(string)
	}
	absolutePath, err := canonicalDirectoryPath(targetPath)
	if err != nil {
		return filesystemconnector.SafeRoot{}, true, err
	}
	metadata, _ := json.Marshal(map[string]any{
		"source":           backupcontracts.MetadataSource,
		"contract_key":     item.Metadata["contract_key"],
		"box_id":           item.Metadata["box_id"],
		"box_root_path":    item.Metadata["box_root_path"],
		"contract_path":    item.Metadata["contract_path"],
		"target_scope":     targetScope,
		"target_path":      absolutePath,
		"backend_root_key": item.BackendRootKey,
	})
	maxFileBytes := watchedRootRequiredSafeRootMaxFileBytes(rootConfig)
	safeRoot := filesystemconnector.SafeRoot{
		RootKey:           rootConfig.SafeRootKey,
		DisplayName:       rootConfig.DisplayName,
		AbsolutePath:      absolutePath,
		AllowList:         true,
		AllowMetadata:     true,
		AllowIngest:       true,
		MaxFileBytes:      maxFileBytes,
		PrivateBackupOnly: true,
		Metadata:          metadata,
	}
	safeRoot = filesystemconnector.NormalizeSafeRoot(safeRoot)
	if err := filesystemconnector.ValidateSafeRoot(safeRoot); err != nil {
		return filesystemconnector.SafeRoot{}, true, err
	}
	return safeRoot, true, nil
}

func watchedRootRequiredSafeRootMaxFileBytes(rootConfig watchedroots.RootConfig) int64 {
	required := filesystemconnector.DefaultMaxFileBytes
	for _, value := range []int64{
		rootConfig.Scan.MaxHashFileBytes,
		rootConfig.BackupPolicy.MaxFileBytes,
		rootConfig.BackupPolicy.MaxBatchBytes,
		rootConfig.SyncPolicy.MaxFileBytes,
	} {
		if value > required {
			required = value
		}
	}
	return required
}

func watchedRootApplyPlanTargetsNode(item projectcontracts.ProjectWatchedRootItem, nodeKey string) bool {
	owner := strings.TrimSpace(item.OwnerNode)
	return owner == "" || owner == strings.TrimSpace(nodeKey)
}

func watchedRootApplyPlanFilterSet(filters []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, filter := range filters {
		filter = filesystemconnector.NormalizeRootKey(filter)
		if filter != "" {
			out[filter] = struct{}{}
		}
	}
	return out
}

func watchedRootApplyPlanMatchesFilter(item projectcontracts.ProjectWatchedRootItem, filters map[string]struct{}) bool {
	for _, key := range []string{item.BackendRootKey, item.Key} {
		key = filesystemconnector.NormalizeRootKey(key)
		if _, ok := filters[key]; ok {
			return true
		}
	}
	return false
}

func firstNonEmptyWatchedRootString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func newWatchedRootsListCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List local watched roots",
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := watchedRootsStatus(opts, "")
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), result))
			}
			if len(result.Roots) == 0 {
				_, err = fmt.Fprintln(opts.out, "no watched roots configured")
				return err
			}
			for _, root := range result.Roots {
				if _, err := fmt.Fprintf(opts.out, "root=%s worker=%s status=%s safe_root=%s path=%s\n",
					root.RootKey,
					root.WorkerKey,
					root.Status,
					root.Config.SafeRootKey,
					root.Config.RootRelativePath,
				); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newWatchedRootsStatusCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status [root-key]",
		Short: "Print local watched-root status",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rootKey := ""
			if len(args) == 1 {
				rootKey = args[0]
			}
			result, err := watchedRootsStatus(opts, rootKey)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), result))
			}
			if len(result.Roots) == 0 {
				_, err = fmt.Fprintln(opts.out, "no watched roots configured")
				return err
			}
			for _, root := range result.Roots {
				summary := "unscanned"
				if root.Summary != nil {
					summary = root.Summary.Status
				}
				if _, err := fmt.Fprintf(opts.out, "root=%s worker=%s status=%s summary=%s\n",
					root.RootKey,
					root.WorkerKey,
					root.Status,
					summary,
				); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newWatchedRootsRunCommand(opts *rootOptions) *cobra.Command {
	var once bool
	var mode string
	var stabilityWindow string
	var noPlanOutputs bool
	var flush bool
	cmd := &cobra.Command{
		Use:   "run <root-key>",
		Short: "Run watched-root local reconciliation once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !once {
				return errors.New("watched-roots run currently requires --once")
			}
			store, config, state, err := opts.loadAll()
			if err != nil {
				return err
			}
			rootKey := filesystemconnector.NormalizeRootKey(args[0])
			runtimeStore := noderuntime.NewStore(store.DataDir)
			release, err := runtimeStore.AcquireWorkerExecutionLock(cmd.Context(), noderuntime.WatchedRootWorkerKey(rootKey))
			if err != nil {
				return err
			}
			defer func() { _ = release() }()
			instance, rootConfig, err := loadWatchedRootInstance(store.DataDir, rootKey)
			if err != nil {
				return err
			}
			fenced, err := runtimeStore.ProjectArchiveFenceActive(projectquiescence.TargetKindWatchedRoot, instance.WorkerKey)
			if err != nil {
				return err
			}
			if !instance.Enabled || fenced {
				return fmt.Errorf("watched root %q is disabled", rootConfig.RootKey)
			}
			if strings.TrimSpace(stabilityWindow) != "" {
				if _, err := time.ParseDuration(stabilityWindow); err != nil {
					return fmt.Errorf("invalid --stability-window: %w", err)
				}
				rootConfig.Scan.StabilityWindow = stabilityWindow
			}
			validated, err := watchedroots.ValidateRootConfigForRun(rootConfig, config.Filesystem)
			if err != nil {
				return err
			}
			result, err := runWatchedRootReconcileAndPlan(cmd.Context(), store, config, state, instance, validated, mode, !noPlanOutputs, flush, true, correlation.Normalize(opts.correlationID))
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), result))
			}
			_, err = fmt.Fprintf(opts.out, "root=%s status=%s included=%d excluded=%d changed=%d deleted=%d\n",
				result.RootKey,
				result.Status,
				result.Counts.Included,
				result.Counts.Excluded,
				result.Counts.Changed,
				result.Counts.Deleted,
			)
			return err
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "Run one reconciliation and exit")
	cmd.Flags().StringVar(&mode, "mode", watchedroots.ScanModeAuto, "Scan mode: auto, full, or dirty")
	cmd.Flags().StringVar(&stabilityWindow, "stability-window", "", "Override file stability window, for example 0s")
	cmd.Flags().BoolVar(&noPlanOutputs, "no-plan-outputs", false, "Skip watched-root sync/index/delete output planning")
	cmd.Flags().BoolVar(&flush, "flush", false, "Push queued watched-root sync/deletion/backup output to main after planning")
	return cmd
}

func newWatchedRootsBackupsCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backups",
		Short: "Inspect local watched-root backup queue state",
	}
	cmd.AddCommand(newWatchedRootsBackupsStatusCommand(opts))
	cmd.AddCommand(newWatchedRootsBackupsOutboxCommand(opts))
	cmd.AddCommand(newWatchedRootsBackupsArtifactsCommand(opts))
	cmd.AddCommand(newWatchedRootsBackupsFailuresCommand(opts))
	return cmd
}

func newWatchedRootsBackupsStatusCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status [root-key]",
		Short: "Print local watched-root backup status",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rootKey := ""
			if len(args) == 1 {
				rootKey = args[0]
			}
			store, config, state, err := opts.loadAll()
			if err != nil {
				return err
			}
			status, err := store.LocalWatchedRootBackupStatus(config, state, filesystemconnector.NormalizeRootKey(rootKey))
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status))
			}
			if _, err := fmt.Fprintf(opts.out, "root=%s artifacts=%d batches=%d items=%d pending=%d retryable=%d accepted=%d failed=%d manual_action=%d pending_bytes=%d\n",
				status.RootKey,
				status.Counts.Artifacts,
				status.Counts.Batches,
				status.Counts.Items,
				status.Counts.Pending,
				status.Counts.Retryable,
				status.Counts.Accepted,
				status.Counts.Failed,
				status.Counts.ManualAction,
				status.Counts.PendingBytes,
			); err != nil {
				return err
			}
			for _, failure := range status.Failed {
				if _, err := fmt.Fprintf(opts.out, "failure root=%s path=%s status=%s retryable=%t manual_action=%t code=%s message=%s\n",
					failure.RootKey,
					emptyPlaceholder(failure.RelativePath),
					failure.Status,
					failure.Retryable,
					failure.ManualAction,
					emptyPlaceholder(failure.ErrorCode),
					emptyPlaceholder(failure.ErrorMessage),
				); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newWatchedRootsBackupsFailuresCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "failures [root-key]",
		Short: "Print local watched-root backup failures",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rootKey := ""
			if len(args) == 1 {
				rootKey = args[0]
			}
			store, config, state, err := opts.loadAll()
			if err != nil {
				return err
			}
			status, err := store.LocalWatchedRootBackupStatus(config, state, filesystemconnector.NormalizeRootKey(rootKey))
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status.Failed))
			}
			if len(status.Failed) == 0 {
				_, err = fmt.Fprintln(opts.out, "no watched-root backup failures")
				return err
			}
			for _, failure := range status.Failed {
				if _, err := fmt.Fprintf(opts.out, "root=%s path=%s status=%s retryable=%t manual_action=%t code=%s message=%s\n",
					failure.RootKey,
					emptyPlaceholder(failure.RelativePath),
					failure.Status,
					failure.Retryable,
					failure.ManualAction,
					emptyPlaceholder(failure.ErrorCode),
					emptyPlaceholder(failure.ErrorMessage),
				); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newWatchedRootsBackupsOutboxCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "outbox [root-key]",
		Short: "Print local watched-root backup outbox items",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rootKey := ""
			if len(args) == 1 {
				rootKey = filesystemconnector.NormalizeRootKey(args[0])
			}
			store, _, _, err := opts.loadAll()
			if err != nil {
				return err
			}
			items, err := store.LoadWatchedRootBackupOutbox()
			if err != nil {
				return err
			}
			filtered := []LocalWatchedRootBackupOutboxItem{}
			for _, item := range items {
				if rootKey != "" && item.RootKey != rootKey {
					continue
				}
				filtered = append(filtered, item)
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), filtered))
			}
			for _, item := range filtered {
				if _, err := fmt.Fprintf(opts.out, "outbox=%s root=%s ref=%s kind=%s status=%s sequence=%d\n",
					item.LocalOutboxID,
					item.RootKey,
					item.LocalRef,
					item.ItemKind,
					item.Status,
					item.LocalSequence,
				); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newWatchedRootsBackupsArtifactsCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "artifacts [root-key]",
		Short: "Print local watched-root backup artifact records",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rootKey := ""
			if len(args) == 1 {
				rootKey = filesystemconnector.NormalizeRootKey(args[0])
			}
			store, _, _, err := opts.loadAll()
			if err != nil {
				return err
			}
			artifacts, err := store.LoadWatchedRootBackupArtifacts()
			if err != nil {
				return err
			}
			filtered := []LocalWatchedRootBackupArtifact{}
			for _, artifact := range artifacts {
				if rootKey != "" && artifact.RootKey != rootKey {
					continue
				}
				filtered = append(filtered, artifact)
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), filtered))
			}
			for _, artifact := range filtered {
				if _, err := fmt.Fprintf(opts.out, "artifact=%s root=%s path=%s status=%s hash=%s bytes=%d private_backup=%s\n",
					artifact.LocalArtifactID,
					artifact.RootKey,
					artifact.RelativePath,
					artifact.Status,
					artifact.ContentHashURI,
					artifact.SizeBytes,
					emptyPlaceholder(artifact.PrivateBackupOperationID),
				); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newWatchedRootsExplainCommand(opts *rootOptions) *cobra.Command {
	var rawPath string
	cmd := &cobra.Command{
		Use:   "explain <root-key>",
		Short: "Explain watched-root policy for one path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(rawPath) == "" {
				return errors.New("--path is required")
			}
			store, config, _, err := opts.loadAll()
			if err != nil {
				return err
			}
			_, rootConfig, err := loadWatchedRootInstance(store.DataDir, args[0])
			if err != nil {
				return err
			}
			validated, err := watchedroots.ValidateRootConfigForRun(rootConfig, config.Filesystem)
			if err != nil {
				return err
			}
			result, err := watchedroots.ExplainPath(watchedroots.NewStore(store.DataDir), validated, rawPath)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), result))
			}
			_, err = fmt.Fprintf(opts.out, "root=%s path=%s included=%t reason=%s\n",
				result.RootKey,
				result.Path,
				result.Classification.Included,
				result.Classification.ReasonCode,
			)
			return err
		},
	}
	cmd.Flags().StringVar(&rawPath, "path", "", "Relative path inside the watched root")
	return cmd
}

func watchedRootsStatus(opts *rootOptions, rootKey string) (watchedRootStatusResult, error) {
	store, config, state, err := opts.loadAll()
	if err != nil {
		return watchedRootStatusResult{}, err
	}
	runtimeStore := noderuntime.NewStore(store.DataDir)
	if err := runtimeStore.EnsureDefaultInstances(noderuntime.DefaultInstanceInput{
		HeartbeatIntervalSeconds: config.HeartbeatIntervalSeconds,
		PollIntervalSeconds:      config.PollIntervalSeconds,
	}); err != nil {
		return watchedRootStatusResult{}, err
	}
	instances, err := runtimeStore.LoadInstances()
	if err != nil {
		return watchedRootStatusResult{}, err
	}
	rootKey = filesystemconnector.NormalizeRootKey(rootKey)
	roots := []watchedRootStatus{}
	for _, instance := range instances {
		if instance.Kind != noderuntime.KindWatchedRoot {
			continue
		}
		rootConfig, err := watchedRootConfigFromInstance(instance)
		status := watchedRootStatus{
			WorkerKey: instance.WorkerKey,
			Status:    "configured",
			Worker:    instance,
		}
		if err != nil {
			status.Status = "invalid_config"
			status.Error = err.Error()
			roots = append(roots, status)
			continue
		}
		if rootKey != "" && rootConfig.RootKey != rootKey {
			continue
		}
		status.RootKey = rootConfig.RootKey
		status.Config = rootConfig
		status.ConfigHash = instance.ConfigHash
		watchedStore := watchedroots.NewStore(store.DataDir)
		status.Paths = watchedStore.RootPaths(rootConfig.RootKey)
		if _, err := watchedroots.ValidateRootConfigForRun(rootConfig, config.Filesystem); err != nil {
			status.Status = "invalid_config"
			status.Error = err.Error()
			roots = append(roots, status)
			continue
		}
		if summary, err := watchedStore.LoadLatestSummary(rootConfig.RootKey); err == nil {
			status.Summary = &summary
			status.Status = summary.Status
		} else if watchedroots.IsNotExist(err) {
			status.Status = "unscanned"
		} else {
			status.Status = "status_error"
			status.Error = err.Error()
		}
		if health, err := runtimeStore.LoadHealth(instance.WorkerKey); err == nil {
			status.Health = &health
		} else if !errors.Is(err, fs.ErrNotExist) {
			return watchedRootStatusResult{}, err
		}
		if checkpoint, err := runtimeStore.LoadCheckpoint(instance.WorkerKey); err == nil {
			status.Checkpoint = &checkpoint
		} else if !errors.Is(err, fs.ErrNotExist) {
			return watchedRootStatusResult{}, err
		}
		if rootCheckpoint, err := watchedStore.LoadCheckpoint(rootConfig.RootKey); err == nil {
			status.RootCheckpoint = &rootCheckpoint
		} else if !watchedroots.IsNotExist(err) {
			return watchedRootStatusResult{}, err
		}
		if hints, err := watchedStore.ListDirtyHints(rootConfig.RootKey); err == nil {
			status.DirtyHints = len(hints)
		} else {
			return watchedRootStatusResult{}, err
		}
		if findings, err := watchedStore.ListFindings(rootConfig.RootKey, 0); err == nil {
			for _, finding := range findings {
				if watchedroots.FindingIsActive(finding) {
					status.Findings++
				}
			}
		} else {
			return watchedRootStatusResult{}, err
		}
		if backupStatus, err := store.LocalWatchedRootBackupStatus(config, state, rootConfig.RootKey); err == nil {
			status.BackupStatus = &backupStatus
		} else {
			return watchedRootStatusResult{}, err
		}
		if states, err := watchedStore.ListPathStates(rootConfig.RootKey, 0); err == nil {
			status.PathCounts = map[string]int{}
			for _, state := range states {
				status.PathCounts[state.Status]++
			}
		} else {
			return watchedRootStatusResult{}, err
		}
		if !instance.Enabled {
			status.Status = "disabled"
		}
		roots = append(roots, status)
	}
	if rootKey != "" && len(roots) == 0 {
		return watchedRootStatusResult{}, fmt.Errorf("watched root %q is not configured", rootKey)
	}
	return watchedRootStatusResult{Roots: roots}, nil
}

func loadWatchedRootInstance(dataDir, rootKey string) (noderuntime.WorkerInstance, watchedroots.RootConfig, error) {
	runtimeStore := noderuntime.NewStore(dataDir)
	instance, err := runtimeStore.LoadInstance(noderuntime.WatchedRootWorkerKey(filesystemconnector.NormalizeRootKey(rootKey)))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return noderuntime.WorkerInstance{}, watchedroots.RootConfig{}, fmt.Errorf("watched root %q is not configured", rootKey)
		}
		return noderuntime.WorkerInstance{}, watchedroots.RootConfig{}, err
	}
	rootConfig, err := watchedRootConfigFromInstance(instance)
	if err != nil {
		return noderuntime.WorkerInstance{}, watchedroots.RootConfig{}, err
	}
	return instance, rootConfig, nil
}

func watchedRootConfigFromInstance(instance noderuntime.WorkerInstance) (watchedroots.RootConfig, error) {
	var rootConfig watchedroots.RootConfig
	if len(instance.ConfigJSON) == 0 {
		return rootConfig, errors.New("watched-root worker has empty config")
	}
	if err := json.Unmarshal(instance.ConfigJSON, &rootConfig); err != nil {
		return rootConfig, err
	}
	return watchedroots.NormalizeRootConfig(rootConfig), nil
}
