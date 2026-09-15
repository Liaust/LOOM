package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/correlation"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/version"
)

type rootOptions struct {
	configPath    string
	statePath     string
	dataDir       string
	jsonOutput    bool
	correlationID string
	out           io.Writer
	errOut        io.Writer
}

func NewRootCommand() *cobra.Command {
	opts := rootOptions{
		out:    os.Stdout,
		errOut: os.Stderr,
	}
	cmd := &cobra.Command{
		Use:           "loom-node-agent",
		Short:         "Run a LOOM node agent on a non-main node",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.PersistentFlags().StringVar(&opts.configPath, "config", "", "Config file path")
	cmd.PersistentFlags().StringVar(&opts.statePath, "state", "", "State file path")
	cmd.PersistentFlags().StringVar(&opts.dataDir, "data-dir", "", "Node-agent data directory")
	cmd.PersistentFlags().BoolVar(&opts.jsonOutput, "json", false, "Render machine-readable JSON")
	cmd.PersistentFlags().StringVar(&opts.correlationID, "correlation-id", "", "Correlation ID for remote requests")

	cmd.AddCommand(newVersionCommand(&opts))
	cmd.AddCommand(newInitCommand(&opts))
	cmd.AddCommand(newStatusCommand(&opts))
	cmd.AddCommand(newEnrollCommand(&opts))
	cmd.AddCommand(newCredentialCommand(&opts))
	cmd.AddCommand(newHeartbeatCommand(&opts))
	cmd.AddCommand(newPollCommand(&opts))
	cmd.AddCommand(newRuntimeWorkersCommand(&opts))
	cmd.AddCommand(newRuntimeOutboxCommand(&opts))
	cmd.AddCommand(newRuntimeInboxCommand(&opts))
	cmd.AddCommand(newProvidersCommand(&opts))
	cmd.AddCommand(newAdvertiseCommand(&opts))
	cmd.AddCommand(newFilesystemCommand(&opts))
	cmd.AddCommand(newWatchedRootsCommand(&opts))
	cmd.AddCommand(newSyncCommand(&opts))
	cmd.AddCommand(newPrivateBackupCommand(&opts))
	cmd.AddCommand(newStorageMountCommand(&opts))
	cmd.AddCommand(newServeCommand(&opts))
	return cmd
}

func newVersionCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print node-agent version",
		RunE: func(cmd *cobra.Command, args []string) error {
			info := version.Current()
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), info))
			}
			_, err := fmt.Fprintf(opts.out, "loom-node-agent %s\n", info.Version)
			return err
		},
	}
}

func newInitCommand(opts *rootOptions) *cobra.Command {
	var config Config
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize local node-agent config and state",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := opts.store()
			if err != nil {
				return err
			}
			if !force {
				if _, err := os.Stat(store.ConfigPath); err == nil {
					return fmt.Errorf("config already exists at %s; pass --force to overwrite", store.ConfigPath)
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			config = normalizeConfig(config)
			if err := store.EnsureDataDirs(); err != nil {
				return err
			}
			if err := store.SaveConfig(config); err != nil {
				return err
			}
			runtimeStore := noderuntime.NewStore(store.DataDir)
			if err := runtimeStore.EnsureDefaultInstances(noderuntime.DefaultInstanceInput{
				HeartbeatIntervalSeconds: config.HeartbeatIntervalSeconds,
				PollIntervalSeconds:      config.PollIntervalSeconds,
			}); err != nil {
				return err
			}
			if force {
				if err := store.SaveState(State{}); err != nil {
					return err
				}
			} else if _, err := os.Stat(store.StatePath); errors.Is(err, os.ErrNotExist) {
				if err := store.SaveState(State{}); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			status, err := opts.status()
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status))
			}
			_, err = fmt.Fprintf(opts.out, "initialized node-agent config at %s\n", store.ConfigPath)
			return err
		},
	}
	cmd.Flags().StringVar(&config.MainURL, "main-url", "", "Main node URL, for example http://10.44.0.2:8080")
	cmd.Flags().StringVar(&config.NodeKey, "node-key", "", "Stable local node key")
	cmd.Flags().StringVar(&config.DisplayName, "display-name", "", "Human-readable node name")
	cmd.Flags().StringVar(&config.NodeKind, "kind", defaultNodeKind, "Node kind")
	cmd.Flags().StringVar(&config.NodeRole, "role", defaultNodeRole, "Node role")
	cmd.Flags().StringVar(&config.RuntimeClass, "runtime-class", defaultRuntimeClass, "Runtime complexity class")
	cmd.Flags().IntVar(&config.HeartbeatIntervalSeconds, "heartbeat-interval", defaultHeartbeatIntervalSeconds, "Heartbeat interval in seconds")
	cmd.Flags().IntVar(&config.PollIntervalSeconds, "poll-interval", defaultPollIntervalSeconds, "Poll interval in seconds")
	cmd.Flags().StringVar(&config.ServiceManager.AllowlistPath, "service-allowlist", "", "Reviewed service allowlist path")
	cmd.Flags().StringVar(&config.ServiceManager.ApplicationSocketPath, "application-socket", "", "Root-owned application helper socket")
	cmd.Flags().StringVar(&config.ServiceManager.HelperPath, "service-manager-helper", "", "Linux service-manager helper path")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing config and reset state")
	_ = cmd.MarkFlagRequired("main-url")
	_ = cmd.MarkFlagRequired("node-key")
	_ = cmd.MarkFlagRequired("display-name")
	return cmd
}

func newStatusCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print local node-agent status",
		RunE: func(cmd *cobra.Command, args []string) error {
			status, err := opts.status()
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status))
			}
			_, err = fmt.Fprintf(opts.out, "node_key=%s node_id=%s credential_configured=%t main_url=%s\n",
				status.Config.NodeKey,
				emptyPlaceholder(status.NodeID),
				status.CredentialConfigured,
				status.Config.MainURL,
			)
			return err
		},
	}
}

func newEnrollCommand(opts *rootOptions) *cobra.Command {
	var token string
	var tokenFile string
	cmd := &cobra.Command{
		Use:   "enroll",
		Short: "Create a pending enrollment request on the main node",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(token) == "" && strings.TrimSpace(tokenFile) != "" {
				payload, err := os.ReadFile(tokenFile)
				if err != nil {
					return err
				}
				token = string(payload)
			}
			if strings.TrimSpace(token) == "" {
				return errors.New("--token or --token-file is required")
			}
			store, config, state, err := opts.loadAll()
			if err != nil {
				return err
			}
			client, err := NewClient(config.MainURL)
			if err != nil {
				return err
			}
			input := nodes.CreateEnrollmentRequestInput{
				EnrollmentToken:       strings.TrimSpace(token),
				RequestedNodeKey:      config.NodeKey,
				RequestedDisplayName:  config.DisplayName,
				RequestedNodeKind:     config.NodeKind,
				RequestedNodeRole:     config.NodeRole,
				RequestedRuntimeClass: config.RuntimeClass,
				RequestedProfileJSON: objectJSON(map[string]any{
					"agent":         "loom-node-agent",
					"runtime_class": config.RuntimeClass,
				}),
				Metadata: objectJSON(map[string]any{
					"source": "loom-node-agent",
					"slice":  "10_part_2",
				}),
			}
			envelope, err := client.Enroll(cmd.Context(), correlation.Normalize(opts.correlationID), "node-agent.enroll."+config.NodeKey, input)
			if err != nil {
				return err
			}
			state.EnrollmentRequestID = envelope.Data.NodeEnrollmentRequestID
			if err := store.SaveState(state); err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, envelope)
			}
			_, err = fmt.Fprintf(opts.out, "enrollment_request=%s status=%s\n", envelope.Data.NodeEnrollmentRequestID, envelope.Data.Status)
			return err
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "Owner-issued node enrollment token")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "Read owner-issued node enrollment token from a file")
	return cmd
}

func newCredentialCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "credential",
		Short: "Manage local node credential material",
	}
	cmd.AddCommand(newCredentialImportCommand(opts))
	return cmd
}

