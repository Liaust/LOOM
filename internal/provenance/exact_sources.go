package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	LinkedSourcesSchemaVersion   = "loom.provenance.linked_sources.v1"
	MaximumLinkedSourcesBytes    = 256 * 1024
	MaximumLinkedSourceItemBytes = 16 * 1024
)

var ErrLinkedSourcesRead = errors.New("linked source receipts could not be read")

// LinkedSourcesTransport is optional: older foundation backends keep their
// existing exact-get methods and cannot silently satisfy an expanded request.
type LinkedSourcesTransport interface {
	GetCandidateWithSources(context.Context, SemanticID, int) (CandidateWithSources, error)
	GetRecordWithSources(context.Context, SemanticID, int) (RecordWithSources, error)
}

type CandidateWithSources struct {
	CandidateLifecycleProjection
	Sources LinkedSources `json:"sources"`
}

type RecordWithSources struct {
	RecordLifecycleProjection
	Sources LinkedSources `json:"sources"`
}

// StoredSourceReceipt is an allowlist, not sanitization. Locator identities
// remain verbatim historical values; this projection never accesses a source.
type StoredSourceReceipt struct {
	SchemaVersion       string    `json:"schema_version"`
	SourceKind          string    `json:"source_kind"`
	Status              string    `json:"status"`
	VerificationPosture string    `json:"verification_posture"`
	ResolverName        string    `json:"resolver_name"`
	ResolverVersion     string    `json:"resolver_version"`
	ResolutionAt        time.Time `json:"resolution_at"`
	CanonicalLocator    *string   `json:"canonical_locator,omitempty"`
	VersionAddress      *string   `json:"version_address,omitempty"`
	ContentDigest       *string   `json:"content_digest,omitempty"`
	GapReason           *string   `json:"gap_reason,omitempty"`
}

// An item is either a full receipt or a source ID with an unavailable reason.
type LinkedSourceItem struct {
	SourceReferenceID SemanticID `json:"source_reference_id"`
	*StoredSourceReceipt
	ReceiptUnavailable string `json:"receipt_unavailable,omitempty"`
}

type LinkedSources struct {
	SchemaVersion    string             `json:"schema_version"`
	ParentKind       string             `json:"parent_kind"`
	ParentID         SemanticID         `json:"parent_id"`
	Posture          string             `json:"posture"`
	Items            []LinkedSourceItem `json:"items"`
	Returned         int                `json:"returned"`
	SourcesTruncated bool               `json:"sources_truncated"`
	IncompleteItems  int                `json:"incomplete_items"`
}

type linkedSourcesReadError struct{ cause error }

func (err *linkedSourcesReadError) Error() string        { return ErrLinkedSourcesRead.Error() }
func (err *linkedSourcesReadError) Unwrap() error        { return err.cause }
func (err *linkedSourcesReadError) Is(target error) bool { return target == ErrLinkedSourcesRead }

func (api *FoundationAPI) readExactSources(ctx context.Context, kind string, id SemanticID, limit int) (any, LinkedSources, error) {
	if err := id.Validate(); err != nil {
		return nil, LinkedSources{}, &FoundationValidationError{Err: err}
	}
	limit, err := boundedLimit(limit)
	if err != nil {
		return nil, LinkedSources{}, &FoundationValidationError{Err: err}
	}
	if api == nil || api.store == nil || api.store.pool == nil || api.service == nil {
		return nil, LinkedSources{}, &linkedSourcesReadError{}
	}
	tx, err := api.store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, LinkedSources{}, &linkedSourcesReadError{cause: err}
	}
	defer func() { _ = tx.Rollback(ctx) }()
	service := *api.service
	service.store = &Store{q: adaptTx(tx)}
	var parent any
	var found bool
	switch kind {
	case "candidate":
		parent, found, err = service.GetCandidateLifecycle(ctx, id, limit)
	case "record":
		parent, found, err = service.GetRecordLifecycle(ctx, id, limit)
	default:
		return nil, LinkedSources{}, &FoundationValidationError{Err: errors.New("unsupported exact source parent")}
	}
	if err != nil {
		return nil, LinkedSources{}, &linkedSourcesReadError{cause: err}
	}
	if !found {
		return nil, LinkedSources{}, &FoundationNotFoundError{Kind: kind, ID: id}
	}
	sources, err := service.store.readLinkedSourceReceipts(ctx, kind, id, limit)
	if err != nil {
		return nil, LinkedSources{}, &linkedSourcesReadError{cause: err}
	}
	expansion, err := projectLinkedSources(kind, id, sources, limit)
	if err != nil {
		return nil, LinkedSources{}, &linkedSourcesReadError{cause: err}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, LinkedSources{}, &linkedSourcesReadError{cause: err}
	}
	return parent, expansion, nil
}

