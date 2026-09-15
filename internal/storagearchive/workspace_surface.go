package storagearchive

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const (
	WorkspaceMovePlanReviewSchemaVersion = "storage.workspace_move_plan_review.v1"
	MaximumWorkspaceSurfaceRequestBytes  = 64 << 10
	MaximumWorkspaceSurfaceResponseBytes = 128 << 10
	maximumWorkspaceSurfaceFindings      = 32
	maximumWorkspaceFindingSummaryBytes  = 512
)

type WorkspaceArchivePlanRequest struct {
	OperationID string        `json:"operation_id,omitempty"`
	Kind        WorkspaceKind `json:"kind"`
	ObjectID    string        `json:"object_id"`
	Slug        string        `json:"slug"`
	Reason      string        `json:"reason"`
}

type WorkspaceRestorePlanRequest struct {
	OperationID        string `json:"operation_id,omitempty"`
	ArchiveOperationID string `json:"archive_operation_id"`
	Reason             string `json:"reason"`
}

type WorkspaceMovePlanReview struct {
	SchemaVersion         string                 `json:"schema_version"`
	OperationID           string                 `json:"operation_id"`
	OperationKind         WorkspaceOperationKind `json:"operation_kind"`
	ArchiveOperationID    string                 `json:"archive_operation_id,omitempty"`
	ArchiveManifestDigest string                 `json:"archive_manifest_digest,omitempty"`
	Kind                  WorkspaceKind          `json:"workspace_kind"`
	ObjectID              string                 `json:"object_id"`
	Slug                  string                 `json:"slug"`
	Source                WorkspacePathRef       `json:"source"`
	Destination           WorkspacePathRef       `json:"destination"`
	InventoryDigest       string                 `json:"inventory_digest"`
	InventoryEntries      int                    `json:"inventory_entries"`
	InventoryBytes        int64                  `json:"inventory_bytes"`
	CatalogRebinds        int                    `json:"catalog_rebinds"`
	ActorID               string                 `json:"actor_id"`
	Reason                string                 `json:"reason"`
	PlannedAt             time.Time              `json:"planned_at"`
	PlanDigest            string                 `json:"plan_digest"`
}

type WorkspaceMoveApplyRequest struct {
	Plan       WorkspaceMovePlanReview `json:"plan"`
	PlanDigest string                  `json:"plan_digest"`
	Confirm    bool                    `json:"confirm"`
}

type WorkspaceMoveRecoverRequest struct {
	OperationID string `json:"operation_id"`
	PlanDigest  string `json:"plan_digest"`
	Confirm     bool   `json:"confirm"`
}

type WorkspaceArchiveFindingSummary struct {
	Code       WorkspaceArchiveFindingCode     `json:"code"`
	Severity   WorkspaceArchiveFindingSeverity `json:"severity"`
	AtPhase    WorkspaceArchivePhase           `json:"at_phase"`
	Summary    string                          `json:"summary"`
	Repairable bool                            `json:"repairable"`
}

type WorkspaceMoveInspectionSummary struct {
	OperationID        string                           `json:"operation_id"`
	OperationKind      WorkspaceOperationKind           `json:"operation_kind"`
	ArchiveOperationID string                           `json:"archive_operation_id,omitempty"`
	PlanDigest         string                           `json:"plan_digest"`
	Kind               WorkspaceKind                    `json:"workspace_kind"`
	ObjectID           string                           `json:"object_id"`
	Slug               string                           `json:"slug"`
	Phase              WorkspaceArchivePhase            `json:"phase"`
	LastSafePhase      WorkspaceArchivePhase            `json:"last_safe_phase,omitempty"`
	Status             WorkspaceArchiveOperationStatus  `json:"status"`
	Custody            WorkspaceCustodyState            `json:"custody"`
	LifecycleState     WorkspaceLifecycleState          `json:"lifecycle_state,omitempty"`
	ActivationState    WorkspaceActivationState         `json:"activation_state,omitempty"`
	Source             WorkspacePathRef                 `json:"source"`
	Destination        WorkspacePathRef                 `json:"destination"`
	PlannedAt          time.Time                        `json:"planned_at"`
	UpdatedAt          time.Time                        `json:"updated_at"`
	CompletedAt        *time.Time                       `json:"completed_at,omitempty"`
	Findings           []WorkspaceArchiveFindingSummary `json:"findings"`
}

