package projects

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"loom.local/loom/internal/requestctx"
)

const MaximumProjectPlanEvidenceBytes = 128 << 20

var (
	ErrProjectPlanEvidenceConflict    = errors.New("project archive plan evidence conflicts")
	ErrProjectPlanEvidenceUnavailable = errors.New("project archive plan evidence unavailable")
)

// ProjectPlanEvidence is private persistence input, never a transport response.
// The storagearchive adapter additionally validates the complete typed plan.
type ProjectPlanEvidence struct {
	OperationID   string
	ProjectID     string
	OperationKind string
	Request       requestctx.Context `json:"-"`
	PlanDigest    string
	PayloadSHA256 string
	Payload       []byte `json:"-"`
}

func ProjectPlanPayloadDigest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func ValidateProjectPlanRequest(req requestctx.Context) error {
	for _, value := range []string{req.ActorID, req.OriginNodeID, req.ScopeID, req.CorrelationID} {
		if value == "" {
			return fmt.Errorf("project archive review requires actor, origin, scope and correlation")
		}
	}
	for _, value := range []string{req.ActorID, req.ActorKey, req.OriginNodeID, req.OriginNodeKey, req.ScopeID, req.ScopeKey, req.CorrelationID, req.FreshnessMode, req.Source} {
		if len(value) > 256 || strings.TrimSpace(value) != value || strings.ContainsFunc(value, unicode.IsControl) {
			return fmt.Errorf("invalid project archive request binding")
		}
	}
	return nil
}

func ValidateProjectPlanEvidence(e ProjectPlanEvidence) error {
	if !projectArchiveOperationIDPattern.MatchString(e.OperationID) || e.ProjectID == "" ||
		(e.OperationKind != "archive" && e.OperationKind != "restore") ||
		!projectArchiveContractDigestPattern.MatchString(e.PlanDigest) ||
		len(e.Payload) < 2 || len(e.Payload) > MaximumProjectPlanEvidenceBytes ||
		ProjectPlanPayloadDigest(e.Payload) != e.PayloadSHA256 || ValidateProjectPlanRequest(e.Request) != nil {
		return ErrProjectPlanEvidenceConflict
	}
	var envelope struct {
		SchemaVersion string             `json:"schema_version"`
		Request       requestctx.Context `json:"request"`
		PlanDigest    string             `json:"plan_digest"`
		Workspace     struct {
			OperationID   string `json:"operation_id"`
			OperationKind string `json:"operation_kind"`
			ObjectID      string `json:"object_id"`
		} `json:"workspace"`
	}
	if json.Unmarshal(e.Payload, &envelope) != nil || envelope.SchemaVersion != "storage.project_physical_"+e.OperationKind+"_plan.v1" || envelope.Request != e.Request || envelope.PlanDigest != e.PlanDigest || envelope.Workspace.OperationID != e.OperationID || envelope.Workspace.OperationKind != e.OperationKind || envelope.Workspace.ObjectID != e.ProjectID {
		return ErrProjectPlanEvidenceConflict
	}
	return nil
}

func (s Service) SaveProjectPlanEvidence(ctx context.Context, evidence ProjectPlanEvidence) error {
	if err := ValidateProjectPlanEvidence(evidence); err != nil {
		return err
	}
	if s.DB == nil {
		return ErrProjectPlanEvidenceUnavailable
	}
	request, _ := json.Marshal(evidence.Request)
	_, err := s.DB.ExecContext(ctx, `INSERT INTO projects.physical_archive_plan_evidence
		(operation_id,project_id,operation_kind,actor_id,origin_node_id,scope_id,request_json,plan_digest,payload_sha256,payload)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10) ON CONFLICT (operation_id) DO NOTHING`,
		evidence.OperationID, evidence.ProjectID, evidence.OperationKind, evidence.Request.ActorID, evidence.Request.OriginNodeID, evidence.Request.ScopeID, string(request), evidence.PlanDigest, evidence.PayloadSHA256, evidence.Payload)
	if err != nil {
		return ErrProjectPlanEvidenceUnavailable
	}
	stored, exists, err := s.LoadProjectPlanEvidence(ctx, evidence.ProjectID, evidence.OperationID)
	if err != nil {
		return err
	}
	if !exists || stored.OperationKind != evidence.OperationKind || stored.Request != evidence.Request || stored.PlanDigest != evidence.PlanDigest || stored.PayloadSHA256 != evidence.PayloadSHA256 || !bytes.Equal(stored.Payload, evidence.Payload) {
		return ErrProjectPlanEvidenceConflict
	}
	return nil
}

func (s Service) LoadProjectPlanEvidence(ctx context.Context, projectID, operationID string) (ProjectPlanEvidence, bool, error) {
	var evidence ProjectPlanEvidence
	if s.DB == nil {
		return evidence, false, ErrProjectPlanEvidenceUnavailable
	}
	if projectID == "" || !projectArchiveOperationIDPattern.MatchString(operationID) {
		return evidence, false, ErrProjectPlanEvidenceConflict
	}
	var request []byte
	err := s.DB.QueryRowContext(ctx, `SELECT operation_id,project_id,operation_kind,request_json,plan_digest,payload_sha256,payload
		FROM projects.physical_archive_plan_evidence WHERE project_id=$1 AND operation_id=$2`, projectID, operationID).Scan(
		&evidence.OperationID, &evidence.ProjectID, &evidence.OperationKind, &request, &evidence.PlanDigest, &evidence.PayloadSHA256, &evidence.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return evidence, false, nil
	}
	if err != nil {
		return ProjectPlanEvidence{}, false, ErrProjectPlanEvidenceUnavailable
	}
	if json.Unmarshal(request, &evidence.Request) != nil || ValidateProjectPlanEvidence(evidence) != nil {
		return ProjectPlanEvidence{}, false, ErrProjectPlanEvidenceConflict
	}
	return evidence, true, nil
}
