package notesworkspace

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
)

type SourceScope struct {
	ID           string
	Path         string
	NodeKey      string
	RootIdentity string
}

func FilesystemGeneration(id, node, path string, device, inode uint64) string {
	physical := fmt.Sprintf("%s:%s:%d:%d", node, path, device, inode)
	return fmt.Sprintf("filesystem:%x", sha256.Sum256([]byte(id+":"+physical)))
}

// EnsureBoxScope records mutation custody without enrolling a Notes source.
// An existing scope's path and physical identity must never be rebound here.
func (s SQLStore) EnsureBoxScope(ctx context.Context, scope SourceScope) error {
	if !strings.HasPrefix(scope.ID, "filesystem_box_") || scope.ID == "filesystem_box_" {
		return fmt.Errorf("box scope ID must start with filesystem_box_ and include an identity")
	}
	if !filepath.IsAbs(scope.Path) || filepath.Clean(scope.Path) != scope.Path || strings.ContainsRune(scope.Path, 0) {
		return fmt.Errorf("box scope path must be absolute and clean")
	}
	if strings.TrimSpace(scope.RootIdentity) == "" {
		return fmt.Errorf("box scope root identity required")
	}
	if s.DB == nil && s.query == nil {
		return fmt.Errorf("notes workspace database required")
	}
	if _, err := s.q().ExecContext(ctx, `INSERT INTO notes_workspace.source_scopes(scope_id,kind,source_path,node_key,root_identity)
 VALUES($1,'box',$2,$3,$4) ON CONFLICT(scope_id) DO NOTHING`, scope.ID, scope.Path, scope.NodeKey, scope.RootIdentity); err != nil {
		return err
	}
	var kind string
	var saved SourceScope
	saved.ID = scope.ID
	if err := s.q().QueryRowContext(ctx, `SELECT kind,source_path,node_key,root_identity FROM notes_workspace.source_scopes WHERE scope_id=$1`, scope.ID).
		Scan(&kind, &saved.Path, &saved.NodeKey, &saved.RootIdentity); err != nil {
		return err
	}
	if kind != "box" || saved != scope {
		return fmt.Errorf("box scope %q does not match retained source identity", scope.ID)
	}
	return nil
}

// ScopePath follows current Notes ownership, retaining the last known scope path
// if its source root is absent. Box scopes always use their immutable path.
func (s SQLStore) ScopePath(ctx context.Context, id string) (string, error) {
	scope, err := s.scopeLocation(ctx, id)
	return scope.Path, err
}

func (s SQLStore) scopeLocation(ctx context.Context, id string) (SourceScope, error) {
	scope := SourceScope{ID: id}
	if s.DB == nil && s.query == nil {
		return scope, fmt.Errorf("notes workspace database required")
	}
	err := s.q().QueryRowContext(ctx, `SELECT CASE WHEN s.kind='notes'
 THEN COALESCE(NULLIF(r.source_path,''),s.source_path,'') ELSE s.source_path END,
 CASE WHEN s.kind='notes' THEN COALESCE(r.node_key,s.node_key,'') ELSE s.node_key END
 FROM notes_workspace.source_scopes s LEFT JOIN knowledge.notes_source_roots r
 ON s.kind='notes' AND r.notes_source_root_id=s.scope_id WHERE s.scope_id=$1
 UNION ALL SELECT COALESCE(source_path,''),COALESCE(node_key,'') FROM knowledge.notes_source_roots
 WHERE notes_source_root_id=$1 AND NOT EXISTS(SELECT 1 FROM notes_workspace.source_scopes WHERE scope_id=$1)`, id).Scan(&scope.Path, &scope.NodeKey)
	return scope, err
}

func scopePathKey(root, relative string) (string, bool) {
	if !filepath.IsAbs(root) || relative == "" || filepath.IsAbs(relative) {
		return "", false
	}
	relative = filepath.Clean(filepath.FromSlash(relative))
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return pathKey(filepath.Join(root, relative)), true
}