func newCredentialImportCommand(opts *rootOptions) *cobra.Command {
	var fromFile string
	var nodeID string
	var credentialID string
	var credentialToken string
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import an approved node credential into local state",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, _, state, err := opts.loadAll()
			if err != nil {
				return err
			}
			if fromFile != "" {
				fileNodeID, fileCredentialID, fileCredentialToken, err := readApprovalFile(fromFile)
				if err != nil {
					return err
				}
				if nodeID == "" {
					nodeID = fileNodeID
				}
				if credentialID == "" {
					credentialID = fileCredentialID
				}
				if credentialToken == "" {
					credentialToken = fileCredentialToken
				}
			}
			nodeID = strings.TrimSpace(nodeID)
			credentialID = strings.TrimSpace(credentialID)
			credentialToken = strings.TrimSpace(credentialToken)
			if nodeID == "" || credentialID == "" || credentialToken == "" {
				return errors.New("--node-id, --credential-id, and --credential-token are required")
			}
			state.NodeID = nodeID
			state.NodeCredentialID = credentialID
			state.CredentialToken = credentialToken
			if err := store.SaveState(state); err != nil {
				return err
			}
			status, err := opts.status()
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status))
			}
			_, err = fmt.Fprintf(opts.out, "imported credential=%s for node=%s\n", credentialID, nodeID)
			return err
		},
	}
	cmd.Flags().StringVar(&fromFile, "from-file", "", "Read approval JSON from a loom node enrollment-request approve response")
	cmd.Flags().StringVar(&nodeID, "node-id", "", "Approved node ID")
	cmd.Flags().StringVar(&credentialID, "credential-id", "", "Approved node credential ID")
	cmd.Flags().StringVar(&credentialToken, "credential-token", "", "Approved node credential token")
	return cmd
}

func newHeartbeatCommand(opts *rootOptions) *cobra.Command {
	var once bool
	var reportedStatus string
	var runtimeVersion string
	var inboxBacklog int
	var outboxBacklog int
	cmd := &cobra.Command{
		Use:   "heartbeat",
		Short: "Send a node heartbeat to main",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !once {
				return errors.New("heartbeat currently requires --once; use serve for the long-running loop")
			}
			envelope, err := opts.sendHeartbeat(cmd.Context(), heartbeatRequestOptions{
				reportedStatus: reportedStatus,
				runtimeVersion: runtimeVersion,
				inboxBacklog:   inboxBacklog,
				outboxBacklog:  outboxBacklog,
			})
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, envelope)
			}
			_, err = fmt.Fprintf(opts.out, "heartbeat=%s presence=%s status=%s\n", envelope.Data.NodeHeartbeatID, envelope.Data.PresenceState, envelope.Data.ReportedStatus)
			return err
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "Send one heartbeat and exit")
	cmd.Flags().StringVar(&reportedStatus, "reported-status", "ok", "Reported node runtime status")
	cmd.Flags().StringVar(&runtimeVersion, "runtime-version", version.Current().Version, "Runtime version reported to main")
	cmd.Flags().IntVar(&inboxBacklog, "inbox-backlog", 0, "Number of pending local inbox messages")
	cmd.Flags().IntVar(&outboxBacklog, "outbox-backlog", 0, "Number of pending local outbox messages")
	return cmd
}

func newPollCommand(opts *rootOptions) *cobra.Command {
	var once bool
	var maxMessages int
	var noFlush bool
	cmd := &cobra.Command{
		Use:   "poll",
		Short: "Poll main for node-directed work",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !once {
				return errors.New("poll currently requires --once")
			}
			result, err := opts.pollOnce(cmd.Context(), maxMessages, !noFlush)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), result))
			}
			_, err = fmt.Fprintf(opts.out, "claimed=%d processed=%d has_more=%t\n", len(result.Poll.Messages), len(result.Processed), result.Poll.HasMore)
			return err
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "Run one poll cycle and exit")
	cmd.Flags().IntVar(&maxMessages, "max-messages", 10, "Maximum number of messages to claim")
	cmd.Flags().BoolVar(&noFlush, "no-flush", false, "Queue local ack/result outbox items without flushing them")
	return cmd
}

func newRuntimeWorkersCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workers",
		Short: "Inspect local node-agent runtime workers",
	}
	cmd.AddCommand(newRuntimeWorkersListCommand(opts))
	cmd.AddCommand(newRuntimeWorkersStatusCommand(opts))
	cmd.AddCommand(newRuntimeWorkersRunCommand(opts))
	return cmd
}

func newRuntimeWorkersListCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List local node-agent runtime worker instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			runtimeStore, _, err := opts.runtimeStoreAndEnv()
			if err != nil {
				return err
			}
			instances, err := runtimeStore.LoadInstances()
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), instances))
			}
			for _, instance := range instances {
				if _, err := fmt.Fprintf(opts.out, "worker=%s kind=%s enabled=%t interval=%ds\n",
					instance.WorkerKey,
					instance.Kind,
					instance.Enabled,
					instance.IntervalSeconds,
				); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newRuntimeWorkersStatusCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print local node-agent runtime worker status",
		RunE: func(cmd *cobra.Command, args []string) error {
			runtimeStore, _, err := opts.runtimeStoreAndEnv()
			if err != nil {
				return err
			}
			status, err := runtimeStore.Status()
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status))
			}
			_, err = fmt.Fprintf(opts.out, "workers=%d healthy=%d degraded=%d blocked=%d manual_action=%d outbox_pending=%d\n",
				status.Summary.Workers.Total,
				status.Summary.Workers.Healthy,
				status.Summary.Workers.Degraded,
				status.Summary.Workers.Blocked,
				status.Summary.Workers.ManualAction,
				status.Summary.Queues.OutboxPending,
			)
			return err
		},
	}
}

func newRuntimeWorkersRunCommand(opts *rootOptions) *cobra.Command {
	var once bool
	cmd := &cobra.Command{
		Use:   "run <worker-key>",
		Short: "Run one local node-agent runtime worker once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !once {
				return errors.New("workers run currently requires --once")
			}
			runtimeStore, env, err := opts.runtimeStoreAndEnv()
			if err != nil {
				return err
			}
			output, err := nodeAgentRuntimeRegistry().RunOnce(cmd.Context(), runtimeStore, env, args[0], correlation.Normalize(opts.correlationID))
			if opts.jsonOutput {
				if renderErr := renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), output)); renderErr != nil {
					return renderErr
				}
			} else {
				_, printErr := fmt.Fprintf(opts.out, "worker=%s run=%s status=%s health=%s\n",
					output.Instance.WorkerKey,
					output.Run.LocalRunID,
					output.Run.Status,
					output.Health.Status,
				)
				if printErr != nil {
					return printErr
				}
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "Run the worker once and exit")
	return cmd
}

func newRuntimeOutboxCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "outbox",
		Short: "Inspect local node-agent runtime outbox",
	}
	cmd.AddCommand(newRuntimeOutboxStatusCommand(opts))
	cmd.AddCommand(newRuntimeOutboxListCommand(opts))
	cmd.AddCommand(newRuntimeOutboxFlushCommand(opts))
	return cmd
}

func newRuntimeOutboxStatusCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print local node-agent runtime outbox status",
		RunE: func(cmd *cobra.Command, args []string) error {
			runtimeStore, _, err := opts.runtimeStoreAndEnv()
			if err != nil {
				return err
			}
			status, err := runtimeStore.OutboxSummary()
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status))
			}
			_, err = fmt.Fprintf(opts.out, "pending=%d inflight=%d done=%d failed=%d manual_action=%d pending_bytes=%d\n",
				status.Counts[noderuntime.OutboxStatusPending],
				status.Counts[noderuntime.OutboxStatusInflight],
				status.Counts[noderuntime.OutboxStatusDone],
				status.Counts[noderuntime.OutboxStatusFailed],
				status.Counts[noderuntime.OutboxStatusManualAction],
				status.TotalPendingBytes,
			)
			return err
		},
	}
}

func newRuntimeOutboxListCommand(opts *rootOptions) *cobra.Command {
	var statuses []string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List local node-agent runtime outbox items",
		RunE: func(cmd *cobra.Command, args []string) error {
			runtimeStore, _, err := opts.runtimeStoreAndEnv()
			if err != nil {
				return err
			}
			items, err := runtimeStore.ListOutbox(statuses, limit)
			if err != nil {
				return err
			}
			items = noderuntime.RedactOutboxItems(items)
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), items))
			}
			for _, item := range items {
				if _, err := fmt.Fprintf(opts.out, "outbox=%s kind=%s status=%s attempts=%d\n",
					item.LocalOutboxID,
					item.Kind,
					item.Status,
					item.AttemptCount,
				); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&statuses, "status", []string{}, "Outbox status filter; repeat or comma-separate")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum items to list")
	return cmd
}