func NewWorkspaceMovePlanReview(plan WorkspaceArchivePlan) (WorkspaceMovePlanReview, error) {
	review := WorkspaceMovePlanReview{
		SchemaVersion:         WorkspaceMovePlanReviewSchemaVersion,
		OperationID:           plan.OperationID,
		OperationKind:         plan.OperationKind,
		ArchiveOperationID:    plan.ArchiveOperationID,
		ArchiveManifestDigest: plan.ArchiveManifestDigest,
		Kind:                  plan.Kind,
		ObjectID:              plan.ObjectID,
		Slug:                  plan.Slug,
		Source:                plan.Source.Path,
		Destination:           plan.Destination.Path,
		InventoryDigest:       plan.Inventory.Digest,
		InventoryEntries:      len(plan.Inventory.Entries),
		InventoryBytes:        plan.Inventory.TotalBytes,
		CatalogRebinds:        len(plan.CatalogRebinds),
		ActorID:               plan.ActorID,
		Reason:                plan.Reason,
		PlannedAt:             plan.PlannedAt,
		PlanDigest:            plan.PlanDigest,
	}
	review.Source.AbsolutePath = ""
	review.Destination.AbsolutePath = ""
	if err := ValidateWorkspaceMovePlanReview(review); err != nil {
		return WorkspaceMovePlanReview{}, err
	}
	return review, nil
}

func ValidateWorkspaceMovePlanReview(review WorkspaceMovePlanReview) error {
	if review.SchemaVersion != WorkspaceMovePlanReviewSchemaVersion || !workspacePathID(review.OperationID, workspaceOperationID) || (review.OperationKind != WorkspaceOperationArchive && review.OperationKind != WorkspaceOperationRestore) || validateWorkspaceObject(review.Kind, review.ObjectID, review.Slug) != nil || !validWorkspaceActorID(review.ActorID) || validateBoundedText("reason", review.Reason, maxWorkspaceArchiveReasonBytes) != nil || review.PlannedAt.IsZero() || !sha256DigestPattern.MatchString(review.PlanDigest) || !sha256DigestPattern.MatchString(review.InventoryDigest) || review.InventoryEntries < 0 || review.InventoryBytes < 0 || review.CatalogRebinds < 0 {
		return fmt.Errorf("invalid workspace move plan review")
	}
	if review.Source.AbsolutePath != "" || review.Destination.AbsolutePath != "" || (review.Source.Root != WorkspaceRootBox && review.Source.Root != WorkspaceRootStorage) || (review.Destination.Root != WorkspaceRootBox && review.Destination.Root != WorkspaceRootStorage) || review.Source.RelativePath == "" || review.Destination.RelativePath == "" {
		return fmt.Errorf("workspace move review paths must be bounded relative references")
	}
	if review.OperationKind == WorkspaceOperationRestore {
		if !workspacePathID(review.ArchiveOperationID, workspaceOperationID) || !sha256DigestPattern.MatchString(review.ArchiveManifestDigest) {
			return fmt.Errorf("restore review lacks authenticated archive binding")
		}
	} else if review.ArchiveOperationID != "" || review.ArchiveManifestDigest != "" {
		return fmt.Errorf("archive review contains restore-only binding")
	}
	return nil
}

func (s WorkspaceMoveService) ReviewArchivePlan(ctx context.Context, input WorkspaceArchivePlanInput) (WorkspaceMovePlanReview, error) {
	plan, err := s.PlanArchive(ctx, input)
	if err != nil {
		return WorkspaceMovePlanReview{}, err
	}
	return NewWorkspaceMovePlanReview(plan)
}

func (s WorkspaceMoveService) ReviewRestorePlan(ctx context.Context, input WorkspaceRestorePlanInput) (WorkspaceMovePlanReview, error) {
	plan, err := s.PlanRestore(ctx, input)
	if err != nil {
		return WorkspaceMovePlanReview{}, err
	}
	return NewWorkspaceMovePlanReview(plan)
}

