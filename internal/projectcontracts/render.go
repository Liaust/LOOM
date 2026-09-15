package projectcontracts

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

func RenderValidation(w io.Writer, report ValidationReport) {
	status := "failed"
	if report.OK {
		status = "ok"
	}
	fmt.Fprintf(w, "Project contract: %s\n", status)
	if report.Project.Slug != "" {
		fmt.Fprintf(w, "Project: %s\n", report.Project.Slug)
	}
	if report.Project.OwnerNode != "" {
		fmt.Fprintf(w, "Owner node: %s\n", report.Project.OwnerNode)
	}
	if provider := firstProvider(report.DerivedProviders); provider != "" {
		fmt.Fprintf(w, "Provider: %s\n", provider)
	}
	if facets := enabledFacetKeys(report.Facets); len(facets) > 0 {
		fmt.Fprintf(w, "Facets: %s\n", strings.Join(facets, ", "))
	}
	if discovered := len(report.Notes); discovered > 0 {
		fmt.Fprintf(w, "Notes: %d discovered\n", discovered)
	}
	if discovered := len(report.Repos); discovered > 0 {
		fmt.Fprintf(w, "Repos: %d discovered\n", discovered)
	}
	if discovered, exposed := scriptSummary(report.Scripts); discovered > 0 {
		fmt.Fprintf(w, "Scripts: %d discovered, %d exposed\n", discovered, exposed)
	}
	if capabilities := scriptCapabilityAddresses(report.Scripts); len(capabilities) > 0 {
		fmt.Fprintf(w, "Capabilities: %s\n", strings.Join(capabilities, ", "))
	}
	if discovered, executable, scriptBacked, blocked := workflowSummary(report.Workflows); discovered > 0 {
		fmt.Fprintf(w, "Workflows: %d discovered, %d executable, %d script-backed, %d blocked\n", discovered, executable, scriptBacked, blocked)
		for _, summary := range workflowSummaries(report.Workflows) {
			fmt.Fprintf(w, "  - %s\n", summary)
		}
	}
	if discovered, activatable := connectorSummary(report.Connectors); discovered > 0 {
		fmt.Fprintf(w, "Connectors: %d discovered, %d activatable endpoints\n", discovered, activatable)
		for _, summary := range connectorProviderSummaries(report.Connectors) {
			fmt.Fprintf(w, "  - %s\n", summary)
		}
	}
	if capabilities := connectorCapabilityAddresses(report.Connectors); len(capabilities) > 0 {
		fmt.Fprintf(w, "Connector capabilities: %s\n", strings.Join(capabilities, ", "))
	}
	if discovered, registrable := moduleSummary(report.Modules); discovered > 0 {
		fmt.Fprintf(w, "Modules: %d discovered, %d registrable\n", discovered, registrable)
		for _, summary := range modulePackageSummaries(report.Modules) {
			fmt.Fprintf(w, "  - %s\n", summary)
		}
	}
	if discovered := scheduleSummary(report.Schedules); discovered > 0 {
		fmt.Fprintf(w, "Schedules: %d discovered\n", discovered)
		for _, summary := range scheduleTargetSummaries(report.Schedules) {
			fmt.Fprintf(w, "  - %s\n", summary)
		}
	}
	if discovered := directEventSummary(report.DirectEvents); discovered > 0 {
		fmt.Fprintf(w, "Direct events: %d discovered\n", discovered)
		for _, summary := range directEventTargetSummaries(report.DirectEvents) {
			fmt.Fprintf(w, "  - %s\n", summary)
		}
	}
	if len(report.WatchedRoots) > 0 {
		fmt.Fprintf(w, "Watched roots: %d planned\n", len(report.WatchedRoots))
		for _, summary := range watchedRootSummaries(report.WatchedRoots) {
			fmt.Fprintf(w, "  - %s\n", summary)
		}
	}
	fmt.Fprintf(w, "Diagnostics: %d errors, %d warnings", report.Summary.Errors, report.Summary.Warnings)
	if report.Summary.Infos > 0 {
		fmt.Fprintf(w, ", %d infos", report.Summary.Infos)
	}
	fmt.Fprintln(w)
	RenderDiagnostics(w, report.Diagnostics)
}

