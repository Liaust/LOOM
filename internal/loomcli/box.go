package loomcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/projects"
)

func newBoxCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "box",
		Short: "Inspect the local LOOM Box filesystem workspace",
	}
	cmd.AddCommand(newBoxInitCommand(opts))
	cmd.AddCommand(newBoxRepairCommand(opts))
	cmd.AddCommand(newBoxStatusCommand(opts))
	cmd.AddCommand(newBoxPathCommand(opts))
	cmd.AddCommand(newBoxWatchPlanCommand(opts))
	cmd.AddCommand(newBoxWatchApplyCommand(opts))
	cmd.AddCommand(newBoxWatchStatusCommand(opts))
	cmd.AddCommand(newBoxDropzoneCommand(opts))
	return cmd
}

type boxCommandFlags struct {
	path           string
	profile        string
	dryRun         bool
	emitShell      bool
	idempotencyKey string
}

func newBoxInitCommand(opts *options) *cobra.Command {
	flags := boxCommandFlags{}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create the local LOOM Box folder template",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveBoxForCLI(opts, flags)
			if err != nil {
				return err
			}
			result, err := box.Init(box.InitInput{Resolved: resolved, DryRun: flags.dryRun})
			result = boxInitResultForCurrentSurface(result)
			if opts.jsonOutput {
				encodeErr := encodeCurrentBoxJSON(cmd.OutOrStdout(), result)
				if err != nil {
					return err
				}
				return encodeErr
			}
			renderBoxInit(cmd, result)
			return err
		},
	}
	addBoxFlags(cmd, &flags)
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "show planned Box files without writing")
	return cmd
}

func newBoxRepairCommand(opts *options) *cobra.Command {
	flags := boxCommandFlags{}
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Repair or complete the local LOOM Box folder template",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveBoxForCLI(opts, flags)
			if err != nil {
				return err
			}
			result, err := box.Init(box.InitInput{Resolved: resolved, DryRun: flags.dryRun})
			result = boxInitResultForCurrentSurface(result)
			if opts.jsonOutput {
				encodeErr := encodeCurrentBoxJSON(cmd.OutOrStdout(), result)
				if err != nil {
					return err
				}
				return encodeErr
			}
			renderBoxRepair(cmd, result)
			return err
		},
	}
	addBoxFlags(cmd, &flags)
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "show planned Box repairs without writing")
	return cmd
}

func newBoxStatusCommand(opts *options) *cobra.Command {
	flags := boxCommandFlags{}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show read-only LOOM Box status",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveBoxForCLI(opts, flags)
			if err != nil {
				return err
			}
			status := boxStatusForCurrentSurface(box.Inspect(resolved))
			if opts.jsonOutput {
				return encodeCurrentBoxJSON(cmd.OutOrStdout(), status)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), status.State)
				return nil
			}
			renderBoxStatus(cmd, status)
			return nil
		},
	}
	addBoxFlags(cmd, &flags)
	return cmd
}

func encodeCurrentBoxJSON(w io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return err
	}
	pruneRetiredBoxJSON(document)
	return json.NewEncoder(w).Encode(document)
}

func pruneRetiredBoxJSON(value any) {
	switch typed := value.(type) {
	case map[string]any:
		delete(typed, "dropzone_state")
		delete(typed, "dropzone_transfers")
		if profile, _ := typed["profile"].(string); profile == box.ProfileMain {
			delete(typed, "lane_state")
			delete(typed, "lane")
		}
		for _, child := range typed {
			pruneRetiredBoxJSON(child)
		}
	case []any:
		for _, child := range typed {
			pruneRetiredBoxJSON(child)
		}
	}
}

func boxInitResultForCurrentSurface(result box.InitResult) box.InitResult {
	result.StatusBefore = boxStatusForCurrentSurface(result.StatusBefore)
	result.StatusAfter = boxStatusForCurrentSurface(result.StatusAfter)
	return result
}

func boxStatusForCurrentSurface(status box.Status) box.Status {
	status.DropzoneState = ""
	status.DropzoneTransfers = nil
	removeLane := status.Profile == box.ProfileMain
	if removeLane {
		status.LaneState = ""
		status.Lane = nil
	}
	status.Areas = currentBoxPathStatuses(status.Areas, removeLane)
	status.Policies = currentBoxPathStatuses(status.Policies, removeLane)
	if status.Contract != nil {
		contract := *status.Contract
		contract.Areas = cloneCurrentBoxAreas(contract.Areas, removeLane)
		contract.Policies = cloneCurrentBoxPolicies(contract.Policies, removeLane)
		status.Contract = &contract
	}
	return status
}

