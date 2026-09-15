package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/policy"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/storagearchive"
)

const (
	workspaceHTTPActorID   = "actor_workspace_http"
	workspaceHTTPOperation = "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FBB"
	workspaceHTTPArchive   = "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FBC"
	workspaceHTTPDigest    = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestWorkspaceArchiveTypedRoutesUseExactCapabilities(t *testing.T) {
	service := &workspaceArchiveHTTPServiceFake{}
	authorizer := &workspaceArchiveAuthorizerFake{}
	server := workspaceArchiveTestServer(service, authorizer).Handler()
	archiveReview := workspaceArchiveHTTPReview(storagearchive.WorkspaceOperationArchive)
	restoreReview := workspaceArchiveHTTPReview(storagearchive.WorkspaceOperationRestore)
	tests := []struct {
		name, method, path, capability string
		body                           any
	}{
		{"plan", http.MethodPost, "/v1/storage/workspace-archive/plan", capabilities.WorkspaceArchivePlanCapability, storagearchive.WorkspaceArchivePlanRequest{Kind: storagearchive.WorkspaceKindTopic, ObjectID: "topic_object", Slug: "topic-one", Reason: "archive"}},
		{"apply", http.MethodPost, "/v1/storage/workspace-archive/apply", capabilities.WorkspaceArchiveApplyCapability, storagearchive.WorkspaceMoveApplyRequest{Plan: archiveReview, PlanDigest: workspaceHTTPDigest, Confirm: true}},
		{"inspect", http.MethodGet, "/v1/storage/workspace-archive/inspect?operation_id=" + workspaceHTTPOperation, capabilities.WorkspaceArchiveInspectCapability, nil},
		{"recover", http.MethodPost, "/v1/storage/workspace-archive/recover", capabilities.WorkspaceArchiveRecoverCapability, storagearchive.WorkspaceMoveRecoverRequest{OperationID: workspaceHTTPOperation, PlanDigest: workspaceHTTPDigest, Confirm: true}},
		{"restore-plan", http.MethodPost, "/v1/storage/workspace-archive/restore-plan", capabilities.WorkspaceArchiveRestorePlanCapability, storagearchive.WorkspaceRestorePlanRequest{ArchiveOperationID: workspaceHTTPArchive, Reason: "restore"}},
		{"restore-apply", http.MethodPost, "/v1/storage/workspace-archive/restore-apply", capabilities.WorkspaceArchiveRestoreApplyCapability, storagearchive.WorkspaceMoveApplyRequest{Plan: restoreReview, PlanDigest: workspaceHTTPDigest, Confirm: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var body bytes.Buffer
			if test.body != nil {
				if err := json.NewEncoder(&body).Encode(test.body); err != nil {
					t.Fatal(err)
				}
			}
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, &body))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if got := authorizer.last(); got != test.capability {
				t.Fatalf("capability=%q want %q", got, test.capability)
			}
		})
	}
	if service.archivePlanActor != workspaceHTTPActorID || service.restorePlanActor != workspaceHTTPActorID {
		t.Fatalf("plan actors archive=%q restore=%q", service.archivePlanActor, service.restorePlanActor)
	}
}

