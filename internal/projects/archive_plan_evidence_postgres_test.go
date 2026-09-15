package projects_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"loom.local/loom/internal/projects"
)

func TestProjectPlanEvidencePostgres(t *testing.T) {
	database, req := projectRepositoryTransactionDatabase(t)
	s := projects.NewService(database)
	ctx := context.Background()
	created, err := s.CreateProject(ctx, req, projects.CreateInput{Name: "Private archive evidence", Slug: fmt.Sprintf("evidence-%d", time.Now().UnixNano()), HomeNodeRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	e := projectEvidenceFixture(t, created.Project.Project.ProjectID, "workspace_archive_operation_"+strings.TrimPrefix(newRepositoryID(), "repo_"), req)
	if err = s.SaveProjectPlanEvidence(ctx, e); err != nil {
		t.Fatal(err)
	}
	// A new service / connection must retrieve identical uint64-bearing bytes.
	newDB, newReq := projectRepositoryTransactionDatabase(t)
	restarted := projects.NewService(newDB)
	if newReq.ActorID != req.ActorID {
		t.Fatal("fixture actor changed")
	}
	got, found, err := restarted.LoadProjectPlanEvidence(ctx, e.ProjectID, e.OperationID)
	if err != nil || !found || !bytes.Equal(got.Payload, e.Payload) || !bytes.Contains(got.Payload, []byte("9007199254740993")) {
		t.Fatalf("restart byte fidelity: %v %v", found, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- restarted.SaveProjectPlanEvidence(ctx, e) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertRowCount(t, database, `SELECT count(*) FROM projects.physical_archive_plan_evidence WHERE operation_id=$1`, 1, e.OperationID)
	other := e
	other.Payload = bytes.ReplaceAll(e.Payload, []byte("9007199254740993"), []byte("9007199254740992"))
	other.PayloadSHA256 = projects.ProjectPlanPayloadDigest(other.Payload)
	if err := restarted.SaveProjectPlanEvidence(ctx, other); err == nil {
		t.Fatal("same operation replaced immutable bytes")
	}
	if _, found, err := s.LoadProjectPlanEvidence(ctx, "project_other", e.OperationID); err != nil || found {
		t.Fatalf("cross-project lookup: %v %v", found, err)
	}
	for _, query := range []string{
		`UPDATE projects.physical_archive_plan_evidence SET payload=payload WHERE operation_id=$1`,
		`DELETE FROM projects.physical_archive_plan_evidence WHERE operation_id=$1`,
	} {
		if _, err := database.ExecContext(ctx, query, e.OperationID); err == nil {
			t.Fatal("immutable row changed")
		}
	}
	// Check SQL bindings independently of Go validation.
	query := `INSERT INTO projects.physical_archive_plan_evidence (operation_id,project_id,operation_kind,actor_id,origin_node_id,scope_id,request_json,plan_digest,payload_sha256,payload) VALUES($1,$2,'archive',$3,$4,$5,$6::jsonb,$7,$8,$9)`
	for _, bad := range []string{"project", "actor", "origin", "scope", "digest", "payload", "size"} {
		t.Run(bad, func(t *testing.T) {
			v := projectEvidenceFixture(t, e.ProjectID, "workspace_archive_operation_"+strings.TrimPrefix(newRepositoryID(), "repo_"), req)
			wantCode := "23514"
			switch bad {
			case "project":
				v.ProjectID = "project_absent"
				wantCode = "23503"
			case "actor":
				v.Request.ActorID = "actor_absent"
				wantCode = "23503"
			case "origin":
				v.Request.OriginNodeID = "node_absent"
				wantCode = "23503"
			case "scope":
				v.Request.ScopeID = "scope_absent"
				wantCode = "23503"
			}
			// Keep all row/payload checks valid when testing a foreign key.
			v = projectEvidenceFixture(t, v.ProjectID, v.OperationID, v.Request)
			digest, hash, payload := v.PlanDigest, v.PayloadSHA256, v.Payload
			switch bad {
			case "digest":
				digest = "sha256:" + strings.Repeat("b", 64)
			case "payload":
				payload = []byte("{}")
			case "size":
				// Valid JSON/UTF-8 with unchanged bindings: only the size limit
				// should refuse this, not an unrelated NUL/parse error.
				payload = append(append([]byte(nil), payload...), bytes.Repeat([]byte(" "), projects.MaximumProjectPlanEvidenceBytes+1-len(payload))...)
				hash = projects.ProjectPlanPayloadDigest(payload)
			}
			request, _ := json.Marshal(v.Request)
			_, err := database.ExecContext(ctx, query, v.OperationID, v.ProjectID, v.Request.ActorID, v.Request.OriginNodeID, v.Request.ScopeID, string(request), digest, hash, payload)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != wantCode {
				t.Fatalf("wanted SQLSTATE %s, got %v", wantCode, err)
			}
		})
	}
}