func (s WorkspaceMoveService) ReplanReviewed(ctx context.Context, review WorkspaceMovePlanReview) (WorkspaceArchivePlan, error) {
	if err := ValidateWorkspaceMovePlanReview(review); err != nil {
		return WorkspaceArchivePlan{}, err
	}
	var (
		plan WorkspaceArchivePlan
		err  error
	)
	if review.OperationKind == WorkspaceOperationArchive {
		plan, err = s.PlanArchive(ctx, WorkspaceArchivePlanInput{
			OperationID: review.OperationID, Kind: review.Kind, ObjectID: review.ObjectID,
			Slug: review.Slug, ActorID: review.ActorID, Reason: review.Reason, PlannedAt: review.PlannedAt,
		})
	} else {
		plan, err = s.PlanRestore(ctx, WorkspaceRestorePlanInput{
			OperationID: review.OperationID, ArchiveOperationID: review.ArchiveOperationID,
			ActorID: review.ActorID, Reason: review.Reason, PlannedAt: review.PlannedAt,
		})
	}
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	if plan.PlanDigest != review.PlanDigest {
		return WorkspaceArchivePlan{}, fmt.Errorf("live workspace evidence no longer matches the reviewed plan digest")
	}
	canonicalReview, err := NewWorkspaceMovePlanReview(plan)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	if !reflect.DeepEqual(canonicalReview, review) {
		return WorkspaceArchivePlan{}, fmt.Errorf("workspace plan review fields do not match the authenticated plan digest")
	}
	return plan, nil
}

func NewWorkspaceMoveInspectionSummary(inspection WorkspaceArchiveInspection) WorkspaceMoveInspectionSummary {
	summary := WorkspaceMoveInspectionSummary{
		OperationID: inspection.Operation.OperationID, OperationKind: inspection.Operation.OperationKind,
		ArchiveOperationID: inspection.Plan.ArchiveOperationID, PlanDigest: inspection.Operation.PlanDigest,
		Kind: inspection.Operation.Kind, ObjectID: inspection.Operation.ObjectID, Slug: inspection.Operation.Slug,
		Phase: inspection.Operation.Phase, LastSafePhase: inspection.Operation.LastSafePhase,
		Status: inspection.Operation.Status, Custody: inspection.Custody,
		ActivationState: inspection.ActivationState, Source: inspection.Operation.Source.Path,
		Destination: inspection.Operation.Destination.Path, PlannedAt: inspection.Operation.PlannedAt,
		UpdatedAt: inspection.Operation.UpdatedAt, CompletedAt: inspection.Operation.CompletedAt,
		Findings: []WorkspaceArchiveFindingSummary{},
	}
	summary.Source.AbsolutePath = ""
	summary.Destination.AbsolutePath = ""
	if inspection.Manifest != nil {
		summary.LifecycleState = inspection.Manifest.LifecycleState
	}
	for index, finding := range inspection.Operation.Findings {
		if index >= maximumWorkspaceSurfaceFindings {
			break
		}
		text := redactWorkspaceFindingSummary(finding.Summary, inspection.Plan)
		if len(text) > maximumWorkspaceFindingSummaryBytes {
			text = text[:maximumWorkspaceFindingSummaryBytes]
		}
		summary.Findings = append(summary.Findings, WorkspaceArchiveFindingSummary{
			Code: finding.Code, Severity: finding.Severity, AtPhase: finding.AtPhase,
			Summary: text, Repairable: finding.Repairable,
		})
	}
	return summary
}

func redactWorkspaceFindingSummary(summary string, plan WorkspaceArchivePlan) string {
	for _, ref := range []WorkspacePathRef{plan.Source.Path, plan.Destination.Path} {
		absolute := filepath.ToSlash(filepath.Clean(ref.AbsolutePath))
		if absolute == "." || absolute == "" {
			continue
		}
		relative := filepath.ToSlash(ref.RelativePath)
		root := strings.TrimSuffix(absolute, "/"+relative)
		if root != absolute && root != "" {
			summary = strings.ReplaceAll(summary, root, "<"+string(ref.Root)+"-root>")
		}
		summary = strings.ReplaceAll(summary, absolute, string(ref.Root)+":"+relative)
	}
	return summary
}