func newRuntimeOutboxFlushCommand(opts *rootOptions) *cobra.Command {
	var maxItems int
	cmd := &cobra.Command{
		Use:   "flush",
		Short: "Flush due local node-agent runtime outbox items",
		RunE: func(cmd *cobra.Command, args []string) error {
			runtimeStore, env, err := opts.runtimeStoreAndEnv()
			if err != nil {
				return err
			}
			run, err := flushDueOutbox(cmd.Context(), env, runtimeStore, maxItems)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), run))
			}
			_, err = fmt.Fprintf(opts.out, "submitted=%d done=%d failed=%d manual_action=%d pending=%d\n",
				run.Submitted,
				run.Done,
				run.Failed,
				run.ManualAction,
				run.Summary.Counts[noderuntime.OutboxStatusPending],
			)
			return err
		},
	}
	cmd.Flags().IntVar(&maxItems, "max-items", 50, "Maximum due outbox items to flush")
	return cmd
}

func newRuntimeInboxCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Inspect local node-agent runtime inbox",
	}
	cmd.AddCommand(newRuntimeInboxStatusCommand(opts))
	return cmd
}

func newRuntimeInboxStatusCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print local node-agent runtime inbox status",
		RunE: func(cmd *cobra.Command, args []string) error {
			runtimeStore, _, err := opts.runtimeStoreAndEnv()
			if err != nil {
				return err
			}
			status, err := runtimeStore.InboxSummary()
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status))
			}
			_, err = fmt.Fprintf(opts.out, "pending=%d done=%d failed=%d legacy=%d\n",
				status.Counts[noderuntime.OutboxStatusPending],
				status.Counts[noderuntime.OutboxStatusDone],
				status.Counts[noderuntime.OutboxStatusFailed],
				status.LegacyRecords,
			)
			return err
		},
	}
}

func newServeCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the long-running node-agent loop",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, config, _, err := opts.loadAll()
			if err != nil {
				return err
			}
			if err := store.EnsureDataDirs(); err != nil {
				return err
			}
			runtimeStore := noderuntime.NewStore(store.DataDir)
			if err := runtimeStore.EnsureDefaultInstances(noderuntime.DefaultInstanceInput{
				HeartbeatIntervalSeconds: config.HeartbeatIntervalSeconds,
				PollIntervalSeconds:      config.PollIntervalSeconds,
			}); err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			supervisor := noderuntime.Supervisor{
				Store:         runtimeStore,
				Registry:      nodeAgentRuntimeRegistry(),
				CorrelationID: correlation.Normalize(opts.correlationID),
				ErrOut:        opts.errOut,
				EnvProvider: func() (noderuntime.Env, error) {
					_, env, err := opts.runtimeStoreAndEnv()
					return env, err
				},
			}
			return supervisor.Run(ctx)
		},
	}
}

type heartbeatRequestOptions struct {
	reportedStatus string
	runtimeVersion string
	inboxBacklog   int
	outboxBacklog  int
}

