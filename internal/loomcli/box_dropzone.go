package loomcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/dropzone"
)

type boxDropzoneFlags struct {
	boxCommandFlags
}

type boxDropzoneContext struct {
	Status box.Status
	Reader dropzone.Reader
}

type boxDropzoneListResult struct {
	Status    dropzone.Status            `json:"status"`
	Transfers []dropzone.TransferSummary `json:"transfers"`
}

type boxDropzoneInspectResult struct {
	Record dropzone.TransferRecord `json:"record"`
}

func newBoxDropzoneCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "dropzone",
		Short:  "Inspect deprecated historical Dropzone evidence",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newBoxDropzoneStatusCommand(opts))
	cmd.AddCommand(newBoxDropzoneListCommand(opts))
	cmd.AddCommand(newBoxDropzoneInspectCommand(opts))
	return cmd
}

func newBoxDropzoneStatusCommand(opts *options) *cobra.Command {
	flags := boxDropzoneFlags{}
	var includeFidelity bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show read-only historical Dropzone transfer status",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := loadBoxDropzoneContext(opts, flags.boxCommandFlags)
			if err != nil {
				return err
			}
			status := dropzoneStatusValue(ctx.Status)
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			}
			renderDropzoneStatus(cmd, ctx.Status, status)
			if includeFidelity {
				renderDropzoneFidelityStatus(cmd, status)
			}
			return nil
		},
	}
	addBoxFlags(cmd, &flags.boxCommandFlags)
	cmd.Flags().BoolVar(&includeFidelity, "include-fidelity", false, "show historical transfer fidelity warnings")
	return cmd
}

func newBoxDropzoneListCommand(opts *options) *cobra.Command {
	flags := boxDropzoneFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List read-only historical Dropzone transfer records",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := loadBoxDropzoneContext(opts, flags.boxCommandFlags)
			if err != nil {
				return err
			}
			records, err := ctx.Reader.List()
			if err != nil {
				return err
			}
			result := boxDropzoneListResult{Status: dropzoneStatusValue(ctx.Status), Transfers: summarizeTransferRecords(records)}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			renderDropzoneList(cmd, ctx.Status, result)
			return nil
		},
	}
	addBoxFlags(cmd, &flags.boxCommandFlags)
	return cmd
}

func newBoxDropzoneInspectCommand(opts *options) *cobra.Command {
	flags := boxDropzoneFlags{}
	cmd := &cobra.Command{
		Use:   "inspect <transfer-id>",
		Short: "Inspect one historical Dropzone transfer record",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := loadBoxDropzoneContext(opts, flags.boxCommandFlags)
			if err != nil {
				return err
			}
			record, err := resolveDropzoneRecord(ctx.Reader, args[0])
			if err != nil {
				return err
			}
			result := boxDropzoneInspectResult{Record: record}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			renderDropzoneInspect(cmd, record)
			return nil
		},
	}
	addBoxFlags(cmd, &flags.boxCommandFlags)
	return cmd
}

func loadBoxDropzoneContext(opts *options, flags boxCommandFlags) (boxDropzoneContext, error) {
	resolved, err := resolveBoxForCLI(opts, flags)
	if err != nil {
		return boxDropzoneContext{}, err
	}
	status := box.Inspect(resolved)
	if status.State != "ok" {
		return boxDropzoneContext{}, fmt.Errorf("LOOM Box is not ready at %s; run loom box status", status.RootPath)
	}
	policy := dropzone.DefaultPolicy(status.Profile)
	diagnostics := []dropzone.Diagnostic{}
	if status.Contract != nil {
		if policyRel := status.Contract.Policies[box.AreaDropzone]; strings.TrimSpace(policyRel) != "" {
			policy, diagnostics = dropzone.LoadPolicy(filepath.Join(status.RootPath, filepath.FromSlash(policyRel)), status.Profile)
			for _, diagnostic := range diagnostics {
				if diagnostic.Severity == "error" {
					return boxDropzoneContext{}, fmt.Errorf("historical Dropzone policy invalid: %s", diagnostic.Message)
				}
			}
		}
	}
	if strings.TrimSpace(status.RuntimeStateReadRoot) == "" {
		return boxDropzoneContext{}, fmt.Errorf("historical Box runtime state is unavailable at %s", status.RootPath)
	}
	reader := dropzone.NewReader(status.RootPath, policy, filepath.Join(status.RuntimeStateReadRoot, "dropzone"))
	if status.DropzoneTransfers == nil {
		historical := dropzone.BuildStatus(dropzone.StatusInput{
			RootPath:    status.RootPath,
			StateRoot:   filepath.Join(status.RuntimeStateReadRoot, "dropzone"),
			Profile:     status.Profile,
			Policy:      policy,
			Diagnostics: diagnostics,
		})
		status.DropzoneTransfers = &historical
	}
	return boxDropzoneContext{Status: status, Reader: reader}, nil
}

func dropzoneStatusValue(status box.Status) dropzone.Status {
	if status.DropzoneTransfers == nil {
		return dropzone.Status{
			RuntimeState: dropzone.RuntimeRetired,
			Profile:      status.Profile,
			RootPath:     status.RootPath,
			Counts:       map[string]int{},
			InspectedAt:  status.InspectedAt,
		}
	}
	return *status.DropzoneTransfers
}

func summarizeTransferRecords(records []dropzone.TransferRecord) []dropzone.TransferSummary {
	summaries := make([]dropzone.TransferSummary, 0, len(records))
	for _, record := range records {
		summaries = append(summaries, dropzone.Summarize(record))
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})
	return summaries
}