func TestPolicyWorkspaceArchiveAuthorizerBindsAuthenticatedContextAndNoRawFilesystemAuthority(t *testing.T) {
	req := requestctx.Context{ActorID: workspaceHTTPActorID, OriginNodeID: "node_main"}
	capability := capabilities.WorkspaceArchiveRestoreApplyCapability
	stub := &workspaceArchivePolicyStub{explanation: policy.PolicyExplanation{
		Decision: policy.Decision{Decision: policy.DecisionAllow},
		Context:  policy.PolicyContext{ActorID: req.ActorID, OriginNodeID: req.OriginNodeID},
		Target:   policy.PolicyTarget{CapabilityAddress: capability},
	}}
	authorizer := PolicyWorkspaceArchiveAuthorizer{service: stub}
	if err := authorizer.Authorize(context.Background(), req, capability); err != nil {
		t.Fatal(err)
	}
	if stub.input.Operation != "capability:"+capability || !strings.Contains(string(stub.input.Metadata), `"filesystem_authority":"none"`) {
		t.Fatalf("policy input=%#v", stub.input)
	}
	stub.explanation.Context.ActorID = "actor_substituted"
	if err := authorizer.Authorize(context.Background(), req, capability); err == nil || !strings.Contains(err.Error(), "does not match authenticated request") {
		t.Fatalf("context substitution error=%v", err)
	}
	stub.explanation.Context.ActorID = req.ActorID
	req.ScopeID = "canonical_project_scope"
	if err := authorizer.Authorize(context.Background(), req, capability); err == nil {
		t.Fatal("accepted a policy decision for another scope")
	}
	stub.explanation.Context.ScopeID = req.ScopeID
	if err := authorizer.Authorize(context.Background(), req, capability); err != nil {
		t.Fatal(err)
	}
	stub.explanation.Decision.Decision = policy.DecisionDeny
	stub.explanation.Decision.ReasonCode = "denied"
	if err := authorizer.Authorize(context.Background(), req, capability); err == nil {
		t.Fatal("denied policy decision was accepted")
	} else if _, ok := err.(*WorkspaceArchiveAuthorizationError); !ok {
		t.Fatalf("denial error=%T %v", err, err)
	}
}

func TestWorkspaceArchiveMutationsRequireConfirmationDigestAndActorBinding(t *testing.T) {
	tests := []struct {
		name string
		body storagearchive.WorkspaceMoveApplyRequest
	}{
		{"missing confirmation", storagearchive.WorkspaceMoveApplyRequest{Plan: workspaceArchiveHTTPReview(storagearchive.WorkspaceOperationArchive), PlanDigest: workspaceHTTPDigest}},
		{"digest substitution", func() storagearchive.WorkspaceMoveApplyRequest {
			input := storagearchive.WorkspaceMoveApplyRequest{Plan: workspaceArchiveHTTPReview(storagearchive.WorkspaceOperationArchive), PlanDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Confirm: true}
			return input
		}()},
		{"actor substitution", func() storagearchive.WorkspaceMoveApplyRequest {
			review := workspaceArchiveHTTPReview(storagearchive.WorkspaceOperationArchive)
			review.ActorID = "actor_substitution"
			return storagearchive.WorkspaceMoveApplyRequest{Plan: review, PlanDigest: workspaceHTTPDigest, Confirm: true}
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &workspaceArchiveHTTPServiceFake{}
			server := workspaceArchiveTestServer(service, &workspaceArchiveAuthorizerFake{}).Handler()
			payload, _ := json.Marshal(test.body)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/storage/workspace-archive/apply", bytes.NewReader(payload)))
			if recorder.Code != http.StatusBadRequest || service.replanCalls != 0 || service.applyCalls != 0 {
				t.Fatalf("status=%d replan=%d apply=%d body=%s", recorder.Code, service.replanCalls, service.applyCalls, recorder.Body.String())
			}
		})
	}
}

func TestWorkspaceArchivePlanRejectsArbitraryRootFields(t *testing.T) {
	service := &workspaceArchiveHTTPServiceFake{}
	server := workspaceArchiveTestServer(service, &workspaceArchiveAuthorizerFake{}).Handler()
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/storage/workspace-archive/plan", strings.NewReader(`{"kind":"topic","object_id":"topic_object","slug":"topic-one","reason":"archive","box_root":"/tmp/other"}`)))
	if recorder.Code != http.StatusBadRequest || service.archivePlanActor != "" {
		t.Fatalf("status=%d service_actor=%q body=%s", recorder.Code, service.archivePlanActor, recorder.Body.String())
	}
}

