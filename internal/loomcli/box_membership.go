package loomcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/projectapply"
	"loom.local/loom/internal/projects"
)

var boxMembershipDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type projectBoxMembershipPlan struct {
	Kind   string `json:"kind"`
	DryRun bool   `json:"dry_run"`
	projects.BoxMembershipReconcileResult
}

type sourceBoxMembershipPlan struct {
	Kind   string `json:"kind"`
	DryRun bool   `json:"dry_run"`
	knowledge.SourceBoxMembershipResult
}

func newBoxMembershipCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "membership", Short: "Inspect and reconcile exact Box metadata membership"}
	var inventoryKind string
	var page projects.BoxMembershipInventoryInput
	inventory := &cobra.Command{Use: "inventory", Args: cobra.NoArgs, Short: "List one bounded page of registry IDs", RunE: func(cmd *cobra.Command, args []string) error {
		prefix, err := boxMembershipPrefix(inventoryKind)
		if err != nil {
			return err
		}
		if page.Limit < 1 || page.Limit > projects.MaxBoxMembershipBatch || page.After != "" && ids.Validate(prefix, page.After) != nil {
			return fmt.Errorf("--limit must be 1-100 and --after must be an exact %s ID", inventoryKind)
		}
		cc, err := resolveCommandContext(opts)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		result, err := cc.Client.BoxMembershipInventory(ctx, cc.CorrelationID, inventoryKind, page)
		if err != nil {
			return err
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Data)
		}
		for _, id := range result.Data.IDs {
			fmt.Fprintln(cmd.OutOrStdout(), id)
		}
		if result.Data.NextAfter != "" && !opts.plainOutput {
			fmt.Fprintln(cmd.OutOrStdout(), "Next after:", result.Data.NextAfter)
		}
		return nil
	}}
	inventory.Flags().StringVar(&inventoryKind, "kind", "", "projects or sources")
	inventory.Flags().StringVar(&page.After, "after", "", "exact registry ID from the preceding page")
	inventory.Flags().IntVar(&page.Limit, "limit", 100, "maximum registry IDs (1-100)")

	var planKind string
	var exactIDs []string
	plan := &cobra.Command{Use: "plan", Args: cobra.NoArgs, Short: "Preview membership for exact registry IDs", RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateBoxMembershipSelection(planKind, exactIDs); err != nil {
			return err
		}
		return runBoxMembershipReconcile(cmd, opts, planKind, exactIDs, false, "", nil)
	}}
	plan.Flags().StringVar(&planKind, "kind", "", "projects or sources")
	plan.Flags().StringSliceVar(&exactIDs, "ids", nil, "comma-separated exact registry IDs (1-100)")

	var applyKind, planFile string
	var yes bool
	apply := &cobra.Command{Use: "apply", Args: cobra.NoArgs, Short: "Apply the exact reviewed metadata plan", RunE: func(cmd *cobra.Command, args []string) error {
		if !yes || planFile == "" {
			return fmt.Errorf("membership apply requires --plan-file and --yes")
		}
		selected, catalog, evidence, err := readBoxMembershipPlan(planFile, applyKind)
		if err != nil {
			return err
		}
		return runBoxMembershipReconcile(cmd, opts, applyKind, selected, true, catalog, evidence)
	}}
	apply.Flags().StringVar(&applyKind, "kind", "", "projects or sources")
	apply.Flags().StringVar(&planFile, "plan-file", "", "exact JSON output from membership plan --json")
	apply.Flags().BoolVar(&yes, "yes", false, "confirm only the exact reviewed metadata plan")
	cmd.AddCommand(inventory, plan, apply)
	return cmd
}

func boxMembershipPrefix(kind string) (string, error) {
	switch kind {
	case "projects":
		return ids.ProjectPrefix, nil
	case "sources":
		return ids.NotesSourceRootPrefix, nil
	default:
		return "", fmt.Errorf("--kind must be projects or sources")
	}
}

func validateBoxMembershipSelection(kind string, values []string) error {
	prefix, err := boxMembershipPrefix(kind)
	if err != nil {
		return err
	}
	if len(values) < 1 || len(values) > projects.MaxBoxMembershipBatch {
		return fmt.Errorf("supply 1-100 unique exact registry IDs")
	}
	seen := map[string]bool{}
	for _, id := range values {
		if ids.Validate(prefix, id) != nil || seen[id] {
			return fmt.Errorf("supply unique exact %s IDs, not names or paths", kind)
		}
		seen[id] = true
	}
	return nil
}

