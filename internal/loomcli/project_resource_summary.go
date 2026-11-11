package loomcli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	pc "loom.local/loom/internal/projectcontracts"
)

func projectResourceSummaryKeys(status pc.DeclarationStatus) []pc.ResourceKey {
	keys := make([]pc.ResourceKey, 0, len(status.Resources))
	for key := range status.Resources {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a := status.Resources[keys[i]].Binding.Kind == pc.DeclarationApplication
		b := status.Resources[keys[j]].Binding.Kind == pc.DeclarationApplication
		if a != b {
			return a
		}
		return keys[i] < keys[j]
	})
	return keys
}

func projectResourceContext(key string, resource pc.DeclarationResourceStatus, status pc.DeclarationStatus, omitted map[string]int) contextResource {
	r := resource.Readiness
	summary := contextResource{Key: key, Kind: resource.Binding.Kind,
		Applied: contextResourceFact(r.Applied, status, omitted), Healthy: contextResourceFact(r.Healthy, status, omitted),
		Protected: contextResourceFact(r.Protected, status, omitted), Verified: contextResourceFact(r.Verified, status, omitted)}
	if declarationDisplayText(r.Desired.Revision) {
		summary.DesiredRevision = r.Desired.Revision
	} else {
		omitted["resource_detail"]++
	}
	if endpoint := r.Endpoint; endpoint != nil {
		if declarationDisplayText(endpoint.URL, endpoint.DNS, endpoint.TLS, endpoint.Routing) {
			copy := *endpoint
			summary.Endpoint = &copy
			summary.PublicationCoverage = "endpoint_only; revision_not_reported"
		} else {
			omitted["publication"]++
		}
	}
	return summary
}

func projectResourceReadAt(value string) string {
	if len(value) > 64 {
		return ""
	}
	return contextReadAt(value)
}

func projectResourceEvidenceLine(readAt string) string {
	if readAt = projectResourceReadAt(readAt); readAt == "" {
		readAt = "unreported"
	}
	return "Resource evidence: resource-only; owner revisions are not declaration proof; read at " + readAt + "; fact observation times not reported."
}

func projectResourceSummaryLines(resource contextResource) []string {
	fact := func(f contextFact) string {
		if f.State == pc.DeclarationUnknown {
			return string(f.State)
		}
		revision := f.Revision
		if revision == "" {
			revision = "unreported"
		}
		return string(f.State) + "@" + revision
	}
	var line string
	if resource.Applied.State == pc.DeclarationUnknown && resource.Healthy.State == pc.DeclarationUnknown && resource.Protected.State == pc.DeclarationUnknown && resource.Verified.State == pc.DeclarationUnknown {
		line = fmt.Sprintf("Resource: %s (%s); applied/healthy/protected/verified=unknown", resource.Key, resource.Kind)
	} else {
		line = fmt.Sprintf("Resource: %s (%s); applied=%s; healthy=%s; protected=%s; verified=%s", resource.Key, resource.Kind, fact(resource.Applied), fact(resource.Healthy), fact(resource.Protected), fact(resource.Verified))
	}
	lines := []string{line}
	if e := resource.Endpoint; e != nil {
		observed := "unreported"
		if !e.ObservedAt.IsZero() {
			observed = e.ObservedAt.UTC().Format(time.RFC3339Nano)
		}
		lines = append(lines, fmt.Sprintf("Publication: %s %s; DNS=%s TLS=%s Route=%s HTTP=%d; observed=%s; endpoint-only, revision unreported", resource.Key, e.URL, e.DNS, e.TLS, e.Routing, e.HTTPStatus, observed))
	}
	return lines
}

func renderProjectResourceSummary(cmd *cobra.Command, status pc.DeclarationStatus) {
	fmt.Fprintln(cmd.OutOrStdout(), "Current resource evidence (application resources first):")
	fmt.Fprintln(cmd.OutOrStdout(), projectResourceEvidenceLine(status.ObservedAt))
	omitted := map[string]int{}
	if status.ObservedAt != "" && projectResourceReadAt(status.ObservedAt) == "" {
		omitted["read_time"]++
	}
	shown, bytes := 0, 0
	for _, key := range projectResourceSummaryKeys(status) {
		resource := status.Resources[key]
		if shown >= 4 || !declarationDisplayText(string(key), string(resource.Binding.Kind)) {
			omitted["resources"]++
			continue
		}
		lines := projectResourceSummaryLines(projectResourceContext(string(key), resource, status, omitted))
		// Reserve navigation and qualifications outside the 2 KiB detail budget.
		if bytes+len(lines[0])+1 > 2048 {
			omitted["resources"]++
			continue
		}
		fmt.Fprintln(cmd.OutOrStdout(), lines[0])
		bytes += len(lines[0]) + 1
		shown++
		if len(lines) > 1 {
			if bytes+len(lines[1])+1 <= 2048 {
				fmt.Fprintln(cmd.OutOrStdout(), lines[1])
				bytes += len(lines[1]) + 1
			} else {
				omitted["publication"]++
			}
		}
	}
	if len(status.Resources) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Resources: none reported; resource evidence unknown.")
	}
	if len(omitted) > 0 {
		keys := make([]string, 0, len(omitted))
		for key := range omitted {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", key, omitted[key]))
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Omitted: "+strings.Join(parts, ", "))
	}
	detail := declarationInspectAction(status.Target, "", "").Command
	if detail == "loom project context --help" {
		fmt.Fprintln(cmd.OutOrStdout(), "Resource detail: scope unknown or undisplayable; "+detail)
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Resource detail: "+detail)
	}
}
