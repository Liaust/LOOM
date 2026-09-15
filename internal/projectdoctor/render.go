package projectdoctor

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

func RenderReport(w io.Writer, report Report) {
	status := "ok"
	if HasFailures(report) {
		status = "failed"
	} else if HasWarnings(report) {
		status = "warning"
	}
	fmt.Fprintf(w, "Project doctor: %s\n", status)
	if report.ProjectRef != "" {
		fmt.Fprintf(w, "Project: %s\n", report.ProjectRef)
	}
	fmt.Fprintf(w, "Checks: %d ok, %d warnings, %d errors, %d blocked, %d skipped\n",
		report.Summary.OK,
		report.Summary.Warnings,
		report.Summary.Errors,
		report.Summary.Blocked,
		report.Summary.Skipped,
	)
	if len(report.Checks) == 0 {
		fmt.Fprintln(w, "No checks were produced.")
		return
	}
	fmt.Fprintln(w, "")
	writer := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "STATUS\tCHECK\tSUBJECT\tSUMMARY")
	for _, check := range report.Checks {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n",
			check.Status,
			firstNonEmpty(check.Title, check.Key),
			firstNonEmpty(check.SubjectRef, check.Facet, "-"),
			check.Summary,
		)
	}
	_ = writer.Flush()
	remediations := []string{}
	for _, check := range report.Checks {
		if strings.TrimSpace(check.Remediation) != "" {
			remediations = append(remediations, check.Remediation)
		}
	}
	if len(remediations) > 0 {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Remediation:")
		seen := map[string]bool{}
		for _, remediation := range remediations {
			if seen[remediation] {
				continue
			}
			seen[remediation] = true
			fmt.Fprintf(w, "  - %s\n", remediation)
		}
	}
}

func RenderDiff(w io.Writer, report DiffReport, includeUnchanged bool) {
	fmt.Fprintln(w, "Project diff")
	if report.ProjectRef != "" {
		fmt.Fprintf(w, "Project: %s\n", report.ProjectRef)
	}
	if report.LocalRoot != "" {
		fmt.Fprintf(w, "Local root: %s\n", report.LocalRoot)
	}
	fmt.Fprintf(w, "Summary: %d changed, %d added, %d removed, %d missing, %d unchanged\n",
		report.Summary.Changed,
		report.Summary.Added,
		report.Summary.Removed,
		report.Summary.Missing,
		report.Summary.Unchanged,
	)
	items := []DiffItem{}
	for _, item := range report.Items {
		if item.Status == DiffUnchanged && !includeUnchanged {
			continue
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		fmt.Fprintln(w, "No drift detected.")
		return
	}
	fmt.Fprintln(w, "")
	writer := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "STATUS\tKIND\tKEY\tLOCAL\tREGISTERED\tSUMMARY")
	for _, item := range items {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			item.Status,
			item.Kind,
			item.Key,
			firstNonEmpty(item.LocalRef, item.LocalHash, "-"),
			firstNonEmpty(item.RegisteredRef, item.RegisteredHash, "-"),
			item.Summary,
		)
	}
	_ = writer.Flush()
}

func DiffHasChanges(report DiffReport) bool {
	return report.Summary.HasChanges()
}