func TestWorkspaceArchiveAuthorizationAndRecoverBindingFailClosed(t *testing.T) {
	t.Run("denied before service", func(t *testing.T) {
		service := &workspaceArchiveHTTPServiceFake{}
		authorizer := &workspaceArchiveAuthorizerFake{err: &WorkspaceArchiveAuthorizationError{ReasonCode: "denied"}}
		server := workspaceArchiveTestServer(service, authorizer).Handler()
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/storage/workspace-archive/inspect?operation_id="+workspaceHTTPOperation, nil))
		if recorder.Code != http.StatusForbidden || service.inspectCalls != 0 {
			t.Fatalf("status=%d inspect=%d", recorder.Code, service.inspectCalls)
		}
	})
	t.Run("recover digest substitution", func(t *testing.T) {
		service := &workspaceArchiveHTTPServiceFake{}
		server := workspaceArchiveTestServer(service, &workspaceArchiveAuthorizerFake{}).Handler()
		payload, _ := json.Marshal(storagearchive.WorkspaceMoveRecoverRequest{OperationID: workspaceHTTPOperation, PlanDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Confirm: true})
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/storage/workspace-archive/recover", bytes.NewReader(payload)))
		if recorder.Code != http.StatusConflict || service.inspectCalls != 1 || service.recoverCalls != 0 {
			t.Fatalf("status=%d inspect=%d recover=%d", recorder.Code, service.inspectCalls, service.recoverCalls)
		}
	})
}

type workspaceArchiveHTTPServiceFake struct {
	archivePlanActor, restorePlanActor string
	replanCalls, applyCalls            int
	inspectCalls, recoverCalls         int
}

type workspaceArchivePolicyStub struct {
	explanation policy.PolicyExplanation
	input       policy.DecisionInput
}

func (stub *workspaceArchivePolicyStub) Explain(_ context.Context, _ requestctx.Context, input policy.DecisionInput) (policy.PolicyExplanation, error) {
	stub.input = input
	return stub.explanation, nil
}

func (f *workspaceArchiveHTTPServiceFake) ReviewArchivePlan(_ context.Context, input storagearchive.WorkspaceArchivePlanInput) (storagearchive.WorkspaceMovePlanReview, error) {
	f.archivePlanActor = input.ActorID
	return workspaceArchiveHTTPReview(storagearchive.WorkspaceOperationArchive), nil
}
func (f *workspaceArchiveHTTPServiceFake) ReviewRestorePlan(_ context.Context, input storagearchive.WorkspaceRestorePlanInput) (storagearchive.WorkspaceMovePlanReview, error) {
	f.restorePlanActor = input.ActorID
	return workspaceArchiveHTTPReview(storagearchive.WorkspaceOperationRestore), nil
}
func (f *workspaceArchiveHTTPServiceFake) ReplanReviewed(_ context.Context, review storagearchive.WorkspaceMovePlanReview) (storagearchive.WorkspaceArchivePlan, error) {
	f.replanCalls++
	return storagearchive.WorkspaceArchivePlan{OperationKind: review.OperationKind, PlanDigest: review.PlanDigest}, nil
}
func (f *workspaceArchiveHTTPServiceFake) ApplyArchive(context.Context, storagearchive.WorkspaceArchivePlan, string) (storagearchive.WorkspaceArchiveInspection, error) {
	f.applyCalls++
	return workspaceArchiveHTTPInspection(storagearchive.WorkspaceOperationArchive), nil
}
func (f *workspaceArchiveHTTPServiceFake) ApplyRestore(context.Context, storagearchive.WorkspaceArchivePlan, string) (storagearchive.WorkspaceArchiveInspection, error) {
	f.applyCalls++
	return workspaceArchiveHTTPInspection(storagearchive.WorkspaceOperationRestore), nil
}
func (f *workspaceArchiveHTTPServiceFake) InspectOperation(context.Context, string) (storagearchive.WorkspaceArchiveInspection, error) {
	f.inspectCalls++
	return workspaceArchiveHTTPInspection(storagearchive.WorkspaceOperationArchive), nil
}
func (f *workspaceArchiveHTTPServiceFake) RecoverOperation(context.Context, string) (storagearchive.WorkspaceArchiveInspection, error) {
	f.recoverCalls++
	return workspaceArchiveHTTPInspection(storagearchive.WorkspaceOperationArchive), nil
}

