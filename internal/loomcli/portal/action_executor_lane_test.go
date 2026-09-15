package portal

import (
	"strings"
	"testing"

	"loom.local/loom/internal/lane"
)

func TestPortalLaneSendPresentationMatchesCanonicalCustodyPhases(t *testing.T) {
	tests := []struct {
		status        string
		wantLifecycle PortalActionLifecycle
		want          []string
		reject        []string
	}{
		{status: lane.BatchStatusAcceptedOnMain, wantLifecycle: ActionLifecycleFailed, want: []string{"promoted on main", "catalog registration still needs repair"}, reject: []string{"export", "publish"}},
		{status: lane.BatchStatusCatalogFailed, wantLifecycle: ActionLifecycleFailed, want: []string{"promoted on main", "catalog registration needs repair"}, reject: []string{"completed"}},
		{status: lane.BatchStatusSourceCleanupFailed, wantLifecycle: ActionLifecycleFailed, want: []string{"promoted and cataloged", "staging cleanup", "remaining reviewed local cleanup"}, reject: []string{"completed"}},
		{status: lane.BatchStatusCataloged, wantLifecycle: ActionLifecycleSucceeded, want: []string{"promoted and cataloged", "intentionally retained"}, reject: []string{"publish still needs repair", "storage view"}},
		{status: lane.BatchStatusLocalCleanupWithheld, wantLifecycle: ActionLifecycleFailed, want: []string{"promoted and cataloged", "changed local source was preserved"}, reject: []string{"accepted and published"}},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			lifecycle, summary, code := portalLaneSendPresentation(test.status, false)
			if lifecycle != test.wantLifecycle {
				t.Fatalf("lifecycle = %q, want %q", lifecycle, test.wantLifecycle)
			}
			if lifecycle == ActionLifecycleFailed && code == "" {
				t.Fatal("failed phase has no stable Portal error code")
			}
			for _, want := range test.want {
				if !strings.Contains(summary, want) {
					t.Fatalf("summary %q missing %q", summary, want)
				}
			}
			for _, reject := range test.reject {
				if strings.Contains(summary, reject) {
					t.Fatalf("summary %q contains obsolete/generic text %q", summary, reject)
				}
			}
		})
	}
}
