package workspaceactions

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
)

type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type Project struct {
	ID       string `json:"id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Root     string `json:"path"`
	Revision string `json:"revision"`
}

func Within(value, parent string) bool {
	return parent != "" && (value == parent || strings.HasPrefix(value, parent+string(filepath.Separator)))
}

// RelatedProjects reads only registry identities intersecting the requested
// paths. Directory names and copied project files cannot create an identity.
func RelatedProjects(ctx context.Context, db Queryer, from, to string) ([]Project, error) {
	return relatedProjectsExceptCustody(ctx, db, from, to, "")
}

func relatedProjectsExceptCustody(ctx context.Context, db Queryer, from, to, custody string) ([]Project, error) {
	if db == nil || !filepath.IsAbs(from) || filepath.Clean(from) != from || to != "" && (!filepath.IsAbs(to) || filepath.Clean(to) != to) {
		return nil, fmt.Errorf("project operation classification unavailable")
	}
	rows, err := db.QueryContext(ctx, `WITH roots AS (
 SELECT p.project_id,p.slug,p.name,p.status,
 CASE WHEN p.status='archived' AND 'workspace:'||COALESCE(p.archive_state->>'operation_id','')=$3 THEN ''
 WHEN p.status='archived' THEN storage.resolve_custody_path('workspace:'||COALESCE(p.archive_state->>'operation_id',''),COALESCE(p.archive_state->>'archive_path',''))
 ELSE COALESCE(NULLIF(r.project_root,''),p.metadata#>>'{v0_3_project_contract,project_root}','') END AS root,
 md5(jsonb_build_array(p.status,p.archive_state,r.project_root,r.registration_revision,r.contract_hash)::text) AS revision
 FROM projects.projects p LEFT JOIN projects.project_contract_registrations r USING(project_id)
)
SELECT project_id,slug,name,status,root,revision FROM roots WHERE root<>'' AND
 ($1=root OR left($1,length(root)+1)=root||'/' OR left(root,length($1)+1)=$1||'/'
 OR $2<>'' AND ($2=root OR left($2,length(root)+1)=root||'/' OR left(root,length($2)+1)=$2||'/'))
ORDER BY root,project_id LIMIT 65`, from, to, custody)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.Status, &p.Root, &p.Revision); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(p.Root) || filepath.Clean(p.Root) != p.Root {
			return nil, fmt.Errorf("registered project %s has an invalid current path", p.ID)
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) > 64 {
		return nil, fmt.Errorf("selection intersects more than 64 projects; choose a narrower folder")
	}
	return items, nil
}

// GuardOrdinaryMove fences roots and ancestors, but permits ordinary files and
// folders inside a project. The lifecycle owner, not this guard, moves projects.
func GuardOrdinaryMove(ctx context.Context, db Queryer, from, to string) error {
	items, err := RelatedProjects(ctx, db, from, to)
	return guardProjectRoots(items, err, from, to)
}

// The retained owner already authenticates this exact archived custody. Its
// prepared move must not ask the ordinary resolver to read its own fenced root.
func GuardRetainedDestination(ctx context.Context, db Queryer, destination, custody string) error {
	if custody != "" && !strings.HasPrefix(custody, "workspace:workspace_archive_operation_") {
		custody = ""
	}
	items, err := relatedProjectsExceptCustody(ctx, db, destination, "", custody)
	return guardProjectRoots(items, err, destination, "")
}

func guardProjectRoots(items []Project, err error, from, to string) error {
	if err != nil {
		return err
	}
	for _, p := range items {
		if Within(p.Root, from) || to != "" && Within(p.Root, to) {
			return fmt.Errorf("project %s requires its reviewed lifecycle or relocation operation; an ordinary folder move/removal cannot change its registered root", p.Slug)
		}
	}
	return nil
}
