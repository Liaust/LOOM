package provenance

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOpenRuntimeRejectsMissingAndWrongDatabaseConfiguration(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		runtime, readiness, err := OpenRuntime(context.Background(), "", true)
		if runtime != nil || err == nil {
			t.Fatalf("OpenRuntime() = runtime=%v readiness=%#v err=%v", runtime, readiness, err)
		}
		if readiness.State != ReadinessNotReady || readiness.Code != ReadinessCodeInvalidConfiguration {
			t.Fatalf("readiness = %#v", readiness)
		}
	})

	t.Run("loom_main", func(t *testing.T) {
		const databaseURL = "postgresql://provenance:must-not-leak@127.0.0.1:1/loom_main"
		runtime, readiness, err := OpenRuntime(context.Background(), databaseURL, true)
		if runtime != nil || !errors.Is(err, ErrWrongDatabase) {
			t.Fatalf("OpenRuntime() = runtime=%v readiness=%#v err=%v", runtime, readiness, err)
		}
		if readiness.Code != ReadinessCodeWrongDatabase || readiness.Database != "loom_main" {
			t.Fatalf("readiness = %#v", readiness)
		}
		if strings.Contains(err.Error(), "must-not-leak") || strings.Contains(err.Error(), databaseURL) {
			t.Fatalf("runtime error exposed connection material: %v", err)
		}
	})

	t.Run("wrong_role", func(t *testing.T) {
		const databaseURL = "postgresql://admin-role:must-not-leak@127.0.0.1:1/loom_provenance"
		runtime, readiness, err := OpenRuntime(context.Background(), databaseURL, true)
		if runtime != nil || !errors.Is(err, ErrWrongRole) {
			t.Fatalf("OpenRuntime() = runtime=%v readiness=%#v err=%v", runtime, readiness, err)
		}
		if readiness.Code != ReadinessCodeWrongRole || readiness.Role != "admin-role" {
			t.Fatalf("readiness = %#v", readiness)
		}
		if strings.Contains(err.Error(), "must-not-leak") || strings.Contains(err.Error(), databaseURL) {
			t.Fatalf("runtime error exposed connection material: %v", err)
		}
	})
}

func TestRuntimeReadinessClassifiesSchemaFailures(t *testing.T) {
	for _, test := range []struct {
		err  error
		code ReadinessCode
	}{
		{ErrSchemaBehind, ReadinessCodeSchemaBehind},
		{ErrSchemaAhead, ReadinessCodeSchemaAhead},
		{ErrHistoryMismatch, ReadinessCodeHistoryMismatch},
		{ErrSchemaTampered, ReadinessCodeSchemaTampered},
		{ErrWrongRole, ReadinessCodeWrongRole},
		{errors.New("migration failure"), ReadinessCodeMigrationFailed},
	} {
		status := MigrationStatus{Database: DatabaseName, AppliedHead: 2, PackagedHead: SchemaHead, PendingVersions: []int{3, 4, 5, 6, 7}}
		readiness := runtimeReadiness(status, test.err)
		if readiness.State != ReadinessNotReady || readiness.Code != test.code || readiness.Database != DatabaseName {
			t.Fatalf("runtimeReadiness(%v) = %#v", test.err, readiness)
		}
	}
}
