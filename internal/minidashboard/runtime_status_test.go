package minidashboard

import (
	"testing"

	"loom.local/loom/internal/maintenance"
)

func TestMigrationsCurrentAcceptsMaintenanceStatusContract(t *testing.T) {
	tests := []struct {
		name   string
		status maintenance.DBStatus
		want   bool
	}{
		{name: "maintenance ok", status: maintenance.DBStatus{MigrationStatus: "ok", CurrentVersion: 61, LatestVersion: 61}, want: true},
		{name: "legacy current", status: maintenance.DBStatus{MigrationStatus: "current", CurrentVersion: 61, LatestVersion: 61}, want: true},
		{name: "pending", status: maintenance.DBStatus{MigrationStatus: "ok", CurrentVersion: 60, LatestVersion: 61, Pending: 1}, want: false},
		{name: "version mismatch", status: maintenance.DBStatus{MigrationStatus: "ok", CurrentVersion: 60, LatestVersion: 61}, want: false},
		{name: "unhealthy", status: maintenance.DBStatus{MigrationStatus: "unhealthy", CurrentVersion: 61, LatestVersion: 61}, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := migrationsCurrent(test.status); got != test.want {
				t.Fatalf("migrationsCurrent(%+v) = %t, want %t", test.status, got, test.want)
			}
		})
	}
}