func currentBoxPathStatuses(items []box.PathStatus, removeLane bool) []box.PathStatus {
	filtered := make([]box.PathStatus, 0, len(items))
	for _, item := range items {
		if item.Key == box.AreaDropzone || (removeLane && item.Key == box.AreaLane) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func cloneCurrentBoxAreas(items map[string]box.Area, removeLane bool) map[string]box.Area {
	filtered := make(map[string]box.Area, len(items))
	for key, item := range items {
		if key == box.AreaDropzone || (removeLane && key == box.AreaLane) {
			continue
		}
		filtered[key] = item
	}
	return filtered
}

func cloneCurrentBoxPolicies(items map[string]string, removeLane bool) map[string]string {
	filtered := make(map[string]string, len(items))
	for key, item := range items {
		if key == box.AreaDropzone || (removeLane && key == box.AreaLane) {
			continue
		}
		filtered[key] = item
	}
	return filtered
}

func newBoxPathCommand(opts *options) *cobra.Command {
	flags := boxCommandFlags{}
	cmd := &cobra.Command{
		Use:   "path",
		Short: "Print the resolved LOOM Box path",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveBoxForCLI(opts, flags)
			if err != nil {
				return err
			}
			result := box.PathResultFromResolved(resolved)
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			fmt.Fprintln(cmd.OutOrStdout(), result.RootPath)
			return nil
		},
	}
	addBoxFlags(cmd, &flags)
	return cmd
}

func newBoxWatchPlanCommand(opts *options) *cobra.Command {
	flags := boxCommandFlags{}
	cmd := &cobra.Command{
		Use:   "watch-plan",
		Short: "Compile Box Notes/Documents policies into node-agent watched-root desired state",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveBoxForCLI(opts, flags)
			if err != nil {
				return err
			}
			plan, err := box.BuildWatchPlan(box.WatchStatusInput{Resolved: resolved})
			if opts.jsonOutput {
				encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
				if err != nil {
					return err
				}
				return encodeErr
			}
			renderBoxWatchPlan(cmd, plan)
			return err
		},
	}
	addBoxFlags(cmd, &flags)
	return cmd
}

func newBoxWatchApplyCommand(opts *options) *cobra.Command {
	flags := boxCommandFlags{}
	cmd := &cobra.Command{
		Use:   "watch-apply",
		Short: "Record Box Notes/Documents watched-root desired state on main",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveBoxForCLI(opts, flags)
			if err != nil {
				return err
			}
			plan, err := box.BuildWatchPlan(box.WatchStatusInput{Resolved: resolved})
			if err != nil {
				if opts.jsonOutput {
					_ = json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
				} else {
					renderBoxWatchPlan(cmd, plan)
				}
				return err
			}
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, flags.idempotencyKey, "box.watch_policy.apply")
			envelope, err := client.ApplyBoxWatchPolicy(ctx, correlationID, box.WatchApplyInput{Resolved: resolved, Plan: &plan, DryRun: flags.dryRun})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not apply Box watch policy.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if flags.emitShell {
				renderBoxWatchShell(cmd, envelope.Data.Plan.Commands)
			} else {
				renderBoxWatchApply(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
			}
			return nil
		},
	}
	addBoxFlags(cmd, &flags)
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "compute desired state without recording Box watched-root registration rows")
	cmd.Flags().BoolVar(&flags.emitShell, "emit-shell", false, "print node-agent shell commands only")
	cmd.Flags().StringVar(&flags.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	return cmd
}

