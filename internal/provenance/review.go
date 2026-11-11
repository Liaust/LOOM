package provenance

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultReviewLimit           = 10
	MaximumReviewExcludedDomains = 32
	MaximumReviewCheckpoints     = 1000
	MaximumReviewCheckpointBytes = 128 << 10
)

type ReviewCheckpoint struct {
	ObjectKind  string     `json:"object_kind"`
	ObjectID    SemanticID `json:"object_id"`
	ChangeToken string     `json:"change_token"`
}

type ReviewRequest struct {
	Limit          int                `json:"limit"`
	Cursor         string             `json:"cursor,omitempty"`
	Domain         string             `json:"domain,omitempty"`
	Visibility     string             `json:"visibility,omitempty"`
	ExcludeDomains []string           `json:"exclude_domains,omitempty"`
	Reviewed       []ReviewCheckpoint `json:"reviewed,omitempty"`
}

type ReviewItem struct {
	ObjectKind     string     `json:"object_kind"`
	ObjectID       SemanticID `json:"object_id"`
	EffectiveState string     `json:"effective_state"`
	ReviewReason   string     `json:"review_reason"`
	ChangeToken    string     `json:"change_token"`
	ChangedAt      time.Time  `json:"changed_at"`
	Domain         string     `json:"domain"`
	Visibility     string     `json:"visibility"`
}

type ReviewResponse struct {
	SchemaVersion string       `json:"schema_version"`
	AsOf          time.Time    `json:"as_of"`
	Items         []ReviewItem `json:"items"`
	NextCursor    string       `json:"next_cursor,omitempty"`
	Truncated     bool         `json:"truncated"`
}

// ReviewTransport extends the foundation read owner without requiring other
// foundation implementations to pretend they support effective review reads.
type ReviewTransport interface {
	ListReview(context.Context, ReviewRequest) (ReviewResponse, error)
}

type reviewCursor struct {
	BoxDigest string     `json:"box_digest,omitempty"`
	AsOf      time.Time  `json:"as_of"`
	ChangedAt time.Time  `json:"changed_at"`
	Kind      string     `json:"kind"`
	ID        SemanticID `json:"id"`
}

func ValidateReviewRequest(request ReviewRequest) error {
	if _, err := boundedLimit(request.Limit); err != nil {
		return err
	}
	if len(request.Domain) > MaximumFoundationFilterBytes || len(request.Visibility) > MaximumFoundationFilterBytes {
		return errors.New("review domain and visibility filters exceed their byte bound")
	}
	if len(request.ExcludeDomains) > MaximumReviewExcludedDomains {
		return errors.New("too many excluded review domains")
	}
	for _, domain := range request.ExcludeDomains {
		if strings.TrimSpace(domain) == "" || len(domain) > MaximumFoundationFilterBytes {
			return errors.New("excluded review domains must be nonempty bounded exact names")
		}
	}
	if len(request.Reviewed) > MaximumReviewCheckpoints {
		return errors.New("too many review checkpoints; retain state rather than silently dropping checkpoints")
	}
	encoded, err := json.Marshal(request.Reviewed)
	if err != nil || len(encoded) > MaximumReviewCheckpointBytes {
		return errors.New("review checkpoints exceed their byte bound")
	}
	for _, checkpoint := range request.Reviewed {
		if checkpoint.ObjectKind != "candidate" && checkpoint.ObjectKind != "case" {
			return errors.New("review checkpoint kind must be candidate or case")
		}
		if err := checkpoint.ObjectID.Validate(); err != nil {
			return err
		}
		if len(checkpoint.ChangeToken) != 32 || strings.Trim(checkpoint.ChangeToken, "0123456789abcdef") != "" {
			return errors.New("review checkpoint change_token must be the returned 32-character token")
		}
	}
	if request.Cursor != "" {
		_, err := decodeReviewCursor(request.Cursor)
		return err
	}
	return nil
}

