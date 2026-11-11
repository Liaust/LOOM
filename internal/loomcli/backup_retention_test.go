package loomcli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackupRetentionCohortRootUsesConfiguredApplicationStorage(t *testing.T) {
	got, err := backupRetentionCohortRoot("/srv/custom/application-data")
	if err != nil || got != "/srv/custom/application-data/notes-recovery" {
		t.Fatalf("root=%q err=%v", got, err)
	}
	for _, root := range []string{"", ".", "/", "/srv/app/../other"} {
		if _, err := backupRetentionCohortRoot(root); err == nil {
			t.Fatalf("unsafe or unknown root accepted: %q", root)
		}
	}
}

func TestBackupRetentionFilePinsProtectCurrentRollbackAndCohorts(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "update")
	cohorts := filepath.Join(root, "cohorts")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cohorts, "notes-one"), 0700); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(state, "active.yaml")
	manifest := "schema_version: loom.update.manifest.v0.5.1\nstatus: succeeded\nbackup_path: " + filepath.Join(root, "backups/operational/operational-current") + "\n"
	if err := os.WriteFile(active, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cohorts, "notes-one/cohort.json"), []byte(`{"schema":"loom.notes_recovery.v1","package_id":"operational-cohort"}`), 0600); err != nil {
		t.Fatal(err)
	}
	pins, err := backupRetentionFilePins(root, cohorts, []string{"operational-manual"})
	if err != nil || len(pins) != 3 || pins["operational-current"] == "" || pins["operational-cohort"] == "" || pins["operational-manual"] == "" {
		t.Fatalf("%v %v", pins, err)
	}
	pins, err = backupRetentionFilePins(root, cohorts, []string{"operational-manual"}, map[string]bool{"notes-one": true})
	if err != nil || len(pins) != 2 || pins["operational-current"] == "" || pins["operational-manual"] == "" || pins["operational-cohort"] != "" {
		t.Fatalf("independent copy pins %v %v", pins, err)
	}
	if err := os.WriteFile(filepath.Join(cohorts, "notes-one/cohort.json"), []byte(`{"schema":"loom.notes_recovery.v1","package_id":"operational-current"}`), 0600); err != nil {
		t.Fatal(err)
	}
	pins, err = backupRetentionFilePins(root, cohorts, nil, map[string]bool{"notes-one": true})
	if err != nil || pins["operational-current"] != "current update/rollback" {
		t.Fatalf("independent copy removed rollback pin: %v %v", pins, err)
	}
	if err := os.WriteFile(active, []byte(manifest+"rollback:\n  serviceonlypossible: true\n  restorerequired: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pins, err = backupRetentionFilePins(root, cohorts, nil, map[string]bool{"notes-one": true})
	if err != nil || len(pins) != 0 {
		t.Fatalf("completed service-only update still pins its dump: %v %v", pins, err)
	}
	if err := os.WriteFile(active, []byte(manifest+"rollback:\n  serviceonlypossible: true\n  restorerequired: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pins, err = backupRetentionFilePins(root, cohorts, nil, map[string]bool{"notes-one": true})
	if err != nil || pins["operational-current"] == "" {
		t.Fatalf("database rollback lost its backup: %v %v", pins, err)
	}
	if err := os.WriteFile(active, []byte("schema_version: loom.update.manifest.v0.5.1\nstatus: running\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := backupRetentionFilePins(root, cohorts, nil); err == nil {
		t.Fatal("active update accepted")
	}
}
