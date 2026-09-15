package provenance

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func exactSourcesRegisterEvidence(t *testing.T, service *Service, registration CandidateRegistration, id int) SemanticID {
	t.Helper()
	source := registration.Sources[0]
	source.SourceReferenceID = semanticID(id)
	_, err := service.RegisterEvidence(t.Context(), EvidenceRegistrationRequest{RegistrationID: semanticID(id + 1), Domain: registration.Domain, Visibility: registration.Visibility, EvidenceContext: "supplemental_context_marker", Producer: registration.Producer, RegisteredAt: fixedClock(), Source: source})
	if err != nil {
		t.Fatal(err)
	}
	return source.SourceReferenceID
}
func exactSourcesLink(t *testing.T, service *Service, registration CandidateRegistration, candidate, source SemanticID, id int) {
	t.Helper()
	reviewer := reviewerIdentity()
	_, err := service.ApplyOperations(t.Context(), ManualOperationBatch{Scope: LifecycleScope{Domain: registration.Domain, Visibility: registration.Visibility}, Producer: reviewer, Operations: []LifecycleOperation{LinkCandidateEvidenceOperation{OperationMetadata: operationMetadata(id, reviewer, source), CandidateID: candidate, SourceReferenceID: source, EvidenceContext: "supplemental_context_marker"}}})
	if err != nil {
		t.Fatal(err)
	}
}
func exactSourcesLedger(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='provenance' ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	for _, table := range tables {
		var data []byte
		err := pool.QueryRow(t.Context(), `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM `+pgx.Identifier{"provenance", table}.Sanitize()+` t`).Scan(&data)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = h.Write([]byte(table))
		_, _ = h.Write(data)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

type exactSourcesTrace struct {
	mu      sync.Mutex
	queries []string
}

func (s *exactSourcesTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, d.SQL)
	return ctx
}
func (s *exactSourcesTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (s *exactSourcesTrace) reset()                                                          { s.mu.Lock(); defer s.mu.Unlock(); s.queries = nil }
func (s *exactSourcesTrace) assertRead(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	joined, begin := 0, false
	for _, q := range s.queries {
		lower := strings.ToLower(strings.TrimSpace(q))
		if strings.HasPrefix(lower, "begin") {
			begin = strings.Contains(lower, "repeatable read") && strings.Contains(lower, "read only")
		}
		if strings.Contains(q, "WITH links AS (") {
			joined++
			if strings.Contains(q, "submitted_json") || strings.Contains(q, "payload_json") || strings.Contains(q, "source_context") {
				t.Fatal("receipt query reads body columns")
			}
		}
		if !(strings.HasPrefix(lower, "select") || strings.HasPrefix(lower, "with") || strings.HasPrefix(lower, "begin") || lower == "commit" || lower == "rollback") {
			t.Fatalf("non-read query: %s", q)
		}
	}
	if !begin || joined != 1 {
		t.Fatalf("snapshot/join contract begin=%t joined=%d queries=%v", begin, joined, s.queries)
	}
}
func exactSourcesReadOnlyAPI(t *testing.T, store *Store) (*FoundationAPI, *exactSourcesTrace) {
	t.Helper()
	ctx := t.Context()
	role := store.pool.Config().ConnConfig.Database + "_reader"
	if _, err := store.pool.Exec(ctx, `CREATE ROLE `+pgx.Identifier{role}.Sanitize()+` LOGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `GRANT USAGE ON SCHEMA provenance TO `+pgx.Identifier{role}.Sanitize()+`; GRANT SELECT ON ALL TABLES IN SCHEMA provenance TO `+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	cfg := store.pool.Config().Copy()
	cfg.ConnConfig.User = role
	trace := &exactSourcesTrace{}
	cfg.ConnConfig.Tracer = trace
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = store.pool.Exec(context.Background(), `DROP OWNED BY `+pgx.Identifier{role}.Sanitize())
		if _, err := store.pool.Exec(context.Background(), `DROP ROLE `+pgx.Identifier{role}.Sanitize()); err != nil {
			t.Error(err)
		}
	})
	if _, err := pool.Exec(ctx, `INSERT INTO provenance.candidates(id) VALUES ('00000000-0000-4000-8000-000000999999')`); err == nil {
		t.Fatal("read-only fixture role can write")
	}
	reader, _ := NewStore(pool)
	service, _ := NewService(reader, WithClock(func() time.Time { panic("exact read invoked clock/writer") }), WithIDFactory(func() (SemanticID, error) { panic("exact read allocated a semantic ID") }))
	trace.reset()
	return &FoundationAPI{store: reader, service: service}, trace
}

func TestExactSourcesLinkedLifecyclePostgres(t *testing.T) {
	ctx := t.Context()
	store, service := newLifecycleService(t, "exact_sources_links", 30000)
	fixture := loadLifecycleParityFixture(t)
	var networkCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { networkCalls.Add(1) }))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "original.txt")
	if err := os.WriteFile(file, []byte("historical version"), 0600); err != nil {
		t.Fatal(err)
	}
	registration := fixture.CandidateRegistration
	registration.Sources = append([]SourceRegistration(nil), registration.Sources...)
	locator := server.URL + "/must-not-fetch?path=" + file + "&token=synthetic\n界"
	version := "frozen-version"
	digest := "sha256:" + strings.Repeat("a", 64)
	sourceContext := "source_context_marker"
	source := &registration.Sources[0]
	source.Kind = "notes_passage"
	source.Status = "resolved"
	source.Verification = "content_verified"
	source.GapReason = nil
	source.CanonicalLocator = &locator
	source.VersionAddress = &version
	source.ContentDigest = &digest
	source.SourceContext = &sourceContext
	source.Submitted = json.RawMessage(`{"submitted_marker":"excluded"}`)
	source.Details = json.RawMessage(`{"payload_marker":"excluded"}`)
	first, err := service.RegisterCandidate(ctx, "exact-first", registration)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.RegisterCandidate(ctx, "exact-second", fixture.DuplicateCandidateRegistration)
	if err != nil {
		t.Fatal(err)
	}
	supplemental := exactSourcesRegisterEvidence(t, service, fixture.DuplicateCandidateRegistration, 31000)
	foreign := exactSourcesRegisterEvidence(t, service, fixture.DuplicateCandidateRegistration, 31002)
	exactSourcesLink(t, service, registration, first.CandidateID, supplemental, 31010)
	// Normal writers reject duplicate membership. Seed a defensive historical
	// overlap through the existing store, without changing those writer rules.
	if err := store.AppendCandidateEvidenceLink(ctx, CandidateEvidenceLink{ID: semanticID(31011), CandidateID: first.CandidateID, SourceReferenceID: first.SourceResults[0].ID, OperationID: semanticID(31010), LinkedAt: fixedClock(), Producer: reviewerIdentity(), Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	supplementalRow, found, err := store.GetSourceReference(ctx, supplemental)
	if err != nil || !found || supplementalRow.CandidateID != nil {
		t.Fatal("fixture is not evidence-only")
	}
	if err := os.WriteFile(file, []byte("current object changed"), 0600); err != nil {
		t.Fatal(err)
	}
	api, trace := exactSourcesReadOnlyAPI(t, store)
	before := exactSourcesLedger(t, store.pool)
	got, err := api.GetCandidateWithSources(ctx, first.CandidateID, 16)
	if err != nil {
		t.Fatal(err)
	}
	trace.assertRead(t)
	old, err := api.GetCandidate(ctx, first.CandidateID, 16)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(old, got.CandidateLifecycleProjection) || got.EffectiveState != "pending" || got.Sources.Returned != 2 || got.Sources.SourcesTruncated || got.Sources.IncompleteItems != 0 {
		t.Fatalf("candidate identity/lifecycle/sources: %#v", got)
	}
	for _, item := range got.Sources.Items {
		if item.SourceReferenceID == foreign || item.SourceReferenceID == second.SourceResults[0].ID {
			t.Fatal("foreign source emitted")
		}
		if item.SourceReferenceID == first.SourceResults[0].ID && (*item.CanonicalLocator != locator || *item.VersionAddress != version || *item.ContentDigest != digest) {
			t.Fatal("current object substituted for historical identity")
		}
		if item.SourceReferenceID == supplemental && (item.Status != "unresolved" || item.VerificationPosture != "unverified") {
			t.Fatal("gap falsely resolved")
		}
	}
	expansion, _ := json.Marshal(got.Sources)
	if strings.Contains(string(expansion), "_marker") {
		t.Fatalf("new receipt leaked raw payload: %s", expansion)
	}
	var frozen struct {
		SourceResults []SourceReference `json:"source_results"`
	}
	if err := json.Unmarshal(got.Candidate.Payload, &frozen); err != nil || len(frozen.SourceResults) != 1 {
		t.Fatalf("fixture no longer demonstrates supplemental payload gap: %s", got.Candidate.Payload)
	}
	if after := exactSourcesLedger(t, store.pool); before != after {
		t.Fatalf("read mutated ledger: %s -> %s", before, after)
	}
	if _, err := api.GetRecordWithSources(ctx, first.CandidateID, 16); !errors.Is(err, ErrFoundationNotFound) {
		t.Fatalf("wrong kind accepted: %v", err)
	}
	if _, err := api.GetCandidateWithSources(ctx, semanticID(99999), 16); !errors.Is(err, ErrFoundationNotFound) {
		t.Fatalf("missing parent accepted: %v", err)
	}
	reviewer := reviewerIdentity()
	scope := LifecycleScope{Domain: registration.Domain, Visibility: registration.Visibility}
	accept := AcceptCandidateOperation{OperationMetadata: operationMetadata(31100, reviewer, first.SourceResults[0].ID, supplemental), CandidateID: first.CandidateID, Accepted: acceptedRecord(31101, registration, reviewer, first.SourceResults[0].ID, supplemental)}
	consolidate := ConsolidateCandidateOperation{OperationMetadata: operationMetadata(31102, reviewer, second.SourceResults[0].ID), CandidateID: second.CandidateID, RecordID: accept.Accepted.Record.ID, Rationale: "same scoped claim"}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{accept, consolidate}}); err != nil {
		t.Fatal(err)
	}
	before = exactSourcesLedger(t, store.pool)
	trace.reset()
	record, err := api.GetRecordWithSources(ctx, accept.Accepted.Record.ID, 16)
	if err != nil {
		t.Fatal(err)
	}
	trace.assertRead(t)
	ordinary, err := api.GetRecord(ctx, accept.Accepted.Record.ID, 16)
	if err != nil || !reflect.DeepEqual(ordinary, record.RecordLifecycleProjection) || record.Sources.Returned != 3 {
		t.Fatalf("record union changed: %#v %v", record, err)
	}
	for candidate, state := range map[SemanticID]string{first.CandidateID: "accepted", second.CandidateID: "consolidated"} {
		got, err := api.GetCandidateWithSources(ctx, candidate, 16)
		if err != nil || got.EffectiveState != state {
			t.Fatalf("state=%s got=%s %v", state, got.EffectiveState, err)
		}
	}
	if after := exactSourcesLedger(t, store.pool); before != after {
		t.Fatal("record/state reads mutated ledger")
	}
	thirdReg := registrationWithSources(fixture.CandidateRegistration, 31200, 1)
	third, err := service.RegisterCandidate(ctx, "exact-third", thirdReg)
	if err != nil {
		t.Fatal(err)
	}
	deferOp := DeferCandidateOperation{OperationMetadata: operationMetadata(31201, reviewer), CandidateID: third.CandidateID, ReasonCode: "missing-evidence", Reason: "bounded fixture deferral"}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{deferOp}}); err != nil {
		t.Fatal(err)
	}
	deferred, err := api.GetCandidateWithSources(ctx, third.CandidateID, 16)
	if err != nil || deferred.EffectiveState != "pending" || len(deferred.LatestDeferral) == 0 {
		t.Fatalf("deferred posture lost: %#v %v", deferred, err)
	}
	reject := RejectCandidateOperation{OperationMetadata: operationMetadata(31202, reviewer), CandidateID: third.CandidateID, Reason: "unsupported fixture claim"}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{reject}}); err != nil {
		t.Fatal(err)
	}
	rejected, err := api.GetCandidateWithSources(ctx, third.CandidateID, 16)
	if err != nil || rejected.EffectiveState != "rejected" {
		t.Fatalf("rejected posture lost: %#v %v", rejected, err)
	}
	if networkCalls.Load() != 0 {
		t.Fatal("exact get fetched a current source")
	}
	data, _ := os.ReadFile(file)
	if string(data) != "current object changed" {
		t.Fatal("exact get wrote source file")
	}
}

func TestExactSourcesLimitsAndFailuresPostgres(t *testing.T) {
	ctx := t.Context()
	store, service := newLifecycleService(t, "exact_sources_limits", 40000)
	fixture := loadLifecycleParityFixture(t)
	reg := registrationWithSources(fixture.CandidateRegistration, 41000, 50)
	candidate, err := service.RegisterCandidate(ctx, "bounded", reg)
	if err != nil {
		t.Fatal(err)
	}
	// Normal registration is bounded to 50; legacy/imported store rows can
	// exceed that. Exercise the independent 100-item read cap without widening
	// any registration contract.
	for i := 50; i < 101; i++ {
		source := candidate.SourceResults[0]
		source.ID = semanticID(41000 + i)
		if err := store.AppendSourceReference(ctx, source); err != nil {
			t.Fatal(err)
		}
	}
	api := &FoundationAPI{store: store, service: service}
	for _, limit := range []int{0, 1, 16, 50, 100} {
		got, err := api.GetCandidateWithSources(ctx, candidate.CandidateID, limit)
		effective := limit
		if effective == 0 {
			effective = 50
		}
		if err != nil || got.Sources.Returned != effective || !got.Sources.SourcesTruncated || got.Sources.IncompleteItems != 0 {
			t.Fatalf("limit=%d got=%#v %v", limit, got.Sources, err)
		}
		for i, item := range got.Sources.Items {
			if item.SourceReferenceID != semanticID(41000+i) {
				t.Fatalf("unstable source ordering: %v", got.Sources.Items)
			}
		}
	}
	// Corruption only in this disposable database. Disable FK triggers for one
	// synthetic missing receipt, then restore normal checking before the read.
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO provenance.candidate_evidence_links(id,candidate_id,source_reference_id,operation_id,linked_at,producer_json,payload_json) VALUES($1,$2,$3,$4,$5,'{}','{}')`, string(semanticID(43000)), string(candidate.CandidateID), string(semanticID(1)), string(semanticID(43001)), fixedClock()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := api.GetCandidateWithSources(ctx, candidate.CandidateID, 16); !errors.Is(err, ErrLinkedSourcesRead) || strings.Contains(err.Error(), "SELECT") {
		t.Fatalf("dangling link became complete set/raw error: %v", err)
	}
	// Query failure must not become a complete empty expansion.
	if _, err := store.pool.Exec(ctx, `ALTER TABLE provenance.candidate_evidence_links RENAME TO unavailable_fixture_links`); err != nil {
		t.Fatal(err)
	}
	if _, err := api.GetCandidateWithSources(ctx, candidate.CandidateID, 16); !errors.Is(err, ErrLinkedSourcesRead) || strings.Contains(err.Error(), "unavailable_fixture_links") {
		t.Fatalf("query failure not safely typed: %v", err)
	}
}

func TestExactSourcesSameSnapshotPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	store, service := newLifecycleService(t, "exact_sources_snapshot", 50000)
	reg := loadLifecycleParityFixture(t).CandidateRegistration
	candidate, err := service.RegisterCandidate(ctx, "snapshot", reg)
	if err != nil {
		t.Fatal(err)
	}
	supplemental := exactSourcesRegisterEvidence(t, service, reg, 51000)
	existing := exactSourcesRegisterEvidence(t, service, reg, 51002)
	exactSourcesLink(t, service, reg, candidate.CandidateID, existing, 51004)
	cfg := store.pool.Config().Copy()
	cfg.ConnConfig.RuntimeParams["application_name"] = "exact_sources_snapshot_reader"
	readerPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer readerPool.Close()
	readerStore, _ := NewStore(readerPool)
	readerService, _ := NewService(readerStore)
	api := &FoundationAPI{store: readerStore, service: readerService}
	writer, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(context.Background())
	if _, err := writer.Exec(ctx, `LOCK TABLE provenance.candidate_evidence_links IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		value CandidateWithSources
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := api.GetCandidateWithSources(ctx, candidate.CandidateID, 16)
		done <- result{value, err}
	}()
	// The parent row establishes the snapshot before its source-ID collection
	// waits on this test's table lock. Publish a link at exactly that boundary.
	for {
		var waiting bool
		err := store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND application_name='exact_sources_snapshot_reader' AND wait_event_type='Lock' AND query LIKE '%candidate_evidence_links%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("reader did not reach snapshot barrier")
		case <-time.After(5 * time.Millisecond):
		}
	}
	txStore := &Store{q: adaptTx(writer)}
	if err := txStore.AppendCandidateEvidenceLink(ctx, CandidateEvidenceLink{ID: semanticID(51006), CandidateID: candidate.CandidateID, SourceReferenceID: supplemental, OperationID: semanticID(51004), LinkedAt: fixedClock(), Producer: reviewerIdentity(), Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || len(got.value.SourceReferenceIDs) != 2 || got.value.Sources.Returned != 2 || got.value.Sources.SourcesTruncated {
			t.Fatalf("torn parent/source snapshot: %#v %v", got.value, got.err)
		}
	case <-ctx.Done():
		t.Fatal("snapshot read did not finish")
	}
	fresh, err := api.GetCandidateWithSources(ctx, candidate.CandidateID, 16)
	if err != nil || len(fresh.SourceReferenceIDs) != 3 || fresh.Sources.Returned != 3 {
		t.Fatalf("new snapshot missed committed evidence: %#v %v", fresh, err)
	}
}
