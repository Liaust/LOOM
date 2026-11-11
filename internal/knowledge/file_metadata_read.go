package knowledge

import (
	"context"
	"database/sql"
	"fmt"
)

const metadataFileCandidatesSQL = ` FROM files.file_metadata f JOIN objects.objects o USING(object_id)
 JOIN knowledge.notes_source_roots r ON r.node_id=f.source_node_id AND starts_with(f.managed_source_path,rtrim(r.source_path,'/')||'/')
 LEFT JOIN projects.projects p ON p.project_id=r.project_id
 WHERE f.source_presence='present' AND o.status='active' AND r.status='active'
 AND f.index_policy IS DISTINCT FROM 'private_no_index'
 AND f.metadata->>'private_no_index' IS DISTINCT FROM 'true' AND o.metadata->>'private_no_index' IS DISTINCT FROM 'true'
 AND f.metadata->>'index_policy' IS DISTINCT FROM 'private_no_index' AND o.metadata->>'index_policy' IS DISTINCT FROM 'private_no_index'`

func (s *Service) metadataFileResults(ctx context.Context, input NotesSearchInput, ref string, limit int) ([]FileSearchResult, error) {
	if ref == "" {
		var err error
		input, err = s.resolveNotesSearchInput(ctx, input)
		if err != nil {
			return nil, err
		}
		// These filters need body-derived information unavailable on a metadata
		// parent. Never silently broaden their meaning to filename matches.
		if input.SourceLifecycle == SourceLifecycleFilterArchived || input.After != "" || input.Before != "" || len(input.Tags) > 0 || input.FileClass != "" {
			return nil, nil
		}
	}
	built := metadataFileCandidatesQuery(ctx, input, ref, limit)
	rows, err := s.store.db.QueryContext(ctx, built.SQL, built.Args...)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		hit            FileSearchResult
		root, relative string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.hit.FileID, &c.hit.Title, &c.hit.Path, &c.hit.Node, &c.hit.Project, &c.hit.Category, &c.root, &c.relative); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	results := []FileSearchResult{}
	seen := map[string]bool{}
	for _, c := range candidates {
		if seen[c.hit.FileID] {
			continue
		}
		if err = s.WithReferenceNotesSource(ctx, c.root, c.hit.Node, c.relative, func(SourceRoot, string) error { return nil }); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		c.hit.Content, c.hit.Lifecycle = "metadata_only", "active"
		c.hit.sourceRoot = c.root
		results = append(results, c.hit)
		seen[c.hit.FileID] = true
	}
	return results, nil
}

func metadataFileCandidatesQuery(ctx context.Context, input NotesSearchInput, ref string, limit int) notesSearchQuery {
	query := `SELECT f.object_id,f.logical_name,f.managed_source_path,r.node_key,COALESCE(p.name,''),` + sourceCategorySQL("r.root_kind") + `,r.notes_source_root_id,
	 substring(f.managed_source_path from length(rtrim(r.source_path,'/'))+2)` + metadataFileCandidatesSQL
	args := []any{}
	query += " AND " + notesSelectedRootSQL(notesBoxSelection(ctx), "r", &args)
	add := func(clause string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND "+clause, len(args))
	}
	if ref != "" {
		add("(f.object_id=$%[1]d OR f.managed_source_path=$%[1]d)", ref)
	} else {
		// Keep this expression aligned with files_file_metadata_search_idx.
		add("to_tsvector('simple',regexp_replace(f.logical_name||' '||f.managed_source_path,'[[:punct:]]',' ','g')) @@ websearch_to_tsquery('simple',$%d)", input.Query)
		add("($%[1]d='' OR r.project_id=$%[1]d)", input.ProjectID)
		add("($%[1]d='' OR r.notes_source_root_id=$%[1]d)", input.NotesSourceRootID)
		add("($%[1]d='' OR r.node_key=$%[1]d)", input.SourceNodeKey)
		add("($%[1]d='' OR ("+sourceCategorySQL("r.root_kind")+")=$%[1]d)", input.SourceCategory)
		add("($%[1]d='' OR strpos(substring(f.managed_source_path from length(rtrim(r.source_path,'/'))+2),$%[1]d)>0)", input.Path)
	}
	args = append(args, limit)
	query += fmt.Sprintf(" ORDER BY f.object_id,r.notes_source_root_id LIMIT $%d", len(args))
	return notesSearchQuery{SQL: query, Args: args}
}

func (s *Service) resolveMetadataFileRead(ctx context.Context, ref string) (fileReadTarget, error) {
	results, err := s.metadataFileResults(ctx, NotesSearchInput{}, ref, 2)
	if err != nil {
		return fileReadTarget{}, err
	}
	if len(results) == 0 {
		return fileReadTarget{}, sql.ErrNoRows
	}
	if len(results) > 1 {
		return fileReadTarget{}, fmt.Errorf("%w: path is ambiguous; use file_id", ErrInvalid)
	}
	hit := results[0]
	var target fileReadTarget
	args := []any{hit.FileID, hit.sourceRoot}
	selection := notesSelectedRootSQL(notesBoxSelection(ctx), "r", &args)
	err = s.store.db.QueryRowContext(ctx, `SELECT f.object_id,r.notes_source_root_id,r.source_path,
	 substring(f.managed_source_path from length(rtrim(r.source_path,'/'))+2),f.managed_source_path,r.node_key
	 `+metadataFileCandidatesSQL+` AND f.object_id=$1 AND r.notes_source_root_id=$2 AND `+selection, args...).Scan(&target.fileID, &target.rootID, &target.rootPath, &target.relative, &target.path, &target.node)
	target.presence = "present"
	return target, err
}
