package projects

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"loom.local/loom/internal/requestctx"
	"sort"
)

// DeclarationMigrationRead is a private owner observation, never a public report.
type DeclarationMigrationRead struct {
	Completeness string
	Revision     string
	Project      Project
	Registration ProjectContractRegistration
	State        DeclarationProjectState
	Authority    DeclarationAuthority
	Repository   *ProjectRepositorySource
	Members      []ProjectRepositoryReadMember
	Facets       []ProjectContractFacet
	Watches      []ProjectWatchedRootRegistration
	WatchStates  []DeclarationWatchResourceState
	Unsupported  DeclarationMigrationRegistrations
	FactCount    int
}

// The selector returns identity only; the caller must authorize before reading
// registration paths or source contents.
func ReadDeclarationMigrationTargetTx(ctx context.Context, tx *sql.Tx, ref string) (Project, error) {
	if tx == nil {
		return Project{}, errors.New("migration_transaction_required")
	}
	p, err := resolveProjectRepositoryReadProjectTx(ctx, tx, ref)
	if err == nil && ref != p.ProjectID && ref != p.Slug {
		return Project{}, sql.ErrNoRows
	}
	return p, err
}

func ReadDeclarationMigrationProjectTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, projectID, nodeID string) (out DeclarationMigrationRead, err error) {
	out.Completeness = "unavailable"
	if tx == nil {
		return out, errors.New("migration_transaction_required")
	}
	out.Authority, err = declarationAuthorityTx(ctx, tx, req, projectID, nodeID, false)
	if err != nil {
		return out, err
	}
	if !out.Authority.CanRead {
		return out, errors.New("migration_read_denied")
	}
	out.Project, err = resolveProjectRepositoryReadProjectTx(ctx, tx, projectID)
	if err != nil {
		return out, err
	}
	// Registry identity and source payloads are read only after exact authorization.
	out.Registration, err = scanContractRegistration(tx.QueryRowContext(ctx, contractRegistrationSelectSQL()+` WHERE project_id=$1`, projectID))
	if err != nil {
		return out, err
	}
	facetRows, queryErr := tx.QueryContext(ctx, contractFacetSelectSQL()+` WHERE project_id=$1 ORDER BY facet_key LIMIT 201`, projectID)
	if queryErr != nil {
		return out, queryErr
	}
	out.Facets, err = scanContractFacets(facetRows)
	facetRows.Close()
	if len(out.Facets) > 200 {
		out.Completeness = "truncated"
		return out, errors.New("migration_facet_bound")
	}
	if err != nil {
		return out, err
	}
	out.Members, err = readProjectRepositoryMembersTx(ctx, tx, projectID)
	if err != nil {
		return out, err
	}
	source, canonical, e := readProjectRepositorySourceTx(ctx, tx, projectID)
	if e != nil && e != sql.ErrNoRows {
		return out, e
	}
	if e == nil {
		out.Repository = &source
		model := ProjectRepositoryReadModel{Project: ProjectRepositoryReadProject{ProjectID: projectID, Status: out.Project.Status}, Source: &ProjectRepositoryReadSource{}, Members: out.Members}
		if err = validateProjectRepositoryReadModel(model, canonical); err != nil {
			return out, err
		}
	} else if len(out.Members) > 0 || out.Registration.ContractSchemaVersion == ProjectRepositoryProjectSchemaV05 {
		return out, errors.New("migration_repository_source_missing")
	}
	// Detect orphaned rows hidden by the existing validated join, with a bounded
	// enumeration of their keys rather than treating equal counts as membership.
	rows, err := tx.QueryContext(ctx, `SELECT member_key,repository_id FROM projects.project_repository_memberships WHERE project_id=$1 ORDER BY member_key LIMIT 501`, projectID)
	if err != nil {
		return out, err
	}
	keys := map[string]string{}
	for rows.Next() {
		var k, id string
		if err = rows.Scan(&k, &id); err != nil {
			break
		}
		keys[k] = id
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(keys) > 500 {
		return out, errors.New("migration_member_bound")
	}
	for _, m := range out.Members {
		if keys[m.Key] != m.RepositoryID {
			return out, errors.New("migration_member_join_incomplete")
		}
		delete(keys, m.Key)
	}
	if len(keys) != 0 {
		return out, errors.New("migration_member_join_incomplete")
	}
	out.State, err = declarationProjectStateTx(ctx, tx, projectID, false)
	if err != nil {
		return out, err
	}
	ids := []string{}
	for _, m := range out.Members {
		ids = append(ids, m.RepositoryID)
	}
	out.Authority, err = declarationRepositoryAuthorityTx(ctx, tx, req, projectID, nodeID, ids, false)
	if err != nil || !out.Authority.CanRead {
		return out, errors.New("migration_member_read_denied")
	}
	rows, err = tx.QueryContext(ctx, projectWatchedRootRegistrationSelectSQL()+` WHERE project_id=$1 ORDER BY backend_root_key LIMIT 4097`, projectID)
	if err != nil {
		return out, err
	}
	out.Watches = []ProjectWatchedRootRegistration{}
	for rows.Next() {
		v, e := scanProjectWatchedRootRegistration(rows)
		if e != nil {
			err = e
			break
		}
		out.Watches = append(out.Watches, v)
		if len(out.Watches) > 4096 {
			out.Completeness = "truncated"
			err = errors.New("migration_watch_bound")
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	out.WatchStates, err = readDeclarationWatchStateTx(ctx, tx, projectID, nodeID)
	if err != nil {
		return out, err
	}
	out.Unsupported.Scripts, err = readMigrationRows(ctx, tx, projectScriptExposureSelectSQL()+` WHERE project_id=$1 LIMIT 4097`, projectID, func(rows *sql.Rows) (ProjectScriptExposure, error) { return scanProjectScriptExposure(rows) })
	if err != nil {
		out.Completeness = "unavailable"
		return out, err
	}
	out.Unsupported.Schedules, err = readMigrationRows(ctx, tx, projectScheduleRegistrationSelectSQL()+` WHERE project_id=$1 LIMIT 4097`, projectID, func(rows *sql.Rows) (ProjectScheduleRegistration, error) {
		return scanProjectScheduleRegistration(rows)
	})
	if err != nil {
		out.Completeness = "unavailable"
		return out, err
	}
	out.Unsupported.DirectEvents, err = readMigrationRows(ctx, tx, projectDirectEventRegistrationSelectSQL()+` WHERE project_id=$1 LIMIT 4097`, projectID, func(rows *sql.Rows) (ProjectDirectEventRegistration, error) {
		return scanProjectDirectEventRegistration(rows)
	})
	if err != nil {
		out.Completeness = "unavailable"
		return out, err
	}
	out.Unsupported.Connectors, err = readMigrationRows(ctx, tx, projectConnectorRegistrationSelectSQL()+` WHERE project_id=$1 LIMIT 4097`, projectID, func(rows *sql.Rows) (ProjectConnectorRegistration, error) {
		return scanProjectConnectorRegistration(rows)
	})
	if err != nil {
		out.Completeness = "unavailable"
		return out, err
	}
	out.Unsupported.Modules, err = readMigrationRows(ctx, tx, projectModuleRegistrationSelectSQL()+` WHERE project_id=$1 LIMIT 4097`, projectID, func(rows *sql.Rows) (ProjectModuleRegistration, error) { return scanProjectModuleRegistration(rows) })
	if err != nil {
		out.Completeness = "unavailable"
		return out, err
	}
	out.Unsupported.Workflows, err = readMigrationRows(ctx, tx, projectWorkflowRegistrationSelectSQL()+` WHERE project_id=$1 LIMIT 4097`, projectID, func(rows *sql.Rows) (ProjectWorkflowRegistration, error) {
		return scanProjectWorkflowRegistration(rows)
	})
	if err != nil {
		out.Completeness = "unavailable"
		return out, err
	}
	out.FactCount = 1 + len(out.Members) + len(out.Facets) + len(out.Watches) + len(out.WatchStates) + out.Unsupported.Count()
	if out.FactCount > 4096 {
		out.Completeness = "truncated"
		return out, errors.New("migration_fact_bound")
	}
	out.Completeness = "complete"
	raw, e := json.Marshal(out)
	if e != nil {
		return out, e
	}
	out.Revision = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	return out, nil
}

type DeclarationMigrationRegistrations struct {
	Scripts      []ProjectScriptExposure
	Schedules    []ProjectScheduleRegistration
	DirectEvents []ProjectDirectEventRegistration
	Connectors   []ProjectConnectorRegistration
	Modules      []ProjectModuleRegistration
	Workflows    []ProjectWorkflowRegistration
}

func (r DeclarationMigrationRegistrations) Count() int {
	return len(r.Scripts) + len(r.Schedules) + len(r.DirectEvents) + len(r.Connectors) + len(r.Modules) + len(r.Workflows)
}

func readMigrationRows[T any](ctx context.Context, tx *sql.Tx, query, projectID string, scan func(*sql.Rows) (T, error)) ([]T, error) {
	out := []T{}
	rows, err := tx.QueryContext(ctx, query, projectID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return out, err
		}
		out = append(out, v)
		if len(out) > 4096 {
			return out, errors.New("migration_owner_bound")
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	// Owner queries retain all fields; sort canonical row bytes independently of
	// physical SQL row order before deriving the exact observation digest.
	sort.Slice(out, func(i, j int) bool {
		a, _ := json.Marshal(out[i])
		b, _ := json.Marshal(out[j])
		return bytes.Compare(a, b) < 0
	})
	return out, nil
}