func RenderPlan(w io.Writer, plan ProjectPlan, includeDiagnostics bool) {
	status := "blocked"
	if plan.Registerable {
		status = "ready"
	}
	if plan.Project.Slug != "" {
		fmt.Fprintf(w, "Project plan: %s (%s)\n", plan.Project.Slug, status)
	} else {
		fmt.Fprintf(w, "Project plan: %s\n", status)
	}
	if provider := firstProvider(plan.DerivedProviders); provider != "" {
		fmt.Fprintf(w, "Provider: %s\n", provider)
	}
	if facets := enabledFacetKeys(plan.Facets); len(facets) > 0 {
		fmt.Fprintf(w, "Facets: %s\n", strings.Join(facets, ", "))
	}
	if discovered := len(plan.Notes); discovered > 0 {
		fmt.Fprintf(w, "Notes: %d discovered\n", discovered)
	}
	if discovered := len(plan.Repos); discovered > 0 {
		fmt.Fprintf(w, "Repos: %d discovered\n", discovered)
	}
	if discovered, exposed := scriptSummary(plan.Scripts); discovered > 0 {
		fmt.Fprintf(w, "Scripts: %d discovered, %d exposed\n", discovered, exposed)
	}
	if capabilities := scriptCapabilityAddresses(plan.Scripts); len(capabilities) > 0 {
		fmt.Fprintf(w, "Capabilities: %s\n", strings.Join(capabilities, ", "))
	}
	if discovered, executable, scriptBacked, blocked := workflowSummary(plan.Workflows); discovered > 0 {
		fmt.Fprintf(w, "Workflows: %d discovered, %d executable, %d script-backed, %d blocked\n", discovered, executable, scriptBacked, blocked)
		for _, summary := range workflowSummaries(plan.Workflows) {
			fmt.Fprintf(w, "  - %s\n", summary)
		}
	}
	if discovered, activatable := connectorSummary(plan.Connectors); discovered > 0 {
		fmt.Fprintf(w, "Connectors: %d discovered, %d activatable endpoints\n", discovered, activatable)
		for _, summary := range connectorProviderSummaries(plan.Connectors) {
			fmt.Fprintf(w, "  - %s\n", summary)
		}
	}
	if capabilities := connectorCapabilityAddresses(plan.Connectors); len(capabilities) > 0 {
		fmt.Fprintf(w, "Connector capabilities: %s\n", strings.Join(capabilities, ", "))
	}
	if discovered, registrable := moduleSummary(plan.Modules); discovered > 0 {
		fmt.Fprintf(w, "Modules: %d discovered, %d registrable\n", discovered, registrable)
		for _, summary := range modulePackageSummaries(plan.Modules) {
			fmt.Fprintf(w, "  - %s\n", summary)
		}
	}
	if discovered := scheduleSummary(plan.Schedules); discovered > 0 {
		fmt.Fprintf(w, "Schedules: %d discovered\n", discovered)
		for _, summary := range scheduleTargetSummaries(plan.Schedules) {
			fmt.Fprintf(w, "  - %s\n", summary)
		}
	}
	if discovered := directEventSummary(plan.DirectEvents); discovered > 0 {
		fmt.Fprintf(w, "Direct events: %d discovered\n", discovered)
		for _, summary := range directEventTargetSummaries(plan.DirectEvents) {
			fmt.Fprintf(w, "  - %s\n", summary)
		}
	}
	if len(plan.WatchedRoots) > 0 {
		fmt.Fprintf(w, "Watched roots: %d planned\n", len(plan.WatchedRoots))
		for _, summary := range watchedRootSummaries(plan.WatchedRoots) {
			fmt.Fprintf(w, "  - %s\n", summary)
		}
	}
	fmt.Fprintf(w, "Diagnostics: %d errors, %d warnings\n", plan.Summary.Errors, plan.Summary.Warnings)
	if len(plan.Actions) > 0 {
		writer := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "ACTION\tTARGET\tSTATUS")
		for _, action := range plan.Actions {
			fmt.Fprintf(writer, "%s\t%s %s\t%s\n", action.Action, action.TargetKind, action.TargetRef, action.Status)
		}
		_ = writer.Flush()
	}
	if includeDiagnostics {
		RenderDiagnostics(w, plan.Diagnostics)
	}
}

func RenderDiagnostics(w io.Writer, diagnostics []Diagnostic) {
	for _, diag := range diagnostics {
		field := diag.Field
		if field == "" {
			field = "-"
		}
		fmt.Fprintf(w, "%s %s %s %s\n", diag.Severity, diag.Code, field, diag.Message)
		if diag.Suggestion != "" {
			fmt.Fprintf(w, "  suggestion: %s\n", diag.Suggestion)
		}
	}
}

func firstProvider(providers []PlanProvider) string {
	if len(providers) == 0 {
		return ""
	}
	return providers[0].CompactAddress
}

func enabledFacetKeys(facets []PlanFacet) []string {
	out := []string{}
	for _, facet := range facets {
		if facet.Enabled {
			out = append(out, facet.Key)
		}
	}
	return out
}
