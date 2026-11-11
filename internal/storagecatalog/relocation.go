package storagecatalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

type RetainedRelocation struct {
	ID          string          `json:"id"`
	CustodyID   string          `json:"custody_id"`
	Original    string          `json:"original"`
	Source      string          `json:"source"`
	Destination string          `json:"destination"`
	Digest      string          `json:"digest"`
	Plan        json.RawMessage `json:"plan"`
	State       string          `json:"state"`
}

func (s Service) ResolveCustodyPath(ctx context.Context, owner, path string) (string, error) {
	if s.DB == nil || owner == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("exact custody and historical path required")
	}
	var current string
	err := s.DB.QueryRowContext(ctx, `SELECT storage.resolve_custody_path($1,$2)`, owner, path).Scan(&current)
	return current, err
}

// One statement binds a current path and its completed relocation revision.
// Prepared moves are rejected by resolve_custody_path, never guessed from disk.
func (s Service) ResolveCustodyLocation(ctx context.Context, owner, original string) (string, string, error) {
	if s.DB == nil || owner == "" || !filepath.IsAbs(original) || filepath.Clean(original) != original {
		return "", "", fmt.Errorf("exact custody and historical path required")
	}
	var current, revision string
	err := s.DB.QueryRowContext(ctx, `SELECT storage.resolve_custody_path($1,$2), COALESCE((SELECT relocation_id FROM storage.custody_locations WHERE custody_id=$1 AND ($2=original_prefix OR left($2,length(original_prefix)+1)=original_prefix||'/') ORDER BY length(original_prefix) DESC LIMIT 1),'')`, owner, original).Scan(&current, &revision)
	return current, revision, err
}

func (s Service) SaveRetainedRelocation(ctx context.Context, r RetainedRelocation) error {
	if s.DB == nil || r.ID == "" || len(r.ID) > 160 || r.CustodyID == "" || len(r.CustodyID) > 512 || !strings.HasPrefix(r.Digest, "sha256:") || len(r.Digest) != 71 || !json.Valid(r.Plan) || len(r.Plan) > 128<<20 {
		return fmt.Errorf("invalid retained relocation plan")
	}
	for _, p := range []string{r.Original, r.Source, r.Destination} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return fmt.Errorf("exact absolute relocation paths required")
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO storage.retained_relocations(relocation_id,custody_id,original_prefix,source_prefix,destination_prefix,plan_digest,plan_json,state) VALUES($1,$2,$3,$4,$5,$6,$7,'planned') ON CONFLICT(relocation_id) DO NOTHING`, r.ID, r.CustodyID, r.Original, r.Source, r.Destination, r.Digest, r.Plan)
	if err != nil {
		return err
	}
	var same bool
	if err = tx.QueryRowContext(ctx, `SELECT custody_id=$2 AND original_prefix=$3 AND source_prefix=$4 AND destination_prefix=$5 AND plan_digest=$6 AND plan_json=$7 FROM storage.retained_relocations WHERE relocation_id=$1`, r.ID, r.CustodyID, r.Original, r.Source, r.Destination, r.Digest, r.Plan).Scan(&same); err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("relocation identity already binds another plan")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO storage.retained_relocation_events(relocation_id,phase,evidence) VALUES($1,'planned',jsonb_build_object('digest',$2::text)) ON CONFLICT(relocation_id,phase) DO NOTHING`, r.ID, r.Digest)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func loadRelocation(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (r RetainedRelocation, err error) {
	err = q.QueryRowContext(ctx, `SELECT relocation_id,custody_id,original_prefix,source_prefix,destination_prefix,plan_digest,plan_json,state FROM storage.retained_relocations WHERE relocation_id=$1`, id).Scan(&r.ID, &r.CustodyID, &r.Original, &r.Source, &r.Destination, &r.Digest, &r.Plan, &r.State)
	return
}
func (s Service) RetainedRelocation(ctx context.Context, id string) (RetainedRelocation, error) {
	if s.DB == nil {
		return RetainedRelocation{}, fmt.Errorf("storage catalog unavailable")
	}
	return loadRelocation(ctx, s.DB, id)
}

// The durable prepared intent survives a crash before/after rename. The shared
// custody writer lock excludes supported readers while bytes and projections
// change. A failed callback leaves that exact intent for explicit recovery.
func (s Service) ApplyRetainedRelocation(ctx context.Context, id, digest string, move func(RetainedRelocation) (RebindPathsInput, json.RawMessage, error)) (RetainedRelocation, error) {
	return s.applyPathRelocation(ctx, id, digest, move, nil, nil)
}

// The accepted filesystem owner commits its project/Notes location successor in
// the same transaction as custody, catalog and completion evidence.
func (s Service) ApplyWorkspacePathRelocation(ctx context.Context, id, digest string, move func(RetainedRelocation) (RebindPathsInput, json.RawMessage, error), prepare, commit func(context.Context, *sql.Tx) error) (RetainedRelocation, error) {
	if commit == nil {
		return RetainedRelocation{}, fmt.Errorf("location successor owner required")
	}
	return s.applyPathRelocation(ctx, id, digest, move, prepare, commit)
}

func (s Service) applyPathRelocation(ctx context.Context, id, digest string, move func(RetainedRelocation) (RebindPathsInput, json.RawMessage, error), prepare, commit func(context.Context, *sql.Tx) error) (RetainedRelocation, error) {
	if s.DB == nil || move == nil {
		return RetainedRelocation{}, fmt.Errorf("trusted catalog and move owner required")
	}
	conn, release, err := acquireWorkspaceCustodySession(ctx, s.DB, false)
	if err != nil {
		return RetainedRelocation{}, err
	}
	defer release()
	r, err := loadRelocation(ctx, conn, id)
	if err != nil {
		return r, err
	}
	if r.Digest != digest {
		return r, fmt.Errorf("relocation digest changed")
	}
	if r.State == "complete" {
		return r, nil
	}
	if r.State != "planned" && r.State != "prepared" {
		return r, fmt.Errorf("relocation is terminal: %s", r.State)
	}
	var otherPrepared bool
	if err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM storage.retained_relocations WHERE state='prepared' AND relocation_id<>$1)`, r.ID).Scan(&otherPrepared); err != nil {
		return r, err
	}
	if otherPrepared {
		return r, fmt.Errorf("an unfinished retained move exists; recover that exact operation before another relocation")
	}
	if r.State == "planned" {
		tx, e := conn.BeginTx(ctx, nil)
		if e != nil {
			return r, e
		}
		if prepare != nil {
			if e = prepare(ctx, tx); e != nil {
				tx.Rollback()
				return r, e
			}
		}
		_, e = tx.ExecContext(ctx, `UPDATE storage.retained_relocations SET state='prepared' WHERE relocation_id=$1 AND state='planned'`, id)
		if e == nil {
			_, e = tx.ExecContext(ctx, `INSERT INTO storage.retained_relocation_events(relocation_id,phase,evidence) VALUES($1,'prepared',jsonb_build_object('digest',$2::text))`, id, digest)
		}
		if e != nil {
			tx.Rollback()
			return r, e
		}
		if e = tx.Commit(); e != nil {
			return r, e
		}
		r.State = "prepared"
	}
	rebind, evidence, err := move(r)
	if err != nil {
		return r, err
	}
	if !json.Valid(evidence) {
		return r, fmt.Errorf("relocation completion evidence required")
	}
	if len(rebind.Entries) > 0 || len(rebind.PhysicalRefs) > 0 {
		rebind, err = normalizeRebindPathsInput(rebind)
		if err != nil {
			return r, err
		}
	}
	// A physical move must never rewrite original-source provenance.
	for _, entry := range rebind.Entries {
		if entry.ExpectedOriginalSourcePath != entry.NewOriginalSourcePath {
			return r, fmt.Errorf("relocation cannot rewrite original source history")
		}
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	if commit != nil {
		if err = commit(ctx, tx); err != nil {
			return r, err
		}
	}
	if err = rebindPathsTx(ctx, tx, rebind, &RebindPathsResult{}); err != nil {
		return r, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO storage.custody_locations(custody_id,original_prefix,current_prefix,relocation_id) VALUES($1,$2,$3,$4) ON CONFLICT(custody_id,original_prefix) DO UPDATE SET current_prefix=excluded.current_prefix,relocation_id=excluded.relocation_id WHERE storage.custody_locations.current_prefix=$5`, r.CustodyID, r.Original, r.Destination, r.ID, r.Source)
	if err != nil {
		return r, err
	}
	var current string
	if err = tx.QueryRowContext(ctx, `SELECT current_prefix FROM storage.custody_locations WHERE custody_id=$1 AND original_prefix=$2`, r.CustodyID, r.Original).Scan(&current); err != nil {
		return r, err
	}
	if current != r.Destination {
		return r, fmt.Errorf("custody location changed since review")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO storage.retained_relocation_events(relocation_id,phase,evidence) VALUES($1,'complete',$2)`, r.ID, evidence); err != nil {
		return r, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE storage.retained_relocations SET state='complete',completed_at=now() WHERE relocation_id=$1 AND state='prepared'`, r.ID); err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	r.State = "complete"
	return r, nil
}

