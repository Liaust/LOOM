package projects

import (
	"context"
	"database/sql"
	"fmt"
)

// ValidateScaffoldTarget checks the globally unique machine slug before any
// directory creation or constructor replay. Foreign metadata is not returned.
func (s Service) ValidateScaffoldTarget(ctx context.Context, slug, root string) error {
	project, err := scanProject(s.DB.QueryRowContext(ctx, projectSelectSQL()+` WHERE p.slug=$1`, slug))
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if err := ensureProjectBoxSelection(ctx, project); err != nil {
		return err
	}
	if err := EnsureProjectMutable(project, "project_scaffold", project.ProjectID); err != nil {
		return err
	}
	var registeredRoot string
	err = s.DB.QueryRowContext(ctx, `SELECT project_root FROM projects.project_contract_registrations WHERE project_id=$1`, project.ProjectID).Scan(&registeredRoot)
	if err != nil || registeredRoot != root {
		return fmt.Errorf("project slug is already registered at another location; use an explicitly qualified machine slug")
	}
	return nil
}
