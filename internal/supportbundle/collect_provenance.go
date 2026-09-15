package supportbundle

import (
	"context"
	"regexp"
	"time"

	"loom.local/loom/internal/provenance"
)

var supportDatabaseNameRE = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

type ProvenanceHealthProvider func(context.Context) (provenance.HealthReport, error)

type ProvenanceSupportSummary struct {
	SchemaVersion string                     `json:"schema_version"`
	CapturedAt    time.Time                  `json:"captured_at"`
	Database      provenance.DatabaseHealth  `json:"database"`
	Counts        provenance.LifecycleCounts `json:"lifecycle_counts"`
	Backup        provenance.BackupFreshness `json:"backup"`
}

func collectProvenance(ctx context.Context, collection CollectionContext) (CollectorOutput, error) {
	if collection.Options.ProvenanceHealthProvider == nil {
		return CollectorOutput{SkipReason: "provenance_health_provider_unavailable"}, nil
	}
	report, err := collection.Options.ProvenanceHealthProvider(ctx)
	if err != nil {
		return CollectorOutput{}, err
	}
	summary := ProvenanceSupportSummary{
		SchemaVersion: provenance.HealthSchemaVersion,
		CapturedAt:    report.CapturedAt.UTC(),
		Database: provenance.DatabaseHealth{
			State:        safeReadinessState(report.Database.State),
			Code:         safeReadinessCode(report.Database.Code),
			AppliedHead:  report.Database.AppliedHead,
			PackagedHead: report.Database.PackagedHead,
		},
		Counts: boundedSupportCounts(report.Counts),
		Backup: provenance.BackupFreshness{
			State:         safeBackupState(report.Backup.State),
			Freshness:     safeBackupFreshness(report.Backup.Freshness),
			CompletedAt:   cloneTime(report.Backup.CompletedAt),
			VerifiedAt:    cloneTime(report.Backup.VerifiedAt),
			AgeSeconds:    cloneInt64(report.Backup.AgeSeconds),
			MaxAgeSeconds: report.Backup.MaxAgeSeconds,
			RecoveryReady: report.Backup.RecoveryReady,
		},
	}
	if supportDatabaseNameRE.MatchString(report.Database.Name) {
		summary.Database.Name = report.Database.Name
	}
	return jsonSummary("summaries/provenance.json", summary, PrivacyDiagnosticSummary)
}

func safeReadinessState(value provenance.ReadinessState) provenance.ReadinessState {
	switch value {
	case provenance.ReadinessReady, provenance.ReadinessNotReady:
		return value
	default:
		return provenance.ReadinessNotReady
	}
}

func safeReadinessCode(value provenance.ReadinessCode) provenance.ReadinessCode {
	switch value {
	case provenance.ReadinessCodeReady,
		provenance.ReadinessCodeInvalidConfiguration,
		provenance.ReadinessCodeWrongDatabase,
		provenance.ReadinessCodeWrongRole,
		provenance.ReadinessCodeDatabaseUnavailable,
		provenance.ReadinessCodeSchemaBehind,
		provenance.ReadinessCodeSchemaAhead,
		provenance.ReadinessCodeHistoryMismatch,
		provenance.ReadinessCodeSchemaTampered,
		provenance.ReadinessCodeMigrationFailed:
		return value
	default:
		return provenance.ReadinessCodeInvalidConfiguration
	}
}

func safeBackupState(value string) string {
	switch value {
	case provenance.BackupStateComplete, provenance.BackupStateInterrupted, provenance.BackupStateMissing:
		return value
	default:
		return provenance.BackupStateMissing
	}
}

func safeBackupFreshness(value string) string {
	switch value {
	case "current", "stale", "missing", "interrupted", "incomplete", "unverified", "future", "invalid":
		return value
	default:
		return "missing"
	}
}

func boundedSupportCounts(input provenance.LifecycleCounts) provenance.LifecycleCounts {
	result := input
	fields := []*int64{
		&result.Candidates, &result.Sources, &result.Records, &result.Producers,
		&result.Relationships, &result.Cases, &result.LifecycleEvents,
		&result.Operations, &result.ProcessingRuns, &result.RegistrationReplays,
	}
	for _, field := range fields {
		if *field < 0 || *field > provenance.HealthCountLimit {
			*field = provenance.HealthCountLimit
			result.Truncated = true
		}
	}
	return result
}

func cloneTime(input *time.Time) *time.Time {
	if input == nil {
		return nil
	}
	value := input.UTC()
	return &value
}

func cloneInt64(input *int64) *int64 {
	if input == nil {
		return nil
	}
	value := *input
	return &value
}