// Cancellation records proven absence of a move. It never edits the original
// plan, removes history, changes a current location or undoes a completed move.
func (s Service) CancelRetainedRelocation(ctx context.Context, id, digest string, prove func(RetainedRelocation) (json.RawMessage, error)) (RetainedRelocation, error) {
	return s.cancelPathRelocation(ctx, id, digest, prove, nil)
}

func (s Service) CancelWorkspacePathRelocation(ctx context.Context, id, digest string, prove func(RetainedRelocation) (json.RawMessage, error), commit func(context.Context, *sql.Tx) error) (RetainedRelocation, error) {
	return s.cancelPathRelocation(ctx, id, digest, prove, commit)
}

func (s Service) cancelPathRelocation(ctx context.Context, id, digest string, prove func(RetainedRelocation) (json.RawMessage, error), commit func(context.Context, *sql.Tx) error) (RetainedRelocation, error) {
	if s.DB == nil || prove == nil {
		return RetainedRelocation{}, fmt.Errorf("trusted catalog and no-effect proof required")
	}
	conn, release, err := acquireWorkspaceCustodySession(ctx, s.DB, false)
	if err != nil {
		return RetainedRelocation{}, err
	}
	defer release()
	r, err := loadRelocation(ctx, conn, id)
	if err != nil {
		return r, err
	}
	if r.Digest != digest {
		return r, fmt.Errorf("relocation digest changed")
	}
	if r.State == "cancelled" {
		return r, nil
	}
	if r.State != "planned" && r.State != "prepared" {
		return r, fmt.Errorf("only an unperformed move can be cancelled; current state: %s", r.State)
	}
	evidence, err := prove(r)
	if err != nil {
		return r, err
	}
	if !json.Valid(evidence) || string(evidence) == "null" {
		return r, fmt.Errorf("cancellation proof required")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	if commit != nil {
		if err = commit(ctx, tx); err != nil {
			return r, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO storage.retained_relocation_events(relocation_id,phase,evidence) VALUES($1,'cancelled',$2)`, id, evidence); err != nil {
		return r, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE storage.retained_relocations SET state='cancelled' WHERE relocation_id=$1 AND state IN ('planned','prepared')`, id); err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	r.State = "cancelled"
	return r, nil
}
