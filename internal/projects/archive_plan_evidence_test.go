package projects_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

func projectEvidenceFixture(t *testing.T, projectID, operationID string, req requestctx.Context) projects.ProjectPlanEvidence {
	t.Helper()
	e := projects.ProjectPlanEvidence{ProjectID: projectID, OperationID: operationID, OperationKind: "archive", Request: req, PlanDigest: "sha256:" + strings.Repeat("a", 64)}
	// This store validates persistence bindings; the adapter validates full plans.
	e.Payload, _ = json.Marshal(map[string]any{"schema_version": "storage.project_physical_archive_plan.v1", "request": req, "plan_digest": e.PlanDigest,
		"workspace": map[string]string{"operation_id": operationID, "operation_kind": "archive", "object_id": projectID}, "private_inode": uint64(9007199254740993)})
	e.PayloadSHA256 = projects.ProjectPlanPayloadDigest(e.Payload)
	return e
}

func TestProjectPlanEvidenceBindingsAndPrivacy(t *testing.T) {
	req := requestctx.Context{ActorID: "actor_fixture", OriginNodeID: "node_fixture", ScopeID: "scope_fixture", CorrelationID: "corr_fixture"}
	e := projectEvidenceFixture(t, "project_fixture", "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV", req)
	if err := projects.ValidateProjectPlanEvidence(e); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*projects.ProjectPlanEvidence){
		"operation": func(v *projects.ProjectPlanEvidence) {
			v.OperationID = "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAZ"
		},
		"project": func(v *projects.ProjectPlanEvidence) { v.ProjectID = "project_other" },
		"kind":    func(v *projects.ProjectPlanEvidence) { v.OperationKind = "restore" },
		"actor":   func(v *projects.ProjectPlanEvidence) { v.Request.ActorID = "actor_other" },
		"scope":   func(v *projects.ProjectPlanEvidence) { v.Request.ScopeID = "scope_other" },
		"origin":  func(v *projects.ProjectPlanEvidence) { v.Request.OriginNodeID = "node_other" },
		"digest":  func(v *projects.ProjectPlanEvidence) { v.PlanDigest = "sha256:" + strings.Repeat("b", 64) },
		"payload": func(v *projects.ProjectPlanEvidence) {
			v.Payload = append([]byte(nil), v.Payload...)
			v.Payload[0] = '['
		},
		"missing_scope":   func(v *projects.ProjectPlanEvidence) { v.Request.ScopeID = "" },
		"request_control": func(v *projects.ProjectPlanEvidence) { v.Request.Source = "hello\nworld" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			v := e
			mutate(&v)
			if projects.ValidateProjectPlanEvidence(v) == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
	raw, _ := json.Marshal(e)
	if strings.Contains(string(raw), "9007199254740993") || strings.Contains(string(raw), "actor_fixture") {
		t.Fatal("private evidence serialized as transport")
	}
	if err := projects.NewService(nil).SaveProjectPlanEvidence(context.Background(), e); !errors.Is(err, projects.ErrProjectPlanEvidenceUnavailable) {
		t.Fatalf("nil database: %v", err)
	}
}
