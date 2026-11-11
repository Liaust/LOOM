package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/requestctx"
)

// AuthenticateRetainedProjectContract verifies the original registered bytes at
// a separately reviewed retained root; it grants no filesystem move authority.
func AuthenticateRetainedProjectContract(detail ProjectRegistrationDetail, retainedRoot string) error {
	if detail.Registration == nil || detail.Registration.ProjectID != detail.Project.Project.ProjectID {
		return fmt.Errorf("retained project registration identity is missing")
	}
	r := detail.Registration
	var contract projectArchiveContractDocument
	if json.Unmarshal(r.Contract, &contract) != nil || contract.Kind != "loom.project" || contract.SchemaVersion != r.ContractSchemaVersion || contract.Project.Slug != detail.Project.Project.Slug || contract.Project.OwnerNode != "main" {
		return fmt.Errorf("retained contract does not identify this Main project")
	}
	// v0.3 assigned identity in the registry; later contracts carry it in source.
	// This exception is retirement-only and still binds the unique registration,
	// project scope, exact original path, schema, slug and source-byte digest.
	if contract.Project.ID != detail.Project.Project.ProjectID && !(contract.SchemaVersion == "project.contract.v0.3" && contract.Project.ID == "") {
		return fmt.Errorf("retained contract project ID does not match registration")
	}
	_, err := authenticateProjectArchiveContract(r.ContractPath, r.ProjectRoot, retainedRoot, r.ContractHash)
	return err
}

type ProjectRegistrationRetirementInput struct {
	ProjectID            string
	ScopeID              string
	RegistrationID       string
	RegistrationRevision int
	ContractHash         string
	ActivationStatus     string
	RepositoryRevision   int64
	Members              []ProjectArchiveRepositoryMemberBinding
	ExpectedArchiveState json.RawMessage
	State                json.RawMessage
}

// RetireProjectRegistration records an already-retained disposition. It never
// moves/deletes payloads and never manufactures a physical-archive operation.
func (s Service) RetireProjectRegistration(ctx context.Context, req requestctx.Context, in ProjectRegistrationRetirementInput) (Project, error) {
	if err := ValidateProjectPlanRequest(req); err != nil {
		return Project{}, err
	}
	var state struct {
		Schema    string `json:"schema_version"`
		ProjectID string `json:"project_id"`
		Digest    string `json:"plan_digest"`
	}
	if json.Unmarshal(in.State, &state) != nil || state.Schema != "project.registration_retirement.v1" || state.ProjectID != in.ProjectID || !projectArchiveContractDigestPattern.MatchString(state.Digest) || in.ScopeID != req.ScopeID || in.RegistrationRevision < 1 {
		return Project{}, fmt.Errorf("invalid registration retirement binding")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	p, err := scanProject(tx.QueryRowContext(ctx, projectSelectSQL()+` WHERE p.project_id=$1 FOR UPDATE OF p`, in.ProjectID))
	if err != nil {
		return Project{}, err
	}
	var currentState, expectedState any
	if json.Unmarshal(p.ArchiveState, &currentState) != nil || json.Unmarshal(in.ExpectedArchiveState, &expectedState) != nil || !reflect.DeepEqual(currentState, expectedState) || p.Status != "active" || p.ProjectScopeID != in.ScopeID {
		return Project{}, fmt.Errorf("project retirement lifecycle changed")
	}
	var id, hash, status, activation string
	var revision int
	if err = tx.QueryRowContext(ctx, `SELECT project_contract_registration_id, registration_revision, contract_hash, registration_status, activation_status FROM projects.project_contract_registrations WHERE project_id=$1 FOR UPDATE`, in.ProjectID).Scan(&id, &revision, &hash, &status, &activation); err != nil {
		return Project{}, err
	}
	if id != in.RegistrationID || revision != in.RegistrationRevision || hash != in.ContractHash || status != ProjectRegistrationStatusRegistered || activation != in.ActivationStatus || (activation != ProjectActivationStatusInactive && activation != ProjectActivationStatusBaseActive) {
		return Project{}, fmt.Errorf("project retirement registration changed or is not inactive")
	}
	if err = validateProjectArchiveSourceRevisionTx(ctx, tx, in.ProjectID, in.RepositoryRevision); err != nil {
		return Project{}, err
	}
	if err = validateProjectArchiveMembersTx(ctx, tx, in.ProjectID, in.Members, p.Status); err != nil {
		return Project{}, err
	}
	if err = validateProjectArchiveOwnedSetTx(ctx, tx, in.ProjectID, in.Members); err != nil {
		return Project{}, err
	}
	var busy bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM jobs.jobs WHERE scope_id=$1 AND status IN ('created','queued','running'))`, in.ScopeID).Scan(&busy); err != nil {
		return Project{}, err
	}
	if busy {
		return Project{}, fmt.Errorf("project retirement has pending or active jobs")
	}
	var scopeStatus string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM scopes.scopes WHERE scope_id=$1 FOR UPDATE`, in.ScopeID).Scan(&scopeStatus); err != nil {
		return Project{}, err
	}
	if scopeStatus != "active" {
		return Project{}, fmt.Errorf("project retirement scope changed")
	}
	for _, q := range []string{
		`UPDATE projects.project_contract_registrations SET registration_status='archived', activation_status='inactive', updated_at=now() WHERE project_id=$1`,
		`UPDATE projects.project_repository_memberships SET lifecycle_status='archived', updated_at=now() WHERE project_id=$1`,
		`UPDATE projects.repositories SET lifecycle_status='archived', updated_at=now() WHERE owning_project_id=$1`,
	} {
		if _, err = tx.ExecContext(ctx, q, in.ProjectID); err != nil {
			return Project{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE projects.projects SET status='archived', archive_state=$2, updated_at=now() WHERE project_id=$1`, in.ProjectID, in.State); err != nil {
		return Project{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE scopes.scopes SET status='archived', updated_at=now() WHERE scope_id=$1`, in.ScopeID); err != nil {
		return Project{}, err
	}
	if _, err = events.AppendTx(ctx, tx, events.AppendInput{EventType: events.TypeProjectArchived, EventLevel: "audit", Request: req, ScopeID: in.ScopeID, TargetKind: "project", TargetID: in.ProjectID, Status: "archived", Result: "ok", Payload: map[string]any{"evidence_kind": "retained_registration_disposition", "archive_state": json.RawMessage(in.State)}}); err != nil {
		return Project{}, err
	}
	p, err = scanProject(tx.QueryRowContext(ctx, projectSelectSQL()+` WHERE p.project_id=$1`, in.ProjectID))
	if err != nil {
		return Project{}, err
	}
	if err = tx.Commit(); err != nil {
		return Project{}, err
	}
	return p, nil
}
