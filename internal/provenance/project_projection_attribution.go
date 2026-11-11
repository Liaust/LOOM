package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projects"
)

// HomeBoxID is supplied by the registered project owner, never by source text
// or a caller's selected/default Box. This operation fills missing attribution;
// it does not transfer an already attributed project to a different Box.
type ProjectProjectionMembershipInput struct {
	ProjectID string
	HomeBoxID string
}

type ProjectProjectionMembershipTransport interface {
	SyncProjectProjectionMembership(context.Context, ProjectProjectionMembershipInput) (ProjectProjectionSyncReceipt, error)
}

func (api *FoundationAPI) SyncProjectProjectionMembership(ctx context.Context, input ProjectProjectionMembershipInput) (ProjectProjectionSyncReceipt, error) {
	if api == nil || api.store == nil || api.service == nil {
		return ProjectProjectionSyncReceipt{}, errors.New("provenance projection runtime is not ready")
	}
	if err := ids.Validate("project", input.ProjectID); err != nil {
		return ProjectProjectionSyncReceipt{}, &FoundationValidationError{Err: err}
	}
	if !boxregistry.IsBoxRef("box:" + input.HomeBoxID) {
		return ProjectProjectionSyncReceipt{}, &FoundationValidationError{Err: errors.New("membership repair requires verified registered home_box_id")}
	}
	if err := checkHomeBox(ctx, input.HomeBoxID); err != nil {
		return ProjectProjectionSyncReceipt{}, err
	}
	var result ProjectProjectionSyncReceipt
	err := api.store.Transact(ctx, func(store *Store) error {
		var err error
		result, err = store.syncProjectProjectionMembership(ctx, input, api.service.clock().UTC())
		return err
	})
	return result, err
}

func (store *Store) syncProjectProjectionMembership(ctx context.Context, input ProjectProjectionMembershipInput, at time.Time) (ProjectProjectionSyncReceipt, error) {
	if err := store.lockProjectProjection(ctx, input.ProjectID); err != nil {
		return ProjectProjectionSyncReceipt{}, err
	}
	// This exact metadata-only read can see the unattributed predecessor only
	// after the caller has verified the target project's current membership.
	var previous projectSnapshot
	var id string
	err := store.q.queryRow(ctx, `SELECT id::text,project_id,projection_revision,source_revision,
 source_identity_digest,source_digest,source_schema_version,snapshot_digest,
 observed_at,searchable_summary,projection_json,created_at
 FROM provenance.project_projection_snapshots WHERE project_id=$1
 ORDER BY projection_revision DESC,id LIMIT 1`, input.ProjectID).Scan(
		&id, &previous.ProjectID, &previous.ProjectionRevision, &previous.SourceRevision,
		&previous.SourceIdentityDigest, &previous.SourceDigest, &previous.SourceSchemaVersion,
		&previous.SnapshotDigest, &previous.ObservedAt, &previous.SearchableSummary, &previous.ProjectionJSON, &previous.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectProjectionSyncReceipt{}, ErrProjectProjectionNotFound
	}
	if err != nil {
		return ProjectProjectionSyncReceipt{}, fmt.Errorf("read retained project attribution: %w", err)
	}
	previous.ID = SemanticID(id)
	if previous.ProjectID != input.ProjectID {
		return ProjectProjectionSyncReceipt{}, errors.New("retained project attribution has mismatched identity")
	}
	current, err := decodeProjectContext(previous.ProjectionJSON, input.ProjectID)
	if err != nil {
		return ProjectProjectionSyncReceipt{}, err
	}
	if current.HomeBoxID != "" && current.HomeBoxID != input.HomeBoxID {
		return ProjectProjectionSyncReceipt{}, boxMismatch()
	}
	repositories, err := store.retainedAttributionRepositories(ctx, previous)
	if err != nil {
		return ProjectProjectionSyncReceipt{}, err
	}
	next := previous
	projectReplayed := current.HomeBoxID == input.HomeBoxID
	if !projectReplayed {
		next.ProjectionJSON, err = withHomeBoxID(previous.ProjectionJSON, input.HomeBoxID)
		if err != nil {
			return ProjectProjectionSyncReceipt{}, err
		}
		next.SnapshotDigest = digestProjection(next.ProjectionJSON)
		next.SourceIdentityDigest, err = projectProjectionSourceIdentity(next)
		if err != nil {
			return ProjectProjectionSyncReceipt{}, err
		}
		next.CreatedAt = at
		next.ID, next.ProjectionRevision, projectReplayed, err = store.appendProjectProjection(ctx, next)
		if err != nil {
			return ProjectProjectionSyncReceipt{}, err
		}
	}
	result := ProjectProjectionSyncReceipt{SchemaVersion: ProjectProjectionSchemaVersion, ProjectID: input.ProjectID,
		ProjectSnapshotID: next.ID, ProjectSourceVersion: previous.SourceRevision, ProjectReplayed: projectReplayed,
		Repositories: []ProjectProjectionSyncRepositoryReceipt{}, Replayed: projectReplayed, SyncedAt: at}
	for _, repository := range repositories {
		rebound, changed, err := attributeRetainedRepository(repository, previous, next, input.HomeBoxID, at)
		if err != nil {
			return ProjectProjectionSyncReceipt{}, err
		}
		replayed := true
		if changed {
			if err := store.requireCurrentAttributionRepository(ctx, repository); err != nil {
				return ProjectProjectionSyncReceipt{}, err
			}
			rebound.ID, rebound.SourceRevision, replayed, err = store.appendRepositoryProjection(ctx, rebound)
			if err != nil {
				return ProjectProjectionSyncReceipt{}, err
			}
		}
		result.Repositories = append(result.Repositories, ProjectProjectionSyncRepositoryReceipt{
			RepositoryID: rebound.RepositoryID, SnapshotID: rebound.ID, SourceVersion: rebound.SourceRevision,
			TrackingStatus: rebound.TrackingStatus, Replayed: replayed})
		result.Replayed = result.Replayed && replayed
	}
	return result, nil
}