func newBoxWatchStatusCommand(opts *options) *cobra.Command {
	flags := boxCommandFlags{}
	cmd := &cobra.Command{
		Use:   "watch-status",
		Short: "Show Box watched-root registration and node-agent report status",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveBoxForCLI(opts, flags)
			if err != nil {
				return err
			}
			plan, err := box.BuildWatchPlan(box.WatchStatusInput{Resolved: resolved})
			if err != nil {
				if opts.jsonOutput {
					_ = json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
				} else {
					renderBoxWatchPlan(cmd, plan)
				}
				return err
			}
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetBoxWatchStatus(ctx, correlationID, box.WatchStatusInput{Resolved: resolved, Plan: &plan})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect Box watch status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderBoxWatchStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	addBoxFlags(cmd, &flags)
	return cmd
}

func addBoxFlags(cmd *cobra.Command, flags *boxCommandFlags) {
	cmd.Flags().StringVar(&flags.path, "path", "", "LOOM Box root path")
	cmd.Flags().StringVar(&flags.profile, "profile", "", "LOOM Box profile (workspace or main)")
}

func resolveBoxForCLI(opts *options, flags boxCommandFlags) (box.Resolved, error) {
	cfg, err := resolveCLIConfig(opts)
	if err != nil {
		return box.Resolved{}, err
	}
	return box.Resolve(box.ResolveInput{
		ExplicitPath:     flags.path,
		ConfiguredPath:   cfg.BoxPath,
		RuntimeStateRoot: cfg.BoxStateRoot,
		ExplicitProfile:  flags.profile,
		ConfigProfile:    cfg.BoxProfile,
		NodeID:           cfg.NodeID,
		NodeRole:         cfg.NodeRole,
	})
}

func renderBoxStatus(cmd *cobra.Command, status box.Status) {
	fmt.Fprintf(cmd.OutOrStdout(), "LOOM Box: %s\n", status.State)
	fmt.Fprintf(cmd.OutOrStdout(), "Path: %s\n", status.RootPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Path source: %s\n", status.PathSource)
	fmt.Fprintf(cmd.OutOrStdout(), "Profile: %s", status.Profile)
	if status.ProfileSource != "" {
		fmt.Fprintf(cmd.OutOrStdout(), " (%s)", status.ProfileSource)
	}
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintf(cmd.OutOrStdout(), "Owner node: %s\n", dashIfEmpty(status.OwnerNode))
	fmt.Fprintf(cmd.OutOrStdout(), "Initialized: %s\n", boolMarker(status.Initialized))
	fmt.Fprintf(cmd.OutOrStdout(), "Contract: %s\n", status.ContractState)
	fmt.Fprintf(cmd.OutOrStdout(), "Contract path: %s\n", status.ContractPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Default project path: %s\n", status.DefaultProjectPath)
	if status.Profile == box.ProfileWorkspace && status.Lane != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Workspace LOOM Lane: %s\n", status.LaneState)
		fmt.Fprintf(cmd.OutOrStdout(), "LOOM Lane pending: %d items, %d files, %d dirs, %s\n",
			status.Lane.PendingItems,
			status.Lane.PendingFiles,
			status.Lane.PendingDirs,
			formatLaneBytes(status.Lane.PendingBytes),
		)
		fmt.Fprintf(cmd.OutOrStdout(), "LOOM Lane preflight: %s\n", dashIfEmpty(status.Lane.Preflight.Status))
		if status.RuntimeStateReadRoot != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Workspace LOOM Lane status path: %s\n", filepath.Join(status.RuntimeStateReadRoot, "lane"))
		}
	} else if status.Profile == box.ProfileMain {
		fmt.Fprintln(cmd.OutOrStdout(), "Main intake: Storage Imports (destination for workspace LOOM Lane transfers)")
	}
	renderBoxPathTable(cmd, "Folders", status.Areas)
	renderBoxPathTable(cmd, "Policies", status.Policies)
	if len(status.Diagnostics) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Diagnostics:")
		for _, diagnostic := range status.Diagnostics {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
			if diagnostic.Suggestion != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "    suggestion: %s\n", diagnostic.Suggestion)
			}
		}
	}
	if status.State == "missing" || status.State == "partial" {
		nextPath := shellQuoteIfNeeded(status.RootPath)
		fmt.Fprintln(cmd.OutOrStdout(), "Next:")
		fmt.Fprintf(cmd.OutOrStdout(), "  loom box init --path %s --profile %s\n", nextPath, status.Profile)
	}
}

