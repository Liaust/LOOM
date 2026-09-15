package nodeagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/response"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
)

func TestReportWatchedRootRunPropagatesFindingResolution(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	store := Store{DataDir: dataDir}
	watchedStore := watchedroots.NewStore(dataDir)
	now := time.Now().UTC()
	current := watchedroots.Finding{
		RootKey:      "notes",
		Kind:         watchedroots.FindingPermissionDenied,
		RelativePath: "locked",
		Status:       watchedroots.FindingStatusOpen,
		Summary:      "path could not be scanned",
		FirstSeenAt:  now,
		LastSeenAt:   now,
	}
	ignored := watchedroots.Finding{
		RootKey:      "notes",
		Kind:         watchedroots.FindingPathCollisionWarning,
		RelativePath: "Case.md",
		Status:       watchedroots.FindingStatusIgnored,
		Summary:      "ignored by operator",
		FirstSeenAt:  now,
		LastSeenAt:   now,
	}
	resolved := watchedroots.Finding{
		RootKey:     "notes",
		Kind:        watchedroots.FindingRootUnavailable,
		Status:      watchedroots.FindingStatusResolved,
		Summary:     "watched root is reachable again",
		FirstSeenAt: now.Add(-time.Hour),
		LastSeenAt:  now,
	}
	for _, finding := range []watchedroots.Finding{current, ignored, resolved} {
		if err := watchedStore.SaveFinding(finding); err != nil {
			t.Fatalf("SaveFinding failed: %v", err)
		}
	}

	var got mainwatchedroots.ReportInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/node-agent/watched-roots/report" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode report: %v", err)
		}
		response.WriteJSON(w, http.StatusCreated, response.Success("corr_test", mainwatchedroots.ReportResult{
			Root: mainwatchedroots.WatchedRoot{WatchedRootID: "watched_root_test"},
			Findings: []mainwatchedroots.Finding{
				{FindingKey: watchedroots.FindingID("notes", watchedroots.FindingPermissionDenied, "locked")},
				{FindingKey: watchedroots.FindingID("notes", watchedroots.FindingPathCollisionWarning, "Case.md")},
			},
			ResolvedCount: 1,
		}))
	}))
	defer server.Close()

	report := reportWatchedRootRun(context.Background(), store, Config{MainURL: server.URL}, State{
		NodeID:          "node_test",
		CredentialToken: "node_cred_secret",
	}, noderuntime.WorkerInstance{
		WorkerKey: "node-agent.watched_root.notes",
	}, watchedroots.ValidatedRoot{
		Config: watchedroots.RootConfig{
			RootKey:     "notes",
			DisplayName: "Notes",
			SafeRootKey: "slice09",
		},
		RootReachable: true,
		ConfigHash:    "sha256:test",
	}, watchedroots.ScanResult{
		Mode:   watchedroots.ScanModeFull,
		Status: watchedroots.RunStatusHealthy,
	}, "corr_test")
	if report.Status != watchedroots.OutputStatusRecorded || report.FindingsReported != 2 || report.FindingsResolved != 1 {
		t.Fatalf("unexpected report result %#v", report)
	}
	if !got.ResolveMissingFindings {
		t.Fatalf("complete full scan should request missing finding resolution: %#v", got)
	}
	if len(got.Findings) != 2 {
		t.Fatalf("expected current open plus ignored findings, got %#v", got.Findings)
	}
	for _, finding := range got.Findings {
		if finding.Kind == watchedroots.FindingRootUnavailable || finding.Status == mainwatchedroots.FindingStatusResolved {
			t.Fatalf("resolved local finding should not be re-reported: %#v", got.Findings)
		}
	}
}

func TestWatchedRootReportCanResolveMissingRequiresCompleteFullScan(t *testing.T) {
	t.Parallel()
	root := watchedroots.ValidatedRoot{RootReachable: true}
	if !watchedRootReportCanResolveMissing(root, watchedroots.ScanResult{Mode: watchedroots.ScanModeFull, Status: watchedroots.RunStatusHealthy}) {
		t.Fatal("healthy full scan should resolve missing findings")
	}
	if watchedRootReportCanResolveMissing(root, watchedroots.ScanResult{Mode: watchedroots.ScanModeDirty, Status: watchedroots.RunStatusHealthy}) {
		t.Fatal("dirty scan should not resolve missing findings")
	}
	if watchedRootReportCanResolveMissing(root, watchedroots.ScanResult{Mode: watchedroots.ScanModeFull, Status: watchedroots.RunStatusBlocked}) {
		t.Fatal("blocked scan should not resolve missing findings")
	}
	if watchedRootReportCanResolveMissing(root, watchedroots.ScanResult{Mode: watchedroots.ScanModeFull, Status: watchedroots.RunStatusDegraded, Counts: watchedroots.ScanCounts{BudgetExhausted: 1}}) {
		t.Fatal("budget-exhausted scan should not resolve missing findings")
	}
	if watchedRootReportCanResolveMissing(watchedroots.ValidatedRoot{RootReachable: false}, watchedroots.ScanResult{Mode: watchedroots.ScanModeFull, Status: watchedroots.RunStatusHealthy}) {
		t.Fatal("unreachable root should not resolve missing findings")
	}
}
