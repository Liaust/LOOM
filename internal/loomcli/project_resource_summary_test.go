package loomcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	pc "loom.local/loom/internal/projectcontracts"
)

func resourceSummaryOutput(t *testing.T, status pc.DeclarationStatus) string {
	t.Helper()
	before, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	renderProjectResourceSummary(cmd, status)
	after, err := json.Marshal(status)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("human summary mutated returned status", err)
	}
	if out.Len() > 4096 || strings.Count(out.String(), "\n") > 12 {
		t.Fatalf("resource summary exceeded budget: %d bytes: %s", out.Len(), out.String())
	}
	return out.String()
}

func TestProjectResourceSummaryApplicationFirstAndSharedQualifications(t *testing.T) {
	status := webDAVContextFixture()
	out := resourceSummaryOutput(t, status)
	if strings.Index(out, "Resource: webdav") > strings.Index(out, "Resource: files") {
		t.Fatal("application facts displaced by non-application resources", out)
	}
	for _, want := range []string{
		"applied=satisfied@installation-revision; healthy=satisfied@generation-revision; protected=satisfied@backup-manifest-revision; verified=unknown",
		"owner revisions are not declaration proof; read at 2026-10-12T08:00:00Z; fact observation times not reported",
		"DNS=satisfied TLS=satisfied Route=satisfied HTTP=207; observed=2026-10-12T07:59:00Z; endpoint-only, revision unreported",
		"Resource detail: loom project status 'verified-project' --node 'verified-owner' --json",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
	for _, resource := range newProjectContext(status).Resources {
		for _, line := range projectResourceSummaryLines(resource) {
			if !strings.Contains(out, line+"\n") {
				t.Fatalf("context/status wording differs: %s", line)
			}
		}
	}
	if strings.Contains(out, "unknown@") || strings.Contains(out, "old-operation") || strings.Contains(out, "Omitted:") {
		t.Fatal("invented evidence or omissions", out)
	}
}

func TestProjectResourceSummaryBoundedApplicationPriority(t *testing.T) {
	status := webDAVContextFixture()
	for i := 0; i < 100; i++ {
		status.Resources[pc.ResourceKey(fmt.Sprintf("a%03d", i))] = pc.DeclarationResourceStatus{Binding: pc.DeclarationBinding{Kind: pc.DeclarationKnowledge}}
	}
	out := resourceSummaryOutput(t, status)
	if strings.Count(out, "Resource: ") != 4 || !strings.Contains(out, "Resource: webdav") || !strings.Contains(out, "Omitted: resources=99") {
		t.Fatal("resource cap/omissions lost", out)
	}
	if resourceSummaryOutput(t, status) != out {
		t.Fatal("map iteration changed summary order")
	}
	for _, size := range []int{128, 700, 2048, 4096, 4097} {
		r := status.Resources["webdav"]
		r.Readiness.Healthy.Revision = strings.Repeat("x", size)
		r.Readiness.Endpoint.URL = "https://example.test/" + strings.Repeat("x", size)
		status.Resources["webdav"] = r
		out = resourceSummaryOutput(t, status)
		if !strings.Contains(out, "Omitted:") || !strings.Contains(out, "Resource detail: loom project status 'verified-project' --node 'verified-owner' --json") {
			t.Fatal("oversized details displaced disclosure/navigation", out)
		}
	}
}

func TestProjectResourceSummaryUnsafeAndUnknownEvidence(t *testing.T) {
	status := webDAVContextFixture()
	r := status.Resources["webdav"]
	r.Readiness.Applied.Revision = ""
	r.Readiness.Healthy.Revision = "unsafe\nINJECTED"
	r.Readiness.Protected.State = "unrecognized"
	r.Readiness.Verified.Revision = "unproven-revision"
	r.Readiness.Endpoint.URL = "unsafe\x1b[31m"
	status.Resources["webdav"] = r
	status.Resources["unsafe\nINJECTED"] = pc.DeclarationResourceStatus{}
	status.ObservedAt = "invalid\nINJECTED"
	out := resourceSummaryOutput(t, status)
	for _, want := range []string{
		"applied=satisfied@unreported; healthy=unknown; protected=unknown; verified=unknown",
		"read at unreported; fact observation times not reported",
		"publication=1", "read_time=1", "readiness_state=1", "resource_detail=1", "resources=1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("unsafe/unknown disclosure missing %q: %s", want, out)
		}
	}
	for _, unsafe := range []string{"INJECTED", "\x1b", "unproven-revision", "unknown@", "Publication:"} {
		if strings.Contains(out, unsafe) {
			t.Fatalf("unsafe or unproven value %q rendered: %s", unsafe, out)
		}
	}
}

func TestProjectResourceSummaryMissingScope(t *testing.T) {
	out := resourceSummaryOutput(t, pc.DeclarationStatus{})
	for _, want := range []string{"Resources: none reported; resource evidence unknown.", "read at unreported", "Resource detail: scope unknown or undisplayable; loom project context --help"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing scope/evidence guessed: %s", out)
		}
	}
}

func TestProjectResourceSummaryOversizedReadTimeAndDetailScope(t *testing.T) {
	status := webDAVContextFixture()
	status.ObservedAt = "2026-10-12T08:00:00." + strings.Repeat("0", 4096) + "Z"
	status.Target.OwnerNodeID = "unsafe\nINJECTED"
	out := resourceSummaryOutput(t, status)
	if !strings.Contains(out, "read at unreported") || !strings.Contains(out, "Omitted: read_time=1") || !strings.Contains(out, "Resource detail: scope unknown or undisplayable; loom project context --help") || strings.Contains(out, "INJECTED") {
		t.Fatal("oversized read time or missing owner obscured scope", out)
	}
}

func TestProjectResourceSummaryIndependentDimensions(t *testing.T) {
	status := webDAVContextFixture()
	r := status.Resources["webdav"]
	r.Readiness.Applied = pc.DeclarationFact{State: pc.DeclarationPending, Revision: "install-owner"}
	r.Readiness.Healthy = pc.DeclarationFact{State: pc.DeclarationFailed, Revision: "health-owner"}
	r.Readiness.Protected = pc.DeclarationFact{State: pc.DeclarationNotApplicable}
	r.Readiness.Verified = pc.DeclarationFact{State: pc.DeclarationSatisfied, Revision: "verification-owner"}
	status.Resources["webdav"] = r
	out := resourceSummaryOutput(t, status)
	if !strings.Contains(out, "applied=pending@install-owner; healthy=failed@health-owner; protected=not_applicable@unreported; verified=satisfied@verification-owner") {
		t.Fatal("independent owner dimensions conflated", out)
	}
}
