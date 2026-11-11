package workspacenotes

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"loom.local/loom/internal/workspacefiles"
)

type Refresh struct {
	ID            string         `json:"id"`
	Root          string         `json:"root"`
	Path          string         `json:"path"`
	Generation    string         `json:"generation"`
	State         string         `json:"state"`
	Started       time.Time      `json:"started"`
	Updated       time.Time      `json:"updated"`
	Counts        map[string]int `json:"counts"`
	Diagnostics   []string       `json:"diagnostics,omitempty"`
	Qualification string         `json:"qualification"`
}

func (s Service) refreshGeneration(ctx context.Context, root string) (string, error) {
	if err := s.checkRoot(ctx, root); err != nil {
		return "", err
	}
	base, _, e := s.Files.SourcePath(ctx, root, "")
	if e != nil {
		return "", e
	}
	source, e := s.Files.MutationSource(ctx, base, "")
	return source.Generation, e
}
func readRefresh(ctx context.Context, q queryer, id string) (v Refresh, err error) {
	var raw []byte
	err = q.QueryRowContext(ctx, `SELECT record FROM knowledge.workspace_metadata_refresh WHERE refresh_id=$1`, id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &v)
	}
	return
}
func saveRefresh(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, v Refresh) error {
	v.Updated = time.Now().UTC()
	v.Counts = nil
	v.Diagnostics = nil
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = q.ExecContext(ctx, `INSERT INTO knowledge.workspace_metadata_refresh(refresh_id,record) VALUES($1,$2) ON CONFLICT(refresh_id) DO UPDATE SET record=EXCLUDED.record,updated_at=now()`, v.ID, raw)
	return e
}