func renderBoxInit(cmd *cobra.Command, result box.InitResult) {
	action := "created"
	if result.DryRun {
		action = "planned"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "LOOM Box init: %s\n", action)
	fmt.Fprintf(cmd.OutOrStdout(), "Path: %s\n", result.RootPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Profile: %s\n", result.Profile)
	fmt.Fprintf(cmd.OutOrStdout(), "Owner node: %s\n", dashIfEmpty(result.OwnerNode))
	if result.DryRun {
		renderStringList(cmd, "Planned directories", result.PlannedDirs)
		renderStringList(cmd, "Planned files", result.PlannedFiles)
	} else {
		renderStringList(cmd, "Created directories", result.CreatedDirs)
		renderStringList(cmd, "Created files", result.CreatedFiles)
		renderStringList(cmd, "Existing directories", result.SkippedDirs)
		renderStringList(cmd, "Existing files", result.SkippedFiles)
	}
	if len(result.Diagnostics) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Diagnostics:")
		for _, diagnostic := range result.Diagnostics {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
			if diagnostic.Path != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "    path: %s\n", diagnostic.Path)
			}
			if diagnostic.Suggestion != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "    suggestion: %s\n", diagnostic.Suggestion)
			}
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", result.StatusAfter.State)
	if result.StatusAfter.Profile == box.ProfileWorkspace && result.StatusAfter.Lane != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Workspace LOOM Lane: %s\n", result.StatusAfter.LaneState)
		if result.StatusAfter.RuntimeStateReadRoot != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Workspace LOOM Lane status path: %s\n", filepath.Join(result.StatusAfter.RuntimeStateReadRoot, "lane"))
		}
	} else if result.StatusAfter.Profile == box.ProfileMain {
		fmt.Fprintln(cmd.OutOrStdout(), "Main intake: Storage Imports (destination for workspace LOOM Lane transfers)")
	}
}

func renderBoxRepair(cmd *cobra.Command, result box.InitResult) {
	action := "applied"
	if result.DryRun {
		action = "planned"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "LOOM Box repair: %s\n", action)
	fmt.Fprintf(cmd.OutOrStdout(), "Path: %s\n", result.RootPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Profile: %s\n", result.Profile)
	fmt.Fprintf(cmd.OutOrStdout(), "Owner node: %s\n", dashIfEmpty(result.OwnerNode))
	if result.DryRun {
		renderStringList(cmd, "Planned directories", result.PlannedDirs)
		renderStringList(cmd, "Planned files", result.PlannedFiles)
	} else {
		renderStringList(cmd, "Created directories", result.CreatedDirs)
		renderStringList(cmd, "Created files", result.CreatedFiles)
		renderStringList(cmd, "Existing directories", result.SkippedDirs)
		renderStringList(cmd, "Existing files", result.SkippedFiles)
	}
	if len(result.Diagnostics) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Diagnostics:")
		for _, diagnostic := range result.Diagnostics {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
			if diagnostic.Path != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "    path: %s\n", diagnostic.Path)
			}
			if diagnostic.Suggestion != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "    suggestion: %s\n", diagnostic.Suggestion)
			}
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", result.StatusAfter.State)
	if result.StatusAfter.Profile == box.ProfileWorkspace && result.StatusAfter.Lane != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Workspace LOOM Lane: %s\n", result.StatusAfter.LaneState)
		if result.StatusAfter.RuntimeStateReadRoot != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Workspace LOOM Lane status path: %s\n", filepath.Join(result.StatusAfter.RuntimeStateReadRoot, "lane"))
		}
	} else if result.StatusAfter.Profile == box.ProfileMain {
		fmt.Fprintln(cmd.OutOrStdout(), "Main intake: Storage Imports (destination for workspace LOOM Lane transfers)")
	}
}