func (opts *rootOptions) store() (Store, error) {
	return ResolveStore(
		firstNonEmpty(opts.configPath, os.Getenv("LOOM_NODE_AGENT_CONFIG")),
		firstNonEmpty(opts.statePath, os.Getenv("LOOM_NODE_AGENT_STATE")),
		firstNonEmpty(opts.dataDir, os.Getenv("LOOM_NODE_AGENT_DATA_DIR")),
	)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (opts *rootOptions) loadAll() (Store, Config, State, error) {
	store, err := opts.store()
	if err != nil {
		return Store{}, Config{}, State{}, err
	}
	config, err := store.LoadConfig()
	if err != nil {
		return Store{}, Config{}, State{}, err
	}
	state, err := store.LoadState()
	if err != nil {
		return Store{}, Config{}, State{}, err
	}
	return store, config, state, nil
}

func (opts *rootOptions) runtimeStoreAndEnv() (noderuntime.Store, noderuntime.Env, error) {
	store, config, state, err := opts.loadAll()
	if err != nil {
		return noderuntime.Store{}, noderuntime.Env{}, err
	}
	runtimeStore := noderuntime.NewStore(store.DataDir)
	if err := runtimeStore.EnsureDefaultInstances(noderuntime.DefaultInstanceInput{
		HeartbeatIntervalSeconds: config.HeartbeatIntervalSeconds,
		PollIntervalSeconds:      config.PollIntervalSeconds,
	}); err != nil {
		return noderuntime.Store{}, noderuntime.Env{}, err
	}
	env := noderuntime.RuntimeEnv(
		store.ConfigPath,
		store.StatePath,
		store.DataDir,
		state.NodeID,
		config.NodeKey,
		config.MainURL,
		strings.TrimSpace(state.CredentialToken) != "",
	)
	return runtimeStore, env, nil
}

func (opts *rootOptions) status() (Status, error) {
	store, config, state, err := opts.loadAll()
	if err != nil {
		return Status{}, err
	}
	runtimeStatus, err := opts.runtimeStatus(store, config)
	if err != nil {
		return Status{}, err
	}
	return Status{
		ConfigPath:           store.ConfigPath,
		StatePath:            store.StatePath,
		DataDir:              store.DataDir,
		Version:              version.Current(),
		Config:               config,
		EnrollmentRequestID:  state.EnrollmentRequestID,
		NodeID:               state.NodeID,
		NodeCredentialID:     state.NodeCredentialID,
		CredentialConfigured: strings.TrimSpace(state.CredentialToken) != "",
		LastHeartbeat:        state.LastHeartbeat,
		LastPoll:             state.LastPoll,
		LastAdvertisement:    state.LastAdvertisement,
		Runtime:              &runtimeStatus,
	}, nil
}

func (opts *rootOptions) runtimeStatus(store Store, config Config) (noderuntime.Status, error) {
	runtimeStore := noderuntime.NewStore(store.DataDir)
	if err := runtimeStore.EnsureDefaultInstances(noderuntime.DefaultInstanceInput{
		HeartbeatIntervalSeconds: config.HeartbeatIntervalSeconds,
		PollIntervalSeconds:      config.PollIntervalSeconds,
	}); err != nil {
		return noderuntime.Status{}, err
	}
	return runtimeStore.Status()
}

func (opts *rootOptions) sendHeartbeat(ctx context.Context, heartbeatOpts heartbeatRequestOptions) (response.Envelope[nodes.Heartbeat], error) {
	store, config, state, err := opts.loadAll()
	if err != nil {
		return response.Envelope[nodes.Heartbeat]{}, err
	}
	runtimeStore := noderuntime.NewStore(store.DataDir)
	if err := runtimeStore.EnsureDefaultInstances(noderuntime.DefaultInstanceInput{
		HeartbeatIntervalSeconds: config.HeartbeatIntervalSeconds,
		PollIntervalSeconds:      config.PollIntervalSeconds,
	}); err != nil {
		return response.Envelope[nodes.Heartbeat]{}, err
	}
	return sendHeartbeatCore(ctx, store, config, state, runtimeStore, correlation.Normalize(opts.correlationID), heartbeatOpts)
}

func (opts *rootOptions) pollOnce(ctx context.Context, maxMessages int, flush bool) (PollRunResult, error) {
	runtimeStore, env, err := opts.runtimeStoreAndEnv()
	if err != nil {
		return PollRunResult{}, err
	}
	env.CorrelationID = correlation.Normalize(opts.correlationID)
	return pollOnceCore(ctx, env, runtimeStore, pollOptions{maxMessages: maxMessages, flush: flush})
}

func nodeAgentMessageCorrelationID(fallback string, message communication.Message) string {
	if message.CorrelationID != nil && strings.TrimSpace(*message.CorrelationID) != "" {
		return correlation.Normalize(*message.CorrelationID)
	}
	return correlation.Normalize(fallback)
}

func buildAckInput(state State, message communication.Message) communication.AckInput {
	processedAt := time.Now().UTC()
	ackInput := communication.AckInput{
		NodeRef:                 state.NodeID,
		CredentialToken:         state.CredentialToken,
		CommunicationMessageRef: message.CommunicationMessageID,
		AckStatus:               communication.AckStatusCompleted,
		IdempotencyKey:          "node-agent.ack." + state.NodeID + "." + message.CommunicationMessageID,
		ProcessedAt:             &processedAt,
		Metadata: objectJSON(map[string]any{
			"source": "loom-node-agent",
			"slice":  "10_part_3",
		}),
	}
	switch message.Kind {
	case communication.KindMainPing:
		ackInput.ResultJSON = objectJSON(map[string]any{
			"pong":          true,
			"node_id":       state.NodeID,
			"message_id":    message.CommunicationMessageID,
			"received_text": payloadString(message.PayloadJSON, "text"),
		})
	case communication.KindMainNoop:
		ackInput.ResultJSON = objectJSON(map[string]any{
			"noop":       true,
			"node_id":    state.NodeID,
			"message_id": message.CommunicationMessageID,
		})
	case communication.KindCapabilityDispatch:
		ackInput.ResultJSON = objectJSON(map[string]any{
			"accepted":   true,
			"node_id":    state.NodeID,
			"message_id": message.CommunicationMessageID,
			"kind":       message.Kind,
		})
	default:
		ackInput.AckStatus = communication.AckStatusRejected
		ackInput.ResultJSON = objectJSON(map[string]any{})
		ackInput.ErrorJSON = objectJSON(map[string]any{
			"code":    "unsupported_message_kind",
			"summary": "The node-agent does not execute this message kind.",
			"kind":    message.Kind,
		})
		return ackInput
	}
	ackInput.ErrorJSON = objectJSON(map[string]any{})
	return ackInput
}

func buildCapabilityResultInput(ctx context.Context, config Config, state State, store Store, message communication.Message) (routing.RemoteResultInput, error) {
	var dispatch routing.RemoteDispatchPayload
	if err := json.Unmarshal(message.PayloadJSON, &dispatch); err != nil {
		return routing.RemoteResultInput{}, err
	}
	if strings.TrimSpace(dispatch.RouteID) == "" || strings.TrimSpace(dispatch.CapabilityCallID) == "" {
		return routing.RemoteResultInput{}, errors.New("dispatch payload is missing route_id or capability_call_id")
	}
	if strings.TrimSpace(dispatch.TargetNodeID) != "" && dispatch.TargetNodeID != state.NodeID {
		return routing.RemoteResultInput{}, fmt.Errorf("dispatch target node %s does not match local node %s", dispatch.TargetNodeID, state.NodeID)
	}

	if applicationDispatchOperation(config, dispatch) != "" {
		return buildApplicationResultInput(ctx, config, state, store, message, dispatch)
	}

	startedAt := time.Now().UTC()
	completedAt := startedAt
	payload := routing.RemoteResultPayload{
		RouteID:              dispatch.RouteID,
		CapabilityCallID:     dispatch.CapabilityCallID,
		NodeID:               state.NodeID,
		ProviderID:           dispatch.ProviderID,
		ProviderAddress:      dispatch.ProviderAddress,
		CapabilityEndpointID: dispatch.CapabilityEndpointID,
		CapabilityAddress:    dispatch.CapabilityAddress,
		Operation:            dispatch.Operation,
		ExecutionStatus:      routing.CapabilityCallStatusCompleted,
		StartedAt:            &startedAt,
		CompletedAt:          &completedAt,
		ResultRefsJSON:       objectJSON(map[string]any{}),
		RuntimeMetadataJSON:  runtimeDispatchMetadata(config, state, message.CommunicationMessageID, dispatch),
	}

	switch {
	case isProjectArchiveQuiescenceDispatch(config, dispatch):
		result, err := executeProjectArchiveQuiescenceDispatch(ctx, config, store, dispatch)
		if err != nil {
			payload.ExecutionStatus = routing.CapabilityCallStatusFailed
			payload.ErrorCode, payload.ErrorMessage = projectArchiveQuiescenceFailure(err)
			payload.ResultJSON = objectJSON(map[string]any{})
			break
		}
		payload.ResultJSON = objectOrEmpty(result)
	case dispatch.RuntimeBinding != nil:
		result, err := executeRuntimeBindingDispatch(ctx, config, state, dispatch, store)
		if err != nil {
			payload.ExecutionStatus = routing.CapabilityCallStatusFailed
			payload.ErrorCode, payload.ErrorMessage = nodeAgentRuntimeFailure(err)
			payload.ResultJSON = objectJSON(map[string]any{})
			break
		}
		payload.ResultJSON = objectOrEmpty(result.Result)
		payload.ResultRefsJSON = objectOrEmpty(result.ResultRefs)
	case dispatch.CapabilityAddress == capabilities.NodeSystemProviderAddress(addressSegment(config.NodeKey))+".echo":
		text := payloadString(dispatch.Input, "text")
		payload.ResultJSON = objectJSON(map[string]any{
			"echo":     text,
			"text":     text,
			"node_id":  state.NodeID,
			"node_key": config.NodeKey,
		})
	case dispatch.CapabilityAddress == capabilities.NodeSystemProviderAddress(addressSegment(config.NodeKey))+".status.read":
		lastHeartbeat := ""
		if state.LastHeartbeat != nil {
			lastHeartbeat = state.LastHeartbeat.ReceivedAt.UTC().Format(time.RFC3339)
		}
		queueCounts, queueCountErr := store.QueueCounts()
		queueCountsJSON := map[string]any{
			"available": false,
		}
		if queueCountErr == nil {
			queueCountsJSON = map[string]any{
				"available": true,
				"inbox":     queueCounts.Inbox,
				"outbox":    queueCounts.Outbox,
			}
		}
		payload.ResultJSON = objectJSON(map[string]any{
			"node_id":       state.NodeID,
			"node_key":      config.NodeKey,
			"runtime_class": config.RuntimeClass,
			"version":       version.Current().Version,
			"current_time":  completedAt.Format(time.RFC3339),
			"generated_at":  completedAt.Format(time.RFC3339),
			"queue_counts":  queueCountsJSON,
			"last_heartbeat": map[string]any{
				"received_at": lastHeartbeat,
				"available":   state.LastHeartbeat != nil,
			},
		})
	case isFilesystemDispatch(dispatch):
		result, err := executeFilesystemDispatch(ctx, config, state, store, dispatch)
		if err != nil {
			payload.ExecutionStatus = routing.CapabilityCallStatusFailed
			payload.ErrorCode = filesystemDispatchErrorCode(err)
			payload.ErrorMessage = filesystemDispatchErrorMessage(err)
			payload.ResultJSON = objectJSON(map[string]any{})
			break
		}
		payload.ResultJSON = result.ResultJSON
		payload.ResultRefsJSON = result.ResultRefsJSON
	default:
		payload.ExecutionStatus = routing.CapabilityCallStatusFailed
		payload.ErrorCode = "node_agent.unsupported_capability"
		payload.ErrorMessage = "node-agent only supports system.echo, system.status.read, system.project.archive.quiesce, and approved filesystem connector operations"
		payload.ResultJSON = objectJSON(map[string]any{})
	}

	idempotencyKey := "capability.result." + state.NodeID + "." + dispatch.CapabilityCallID
	return routing.RemoteResultInput{
		NodeRef:         state.NodeID,
		CredentialToken: state.CredentialToken,
		IdempotencyKey:  idempotencyKey,
		Payload:         payload,
		Metadata: objectJSON(map[string]any{
			"source":              "loom-node-agent",
			"slice":               "11_part_2",
			"dispatch_message_id": message.CommunicationMessageID,
		}),
	}, nil
}

func nodeAgentRuntimeFailure(err error) (string, string) {
	if code, message, ok := routing.RuntimeFailureCode(err); ok {
		return code, message
	}
	return "node_agent.runtime_execution_failed", err.Error()
}

func objectOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func payloadString(raw json.RawMessage, key string) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	value, _ := payload[key].(string)
	return value
}

func readApprovalFile(path string) (nodeID, credentialID, credentialToken string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", "", err
	}
	defer file.Close()
	var envelope struct {
		Data struct {
			Node            nodes.Node           `json:"node"`
			Credential      nodes.NodeCredential `json:"credential"`
			CredentialToken string               `json:"credential_token"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&envelope); err != nil {
		return "", "", "", err
	}
	return envelope.Data.Node.NodeID, envelope.Data.Credential.NodeCredentialID, envelope.Data.CredentialToken, nil
}

func renderJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func localSuccess[T any](correlationID string, data T) response.Envelope[T] {
	meta := response.NewMeta(correlationID)
	meta.Source = "node-agent-local"
	meta.Freshness = "local"
	return response.Envelope[T]{
		OK:   true,
		Data: data,
		Meta: meta,
	}
}

func emptyPlaceholder(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