func resolveDropzoneRecord(reader dropzone.Reader, ref string) (dropzone.TransferRecord, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return dropzone.TransferRecord{}, errors.New("transfer reference is required")
	}
	if record, err := reader.Load(ref); err == nil {
		return record, nil
	}
	records, err := reader.List()
	if err != nil {
		return dropzone.TransferRecord{}, err
	}
	matches := []dropzone.TransferRecord{}
	for _, record := range records {
		if record.TransferID == ref || record.RelativeDropzonePath == ref || strings.HasPrefix(record.TransferID, ref) {
			matches = append(matches, record)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return dropzone.TransferRecord{}, fmt.Errorf("transfer reference %q matched %d records; use the full transfer_id", ref, len(matches))
	}
	return dropzone.TransferRecord{}, fmt.Errorf("historical Dropzone transfer %q not found", ref)
}

func renderDropzoneStatus(cmd *cobra.Command, boxStatus box.Status, status dropzone.Status) {
	fmt.Fprintf(cmd.OutOrStdout(), "Dropzone: %s\n", dashIfEmpty(status.RuntimeState))
	fmt.Fprintln(cmd.OutOrStdout(), "Compatibility: deprecated, historical inspection only")
	fmt.Fprintf(cmd.OutOrStdout(), "Box: %s\n", dashIfEmpty(boxStatus.RootPath))
	fmt.Fprintf(cmd.OutOrStdout(), "Profile: %s\n", dashIfEmpty(status.Profile))
	fmt.Fprintf(cmd.OutOrStdout(), "Historical path: %s\n", dashIfEmpty(status.DropzonePath))
	renderDropzoneCounts(cmd, status.Counts)
	renderDropzoneSummaryGroups(cmd, status)
	renderDropzoneDiagnostics(cmd, status.Diagnostics)
}

func renderDropzoneFidelityStatus(cmd *cobra.Command, status dropzone.Status) {
	activeWarnings := dropzoneFidelityWarningCount(status.Active)
	acceptedWarnings := dropzoneFidelityWarningCount(status.Accepted)
	failedWarnings := dropzoneFidelityWarningCount(status.Failed)
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), "Fidelity:")
	fmt.Fprintf(cmd.OutOrStdout(), "  active_warning_transfers=%d accepted_warning_transfers=%d failed_warning_transfers=%d\n", activeWarnings, acceptedWarnings, failedWarnings)
	renderDropzoneTransferFidelityWarnings(cmd, "active", status.Active)
	renderDropzoneTransferFidelityWarnings(cmd, "accepted", status.Accepted)
	renderDropzoneTransferFidelityWarnings(cmd, "failed", status.Failed)
}

func dropzoneFidelityWarningCount(transfers []dropzone.TransferSummary) int {
	count := 0
	for _, transfer := range transfers {
		if len(transfer.FidelityWarnings) > 0 {
			count++
		}
	}
	return count
}

func renderDropzoneTransferFidelityWarnings(cmd *cobra.Command, group string, transfers []dropzone.TransferSummary) {
	for _, transfer := range transfers {
		for _, warning := range transfer.FidelityWarnings {
			fmt.Fprintf(cmd.OutOrStdout(), "  warning group=%s transfer=%s path=%s %s\n", group, transfer.TransferID, transfer.RelativeDropzonePath, warning)
		}
	}
}

func renderDropzoneList(cmd *cobra.Command, boxStatus box.Status, result boxDropzoneListResult) {
	renderDropzoneStatus(cmd, boxStatus, result.Status)
	if len(result.Transfers) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No historical Dropzone transfer records.")
		return
	}
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), "Historical transfers:")
	for _, transfer := range result.Transfers {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s %s %s\n", transfer.Status, transfer.TransferID, transfer.RelativeDropzonePath)
	}
}

func renderDropzoneInspect(cmd *cobra.Command, record dropzone.TransferRecord) {
	fmt.Fprintf(cmd.OutOrStdout(), "Historical Dropzone transfer: %s\n", record.TransferID)
	fmt.Fprintf(cmd.OutOrStdout(), "File: %s\n", record.RelativeDropzonePath)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", record.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Bytes: %d/%d\n", record.UploadedBytes, record.FileSizeBytes)
	fmt.Fprintf(cmd.OutOrStdout(), "Safe to delete: %t\n", record.SafeToDelete)
	fmt.Fprintln(cmd.OutOrStdout(), "Mutation: unavailable (Dropzone runtime retired)")
}

func renderDropzoneCounts(cmd *cobra.Command, counts map[string]int) {
	if len(counts) == 0 {
		return
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s=%d\n", key, counts[key])
	}
}

func renderDropzoneSummaryGroups(cmd *cobra.Command, status dropzone.Status) {
	for _, group := range []struct {
		label  string
		values []dropzone.TransferSummary
	}{{"Historical active", status.Active}, {"Historical failed", status.Failed}, {"Historical accepted", status.Accepted}} {
		if len(group.values) == 0 {
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s:\n", group.label)
		for _, transfer := range group.values {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s %s %s\n", transfer.Status, transfer.TransferID, transfer.RelativeDropzonePath)
		}
	}
}

func renderDropzoneDiagnostics(cmd *cobra.Command, diagnostics []dropzone.Diagnostic) {
	for _, diagnostic := range diagnostics {
		fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", strings.ToUpper(diagnostic.Severity), diagnostic.Code, diagnostic.Message)
	}
}