type workspaceArchiveAuthorizerFake struct {
	capabilities []string
	err          error
}

func (f *workspaceArchiveAuthorizerFake) Authorize(_ context.Context, _ requestctx.Context, capability string) error {
	f.capabilities = append(f.capabilities, capability)
	return f.err
}
func (f *workspaceArchiveAuthorizerFake) last() string {
	if len(f.capabilities) == 0 {
		return ""
	}
	return f.capabilities[len(f.capabilities)-1]
}

func workspaceArchiveTestServer(service WorkspaceArchiveLifecycleService, authorizer WorkspaceArchiveAuthorizer) Server {
	return NewServer(Services{
		WorkspaceArchive: service, WorkspaceArchiveAuthorizer: authorizer,
		WorkspaceArchiveRequestResolver: func(context.Context, string) (requestctx.Context, error) {
			return requestctx.Context{ActorID: workspaceHTTPActorID, OriginNodeID: "node_main"}, nil
		},
	}, slog.Default())
}

func workspaceArchiveHTTPReview(kind storagearchive.WorkspaceOperationKind) storagearchive.WorkspaceMovePlanReview {
	review := storagearchive.WorkspaceMovePlanReview{
		SchemaVersion: storagearchive.WorkspaceMovePlanReviewSchemaVersion,
		OperationID:   workspaceHTTPOperation, OperationKind: kind,
		Kind: storagearchive.WorkspaceKindTopic, ObjectID: "topic_object", Slug: "topic-one",
		Source:          storagearchive.WorkspacePathRef{Root: storagearchive.WorkspaceRootBox, RelativePath: "Topics/topic-one"},
		Destination:     storagearchive.WorkspacePathRef{Root: storagearchive.WorkspaceRootStorage, RelativePath: "archive/topics/topic-one/workspace"},
		InventoryDigest: workspaceHTTPDigest, ActorID: workspaceHTTPActorID, Reason: "reviewed",
		PlannedAt: time.Unix(1_900_000_000, 0).UTC(), PlanDigest: workspaceHTTPDigest,
	}
	if kind == storagearchive.WorkspaceOperationRestore {
		review.ArchiveOperationID = workspaceHTTPArchive
		review.ArchiveManifestDigest = workspaceHTTPDigest
		review.Source, review.Destination = review.Destination, review.Source
	}
	return review
}

func workspaceArchiveHTTPInspection(kind storagearchive.WorkspaceOperationKind) storagearchive.WorkspaceArchiveInspection {
	review := workspaceArchiveHTTPReview(kind)
	return storagearchive.WorkspaceArchiveInspection{
		Operation: storagearchive.WorkspaceArchiveOperation{
			OperationID: review.OperationID, OperationKind: kind, PlanDigest: review.PlanDigest,
			Kind: review.Kind, ObjectID: review.ObjectID, Slug: review.Slug, ActorID: review.ActorID,
			Source: storagearchive.WorkspacePathBinding{Path: review.Source}, Destination: storagearchive.WorkspacePathBinding{Path: review.Destination},
			Phase: storagearchive.PhaseArchiveComplete, Status: storagearchive.OperationStatusComplete,
			PlannedAt: review.PlannedAt, UpdatedAt: review.PlannedAt,
		},
		Plan:    storagearchive.WorkspaceArchivePlan{ArchiveOperationID: review.ArchiveOperationID},
		Custody: storagearchive.CustodyArchived,
	}
}