func runBoxMembershipReconcile(cmd *cobra.Command, opts *options, kind string, selected []string, apply bool, catalog string, evidence map[string]string) error {
	cc, err := resolveCommandContext(opts)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	if kind == "projects" {
		result, err := cc.Client.ReconcileProjectBoxMembership(ctx, cc.CorrelationID, projects.BoxMembershipReconcileInput{ProjectIDs: selected, Apply: apply, CatalogDigest: catalog, Evidence: evidence})
		if err != nil {
			return err
		}
		returned := make([]string, 0, len(result.Data.Changes))
		for _, change := range result.Data.Changes {
			returned = append(returned, change.ProjectID)
		}
		if err := validateBoxMembershipResponse(kind, selected, returned); err != nil {
			return err
		}
		if opts.jsonOutput {
			if !apply {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(projectBoxMembershipPlan{Kind: kind, DryRun: true, BoxMembershipReconcileResult: result.Data})
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Data)
		}
		for _, change := range result.Data.Changes {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", change.ProjectID, change.Status, change.HomeBoxID, change.Reason)
		}
		return nil
	}
	result, err := cc.Client.ReconcileSourceBoxMembership(ctx, cc.CorrelationID, knowledge.SourceBoxMembershipInput{SourceRootIDs: selected, Apply: apply, CatalogDigest: catalog, Evidence: evidence})
	if err != nil {
		return err
	}
	returned := make([]string, 0, len(result.Data.Changes))
	for _, change := range result.Data.Changes {
		returned = append(returned, change.SourceRootID)
	}
	if err := validateBoxMembershipResponse(kind, selected, returned); err != nil {
		return err
	}
	if opts.jsonOutput {
		if !apply {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(sourceBoxMembershipPlan{Kind: kind, DryRun: true, SourceBoxMembershipResult: result.Data})
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Data)
	}
	for _, change := range result.Data.Changes {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", change.SourceRootID, change.Status, change.BoxID, change.Reason)
	}
	return nil
}

func validateBoxMembershipResponse(kind string, selected, returned []string) error {
	if err := validateBoxMembershipSelection(kind, returned); err != nil {
		return fmt.Errorf("membership response did not preserve the exact ID selection")
	}
	if len(selected) != len(returned) {
		return fmt.Errorf("membership response did not preserve the exact ID selection")
	}
	wanted := make(map[string]bool, len(selected))
	for _, id := range selected {
		wanted[id] = true
	}
	for _, id := range returned {
		if !wanted[id] {
			return fmt.Errorf("membership response broadened the exact ID selection")
		}
	}
	return nil
}

func readBoxMembershipPlan(path, kind string) ([]string, string, map[string]string, error) {
	if _, err := boxMembershipPrefix(kind); err != nil {
		return nil, "", nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", nil, fmt.Errorf("membership plan unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, "", nil, fmt.Errorf("membership plan must be a bounded regular JSON file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return nil, "", nil, fmt.Errorf("membership plan exceeds bound")
	}
	if _, err := projectapply.CanonicalJSON(raw); err != nil {
		return nil, "", nil, fmt.Errorf("membership plan is not canonicalizable JSON")
	}
	var values []string
	var catalog string
	evidence := map[string]string{}
	add := func(id, digest, status, boxID string) error {
		if !boxMembershipDigest.MatchString(digest) || evidence[id] != "" {
			return fmt.Errorf("plan requires unique exact IDs and per-ID evidence digests")
		}
		switch status {
		case "would_assign", "unchanged":
			if boxID == "" {
				return fmt.Errorf("planned membership lacks Box identity")
			}
		case "unassigned", "conflict":
		default:
			return fmt.Errorf("plan must contain dry-run statuses, not applied assignments")
		}
		values = append(values, id)
		evidence[id] = digest
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if kind == "projects" {
		var plan projectBoxMembershipPlan
		if err := d.Decode(&plan); err != nil || plan.Kind != kind || !plan.DryRun {
			return nil, "", nil, fmt.Errorf("expected exact projects dry-run JSON")
		}
		catalog = plan.CatalogDigest
		for _, change := range plan.Changes {
			if err := add(change.ProjectID, change.EvidenceDigest, change.Status, change.HomeBoxID); err != nil {
				return nil, "", nil, err
			}
		}
	} else {
		var plan sourceBoxMembershipPlan
		if err := d.Decode(&plan); err != nil || plan.Kind != kind || !plan.DryRun {
			return nil, "", nil, fmt.Errorf("expected exact sources dry-run JSON")
		}
		catalog = plan.CatalogDigest
		for _, change := range plan.Changes {
			if err := add(change.SourceRootID, change.EvidenceDigest, change.Status, change.BoxID); err != nil {
				return nil, "", nil, err
			}
		}
	}
	if !boxMembershipDigest.MatchString(catalog) {
		return nil, "", nil, fmt.Errorf("plan requires an exact catalog digest")
	}
	if err := validateBoxMembershipSelection(kind, values); err != nil {
		return nil, "", nil, err
	}
	return values, catalog, evidence, nil
}