func (s Service) StartRefresh(ctx context.Context, root, relative string) (out Refresh, err error) {
	if err := s.checkRoot(ctx, root); err != nil {
		return out, err
	}
	normalized, e := normalizeCatalogInput(CatalogInput{Root: root, Path: relative})
	if e != nil {
		return out, e
	}
	relative = normalized.Path
	if entry, e := s.Files.Stat(ctx, root, relative); e != nil {
		return out, e
	} else if entry.Kind != "directory" {
		return out, workspacefiles.ErrNotDirectory
	}
	generation, e := s.refreshGeneration(ctx, root)
	if e != nil {
		return out, e
	}
	out = Refresh{ID: "metadata_refresh_" + rand.Text(), Root: root, Path: relative, Generation: generation, State: "ready", Started: time.Now().UTC(), Qualification: "Explicit Markdown navigation metadata only; completed directories are not revisited. Not an atomic filesystem census, text index, embedding or backup proof."}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	// One unfinished scope is reused rather than spawning duplicate crawls.
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "metadata-refresh:"+root+":"+relative); e != nil {
		return out, e
	}
	var raw []byte
	e = tx.QueryRowContext(ctx, `SELECT record FROM knowledge.workspace_metadata_refresh WHERE record->>'root'=$1 AND record->>'path'=$2 AND record->>'generation'=$3 AND record->>'state' IN ('ready','paused','complete_with_errors') ORDER BY updated_at DESC LIMIT 1`, root, relative, generation).Scan(&raw)
	if e == nil {
		if e = json.Unmarshal(raw, &out); e != nil {
			return out, e
		}
		return out, tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	if e = saveRefresh(ctx, tx, out); e != nil {
		return out, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO knowledge.workspace_metadata_refresh_items(refresh_id,path,kind) VALUES($1,$2,'directory')`, out.ID, relative); e != nil {
		return out, e
	}
	// Known missing files need a confirmed source absence, never "not seen".
	if _, e = tx.ExecContext(ctx, `INSERT INTO knowledge.workspace_metadata_refresh_items(refresh_id,path,kind) SELECT $1,path,'file' FROM knowledge.workspace_note_documents WHERE root_ref=$2 AND NOT retired AND path ~* '\.(md|markdown)$' AND ($3='' OR path=$3 OR left(path,length($3)+1)=$3||'/') ON CONFLICT DO NOTHING`, out.ID, root, relative); e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (s Service) RefreshStatus(ctx context.Context, id string) (v Refresh, err error) {
	v, err = readRefresh(ctx, s.DB, id)
	if err != nil {
		return
	}
	if err = s.checkRoot(ctx, v.Root); err != nil {
		return Refresh{}, err
	}
	v.Counts = map[string]int{}
	rows, e := s.DB.QueryContext(ctx, `SELECT CASE WHEN state='done' THEN detail ELSE state END,count(*) FROM knowledge.workspace_metadata_refresh_items WHERE refresh_id=$1 GROUP BY 1`, id)
	if e != nil {
		return v, e
	}
	for rows.Next() {
		var key string
		var count int
		if e = rows.Scan(&key, &count); e != nil {
			rows.Close()
			return v, e
		}
		v.Counts[key] = count
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return v, e
	}
	rows, e = s.DB.QueryContext(ctx, `SELECT path,detail FROM knowledge.workspace_metadata_refresh_items WHERE refresh_id=$1 AND state IN ('error','skipped') ORDER BY path LIMIT 20`, id)
	if e != nil {
		return v, e
	}
	for rows.Next() {
		var p, d string
		if e = rows.Scan(&p, &d); e != nil {
			rows.Close()
			return v, e
		}
		v.Diagnostics = append(v.Diagnostics, p+": "+d)
	}
	e = rows.Err()
	rows.Close()
	return v, e
}
func (s Service) LatestRefresh(ctx context.Context, root string) (Refresh, error) {
	if err := s.checkRoot(ctx, root); err != nil {
		return Refresh{}, err
	}
	var id string
	e := s.DB.QueryRowContext(ctx, `SELECT refresh_id FROM knowledge.workspace_metadata_refresh WHERE record->>'root'=$1 ORDER BY updated_at DESC LIMIT 1`, root).Scan(&id)
	if e != nil {
		return Refresh{}, e
	}
	return s.RefreshStatus(ctx, id)
}
func (s Service) ControlRefresh(ctx context.Context, id, action string) (Refresh, error) {
	if action != "pause" && action != "cancel" && action != "retry" {
		return Refresh{}, workspacefiles.ErrInvalidPath
	}
	return s.withRefresh(ctx, id, func(conn *sql.Conn, v *Refresh) error {
		if action == "retry" {
			if v.State == "cancelled" {
				return fmt.Errorf("cancelled metadata refresh cannot resume")
			}
			if _, e := conn.ExecContext(ctx, `UPDATE knowledge.workspace_metadata_refresh_items SET state='pending',detail='' WHERE refresh_id=$1 AND state='error'`, id); e != nil {
				return e
			}
			v.State = "ready"
			return saveRefresh(ctx, conn, *v)
		}
		if v.State == "complete" || v.State == "complete_with_errors" || v.State == "cancelled" {
			return nil
		}
		v.State = map[string]string{"pause": "paused", "cancel": "cancelled"}[action]
		return saveRefresh(ctx, conn, *v)
	})
}
func (s Service) withRefresh(ctx context.Context, id string, fn func(*sql.Conn, *Refresh) error) (Refresh, error) {
	conn, e := s.DB.Conn(ctx)
	if e != nil {
		return Refresh{}, e
	}
	defer conn.Close()
	key := "metadata-refresh-run:" + id
	var locked bool
	if e = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&locked); e != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return Refresh{}, e
	}
	if !locked {
		return Refresh{}, fmt.Errorf("metadata refresh step is already active")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		if e := conn.QueryRowContext(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key).Scan(&unlocked); e != nil || !unlocked {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	v, e := readRefresh(ctx, conn, id)
	if e != nil {
		return v, e
	}
	if e := s.checkRoot(ctx, v.Root); e != nil {
		return Refresh{}, e
	}
	if e = fn(conn, &v); e != nil {
		return v, e
	}
	return s.RefreshStatus(ctx, id)
}
func (s Service) StepRefresh(ctx context.Context, id string) (Refresh, error) {
	return s.withRefresh(ctx, id, func(conn *sql.Conn, v *Refresh) error {
		if v.State == "complete" || v.State == "complete_with_errors" || v.State == "cancelled" {
			return nil
		}
		generation, e := s.refreshGeneration(ctx, v.Root)
		if e != nil {
			return e
		}
		if generation != v.Generation {
			return fmt.Errorf("metadata source identity changed; create a new refresh for the current Box")
		}
		v.State = "ready"
		deadline := time.Now().Add(2 * time.Second)
		for n := 0; n < 40 && time.Now().Before(deadline); n++ {
			var relative, kind, cursor string
			e = conn.QueryRowContext(ctx, `SELECT path,kind,cursor FROM knowledge.workspace_metadata_refresh_items WHERE refresh_id=$1 AND state='pending' ORDER BY kind DESC,path LIMIT 1`, id).Scan(&relative, &kind, &cursor)
			if errors.Is(e, sql.ErrNoRows) {
				v.State = "complete"
				var failed bool
				if e = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM knowledge.workspace_metadata_refresh_items WHERE refresh_id=$1 AND state='error')`, id).Scan(&failed); e != nil {
					return e
				}
				if failed {
					v.State = "complete_with_errors"
				}
				break
			}
			if e != nil {
				return e
			}
			if denied := s.Files.NavigationPath(v.Root, relative); denied != nil {
				if _, e = conn.ExecContext(ctx, `UPDATE knowledge.workspace_metadata_refresh_items SET state='skipped',detail='hidden or rebuildable path excluded' WHERE refresh_id=$1 AND path=$2`, id, relative); e != nil {
					return e
				}
				continue
			}
			if kind == "directory" {
				page, e := s.Files.List(ctx, workspacefiles.ListInput{Root: v.Root, Path: relative, Limit: 100, Cursor: cursor})
				if errors.Is(e, workspacefiles.ErrInvalidCursor) {
					_, e = conn.ExecContext(ctx, `UPDATE knowledge.workspace_metadata_refresh_items SET cursor='' WHERE refresh_id=$1 AND path=$2`, id, relative)
					if e != nil {
						return e
					}
					break
				}
				if e != nil {
					if _, e = conn.ExecContext(ctx, `UPDATE knowledge.workspace_metadata_refresh_items SET state='error',detail=$3 WHERE refresh_id=$1 AND path=$2`, id, relative, boundedRefreshError(e)); e != nil {
						return e
					}
					continue
				}
				tx, e := conn.BeginTx(ctx, nil)
				if e != nil {
					return e
				}
				for _, entry := range page.Entries {
					if entry.Kind != "directory" && (entry.Kind != "file" || !notePath(entry.RelativePath)) {
						continue
					}
					if _, e = tx.ExecContext(ctx, `INSERT INTO knowledge.workspace_metadata_refresh_items(refresh_id,path,kind) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, entry.RelativePath, entry.Kind); e != nil {
						break
					}
				}
				state := "pending"
				if !page.HasMore {
					state = "done"
				}
				if e == nil {
					_, e = tx.ExecContext(ctx, `UPDATE knowledge.workspace_metadata_refresh_items SET state=$3,cursor=$4,detail='directories' WHERE refresh_id=$1 AND path=$2`, id, relative, state, page.NextCursor)
				}
				if e == nil {
					e = tx.Commit()
				} else {
					tx.Rollback()
				}
				if e != nil {
					return e
				}
			} else {
				before, oldErr := readDocument(ctx, conn, v.Root, relative)
				document, observed := s.Observe(ctx, v.Root, relative)
				state, detail := "done", "refreshed"
				if observed == nil && oldErr == nil && before.current(document.Revision) {
					detail = "unchanged"
				}
				if observed != nil {
					state, detail = "error", boundedRefreshError(observed)
					if errors.Is(observed, fs.ErrNotExist) {
						generation, e = s.refreshGeneration(ctx, v.Root)
						if e != nil {
							return e
						}
						if generation != v.Generation {
							return fmt.Errorf("metadata source identity changed")
						}
						if _, confirm := s.Files.Stat(ctx, v.Root, relative); errors.Is(confirm, fs.ErrNotExist) {
							if e = s.Retire(ctx, v.Root, relative); e != nil {
								return e
							}
							state, detail = "done", "retired"
						}
					}
				}
				if _, e = conn.ExecContext(ctx, `UPDATE knowledge.workspace_metadata_refresh_items SET state=$3,detail=$4 WHERE refresh_id=$1 AND path=$2`, id, relative, state, detail); e != nil {
					return e
				}
			}
		}
		return saveRefresh(ctx, conn, *v)
	})
}
func boundedRefreshError(e error) string {
	s := e.Error()
	if len(s) > 300 {
		s = s[:300]
	}
	return strings.ReplaceAll(s, "\x00", "")
}
