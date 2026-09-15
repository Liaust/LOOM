package projectapply

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	"loom.local/loom/internal/ids"
	pc "loom.local/loom/internal/projectcontracts"
)

type journalQuery interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type operation struct {
	ID           string
	Principal    Principal
	Request      pc.DeclarationApplyRequest
	RequestHash  string
	Resolution   Resolution
	State        pc.DeclarationOperationState
	SupersededBy string
	Errors       []pc.DeclarationError
	Actions      map[string]*actionRecord
}
type actionRecord struct {
	State   pc.DeclarationOperationState
	Token   string
	Receipt *Receipt
	Error   *pc.DeclarationError
}
type projectLock struct {
	conn *sql.Conn
	key  int64
	pid  int
}

func (s *Service) lock(ctx context.Context, project string) (*projectLock, error) {
	c, e := s.db.Conn(ctx)
	if e != nil {
		return nil, fail(pc.DeclarationTargetUnavailable, "journal_unavailable")
	}
	sum := sha256.Sum256([]byte("projectapply:" + project))
	l := &projectLock{conn: c, key: int64(binary.BigEndian.Uint64(sum[:8]))}
	if _, e = c.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, l.key); e == nil {
		e = c.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&l.pid)
	}
	if e != nil {
		l.close()
		return nil, fail(pc.DeclarationTargetUnavailable, "lock_unavailable")
	}
	return l, nil
}
func (l *projectLock) Check(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return fail(pc.DeclarationOwnerFailed, "execution_cancelled")
	}
	var ok bool
	// Inspect the actual session-held lock; Ping alone would miss explicit unlock.
	e := l.conn.QueryRowContext(ctx, `SELECT pg_backend_pid()=$1 AND EXISTS (
 SELECT 1 FROM pg_locks WHERE locktype='advisory' AND pid=pg_backend_pid()
 AND classid=$2::oid AND objid=$3::oid AND objsubid=1 AND granted)`, l.pid, uint32(uint64(l.key)>>32), uint32(l.key)).Scan(&ok)
	if e != nil || !ok {
		return fail(pc.DeclarationTargetUnavailable, "execution_fence_lost")
	}
	return nil
}
func (l *projectLock) close() {
	ctx, c := context.WithTimeout(context.Background(), 3*time.Second)
	defer c()
	if _, err := l.conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, l.key); err != nil {
		// Never return a possibly locked session to the pool after an uncertain
		// unlock. Raw's ErrBadConn contract discards the underlying connection.
		_ = l.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	_ = l.conn.Close()
}
func requestIdentity(r pc.DeclarationApplyRequest) (pc.DeclarationApplyRequest, string, error) {
	r.OperationID = ""
	r.Effects = append([]pc.DeclarationEffect{}, r.Effects...)
	sortEffects(r.Effects)
	r.ApprovalRefs = append([]string{}, r.ApprovalRefs...)
	sortStrings(r.ApprovalRefs)
	raw, e := canonicalValue(r)
	if e != nil {
		return r, "", e
	}
	return r, hashBytes(raw), nil
}
func loadOperation(ctx context.Context, q journalQuery, id string) (*operation, error) {
	o := &operation{ID: id, Actions: map[string]*actionRecord{}}
	var req, res, errs []byte
	e := q.QueryRowContext(ctx, `SELECT actor_id,origin_node_id,request_hash,request,resolution,state,COALESCE(superseded_by,''),errors FROM projects.declaration_operations WHERE operation_id=$1`, id).Scan(&o.Principal.ActorID, &o.Principal.OriginNodeID, &o.RequestHash, &req, &res, &o.State, &o.SupersededBy, &errs)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, fail(pc.DeclarationReferenceMissing, "operation_missing")
	}
	if e != nil {
		return nil, fail(pc.DeclarationTargetUnavailable, "journal_unavailable")
	}
	for _, v := range []struct {
		raw []byte
		out any
	}{{req, &o.Request}, {res, &o.Resolution}, {errs, &o.Errors}} {
		if json.Unmarshal(v.raw, v.out) != nil {
			return nil, fail(pc.DeclarationInvalid, "journal_corrupt")
		}
	}
	rows, e := q.QueryContext(ctx, `SELECT action_id,token,state,receipt,error FROM projects.declaration_action_receipts WHERE operation_id=$1`, id)
	if e != nil {
		return nil, fail(pc.DeclarationTargetUnavailable, "journal_unavailable")
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var raw, errRaw []byte
		a := &actionRecord{}
		if e = rows.Scan(&id, &a.Token, &a.State, &raw, &errRaw); e != nil {
			return nil, fail(pc.DeclarationTargetUnavailable, "journal_unavailable")
		}
		if len(raw) > 0 && json.Unmarshal(raw, &a.Receipt) != nil {
			return nil, fail(pc.DeclarationInvalid, "journal_corrupt")
		}
		if len(errRaw) > 0 && json.Unmarshal(errRaw, &a.Error) != nil {
			return nil, fail(pc.DeclarationInvalid, "journal_corrupt")
		}
		o.Actions[id] = a
	}
	if rows.Err() != nil {
		return nil, fail(pc.DeclarationTargetUnavailable, "journal_unavailable")
	}
	if len(o.Actions) != len(o.Resolution.Plan.Basis.Actions) {
		return nil, fail(pc.DeclarationInvalid, "journal_incomplete")
	}
	return o, nil
}
func findOperation(ctx context.Context, q journalQuery, p Principal, project, key string) (string, error) {
	var id string
	e := q.QueryRowContext(ctx, `SELECT operation_id FROM projects.declaration_operations WHERE actor_id=$1 AND origin_node_id=$2 AND project_id=$3 AND operation_kind='declaration_apply' AND idempotency_key=$4`, p.ActorID, p.OriginNodeID, project, key).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		return "", nil
	}
	if e != nil {
		return "", fail(pc.DeclarationTargetUnavailable, "journal_unavailable")
	}
	return id, nil
}

