package knowledge

import (
	"strings"
	"testing"
)

func TestWorkspaceCompletedScanWarningsDoNotRevokeSource(t *testing.T) {
	summary := `{"summary":{"root_reachable":true,"findings":3},"scan_counts":{"errors":0,"budget_exhausted":0,"missing_deferred":0}}`
	metadata := []byte(`{"root_reachable":true}`)
	if !workspaceOwnerReportUsable("degraded", []byte(summary), metadata) {
		t.Fatal("completed reachable source denied for unrelated fidelity warnings")
	}
	if !workspaceOwnerReportUsable("degraded", []byte(strings.Replace(summary, `"budget_exhausted":0`, `"budget_exhausted":1`, 1)), metadata) {
		t.Fatal("reachable source denied solely because inventory continues on another page")
	}
	for _, bad := range []string{"{}", strings.Replace(summary, `"errors":0`, `"errors":1`, 1), strings.Replace(summary, `"missing_deferred":0`, `"missing_deferred":1`, 1)} {
		if workspaceOwnerReportUsable("degraded", []byte(bad), metadata) {
			t.Fatalf("incomplete or failed source admitted: %s", bad)
		}
	}
	for _, status := range []string{"blocked", "stale", "disabled", "failed"} {
		if workspaceOwnerReportUsable(status, []byte(summary), metadata) {
			t.Fatalf("withdrawn source admitted: %s", status)
		}
	}
	if workspaceOwnerReportUsable("degraded", []byte(summary), []byte(`{"root_reachable":false}`)) {
		t.Fatal("unreachable source admitted")
	}
}