func decodeReviewCursor(raw string) (reviewCursor, error) {
	var cursor reviewCursor
	if len(raw) > 1024 {
		return cursor, errors.New("review cursor exceeds its byte bound")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cursor, errors.New("review cursor is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || cursor.AsOf.IsZero() || cursor.ChangedAt.IsZero() || cursor.ChangedAt.After(cursor.AsOf) || (cursor.Kind != "candidate" && cursor.Kind != "case") || cursor.ID.Validate() != nil {
		return cursor, errors.New("review cursor is invalid")
	}
	// Reject trailing objects as well as a manually edited cursor document.
	if !json.Valid(payload) {
		return cursor, errors.New("review cursor is invalid")
	}
	return cursor, nil
}

func (api *FoundationAPI) ListReview(ctx context.Context, request ReviewRequest) (ReviewResponse, error) {
	if err := ValidateReviewRequest(request); err != nil {
		return ReviewResponse{}, &FoundationValidationError{Err: err}
	}
	if api == nil || api.store == nil {
		return ReviewResponse{}, errors.New("provenance review store is unavailable")
	}
	return api.store.listReview(ctx, request, time.Now().UTC())
}

// One bounded SQL projection selects effective work and checkpoint suppression
// before LIMIT. It returns metadata only, never submitted claims or event bodies.
func (store *Store) listReview(ctx context.Context, request ReviewRequest, now time.Time) (ReviewResponse, error) {
	if request.Limit == 0 {
		request.Limit = DefaultReviewLimit
	}
	limit, err := boundedLimit(request.Limit)
	if err != nil {
		return ReviewResponse{}, err
	}
	ctx, err = store.boxSelectionContext(ctx)
	if err != nil {
		return ReviewResponse{}, err
	}
	cursor := reviewCursor{AsOf: now.UTC()}
	if request.Cursor != "" {
		cursor, err = decodeReviewCursor(request.Cursor)
		if err != nil {
			return ReviewResponse{}, err
		}
		if cursor.BoxDigest != selectionDigest(ctx) {
			return ReviewResponse{}, &FoundationValidationError{Err: errors.New("review cursor belongs to another Box selection")}
		}
	}
	cursor.BoxDigest = selectionDigest(ctx)
	reviewed := request.Reviewed
	if reviewed == nil {
		reviewed = []ReviewCheckpoint{}
	}
	checkpoints, err := json.Marshal(reviewed)
	if err != nil {
		return ReviewResponse{}, err
	}
	exclusions := request.ExcludeDomains
	if exclusions == nil {
		exclusions = []string{}
	}
	var afterTime *time.Time
	if request.Cursor != "" {
		afterTime = &cursor.ChangedAt
	}
	rows, err := store.q.query(ctx, reviewSQL, cursor.AsOf, request.Domain, request.Visibility, exclusions,
		checkpoints, afterTime, cursor.Kind, string(cursor.ID), limit+1, selectedBoxes(ctx))
	if err != nil {
		return ReviewResponse{}, fmt.Errorf("list effective provenance review: %w", err)
	}
	defer rows.Close()
	result := ReviewResponse{SchemaVersion: SchemaVersion, AsOf: cursor.AsOf, Items: make([]ReviewItem, 0, limit+1)}
	for rows.Next() {
		var item ReviewItem
		var id string
		if err := rows.Scan(&item.ObjectKind, &id, &item.EffectiveState, &item.ReviewReason, &item.ChangeToken, &item.ChangedAt, &item.Domain, &item.Visibility); err != nil {
			return ReviewResponse{}, fmt.Errorf("scan effective provenance review: %w", err)
		}
		item.ObjectID = SemanticID(id)
		item.ChangedAt = item.ChangedAt.UTC()
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return ReviewResponse{}, fmt.Errorf("iterate effective provenance review: %w", err)
	}
	result.Truncated = len(result.Items) > limit
	if result.Truncated {
		result.Items = result.Items[:limit]
		last := result.Items[limit-1]
		encoded, err := json.Marshal(reviewCursor{AsOf: cursor.AsOf, ChangedAt: last.ChangedAt, Kind: last.ObjectKind, ID: last.ObjectID, BoxDigest: cursor.BoxDigest})
		if err != nil {
			return ReviewResponse{}, err
		}
		result.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return result, nil
}

var reviewSQL = `
WITH deferrals AS (
 SELECT DISTINCT ON (e.candidate_id) e.candidate_id, e.id, coalesce(o.applied_at,e.occurred_at) AS at,
        nullif(e.payload_json->>'next_review_at','')::timestamptz AS due
 FROM provenance.candidate_events e JOIN provenance.operation_history o ON o.id=e.operation_id
 WHERE e.event_type='deferred' ORDER BY e.candidate_id,e.id DESC
), evidence AS (
 SELECT DISTINCT ON (e.candidate_id) e.candidate_id,e.id,coalesce(o.applied_at,e.linked_at) AS at
 FROM provenance.candidate_evidence_links e JOIN provenance.operation_history o ON o.id=e.operation_id
 ORDER BY e.candidate_id,coalesce(o.applied_at,e.linked_at) DESC,e.id DESC
), pending AS (
 SELECT c.id,c.domain,c.visibility,c.registered_at,d.id AS defer_id,d.at AS deferred_at,d.due,
        e.id AS evidence_id,e.at AS evidence_at
 FROM provenance.candidates c LEFT JOIN deferrals d ON d.candidate_id=c.id LEFT JOIN evidence e ON e.candidate_id=c.id
 WHERE NOT EXISTS (SELECT 1 FROM provenance.candidate_events t WHERE t.candidate_id=c.id AND t.event_type IN ('accepted','consolidated','rejected'))
 AND ` + anchorsBoxSQL("c.submitted_json->'anchors'", "$10") + `
), case_deferrals AS (
 SELECT DISTINCT ON (e.case_id) e.case_id,e.id,coalesce(o.applied_at,e.occurred_at) AS at,
        coalesce(nullif(e.payload_json->>'next_review_at',''),nullif(e.payload_json->'payload'->>'next_review_at',''))::timestamptz AS due
 FROM provenance.case_events e LEFT JOIN provenance.operation_history o ON o.id=e.operation_id
 WHERE e.event_type='review_deferred'
 ORDER BY e.case_id,e.occurred_at DESC,CASE WHEN jsonb_typeof(e.payload_json->'append_order')='number' THEN (e.payload_json->>'append_order')::bigint ELSE 0 END DESC,e.id DESC
), case_outcomes AS (
 SELECT DISTINCT ON (e.case_id) e.case_id,e.outcome
 FROM provenance.case_events e WHERE e.outcome IS NOT NULL
 ORDER BY e.case_id,e.occurred_at DESC,CASE WHEN jsonb_typeof(e.payload_json->'append_order')='number' THEN (e.payload_json->>'append_order')::bigint ELSE 0 END DESC,e.id DESC
), activity AS (
 SELECT e.case_id,'event:'||e.id::text AS token,coalesce(o.applied_at,e.occurred_at) AS at
 FROM provenance.case_events e LEFT JOIN provenance.operation_history o ON o.id=e.operation_id
 UNION ALL
 SELECT m.case_id,'member:'||m.id::text,coalesce(o.applied_at,m.attached_at)
 FROM provenance.case_members m LEFT JOIN provenance.operation_history o ON o.id=m.operation_id
 UNION ALL
 SELECT m.case_id,'evidence:'||e.id::text,e.at FROM provenance.case_members m JOIN evidence e ON e.candidate_id=m.candidate_id
 UNION ALL
 SELECT m.case_id,'record-event:'||e.id::text,o.applied_at FROM provenance.case_members m
 JOIN provenance.record_events e ON e.record_id=m.record_id JOIN provenance.operation_history o ON o.id=e.operation_id
 UNION ALL
 SELECT m.case_id,'record-source:'||s.source_reference_id::text,s.linked_at
 FROM provenance.case_members m JOIN provenance.record_sources s ON s.record_id=m.record_id
 UNION ALL
 SELECT e.resolution_case_id,'registered-evidence:'||e.id::text,e.registered_at
 FROM provenance.evidence_registrations e WHERE e.resolution_case_id IS NOT NULL
), case_activity AS (
 SELECT DISTINCT ON (case_id) case_id,token,at,count(*) OVER (PARTITION BY case_id) AS entries
 FROM activity ORDER BY case_id,at DESC,token DESC
), case_due AS (
 SELECT m.case_id,max(p.due) AS due,string_agg(p.defer_id::text,',' ORDER BY p.defer_id) AS token
 FROM provenance.case_members m JOIN pending p ON p.id=m.candidate_id
 WHERE p.due <= $1 GROUP BY m.case_id
), work AS (
 SELECT 'candidate'::text AS kind,p.id,'pending'::text AS state,
        CASE WHEN p.evidence_at > p.deferred_at THEN 'changed_evidence' WHEN p.defer_id IS NOT NULL THEN 'deferral_due' ELSE 'unreviewed_pending' END AS reason,
        md5(concat_ws(':',p.id::text,extract(epoch FROM p.registered_at)::text,p.defer_id::text,p.evidence_id::text,extract(epoch FROM p.evidence_at)::text,
            CASE WHEN p.due <= $1 THEN extract(epoch FROM p.due)::text ELSE '' END)) AS token,
        greatest(p.registered_at,p.deferred_at,p.evidence_at,CASE WHEN p.due <= $1 THEN p.due END) AS changed_at,p.domain,p.visibility
 FROM pending p WHERE p.defer_id IS NULL OR p.evidence_at > p.deferred_at OR p.due <= $1
 UNION ALL
 SELECT 'case',c.id,coalesce(o.outcome,c.initial_status),
        CASE WHEN a.at > d.at THEN 'changed_evidence' WHEN d.due <= $1 OR cd.due IS NOT NULL THEN 'deferral_due' ELSE 'unresolved_case' END,
        md5(concat_ws(':',c.id::text,extract(epoch FROM c.created_at)::text,a.token,a.entries::text,extract(epoch FROM a.at)::text,d.id::text,cd.token,
            CASE WHEN d.due <= $1 THEN extract(epoch FROM d.due)::text ELSE '' END)),
        greatest(c.created_at,a.at,d.at,cd.due,CASE WHEN d.due <= $1 THEN d.due END),c.domain,c.visibility
 FROM provenance.resolution_cases c LEFT JOIN case_outcomes o ON o.case_id=c.id
 LEFT JOIN case_deferrals d ON d.case_id=c.id LEFT JOIN case_activity a ON a.case_id=c.id LEFT JOIN case_due cd ON cd.case_id=c.id
 WHERE lower(btrim(c.initial_status)) NOT IN ('closed','resolved','dismissed')
   AND ` + caseBoxSQL("c.id", "$10") + `
   AND NOT EXISTS (SELECT 1 FROM provenance.case_events t WHERE t.case_id=c.id AND lower(btrim(t.outcome)) IN ('closed','resolved','dismissed'))
   AND (d.id IS NULL OR a.at > d.at OR d.due <= $1 OR cd.due IS NOT NULL)
)
SELECT kind,id::text,state,reason,token,changed_at,domain,visibility FROM work
WHERE changed_at <= $1 AND ($2::text='' OR domain=$2) AND ($3::text='' OR visibility=$3)
  AND NOT (domain=ANY($4::text[]))
  AND NOT EXISTS (SELECT 1 FROM jsonb_to_recordset($5::jsonb) AS seen(object_kind text,object_id text,change_token text)
                  WHERE seen.object_kind=kind AND seen.object_id=id::text AND seen.change_token=token)
  AND ($6::timestamptz IS NULL OR (changed_at,kind,id::text)>($6,$7::text,$8::text))
ORDER BY changed_at,kind,id::text LIMIT $9
`
