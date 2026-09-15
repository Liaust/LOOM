package supportbundle

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/health"
	"loom.local/loom/internal/loomcli/portal"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/response"
	loomstatus "loom.local/loom/internal/status"
)

func TestCoreCollectorsUseClientAndDoctorProvider(t *testing.T) {
	root := filepath.Clean("/Users/tester/loom-box")
	output := filepath.Join(t.TempDir(), "support.tar.gz")
	doctorData := portal.BuildDoctorDataFromFindings(fixedTestTime(), []portal.DoctorFinding{
		{
			ID:                      "doctor.jobs.critical",
			Area:                    portal.DoctorAreaJobs,
			Severity:                portal.DoctorSeverityCritical,
			Title:                   "Critical indexing failure",
			Impact:                  "Search is incomplete.",
			SafeNextAction:          "Retry failed index work",
			TargetKind:              portal.DoctorTargetKindScreen,
			TargetRef:               portal.ScreenJobs,
			InspectActionID:         "jobs_search.open",
			SafeRepairActionID:      "index.retry_failed",
			DangerousRepairActionID: "storage.archive",
			RawDetails:              map[string]string{"token": "secret-token"},
		},
		{
			ID:             "doctor.storage.warning",
			Area:           portal.DoctorAreaStorage,
			Severity:       portal.DoctorSeverityWarning,
			Title:          "Storage export stale",
			SafeNextAction: "Refresh storage export",
		},
	}, nil)

	result, err := Create(context.Background(), Options{
		OutputPath: output,
		Profile:    ProfileDefault,
		CoreClient: fakeCoreClient{
			health: health.Report{
				Service: "loomd",
				Status:  "ok",
				Checks:  health.Checks{Storage: health.StorageCheck{Status: "ok", DataDir: filepath.Join(root, "data")}},
			},
			status:   loomstatus.Report{Status: "ok"},
			setup:    bootstrap.Summary{Ready: true},
			database: maintenance.DBStatus{Status: "ok", MigrationStatus: "ok"},
		},
		DoctorDataProvider: func(context.Context, Options) (portal.DoctorData, error) {
			return doctorData, nil
		},
		MaxItems:    1,
		Now:         fixedTestTime(),
		Runtime:     RuntimeInfo{Environment: "test", NodeID: "main", NodeKind: "main", NodeRole: "main"},
		PathAliases: []PathAlias{{Label: "$LOOM_BOX", Root: root}},
	}, nil)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	for _, key := range []string{"version", "node", "health", "status", "setup", "database", "doctor"} {
		if !sectionStatusExists(result.Sections, key, SectionStatusIncluded) {
			t.Fatalf("section %s not included: %#v", key, result.Sections)
		}
	}
	doctorText := readArchiveText(t, output, "loom-support/summaries/doctor.json")
	for _, leaked := range []string{"secret-token", "storage.archive", "raw_details"} {
		if strings.Contains(strings.ToLower(doctorText), strings.ToLower(leaked)) {
			t.Fatalf("doctor summary leaked %q:\n%s", leaked, doctorText)
		}
	}
	if !strings.Contains(doctorText, "Critical indexing failure") || strings.Contains(doctorText, "Storage export stale") {
		t.Fatalf("doctor max item bound not enforced:\n%s", doctorText)
	}
	attentionText := readArchiveText(t, output, "loom-support/human/attention.txt")
	if !strings.Contains(attentionText, "Retry failed index work") {
		t.Fatalf("human attention summary missing safe next action:\n%s", attentionText)
	}
	healthText := readArchiveText(t, output, "loom-support/summaries/health.json")
	if strings.Contains(healthText, root) || !strings.Contains(healthText, "$LOOM_BOX/data") {
		t.Fatalf("health paths were not aliased:\n%s", healthText)
	}
}

func TestCoreCollectorFailuresAreIndependentAndRedacted(t *testing.T) {
	root := filepath.Clean("/Users/tester/loom-box")
	output := filepath.Join(t.TempDir(), "support.tar.gz")
	result, err := Create(context.Background(), Options{
		OutputPath: output,
		Profile:    ProfileDefault,
		CoreClient: fakeCoreClient{
			healthErr: errors.New("health failed password=secret path=" + filepath.Join(root, "runtime")),
			status:    loomstatus.Report{Status: "ok"},
			setup:     bootstrap.Summary{Ready: true},
			database:  maintenance.DBStatus{Status: "ok"},
		},
		DoctorDataProvider: func(context.Context, Options) (portal.DoctorData, error) {
			return portal.BuildDoctorData(portal.Snapshot{CapturedAt: fixedTestTime()}), nil
		},
		Now:         fixedTestTime(),
		PathAliases: []PathAlias{{Label: "$LOOM_BOX", Root: root}},
	}, nil)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if !sectionStatusExists(result.Sections, "health", SectionStatusFailed) {
		t.Fatalf("health failure not recorded independently: %#v", result.Sections)
	}
	if !sectionStatusExists(result.Sections, "version", SectionStatusIncluded) || !sectionStatusExists(result.Sections, "status", SectionStatusIncluded) {
		t.Fatalf("successful sections should be preserved after health failure: %#v", result.Sections)
	}
	health := findSectionResult(result.Sections, "health")
	if health == nil {
		t.Fatalf("missing health section: %#v", result.Sections)
	}
	if strings.Contains(health.Error, "secret") || strings.Contains(health.Error, root) {
		t.Fatalf("health error was not redacted/aliased: %q", health.Error)
	}
	if !strings.Contains(health.Error, "[REDACTED]") || !strings.Contains(health.Error, "$LOOM_BOX/runtime") {
		t.Fatalf("health error missing redaction/alias markers: %q", health.Error)
	}
}

type fakeCoreClient struct {
	health    health.Report
	status    loomstatus.Report
	setup     bootstrap.Summary
	database  maintenance.DBStatus
	healthErr error
	statusErr error
	setupErr  error
	dbErr     error
}

func (f fakeCoreClient) Health(_ context.Context, correlationID string) (response.Envelope[health.Report], error) {
	if f.healthErr != nil {
		return response.Envelope[health.Report]{}, f.healthErr
	}
	return response.Success(correlationID, f.health), nil
}

func (f fakeCoreClient) Status(_ context.Context, correlationID string) (response.Envelope[loomstatus.Report], error) {
	if f.statusErr != nil {
		return response.Envelope[loomstatus.Report]{}, f.statusErr
	}
	return response.Success(correlationID, f.status), nil
}

func (f fakeCoreClient) BootstrapStatus(_ context.Context, correlationID string) (response.Envelope[bootstrap.Summary], error) {
	if f.setupErr != nil {
		return response.Envelope[bootstrap.Summary]{}, f.setupErr
	}
	return response.Success(correlationID, f.setup), nil
}

func (f fakeCoreClient) MaintenanceDBStatus(_ context.Context, correlationID string) (response.Envelope[maintenance.DBStatus], error) {
	if f.dbErr != nil {
		return response.Envelope[maintenance.DBStatus]{}, f.dbErr
	}
	return response.Success(correlationID, f.database), nil
}

func findSectionResult(items []SectionResult, key string) *SectionResult {
	for i := range items {
		if items[i].Key == key {
			return &items[i]
		}
	}
	return nil
}