func renderBoxWatchPlan(cmd *cobra.Command, plan box.WatchPlan) {
	fmt.Fprintf(cmd.OutOrStdout(), "Box watch plan: %s\n", dashIfEmpty(plan.BoxID))
	fmt.Fprintf(cmd.OutOrStdout(), "Path: %s\n", dashIfEmpty(plan.RootPath))
	fmt.Fprintf(cmd.OutOrStdout(), "Profile: %s\n", dashIfEmpty(plan.Profile))
	fmt.Fprintf(cmd.OutOrStdout(), "Owner node: %s\n", dashIfEmpty(plan.OwnerNode))
	if plan.ContractPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Contract: %s\n", plan.ContractPath)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Watched roots: %d\n", len(plan.WatchedRoots))
	if len(plan.WatchedRoots) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Desired watched roots:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "AREA\tROOT\tPATH\tSYNC\tINDEX\tBACKUP\tWORKER")
		for _, root := range plan.WatchedRoots {
			fmt.Fprintf(writer, "%s\t%s\t%s:%s\t%s\t%s\t%s\t%s\n",
				dashIfEmpty(root.Key),
				dashIfEmpty(root.BackendRootKey),
				dashIfEmpty(root.SafeRootKey),
				dashIfEmpty(root.RootRelativePath),
				dashIfEmpty(root.SyncMode),
				dashIfEmpty(root.IndexMode),
				dashIfEmpty(root.BackupMode),
				dashIfEmpty(root.WorkerKey),
			)
		}
		_ = writer.Flush()
	}
	if len(plan.Excluded) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Excluded areas:")
		for _, excluded := range plan.Excluded {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s\n", excluded.Area, excluded.Reason)
		}
	}
	if len(plan.Commands) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Node-agent apply:")
		fmt.Fprintln(cmd.OutOrStdout(), "  loom box watch-plan --json > /tmp/loom-box-watch-plan.json")
		renderBoxWatchShell(cmd, plan.Commands)
	}
	renderBoxWatchDiagnostics(cmd, plan.Diagnostics)
}

func renderBoxWatchApply(cmd *cobra.Command, result box.WatchApplyResult) {
	action := "recorded"
	if result.DryRun {
		action = "dry-run"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Box watch policy: %s\n", action)
	renderBoxWatchPlan(cmd, result.Plan)
	renderBoxWatchRegistrations(cmd, result.Registrations)
}

func renderBoxWatchStatus(cmd *cobra.Command, result box.WatchStatusResult) {
	fmt.Fprintln(cmd.OutOrStdout(), "Box watch status")
	renderBoxWatchPlan(cmd, result.Plan)
	renderBoxWatchRegistrations(cmd, result.Registrations)
	if len(result.Statuses) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Node-agent reports:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "ROOT\tSTATUS\tWORKER\tLAST_REPORT")
		for _, status := range result.Statuses {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n",
				dashIfEmpty(status.Root.RootKey),
				dashIfEmpty(status.Root.Status),
				dashIfEmpty(status.Root.WorkerKey),
				status.Root.LastReportedAt.Format(time.RFC3339),
			)
		}
		_ = writer.Flush()
	}
}

func renderBoxWatchRegistrations(cmd *cobra.Command, registrations []box.WatchRootRegistration) {
	if len(registrations) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Box watched-root registrations:")
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "AREA\tROOT\tNODE\tSTATUS\tSYNC\tINDEX\tBACKUP\tREPORTED")
	for _, registration := range registrations {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			registration.AreaKey,
			registration.BackendRootKey,
			registration.OwnerNodeKey,
			registration.ActivationStatus,
			registration.SyncMode,
			registration.IndexMode,
			registration.BackupMode,
			timePtrOrDash(registration.LastReportedAt),
		)
	}
	_ = writer.Flush()
}

func renderBoxWatchShell(cmd *cobra.Command, commands []projects.ProjectWatchedRootCommand) {
	for _, command := range commands {
		if command.Description != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  # %s\n", command.Description)
		}
		if strings.TrimSpace(command.Shell) != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", command.Shell)
		}
	}
}

func renderBoxWatchDiagnostics(cmd *cobra.Command, diagnostics []box.Diagnostic) {
	if len(diagnostics) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Diagnostics:")
	for _, diagnostic := range diagnostics {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
		if diagnostic.Path != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "    path: %s\n", diagnostic.Path)
		}
		if diagnostic.Suggestion != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "    suggestion: %s\n", diagnostic.Suggestion)
		}
	}
}

func renderBoxPathTable(cmd *cobra.Command, title string, rows []box.PathStatus) {
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s:\n", title)
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "KEY\tSTATUS\tPATH")
	for _, row := range rows {
		fmt.Fprintf(writer, "%s\t%s\t%s\n", row.Key, row.Status, row.RelativePath)
	}
	_ = writer.Flush()
}

func renderStringList(cmd *cobra.Command, title string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s:\n", title)
	for _, value := range values {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", value)
	}
}

func shellQuoteIfNeeded(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"\\$`") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
