package knowledge

import (
	"encoding/json"
	"testing"

	"loom.local/loom/internal/nodeagent/watchedroots"
)

func TestWorkspaceAuthorityOperationalUpdatePostgres(t *testing.T) {
	f := declarationEnrollmentDB(t)
	item := f.analysis.Report.WatchedRoots[0]
	f.report(item)
	f.correlate(t)
	root := f.reconcile(t, SourceRootStatusActive)
	generation := func() string {
		t.Helper()
		var value string
		if err := f.service.WithNotesWorkspaceRoot(t.Context(), root.NotesSourceRootID, "main", func(_ SourceRoot, got string) error { value = got; return nil }); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := generation()
	var config watchedroots.RootConfig
	if err := json.Unmarshal(item.ConfigJSON, &config); err != nil {
		t.Fatal(err)
	}
	config.Scan.FullRescanInterval = "12h"
	config.Scan.MaxFilesPerRun++
	config.Watch.DebounceWindow = "2s"
	item.ConfigHash = watchedroots.ConfigHash(config)
	item.ConfigJSON, _ = json.Marshal(config)
	var metadata map[string]any
	if err := json.Unmarshal(mustJSON(t, item.Metadata), &metadata); err != nil {
		t.Fatal(err)
	}
	metadata["knowledge_source"].(map[string]any)["config_hash"] = item.ConfigHash
	item.Metadata = metadata
	input := f.input
	input.ConfigHash, input.ConfigJSON = item.ConfigHash, item.ConfigJSON
	input.Metadata = mustJSON(t, metadata)
	// Simulate a compiler pacing-default upgrade while retaining the exact
	// accepted declaration. The owner must still match its accepted plan.
	if _, err := f.db.Exec(`UPDATE projects.project_contract_registrations SET
	 registration_plan_json=jsonb_set(registration_plan_json,'{watched_roots}',$2),
	 validation_report_json=jsonb_set(validation_report_json,'{watched_roots}',$2)
	 WHERE project_contract_registration_id=$1`, input.ProjectContractRegistrationID, mustJSON(t, []any{item})); err != nil {
		t.Fatal(err)
	}
	f.upsert(t, input)
	f.report(item)
	f.correlate(t)
	root = f.reconcile(t, SourceRootStatusActive)
	if after := generation(); after != before {
		t.Fatal("timing-only update invalidated current native bindings", before, after)
	}
	input.ActivationStatus = "disabled"
	f.upsert(t, input)
	called := false
	if err := f.service.WithNotesWorkspaceRoot(t.Context(), root.NotesSourceRootID, "main", func(SourceRoot, string) error { called = true; return nil }); err == nil || called {
		t.Fatal("retained authority anchor bypassed real withdrawal")
	}
}
