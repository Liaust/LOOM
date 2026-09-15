package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/workers"
)

func TestDBMaintenanceRuntimeDescriptorAndInstance(t *testing.T) {
	runtime := NewDBMaintenanceRuntime(nil, maintenance.Service{}, "", "")
	if runtime.Kind() != workers.KindDBMaintenance {
		t.Fatalf("kind = %q, want %q", runtime.Kind(), workers.KindDBMaintenance)
	}
	descriptor := runtime.Describe()
	if descriptor.WorkerKind != workers.KindDBMaintenance {
		t.Fatalf("descriptor kind = %q, want %q", descriptor.WorkerKind, workers.KindDBMaintenance)
	}
	instances := runtime.DefaultInstances()
	if len(instances) != 1 {
		t.Fatalf("default instance count = %d, want 1", len(instances))
	}
	if instances[0].WorkerKey != "main.db_maintenance" {
		t.Fatalf("worker key = %q, want main.db_maintenance", instances[0].WorkerKey)
	}
}

func TestDBMaintenanceRuntimeValidateConfig(t *testing.T) {
	runtime := NewDBMaintenanceRuntime(nil, maintenance.Service{}, "", "")
	if err := runtime.ValidateConfig(context.Background(), runtime.DefaultConfig()); err != nil {
		t.Fatalf("ValidateConfig returned error: %v", err)
	}
	err := runtime.ValidateConfig(context.Background(), json.RawMessage(`{"mode":"destructive"}`))
	if !errors.Is(err, workers.ErrInvalid) {
		t.Fatalf("ValidateConfig error = %v, want workers.ErrInvalid", err)
	}
}

func TestParseDBMaintenanceConfigDefaults(t *testing.T) {
	config, err := parseDBMaintenanceConfig(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("parseDBMaintenanceConfig returned error: %v", err)
	}
	if config.Mode != "light" {
		t.Fatalf("mode = %q, want light", config.Mode)
	}
	if !config.CheckMigrations || !config.CheckTableSizes || !config.CheckQueueDepths {
		t.Fatalf("expected default checks to be enabled: %#v", config)
	}
	if config.LargeTableWarningBytes != defaultLargeTableWarningBytes {
		t.Fatalf("threshold = %d, want %d", config.LargeTableWarningBytes, defaultLargeTableWarningBytes)
	}
}

func TestDBMaintenanceStatus(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{status: "ok", want: "ok"},
		{status: "degraded", want: "warning"},
		{status: "unhealthy", want: "critical"},
	}
	for _, tt := range tests {
		if got := dbMaintenanceStatus(migrations.Result{Status: tt.status}); got != tt.want {
			t.Fatalf("dbMaintenanceStatus(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestDBMaintenanceSummaryIsObject(t *testing.T) {
	summary, err := dbMaintenanceSummary(
		migrations.Result{Status: "ok", CurrentVersion: 18, LatestVersion: 18},
		[]tableSizeSummary{{Schema: "workers", Table: "worker_runs", Qualified: "workers.worker_runs", Bytes: 128}},
		queueCounts{QueuedJobs: 1},
		"ok",
	)
	if err != nil {
		t.Fatalf("dbMaintenanceSummary returned error: %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(summary, &value); err != nil {
		t.Fatalf("summary is invalid JSON: %v", err)
	}
	if value["migration_status"] != "ok" {
		t.Fatalf("migration_status = %v, want ok", value["migration_status"])
	}
}
