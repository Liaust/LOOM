package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type healthQueryTracer struct {
	mu      sync.Mutex
	queries []string
}

func (tracer *healthQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	tracer.mu.Lock()
	tracer.queries = append(tracer.queries, data.SQL)
	tracer.mu.Unlock()
	return ctx
}

func (*healthQueryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (tracer *healthQueryTracer) snapshot() []string {
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	return append([]string(nil), tracer.queries...)
}

func TestRecoverySnapshotIsDeterministicCompleteAndContentFreePostgres(t *testing.T) {
	ctx := context.Background()
	pool, store, database := migratedStore(t, "health_snapshot")
	registeredAt := time.Date(2026, 8, 29, 18, 0, 0, 123456000, time.UTC)
	candidateID := SemanticID("00000000-0000-4000-8000-00000000a501")
	sourceID := SemanticID("00000000-0000-4000-8000-00000000a502")
	claim := "claim-body-MUST-NOT-LEAK"
	excerpt := "source-excerpt-MUST-NOT-LEAK"
	submitted := json.RawMessage(`{"credential":"submitted-payload-MUST-NOT-LEAK"}`)
	if err := store.AppendCandidate(ctx, Candidate{
		ID: candidateID, SchemaVersion: SchemaVersion, State: "pending", Domain: "test", Visibility: "private",
		RecordKind: "decision", Claim: claim, RecordContext: "context", AssertionPosture: "reported",
		ProducerID: "producer", RegisteredAt: registeredAt, Submitted: submitted, Payload: submitted,
	}); err != nil {
		t.Fatal(err)
	}
	locator := "git+file:///fixture"
	digest := "sha256:" + strings.Repeat("a", 64)
	if err := store.AppendSourceReference(ctx, SourceReference{
		ID: sourceID, CandidateID: &candidateID, SchemaVersion: SchemaVersion, SourceKind: "git", Status: "resolved",
		VerificationPosture: "content_verified", ResolverName: "git", ResolverVersion: "1.0", ResolutionAt: registeredAt,
		CanonicalLocator: &locator, ContentDigest: &digest, SourceContext: &excerpt, Submitted: submitted, Payload: submitted,
	}); err != nil {
		t.Fatal(err)
	}

	first, err := SnapshotLedger(ctx, pool, database)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SnapshotLedger(ctx, pool, database)
	if err != nil {
		t.Fatal(err)
	}
	if err := CompareRecoverySnapshots(first, second); err != nil {
		t.Fatalf("stable snapshot mismatch: %v", err)
	}
	if len(first.RelationCounts) != len(RecoveryRelations()) || first.RelationCounts["candidates"] != 1 || first.RelationCounts["source_references"] != 1 {
		t.Fatalf("snapshot coverage = %#v", first)
	}

	completed := registeredAt
	verified := registeredAt
	report, err := BuildHealthReport(ctx, pool, RuntimeReadiness{
		State: ReadinessReady, Code: ReadinessCodeReady, Database: database, AppliedHead: SchemaHead, PackagedHead: SchemaHead,
	}, BackupObservation{State: BackupStateComplete, CompletedAt: &completed, VerifiedAt: &verified}, registeredAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{claim, excerpt, "submitted-payload-MUST-NOT-LEAK", "git+file"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("health report leaked %q: %s", forbidden, raw)
		}
	}
	if report.Counts.Candidates != 1 || report.Counts.Sources != 1 || report.Backup.Freshness != "current" {
		t.Fatalf("health metadata = %#v", report)
	}

	evidenceSourceID := SemanticID("00000000-0000-4000-8000-00000000a503")
	evidenceSource := SourceReference{
		ID: evidenceSourceID, SchemaVersion: SchemaVersion, SourceKind: "git", Status: "resolved",
		VerificationPosture: "content_verified", ResolverName: "git", ResolverVersion: "1.0", ResolutionAt: registeredAt,
		CanonicalLocator: &locator, ContentDigest: &digest, SourceContext: &excerpt, Submitted: submitted, Payload: submitted,
	}
	if err := store.AppendSourceReference(ctx, evidenceSource); err != nil {
		t.Fatal(err)
	}
	caseID := SemanticID("00000000-0000-4000-8000-00000000a504")
	if err := store.AppendResolutionCase(ctx, ResolutionCase{
		ID: caseID, SchemaVersion: SchemaVersion, Issue: "test issue", Domain: "test", Visibility: "private",
		InitialStatus: "open", CreatedAt: registeredAt, Payload: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCaseEvent(ctx, CaseEvent{
		ID: SemanticID("00000000-0000-4000-8000-00000000a505"), CaseID: caseID, SchemaVersion: SchemaVersion,
		EventType: "evidence_added", OccurredAt: registeredAt, Producer: ProducerIdentity{ProducerID: "producer", ProducerKind: "test"},
		Summary: "metadata summary", EvidenceSourceIDs: []SemanticID{evidenceSourceID}, Payload: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := SnapshotLedger(ctx, pool, database); err != nil {
		t.Fatalf("valid case evidence relation failed: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE provenance.source_references DISABLE TRIGGER reject_ledger_mutation`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM provenance.source_references WHERE id=$1`, string(evidenceSourceID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE provenance.source_references ENABLE TRIGGER reject_ledger_mutation`); err != nil {
		t.Fatal(err)
	}
	if _, err := SnapshotLedger(ctx, pool, database); !errors.Is(err, ErrLedgerIntegrity) || !strings.Contains(err.Error(), "case event evidence") {
		t.Fatalf("missing case evidence relation error = %v", err)
	}
	if err := store.AppendSourceReference(ctx, evidenceSource); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `ALTER TABLE provenance.source_references DISABLE TRIGGER reject_ledger_mutation`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM provenance.source_references WHERE id=$1`, string(sourceID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE provenance.source_references ENABLE TRIGGER reject_ledger_mutation`); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildHealthReport(ctx, pool, RuntimeReadiness{
		State: ReadinessReady, Code: ReadinessCodeReady, Database: database,
		AppliedHead: SchemaHead, PackagedHead: SchemaHead,
	}, BackupObservation{State: BackupStateMissing}, registeredAt.Add(2*time.Hour)); !errors.Is(err, ErrLedgerIntegrity) {
		t.Fatalf("health missing source relation error = %v", err)
	}
	if _, err := SnapshotLedger(ctx, pool, database); !errors.Is(err, ErrLedgerIntegrity) {
		t.Fatalf("missing source relation error = %v", err)
	}
}

func TestBuildHealthReportUsesBoundedAggregateQueriesPostgres(t *testing.T) {
	ctx := context.Background()
	pool, _, database := migratedStore(t, "health_query_shape")
	tracer := &healthQueryTracer{}
	config := pool.Config()
	config.ConnConfig.Tracer = tracer
	tracedPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer tracedPool.Close()

	report, err := BuildHealthReport(ctx, tracedPool, RuntimeReadiness{
		State: ReadinessReady, Code: ReadinessCodeReady, Database: database,
		AppliedHead: SchemaHead, PackagedHead: SchemaHead,
	}, BackupObservation{State: BackupStateMissing}, time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if report.Database.Name != database {
		t.Fatalf("health database = %q, want %q", report.Database.Name, database)
	}

	seenCounts := make(map[string]bool, len(healthCountRelations))
	for _, query := range tracer.snapshot() {
		normalized := strings.ToLower(strings.Join(strings.Fields(query), " "))
		for _, forbidden := range []string{"to_jsonb(", ".claim", ".source_context", ".submitted", ".payload"} {
			if strings.Contains(normalized, forbidden) {
				t.Fatalf("health query reads or serializes semantic payload via %q: %s", forbidden, query)
			}
		}
		for _, relation := range healthCountRelations {
			countQuery := `select count(*) from "provenance"."` + relation + `"`
			if normalized == countQuery {
				seenCounts[relation] = true
			}
		}
		if strings.Contains(normalized, `from "provenance".`) && !strings.HasPrefix(normalized, `select count(*) from "provenance".`) {
			t.Fatalf("health query reads provenance rows outside aggregate counts: %s", query)
		}
		allowedSchemaMetadata := strings.HasPrefix(normalized, "select version, name, sha256 from provenance.schema_migrations")
		if strings.Contains(normalized, "from provenance.") && !strings.HasPrefix(normalized, "select exists (") && !allowedSchemaMetadata {
			t.Fatalf("health query reads provenance rows outside integrity checks: %s", query)
		}
	}
	if len(seenCounts) != len(healthCountRelations) {
		t.Fatalf("health count query coverage = %v, want all %v", seenCounts, healthCountRelations)
	}
}