func (store *Store) readLinkedSourceReceipts(ctx context.Context, kind string, id SemanticID, limit int) ([]SourceReference, error) {
	links := `SELECT source_reference_id FROM provenance.record_sources WHERE record_id=$1`
	if kind == "candidate" {
		links = `SELECT id AS source_reference_id FROM provenance.source_references WHERE candidate_id=$1
		UNION SELECT source_reference_id FROM provenance.candidate_evidence_links WHERE candidate_id=$1`
	}
	// The left join detects a corrupt dangling link instead of claiming an empty
	// complete set. Select only allowlisted columns; no source bodies are read.
	rows, err := store.q.query(ctx, `WITH links AS (`+links+`), selected AS (
	 SELECT DISTINCT source_reference_id FROM links ORDER BY source_reference_id LIMIT $2)
	 SELECT selected.source_reference_id::text,s.id::text,
	 COALESCE(s.schema_version,''),COALESCE(s.source_kind,''),COALESCE(s.status,''),
	 COALESCE(s.verification_posture,''),COALESCE(s.resolver_name,''),COALESCE(s.resolver_version,''),
	 s.resolution_at,s.canonical_locator,s.version_address,s.content_digest,s.gap_reason
	 FROM selected LEFT JOIN provenance.source_references s ON s.id=selected.source_reference_id
	 ORDER BY selected.source_reference_id`, string(id), limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SourceReference{}
	for rows.Next() {
		var item SourceReference
		var actual *string
		var resolved *time.Time
		if err := rows.Scan(&item.ID, &actual, &item.SchemaVersion, &item.SourceKind, &item.Status,
			&item.VerificationPosture, &item.ResolverName, &item.ResolverVersion, &resolved,
			&item.CanonicalLocator, &item.VersionAddress, &item.ContentDigest, &item.GapReason); err != nil {
			return nil, err
		}
		if actual == nil || *actual != string(item.ID) || resolved == nil {
			return nil, ErrLinkedSourcesRead
		}
		item.ResolutionAt = resolved.UTC()
		result = append(result, item)
	}
	return result, rows.Err()
}

func projectLinkedSources(kind string, id SemanticID, sources []SourceReference, limit int) (LinkedSources, error) {
	out := LinkedSources{SchemaVersion: LinkedSourcesSchemaVersion, ParentKind: kind, ParentID: id,
		Posture: "stored_resolution", Items: []LinkedSourceItem{}}
	for index, source := range sources {
		if index == limit {
			out.SourcesTruncated = true
			break
		}
		if err := source.ID.Validate(); err != nil {
			return LinkedSources{}, err
		}
		receipt := &StoredSourceReceipt{SchemaVersion: source.SchemaVersion, SourceKind: source.SourceKind,
			Status: source.Status, VerificationPosture: source.VerificationPosture, ResolverName: source.ResolverName,
			ResolverVersion: source.ResolverVersion, ResolutionAt: source.ResolutionAt,
			CanonicalLocator: source.CanonicalLocator, VersionAddress: source.VersionAddress, ContentDigest: source.ContentDigest, GapReason: source.GapReason}
		item := LinkedSourceItem{SourceReferenceID: source.ID, StoredSourceReceipt: receipt}
		values := []string{receipt.SchemaVersion, receipt.SourceKind, receipt.Status, receipt.VerificationPosture, receipt.ResolverName, receipt.ResolverVersion}
		for _, value := range []*string{receipt.CanonicalLocator, receipt.VersionAddress, receipt.ContentDigest, receipt.GapReason} {
			if value != nil {
				values = append(values, *value)
			}
		}
		for _, value := range values {
			if !utf8.ValidString(value) {
				item.StoredSourceReceipt, item.ReceiptUnavailable = nil, "invalid_encoding"
				break
			}
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return LinkedSources{}, err
		}
		if len(encoded) > MaximumLinkedSourceItemBytes {
			item.StoredSourceReceipt, item.ReceiptUnavailable = nil, "item_exceeds_bound"
		}
		out.Items = append(out.Items, item)
		out.Returned++
		if item.ReceiptUnavailable != "" {
			out.IncompleteItems++
		}
		// Marshal all truth fields before accepting a whole item. false is one
		// byte longer than true, so this also reserves final truncation metadata.
		encoded, err = json.Marshal(out)
		if err != nil {
			return LinkedSources{}, err
		}
		if len(encoded) > MaximumLinkedSourcesBytes {
			out.Items = out.Items[:len(out.Items)-1]
			out.Returned--
			if item.ReceiptUnavailable != "" {
				out.IncompleteItems--
			}
			out.SourcesTruncated = true
			break
		}
	}
	return out, nil
}