// Read only children already attached to this exact stored parent, including
// legacy parents without a captured memberships array. No registry/file scan.
func (store *Store) retainedAttributionRepositories(ctx context.Context, project projectSnapshot) ([]repositorySnapshot, error) {
	rows, err := store.q.query(ctx, `SELECT DISTINCT ON (repository_id)
 id::text,repository_id,source_revision,source_identity_digest,source_version,source_digest,
 snapshot_digest,observed_commit,tracking_status,observed_at,searchable_summary,card_json
 FROM provenance.repository_projection_snapshots WHERE project_id=$1 AND project_snapshot_id=$2::uuid
 ORDER BY repository_id,source_revision DESC,id LIMIT $3`, project.ProjectID, string(project.ID), projects.ProjectRepositoryReadMemberLimit+1)
	if err != nil {
		return nil, fmt.Errorf("read retained attribution repositories: %w", err)
	}
	defer rows.Close()
	result := []repositorySnapshot{}
	for rows.Next() {
		var repository repositorySnapshot
		var id string
		if err := rows.Scan(&id, &repository.RepositoryID, &repository.SourceRevision, &repository.SourceIdentityDigest,
			&repository.SourceVersion, &repository.SourceDigest, &repository.SnapshotDigest, &repository.ObservedCommit,
			&repository.TrackingStatus, &repository.ObservedAt, &repository.SearchableSummary, &repository.CardJSON); err != nil {
			return nil, err
		}
		repository.ID, repository.ProjectID, repository.ProjectSnapshotID = SemanticID(id), project.ProjectID, project.ID
		result = append(result, repository)
		if len(result) > projects.ProjectRepositoryReadMemberLimit {
			return nil, errors.New("retained attribution exceeds repository bound")
		}
	}
	return result, rows.Err()
}

func attributeRetainedRepository(repository repositorySnapshot, previous, next projectSnapshot, home string, at time.Time) (repositorySnapshot, bool, error) {
	var card RepositoryCard
	if err := json.Unmarshal(repository.CardJSON, &card); err != nil {
		return repositorySnapshot{}, false, err
	}
	if card.RepositoryID != repository.RepositoryID || card.OwningProject.ProjectID != next.ProjectID {
		return repositorySnapshot{}, false, errors.New("retained attribution repository has mismatched identity")
	}
	if card.OwningProject.HomeBoxID != "" && card.OwningProject.HomeBoxID != home {
		return repositorySnapshot{}, false, boxMismatch()
	}
	if previous.ID == next.ID && card.OwningProject.HomeBoxID == home {
		return repository, false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(repository.CardJSON, &fields); err != nil {
		return repositorySnapshot{}, false, err
	}
	owner, err := withHomeBoxID(fields["owning_project"], home)
	if err != nil {
		return repositorySnapshot{}, false, err
	}
	fields["owning_project"] = owner
	repository.CardJSON, err = json.Marshal(fields)
	if err != nil {
		return repositorySnapshot{}, false, err
	}
	repository.ProjectSnapshotID, repository.CreatedAt = next.ID, at
	repository.SnapshotDigest = digestProjection(repository.CardJSON)
	repository.SourceIdentityDigest = digestProjection(mustJSON(struct {
		Schema   string
		Parent   SemanticID
		Previous SemanticID
		Digest   string
	}{"loom.provenance.repository_box_attribution.v1", next.ID, repository.ID, repository.SnapshotDigest}))
	return repository, true, nil
}

func (store *Store) requireCurrentAttributionRepository(ctx context.Context, repository repositorySnapshot) error {
	if _, err := store.q.exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "loom:provenance:repository-projection:"+repository.RepositoryID); err != nil {
		return err
	}
	var id string
	if err := store.q.queryRow(ctx, `SELECT id::text FROM provenance.repository_projection_snapshots
 WHERE repository_id=$1 ORDER BY source_revision DESC,id LIMIT 1`, repository.RepositoryID).Scan(&id); err != nil {
		return err
	}
	if id != string(repository.ID) {
		return &FoundationConflictError{Err: errors.New("retained repository capture changed; use normal explicit project refresh to reconcile")}
	}
	return nil
}

func withHomeBoxID(payload json.RawMessage, id string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("retained attribution requires a projection object")
	}
	fields["home_box_id"], _ = json.Marshal(id)
	return json.Marshal(fields)
}