// Discover only an exact previously journaled request before fresh planning.
// Keys remain project-scoped. More than one candidate is never a latest-wins
// choice; the caller still reloads under the selected project's lock.
func findExactRequestOperation(ctx context.Context, q journalQuery, p Principal, request pc.DeclarationApplyRequest, hash string) (string, error) {
	rows, err := q.QueryContext(ctx, `SELECT operation_id,request FROM projects.declaration_operations WHERE actor_id=$1 AND origin_node_id=$2 AND operation_kind='declaration_apply' AND idempotency_key=$3 AND request_hash=$4 ORDER BY operation_id LIMIT 2`, p.ActorID, p.OriginNodeID, request.IdempotencyKey, hash)
	if err != nil {
		return "", fail(pc.DeclarationTargetUnavailable, "journal_unavailable")
	}
	defer rows.Close()
	var found string
	for rows.Next() {
		var id string
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return "", fail(pc.DeclarationTargetUnavailable, "journal_unavailable")
		}
		var stored pc.DeclarationApplyRequest
		if found != "" || strictOwnerPayload(raw, &stored) != nil || !same(stored, request) {
			return "", fail(pc.DeclarationOperationConflict, "exact_request_identity_conflict")
		}
		found = id
	}
	if rows.Err() != nil {
		return "", fail(pc.DeclarationTargetUnavailable, "journal_unavailable")
	}
	return found, nil
}
func stableToken(x Resolution, a pc.DeclarationAction) string {
	raw, _ := canonicalValue(struct {
		PlanID    string `json:"plan_id"`
		ActionID  string `json:"action_id"`
		InputHash string `json:"input_hash"`
	}{x.Plan.PlanID, a.ID, a.InputHash})
	return hashBytes(raw)
}
func createOperation(ctx context.Context, l *projectLock, p Principal, r pc.DeclarationApplyRequest, hash string, x Resolution) (*operation, error) {
	o := &operation{ID: ids.NewJobID(), Principal: p, Request: r, RequestHash: hash, Resolution: x, State: pc.DeclarationOperationQueued, Errors: []pc.DeclarationError{}, Actions: map[string]*actionRecord{}}
	tx, e := l.conn.BeginTx(ctx, nil)
	if e != nil {
		return nil, fail(pc.DeclarationTargetUnavailable, "journal_unavailable")
	}
	defer tx.Rollback()
	req, _ := canonicalValue(r)
	res, _ := canonicalValue(x)
	t := x.Plan.Basis.Target
	_, e = tx.ExecContext(ctx, `INSERT INTO projects.declaration_operations(operation_id,actor_id,origin_node_id,project_id,target_node_id,project_root,location_revision,operation_kind,idempotency_key,request_hash,plan_id,request,resolution,state) VALUES($1,$2,$3,$4,$5,$6,$7,'declaration_apply',$8,$9,$10,$11,$12,'queued')`, o.ID, p.ActorID, p.OriginNodeID, t.ProjectID, t.OwnerNodeID, t.ProjectRoot, t.LocationRevision, r.IdempotencyKey, hash, x.Plan.PlanID, req, res)
	if e != nil {
		return nil, fail(pc.DeclarationTargetUnavailable, "journal_create_failed")
	}
	for _, a := range x.Plan.Basis.Actions {
		ar := &actionRecord{State: pc.DeclarationOperationQueued, Token: stableToken(x, a)}
		o.Actions[a.ID] = ar
		_, e = tx.ExecContext(ctx, `INSERT INTO projects.declaration_action_receipts(operation_id,action_id,owner,input_hash,token,state) VALUES($1,$2,$3,$4,$5,'queued')`, o.ID, a.ID, a.Owner, a.InputHash, ar.Token)
		if e != nil {
			return nil, fail(pc.DeclarationTargetUnavailable, "journal_create_failed")
		}
	}
	// A plan ID also binds actor, effect selection and prerequisite facts; its
	// inequality is not evidence of a conflicting desired revision. Inspect
	// pending owner/resource scopes under the same project lock and transaction.
	rows, e := tx.QueryContext(ctx, `SELECT operation_id FROM projects.declaration_operations WHERE project_id=$1 AND operation_id<>$2 AND plan_id<>$3 AND state IN ('queued','running','partial','failed') ORDER BY created_at,operation_id`, t.ProjectID, o.ID, x.Plan.PlanID)
	if e != nil {
		return nil, fail(pc.DeclarationTargetUnavailable, "journal_unavailable")
	}
	candidates := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, fail(pc.DeclarationTargetUnavailable, "journal_unavailable")
		}
		candidates = append(candidates, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, fail(pc.DeclarationTargetUnavailable, "journal_unavailable")
	}
	for _, id := range candidates {
		old, err := loadOperation(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		overlap, changed, err := desiredSupersession(old, x)
		if err != nil {
			return nil, err
		}
		if !overlap {
			continue
		}
		if !changed {
			// Equivalent unfinished work under a different plan would mint different
			// tokens. A missing/uncertain owner receipt never authorizes repeating it.
			// Preserve the original; its authorized resume can recover exact tokens.
			// Same-plan requests are excluded above because they already share tokens.
			return nil, fail(pc.DeclarationOperationConflict, "unfinished_overlapping_operation")
		}
		if _, e = tx.ExecContext(ctx, `UPDATE projects.declaration_operations SET state='superseded',superseded_by=$1,updated_at=now() WHERE operation_id=$2`, o.ID, id); e != nil {
			return nil, fail(pc.DeclarationTargetUnavailable, "journal_create_failed")
		}
	}
	if e = tx.Commit(); e != nil {
		// Commit may have reached PostgreSQL. Re-read while retaining this session's
		// fence; a successful read gives the caller a durable operation reference.
		recovery, c := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer c()
		if l.Check(recovery) == nil {
			if known, err := loadOperation(recovery, l.conn, o.ID); err == nil {
				return known, nil
			}
		}
		return o, fail(pc.DeclarationTargetUnavailable, "journal_commit_uncertain")
	}
	return o, nil
}
func saveAction(ctx context.Context, l *projectLock, o *operation, a pc.DeclarationAction, ar *actionRecord) error {
	if e := l.Check(ctx); e != nil {
		return e
	}
	var receipt, issue any
	if ar.Receipt != nil {
		raw, _ := canonicalValue(ar.Receipt)
		receipt = raw
	}
	if ar.Error != nil {
		raw, _ := canonicalValue(ar.Error)
		issue = raw
	}
	_, e := l.conn.ExecContext(ctx, `UPDATE projects.declaration_action_receipts SET state=$3,receipt=$4,error=$5,updated_at=now() WHERE operation_id=$1 AND action_id=$2`, o.ID, a.ID, ar.State, receipt, issue)
	if e != nil {
		return fail(pc.DeclarationTargetUnavailable, "journal_write_failed")
	}
	o.Actions[a.ID] = ar
	return nil
}
func saveState(ctx context.Context, l *projectLock, o *operation, state pc.DeclarationOperationState, errs []pc.DeclarationError) error {
	if e := l.Check(ctx); e != nil {
		return e
	}
	raw, _ := canonicalValue(errs)
	_, e := l.conn.ExecContext(ctx, `UPDATE projects.declaration_operations SET state=$2,errors=$3,updated_at=now() WHERE operation_id=$1`, o.ID, state, raw)
	if e != nil {
		return fail(pc.DeclarationTargetUnavailable, "journal_write_failed")
	}
	o.State = state
	o.Errors = errs
	return nil
}
