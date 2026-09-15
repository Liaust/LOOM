package objects

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/objectstore"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

type Object struct {
	ObjectID     string          `json:"object_id"`
	ObjectType   string          `json:"object_type"`
	Slug         *string         `json:"slug,omitempty"`
	Name         string          `json:"name"`
	OwnerActorID *string         `json:"owner_actor_id,omitempty"`
	HomeScopeID  *string         `json:"home_scope_id,omitempty"`
	StateClass   string          `json:"state_class"`
	Status       string          `json:"status"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	Metadata     json.RawMessage `json:"metadata"`
}

type ObjectVersion struct {
	ObjectVersionID  string          `json:"object_version_id"`
	ObjectID         string          `json:"object_id"`
	VersionNumber    int             `json:"version_number"`
	BlobID           *string         `json:"blob_id,omitempty"`
	ContentHash      *string         `json:"content_hash,omitempty"`
	SourceNodeID     *string         `json:"source_node_id,omitempty"`
	SourcePath       *string         `json:"source_path,omitempty"`
	CreatedByActorID *string         `json:"created_by_actor_id,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	SyncedAt         *time.Time      `json:"synced_at,omitempty"`
	SizeBytes        *int64          `json:"size_bytes,omitempty"`
	MimeType         *string         `json:"mime_type,omitempty"`
	Status           string          `json:"status"`
	Metadata         json.RawMessage `json:"metadata"`
}

type Blob struct {
	BlobID        string          `json:"blob_id"`
	HashAlgorithm string          `json:"hash_algorithm"`
	HashHex       string          `json:"hash_hex"`
	HashURI       string          `json:"hash_uri"`
	SizeBytes     int64           `json:"size_bytes"`
	StoragePath   string          `json:"storage_path"`
	MimeType      *string         `json:"mime_type,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	VerifiedAt    *time.Time      `json:"verified_at,omitempty"`
	Status        string          `json:"status"`
	Metadata      json.RawMessage `json:"metadata"`
}

type FileMetadata struct {
	ObjectID        string          `json:"object_id"`
	LogicalName     string          `json:"logical_name"`
	Extension       *string         `json:"extension,omitempty"`
	MimeType        *string         `json:"mime_type,omitempty"`
	SourceNodeID    *string         `json:"source_node_id,omitempty"`
	SourcePath      *string         `json:"source_path,omitempty"`
	SourceMtime     *time.Time      `json:"source_mtime,omitempty"`
	LatestVersionID *string         `json:"latest_version_id,omitempty"`
	TextExtractable bool            `json:"text_extractable"`
	RawBackupPolicy string          `json:"raw_backup_policy"`
	IndexPolicy     string          `json:"index_policy"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	Metadata        json.RawMessage `json:"metadata"`
}

type ObjectScopeLink struct {
	ObjectScopeLinkID string          `json:"object_scope_link_id"`
	ObjectID          string          `json:"object_id"`
	ScopeID           string          `json:"scope_id"`
	RelationshipType  string          `json:"relationship_type"`
	IsPrimary         bool            `json:"is_primary"`
	RelevanceStatus   string          `json:"relevance_status"`
	ValidFrom         time.Time       `json:"valid_from"`
	ValidUntil        *time.Time      `json:"valid_until,omitempty"`
	CreatedByActorID  *string         `json:"created_by_actor_id,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	Metadata          json.RawMessage `json:"metadata"`
}

type ObjectLocation struct {
	ObjectLocationID    string          `json:"object_location_id"`
	ObjectID            string          `json:"object_id"`
	VersionID           *string         `json:"version_id,omitempty"`
	NodeID              string          `json:"node_id"`
	LocationType        string          `json:"location_type"`
	PathOrURI           string          `json:"path_or_uri"`
	IsCanonicalLocation bool            `json:"is_canonical_location"`
	FreshnessState      string          `json:"freshness_state"`
	LastVerifiedAt      *time.Time      `json:"last_verified_at,omitempty"`
	CreatedAt           time.Time       `json:"created_at"`
	Metadata            json.RawMessage `json:"metadata"`
}

type ObjectDetail struct {
	Object        Object            `json:"object"`
	LatestVersion *ObjectVersion    `json:"latest_version,omitempty"`
	Blob          *Blob             `json:"blob,omitempty"`
	File          *FileMetadata     `json:"file,omitempty"`
	ScopeLinks    []ObjectScopeLink `json:"scope_links,omitempty"`
	Locations     []ObjectLocation  `json:"locations,omitempty"`
}

type IngestFileInput struct {
	Path             string          `json:"path"`
	ProjectRef       string          `json:"project_ref"`
	ScopeRef         string          `json:"scope_ref"`
	Name             string          `json:"name"`
	RelationshipType string          `json:"relationship_type"`
	ObjectType       string          `json:"object_type,omitempty"`
	StateClass       string          `json:"state_class,omitempty"`
	CreatedByJobID   string          `json:"created_by_job_id,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
}

type IngestFileResult struct {
	Object   ObjectDetail `json:"object"`
	EventIDs []string     `json:"event_ids,omitempty"`
}

type ListFilter struct {
	Limit      int
	ProjectRef string
	ScopeRef   string
	ObjectType string
}

type Service struct {
	DB    *sql.DB
	Store objectstore.Store
}

func NewService(db *sql.DB, store objectstore.Store) Service {
	return Service{DB: db, Store: store}
}

// ObjectVersionSource describes the selected retained version, never "latest".
// It is an internal execution input, not a new public object-detail view.
type ObjectVersionSource struct {
	ObjectID        string
	ObjectVersionID string
	VersionStatus   string
	HomeScopeID     *string
	BlobID          string
	Blob            objectstore.StoredBlob
}

var (
	ErrObjectVersionPin         = errors.New("source_version_pin_invalid")
	ErrObjectVersionUnavailable = errors.New("source_version_unavailable")
	ErrObjectVersionMetadata    = errors.New("source_version_metadata_invalid")
)

// ReadObjectVersionSource resolves the exact object/version/blob association in
// one statement. Retained superseded versions remain eligible; current object
// status and the caller's current project authorization still apply.
func (s Service) ReadObjectVersionSource(ctx context.Context, objectID, versionID string) (ObjectVersionSource, error) {
	if err := ctx.Err(); err != nil {
		return ObjectVersionSource{}, err
	}
	if ids.Validate(ids.ObjectPrefix, objectID) != nil || ids.Validate(ids.ObjectVersionPrefix, versionID) != nil {
		return ObjectVersionSource{}, ErrObjectVersionPin
	}
	if s.DB == nil {
		return ObjectVersionSource{}, ErrObjectVersionUnavailable
	}
	var source ObjectVersionSource
	var objectStatus, versionObject string
	var versionBlob, versionHash, blobID, algorithm, hash, uri, path, blobStatus sql.NullString
	var versionSize, blobSize sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `
		SELECT o.object_id, o.home_scope_id, o.status,
		       v.object_version_id, v.object_id, v.status, v.blob_id, v.content_hash, v.size_bytes,
		       b.blob_id, b.hash_algorithm, b.hash_hex, b.hash_uri, b.size_bytes, b.storage_path, b.status
		FROM objects.objects o
		JOIN objects.object_versions v ON v.object_id = o.object_id AND v.object_version_id = $2
		LEFT JOIN files.blobs b ON b.blob_id = v.blob_id
		WHERE o.object_id = $1
	`, objectID, versionID).Scan(&source.ObjectID, &source.HomeScopeID, &objectStatus,
		&source.ObjectVersionID, &versionObject, &source.VersionStatus, &versionBlob, &versionHash, &versionSize,
		&blobID, &algorithm, &hash, &uri, &blobSize, &path, &blobStatus)
	if err != nil {
		if ctx.Err() != nil {
			return ObjectVersionSource{}, ctx.Err()
		}
		return ObjectVersionSource{}, ErrObjectVersionUnavailable
	}
	if source.ObjectID != objectID || source.ObjectVersionID != versionID || versionObject != objectID || objectStatus != "active" || (source.VersionStatus != "active" && source.VersionStatus != "superseded") || !blobStatus.Valid || blobStatus.String != "verified" {
		return ObjectVersionSource{}, ErrObjectVersionUnavailable
	}
	if !versionBlob.Valid || versionBlob.String == "" || !blobID.Valid || blobID.String != versionBlob.String || !versionHash.Valid || !versionSize.Valid || !blobSize.Valid || versionSize.Int64 < 0 || versionSize.Int64 != blobSize.Int64 || !algorithm.Valid || algorithm.String != "sha256" || !hash.Valid || len(hash.String) != 64 || strings.Trim(hash.String, "0123456789abcdef") != "" || !uri.Valid || uri.String != "sha256:"+hash.String || versionHash.String != uri.String || !path.Valid || path.String == "" {
		return ObjectVersionSource{}, ErrObjectVersionMetadata
	}
	source.BlobID = blobID.String
	source.Blob = objectstore.StoredBlob{HashAlgorithm: algorithm.String, HashHex: hash.String, HashURI: uri.String, SizeBytes: blobSize.Int64, StoragePath: path.String}
	return source, nil
}

func (s Service) ListObjects(ctx context.Context, filter ListFilter) ([]Object, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}

	query := objectSelectSQL() + ` WHERE o.status = 'active'`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if strings.TrimSpace(filter.ObjectType) != "" {
		add("o.object_type =", strings.TrimSpace(filter.ObjectType))
	}
	if strings.TrimSpace(filter.ProjectRef) != "" || strings.TrimSpace(filter.ScopeRef) != "" {
		scopeID, err := s.resolveTargetScope(ctx, filter.ProjectRef, filter.ScopeRef)
		if err != nil {
			return nil, err
		}
		add("EXISTS (SELECT 1 FROM objects.object_scope_links osl WHERE osl.object_id = o.object_id AND osl.scope_id =", scopeID)
		query += ")"
	}

	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY o.updated_at DESC, o.name LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var objects []Object
	for rows.Next() {
		object, err := scanObject(rows)
		if err != nil {
			return nil, err
		}
		objects = append(objects, object)
	}
	return objects, rows.Err()
}

func (s Service) GetObject(ctx context.Context, ref string) (ObjectDetail, error) {
	object, err := s.resolveObjectRef(ctx, ref)
	if err != nil {
		return ObjectDetail{}, err
	}

	detail := ObjectDetail{Object: object}
	if version, err := s.latestVersion(ctx, object.ObjectID); err == nil {
		detail.LatestVersion = &version
		if version.BlobID != nil {
			blob, err := s.getBlob(ctx, *version.BlobID)
			if err != nil {
				return ObjectDetail{}, err
			}
			detail.Blob = &blob
		}
	} else if err != sql.ErrNoRows {
		return ObjectDetail{}, err
	}
	if file, err := s.getFileMetadata(ctx, object.ObjectID); err == nil {
		detail.File = &file
	} else if err != sql.ErrNoRows {
		return ObjectDetail{}, err
	}
	scopeLinks, err := s.listScopeLinks(ctx, object.ObjectID)
	if err != nil {
		return ObjectDetail{}, err
	}
	detail.ScopeLinks = scopeLinks
	locations, err := s.listLocations(ctx, object.ObjectID)
	if err != nil {
		return ObjectDetail{}, err
	}
	detail.Locations = locations
	return detail, nil
}

func (s Service) ListObjectVersions(ctx context.Context, objectRef string) ([]ObjectVersion, error) {
	object, err := s.resolveObjectRef(ctx, objectRef)
	if err != nil {
		return nil, err
	}

	rows, err := s.DB.QueryContext(ctx, objectVersionSelectSQL()+`
		WHERE object_id = $1
		ORDER BY version_number DESC
	`, object.ObjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var versions []ObjectVersion
	for rows.Next() {
		version, err := scanObjectVersion(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

func (s Service) IngestFile(ctx context.Context, req requestctx.Context, input IngestFileInput) (IngestFileResult, error) {
	return s.ingestFile(ctx, req, input, nil)
}

// Package snapshots alone supply typed initial-version metadata. Public ingestion
// keeps its existing metadata defaults.
func (s Service) ingestFile(ctx context.Context, req requestctx.Context, input IngestFileInput, versionMetadata json.RawMessage) (IngestFileResult, error) {
	sourcePath, err := normalizeSourcePath(input.Path)
	if err != nil {
		return IngestFileResult{}, err
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return IngestFileResult{}, fmt.Errorf("stat source file: %w", err)
	}
	if sourceInfo.IsDir() {
		return IngestFileResult{}, fmt.Errorf("source path is a directory")
	}

	scopeID, err := s.resolveTargetScope(ctx, input.ProjectRef, input.ScopeRef)
	if err != nil {
		return IngestFileResult{}, err
	}

	logicalName := strings.TrimSpace(input.Name)
	if logicalName == "" {
		logicalName = filepath.Base(sourcePath)
	}
	relationshipType := strings.TrimSpace(input.RelationshipType)
	if relationshipType == "" {
		relationshipType = "primary"
	}
	if !validRelationship(relationshipType) {
		return IngestFileResult{}, fmt.Errorf("unsupported object relationship type: %s", relationshipType)
	}
	objectType := strings.TrimSpace(input.ObjectType)
	if objectType == "" {
		objectType = "file"
	}
	if !validObjectType(objectType) {
		return IngestFileResult{}, fmt.Errorf("unsupported object type: %s", objectType)
	}
	stateClass := strings.TrimSpace(input.StateClass)
	if stateClass == "" {
		stateClass = "canonical"
	}
	if !validStateClass(stateClass) {
		return IngestFileResult{}, fmt.Errorf("unsupported object state class: %s", stateClass)
	}
	createdByJobID := strings.TrimSpace(input.CreatedByJobID)

	metadata, err := normalizeJSON(input.Metadata)
	if err != nil {
		return IngestFileResult{}, fmt.Errorf("object metadata must be a JSON object: %w", err)
	}
	mimeType := detectMIME(sourcePath)
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(sourcePath)), ".")
	textExtractable := isTextExtractable(mimeType, extension)

	stored, err := s.Store.PutFile(ctx, sourcePath)
	if err != nil {
		return IngestFileResult{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return IngestFileResult{}, err
	}
	defer tx.Rollback()

	blob, err := upsertBlobTx(ctx, tx, stored, mimeType)
	if err != nil {
		return IngestFileResult{}, err
	}

	objectID := ids.NewObjectID()
	versionID := ids.NewObjectVersionID()
	locationID := ids.NewObjectLocationID()
	scopeLinkID := ids.NewObjectScopeLinkID()

	object, err := insertObjectTx(ctx, tx, objectID, objectType, logicalName, req.ActorID, scopeID, stateClass, metadata)
	if err != nil {
		return IngestFileResult{}, err
	}
	version, err := insertVersionTx(ctx, tx, versionID, objectID, blob.BlobID, stored.HashURI, req.OriginNodeID, sourcePath, req.ActorID, createdByJobID, stored.SizeBytes, mimeType, versionMetadata)
	if err != nil {
		return IngestFileResult{}, err
	}
	if err := insertFileMetadataTx(ctx, tx, objectID, logicalName, extension, mimeType, req.OriginNodeID, sourcePath, sourceInfo.ModTime(), versionID, textExtractable); err != nil {
		return IngestFileResult{}, err
	}
	scopeLink, err := insertScopeLinkTx(ctx, tx, scopeLinkID, objectID, scopeID, relationshipType, req.ActorID)
	if err != nil {
		return IngestFileResult{}, err
	}
	location, err := insertObjectLocationTx(ctx, tx, locationID, objectID, versionID, req.OriginNodeID, stored.StoragePath)
	if err != nil {
		return IngestFileResult{}, err
	}

	eventIDs := []string{}
	for _, eventInput := range []events.AppendInput{
		{
			EventType:  events.TypeObjectIngested,
			EventLevel: "audit",
			Request:    req,
			ScopeID:    scopeID,
			TargetKind: "object",
			TargetID:   objectID,
			JobID:      createdByJobID,
			Status:     "created",
			Result:     "ok",
			Payload: map[string]any{
				"object_id":   objectID,
				"object_type": objectType,
				"version_id":  versionID,
				"blob_id":     blob.BlobID,
				"hash_uri":    stored.HashURI,
				"source_path": sourcePath,
			},
			VisibilityClass: "internal",
		},
		{
			EventType:  events.TypeObjectVersionCreated,
			EventLevel: "audit",
			Request:    req,
			ScopeID:    scopeID,
			TargetKind: "object",
			TargetID:   objectID,
			JobID:      createdByJobID,
			Status:     "created",
			Result:     "ok",
			Payload: map[string]any{
				"object_id":      objectID,
				"version_id":     versionID,
				"version_number": 1,
				"content_hash":   stored.HashURI,
				"size_bytes":     stored.SizeBytes,
			},
			VisibilityClass: "internal",
		},
		{
			EventType:  events.TypeObjectScopeLinked,
			EventLevel: "audit",
			Request:    req,
			ScopeID:    scopeID,
			TargetKind: "object",
			TargetID:   objectID,
			JobID:      createdByJobID,
			Status:     "created",
			Result:     "ok",
			Payload: map[string]any{
				"object_id":            objectID,
				"object_scope_link_id": scopeLinkID,
				"relationship_type":    relationshipType,
				"linked_scope_id":      scopeID,
				"is_primary":           relationshipType == "primary",
			},
			VisibilityClass: "internal",
		},
	} {
		event, err := events.AppendTx(ctx, tx, eventInput)
		if err != nil {
			return IngestFileResult{}, err
		}
		eventIDs = append(eventIDs, event.EventID)
	}

	if err := tx.Commit(); err != nil {
		return IngestFileResult{}, err
	}

	fileMetadata, err := s.getFileMetadata(ctx, objectID)
	if err != nil {
		return IngestFileResult{}, err
	}
	detail := ObjectDetail{
		Object:        object,
		LatestVersion: &version,
		Blob:          &blob,
		File:          &fileMetadata,
		ScopeLinks:    []ObjectScopeLink{scopeLink},
		Locations:     []ObjectLocation{location},
	}
	return IngestFileResult{Object: detail, EventIDs: eventIDs}, nil
}

func (s Service) IngestFileVersion(ctx context.Context, req requestctx.Context, objectRef string, input IngestFileInput) (IngestFileResult, error) {
	sourcePath, err := normalizeSourcePath(input.Path)
	if err != nil {
		return IngestFileResult{}, err
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return IngestFileResult{}, fmt.Errorf("stat source file: %w", err)
	}
	if sourceInfo.IsDir() {
		return IngestFileResult{}, fmt.Errorf("source path is a directory")
	}
	object, err := s.resolveObjectRef(ctx, objectRef)
	if err != nil {
		return IngestFileResult{}, err
	}
	scopeID, err := s.resolveTargetScope(ctx, input.ProjectRef, input.ScopeRef)
	if err != nil {
		return IngestFileResult{}, err
	}
	logicalName := strings.TrimSpace(input.Name)
	if logicalName == "" {
		logicalName = filepath.Base(sourcePath)
	}
	relationshipType := strings.TrimSpace(input.RelationshipType)
	if relationshipType == "" {
		relationshipType = "primary"
	}
	if !validRelationship(relationshipType) {
		return IngestFileResult{}, fmt.Errorf("unsupported object relationship type: %s", relationshipType)
	}
	createdByJobID := strings.TrimSpace(input.CreatedByJobID)
	metadata, err := normalizeJSON(input.Metadata)
	if err != nil {
		return IngestFileResult{}, fmt.Errorf("object metadata must be a JSON object: %w", err)
	}
	mimeType := detectMIME(sourcePath)
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(sourcePath)), ".")
	textExtractable := isTextExtractable(mimeType, extension)
	stored, err := s.Store.PutFile(ctx, sourcePath)
	if err != nil {
		return IngestFileResult{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return IngestFileResult{}, err
	}
	defer tx.Rollback()

	blob, err := upsertBlobTx(ctx, tx, stored, mimeType)
	if err != nil {
		return IngestFileResult{}, err
	}
	version, err := insertNextVersionTx(ctx, tx, ids.NewObjectVersionID(), object.ObjectID, blob.BlobID, stored.HashURI, req.OriginNodeID, sourcePath, req.ActorID, createdByJobID, stored.SizeBytes, mimeType, metadata)
	if err != nil {
		return IngestFileResult{}, err
	}
	if err := updateFileMetadataTx(ctx, tx, object.ObjectID, logicalName, extension, mimeType, req.OriginNodeID, sourcePath, sourceInfo.ModTime(), version.ObjectVersionID, textExtractable, metadata); err != nil {
		return IngestFileResult{}, err
	}
	scopeLink, err := ensureScopeLinkTx(ctx, tx, ids.NewObjectScopeLinkID(), object.ObjectID, scopeID, relationshipType, req.ActorID)
	if err != nil {
		return IngestFileResult{}, err
	}
	location, err := insertObjectLocationTx(ctx, tx, ids.NewObjectLocationID(), object.ObjectID, version.ObjectVersionID, req.OriginNodeID, stored.StoragePath)
	if err != nil {
		return IngestFileResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE objects.objects
		SET name = $2,
		    updated_at = now(),
		    metadata = metadata || $3::jsonb
		WHERE object_id = $1
	`, object.ObjectID, logicalName, metadata); err != nil {
		return IngestFileResult{}, err
	}
	event, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeObjectVersionCreated,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    scopeID,
		TargetKind: "object",
		TargetID:   object.ObjectID,
		JobID:      createdByJobID,
		Status:     "created",
		Result:     "ok",
		Payload: map[string]any{
			"object_id":      object.ObjectID,
			"version_id":     version.ObjectVersionID,
			"version_number": version.VersionNumber,
			"content_hash":   stored.HashURI,
			"size_bytes":     stored.SizeBytes,
			"source_path":    sourcePath,
		},
		VisibilityClass: "internal",
	})
	if err != nil {
		return IngestFileResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return IngestFileResult{}, err
	}
	detail, err := s.GetObject(ctx, object.ObjectID)
	if err != nil {
		return IngestFileResult{}, err
	}
	if detail.LatestVersion == nil || detail.LatestVersion.ObjectVersionID != version.ObjectVersionID {
		detail.LatestVersion = &version
		detail.Blob = &blob
	}
	if len(detail.ScopeLinks) == 0 {
		detail.ScopeLinks = []ObjectScopeLink{scopeLink}
	}
	if len(detail.Locations) == 0 {
		detail.Locations = []ObjectLocation{location}
	}
	return IngestFileResult{Object: detail, EventIDs: []string{event.EventID}}, nil
}

func (s Service) resolveTargetScope(ctx context.Context, projectRef, scopeRef string) (string, error) {
	projectRef = strings.TrimSpace(projectRef)
	scopeRef = strings.TrimSpace(scopeRef)
	if projectRef != "" && scopeRef != "" {
		return "", fmt.Errorf("provide either project_ref or scope_ref, not both")
	}
	if projectRef != "" {
		project, err := projects.NewService(s.DB).ResolveProjectRef(ctx, projectRef)
		if err != nil {
			return "", fmt.Errorf("resolve project: %w", err)
		}
		return project.ProjectScopeID, nil
	}
	if scopeRef != "" {
		return s.resolveScopeRef(ctx, scopeRef)
	}
	return "", fmt.Errorf("project_ref or scope_ref is required")
}

func (s Service) resolveScopeRef(ctx context.Context, ref string) (string, error) {
	var scopeID string
	err := s.DB.QueryRowContext(ctx, `
		SELECT scope_id
		FROM scopes.scopes
		WHERE scope_id = $1 OR scope_key = $1 OR slug = $1
		LIMIT 1
	`, strings.TrimSpace(ref)).Scan(&scopeID)
	if err != nil {
		return "", err
	}
	return scopeID, nil
}

func (s Service) resolveObjectRef(ctx context.Context, ref string) (Object, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Object{}, fmt.Errorf("object ref is required")
	}
	row := s.DB.QueryRowContext(ctx, objectSelectSQL()+`
		WHERE o.object_id = $1 OR o.slug = $1
		LIMIT 1
	`, ref)
	return scanObject(row)
}

func upsertBlobTx(ctx context.Context, tx *sql.Tx, stored objectstore.StoredBlob, mimeType string) (Blob, error) {
	row := tx.QueryRowContext(ctx, `
		INSERT INTO files.blobs (
			blob_id, hash_algorithm, hash_hex, hash_uri, size_bytes, storage_path,
			mime_type, verified_at, status, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''), now(), 'verified', '{"slice":"3"}'::jsonb)
		ON CONFLICT (hash_algorithm, hash_hex)
		DO UPDATE SET
			verified_at = now(),
			status = 'verified',
			mime_type = COALESCE(files.blobs.mime_type, EXCLUDED.mime_type)
		RETURNING blob_id, hash_algorithm, hash_hex, hash_uri, size_bytes, storage_path,
		          mime_type, created_at, verified_at, status, metadata
	`, ids.NewBlobID(), stored.HashAlgorithm, stored.HashHex, stored.HashURI, stored.SizeBytes, stored.StoragePath, mimeType)
	return scanBlob(row)
}

func insertObjectTx(ctx context.Context, tx *sql.Tx, objectID, objectType, name, actorID, scopeID, stateClass string, metadata []byte) (Object, error) {
	row := tx.QueryRowContext(ctx, `
		INSERT INTO objects.objects (
			object_id, object_type, name, owner_actor_id, home_scope_id,
			state_class, status, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, 'active', $7)
		RETURNING object_id, object_type, slug, name, owner_actor_id, home_scope_id,
		          state_class, status, created_at, updated_at, metadata
	`, objectID, objectType, name, actorID, scopeID, stateClass, metadata)
	return scanObject(row)
}

func insertVersionTx(ctx context.Context, tx *sql.Tx, versionID, objectID, blobID, contentHash, nodeID, sourcePath, actorID, createdByJobID string, sizeBytes int64, mimeType string, metadata json.RawMessage) (ObjectVersion, error) {
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	row := tx.QueryRowContext(ctx, `
		INSERT INTO objects.object_versions (
			object_version_id, object_id, version_number, blob_id, content_hash,
			source_node_id, source_path, created_by_actor_id, created_by_job_id,
			size_bytes, mime_type, status, metadata
		)
		VALUES ($1, $2, 1, $3, $4, $5, $6, $7, nullif($8, ''), $9, nullif($10, ''), 'active',
		        '{"slice":"3","initial_version":true}'::jsonb || $11::jsonb)
		RETURNING object_version_id, object_id, version_number, blob_id, content_hash,
		          source_node_id, source_path, created_by_actor_id, created_at, synced_at,
		          size_bytes, mime_type, status, metadata
	`, versionID, objectID, blobID, contentHash, nodeID, sourcePath, actorID, createdByJobID, sizeBytes, mimeType, metadata)
	return scanObjectVersion(row)
}

func insertNextVersionTx(ctx context.Context, tx *sql.Tx, versionID, objectID, blobID, contentHash, nodeID, sourcePath, actorID, createdByJobID string, sizeBytes int64, mimeType string, metadata []byte) (ObjectVersion, error) {
	if _, err := tx.ExecContext(ctx, `
		UPDATE objects.object_versions
		SET status = 'superseded'
		WHERE object_id = $1 AND status = 'active'
	`, objectID); err != nil {
		return ObjectVersion{}, err
	}
	row := tx.QueryRowContext(ctx, `
		INSERT INTO objects.object_versions (
			object_version_id, object_id, version_number, blob_id, content_hash,
			source_node_id, source_path, created_by_actor_id, created_by_job_id,
			size_bytes, mime_type, status, metadata
		)
		SELECT $1, $2, COALESCE(MAX(version_number), 0) + 1, $3, $4,
		       nullif($5, ''), $6, nullif($7, ''), nullif($8, ''),
		       $9, nullif($10, ''), 'active',
		       '{"slice":"10_part_2","appended_version":true}'::jsonb || $11::jsonb
		FROM objects.object_versions
		WHERE object_id = $2
		RETURNING object_version_id, object_id, version_number, blob_id, content_hash,
		          source_node_id, source_path, created_by_actor_id, created_at, synced_at,
		          size_bytes, mime_type, status, metadata
	`, versionID, objectID, blobID, contentHash, nodeID, sourcePath, actorID, createdByJobID, sizeBytes, mimeType, metadata)
	return scanObjectVersion(row)
}

func insertFileMetadataTx(ctx context.Context, tx *sql.Tx, objectID, logicalName, extension, mimeType, nodeID, sourcePath string, sourceMtime time.Time, latestVersionID string, textExtractable bool) error {
	indexPolicy := "metadata_only"
	if textExtractable {
		indexPolicy = "text_later"
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO files.file_metadata (
			object_id, logical_name, extension, mime_type, source_node_id, source_path,
			source_mtime, latest_version_id, text_extractable, raw_backup_policy,
			index_policy, metadata
		)
		VALUES ($1, $2, nullif($3, ''), nullif($4, ''), $5, $6, $7, $8, $9,
		        'normal', $10, '{"slice":"3","slice_4_index_policy":true}'::jsonb)
	`, objectID, logicalName, extension, mimeType, nodeID, sourcePath, sourceMtime, latestVersionID, textExtractable, indexPolicy)
	return err
}

func updateFileMetadataTx(ctx context.Context, tx *sql.Tx, objectID, logicalName, extension, mimeType, nodeID, sourcePath string, sourceMtime time.Time, latestVersionID string, textExtractable bool, metadata []byte) error {
	indexPolicy := "metadata_only"
	if textExtractable {
		indexPolicy = "text_later"
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE files.file_metadata
		SET logical_name = $2,
		    extension = nullif($3, ''),
		    mime_type = nullif($4, ''),
		    source_node_id = nullif($5, ''),
		    source_path = $6,
		    source_mtime = $7,
		    latest_version_id = $8,
		    text_extractable = $9,
		    index_policy = $10,
		    metadata = metadata || $11::jsonb,
		    updated_at = now()
		WHERE object_id = $1
	`, objectID, logicalName, extension, mimeType, nodeID, sourcePath, sourceMtime, latestVersionID, textExtractable, indexPolicy, metadata)
	return err
}

func insertScopeLinkTx(ctx context.Context, tx *sql.Tx, linkID, objectID, scopeID, relationshipType, actorID string) (ObjectScopeLink, error) {
	row := tx.QueryRowContext(ctx, `
		INSERT INTO objects.object_scope_links (
			object_scope_link_id, object_id, scope_id, relationship_type,
			is_primary, relevance_status, created_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, 'active', $6, '{"slice":"3"}'::jsonb)
		RETURNING object_scope_link_id, object_id, scope_id, relationship_type,
		          is_primary, relevance_status, valid_from, valid_until,
		          created_by_actor_id, created_at, metadata
	`, linkID, objectID, scopeID, relationshipType, relationshipType == "primary", actorID)
	return scanObjectScopeLink(row)
}

func ensureScopeLinkTx(ctx context.Context, tx *sql.Tx, linkID, objectID, scopeID, relationshipType, actorID string) (ObjectScopeLink, error) {
	row := tx.QueryRowContext(ctx, `
		INSERT INTO objects.object_scope_links (
			object_scope_link_id, object_id, scope_id, relationship_type,
			is_primary, relevance_status, created_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, 'active', nullif($6, ''), '{"slice":"10_part_2"}'::jsonb)
		ON CONFLICT (object_id, scope_id, relationship_type)
		DO UPDATE SET
			relevance_status = 'active',
			valid_until = NULL,
			metadata = objects.object_scope_links.metadata || '{"slice_10_part_2_touched":true}'::jsonb
		RETURNING object_scope_link_id, object_id, scope_id, relationship_type,
		          is_primary, relevance_status, valid_from, valid_until,
		          created_by_actor_id, created_at, metadata
	`, linkID, objectID, scopeID, relationshipType, relationshipType == "primary", actorID)
	return scanObjectScopeLink(row)
}

func insertObjectLocationTx(ctx context.Context, tx *sql.Tx, locationID, objectID, versionID, nodeID, path string) (ObjectLocation, error) {
	row := tx.QueryRowContext(ctx, `
		INSERT INTO files.object_locations (
			object_location_id, object_id, version_id, node_id, location_type,
			path_or_uri, is_canonical_location, freshness_state, last_verified_at,
			metadata
		)
		VALUES ($1, $2, $3, $4, 'object_store', $5, true, 'fresh', now(), '{"slice":"3"}'::jsonb)
		RETURNING object_location_id, object_id, version_id, node_id, location_type,
		          path_or_uri, is_canonical_location, freshness_state, last_verified_at,
		          created_at, metadata
	`, locationID, objectID, versionID, nodeID, path)
	return scanObjectLocation(row)
}

func (s Service) latestVersion(ctx context.Context, objectID string) (ObjectVersion, error) {
	row := s.DB.QueryRowContext(ctx, objectVersionSelectSQL()+`
		WHERE object_id = $1
		ORDER BY version_number DESC
		LIMIT 1
	`, objectID)
	return scanObjectVersion(row)
}

func (s Service) getBlob(ctx context.Context, ref string) (Blob, error) {
	row := s.DB.QueryRowContext(ctx, blobSelectSQL()+`
		WHERE blob_id = $1 OR hash_uri = $1
		LIMIT 1
	`, ref)
	return scanBlob(row)
}

func (s Service) getFileMetadata(ctx context.Context, objectID string) (FileMetadata, error) {
	row := s.DB.QueryRowContext(ctx, fileMetadataSelectSQL()+`
		WHERE object_id = $1
	`, objectID)
	return scanFileMetadata(row)
}

func (s Service) listScopeLinks(ctx context.Context, objectID string) ([]ObjectScopeLink, error) {
	rows, err := s.DB.QueryContext(ctx, objectScopeLinkSelectSQL()+`
		WHERE object_id = $1
		ORDER BY is_primary DESC, created_at ASC
	`, objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []ObjectScopeLink
	for rows.Next() {
		link, err := scanObjectScopeLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

func (s Service) listLocations(ctx context.Context, objectID string) ([]ObjectLocation, error) {
	rows, err := s.DB.QueryContext(ctx, objectLocationSelectSQL()+`
		WHERE object_id = $1
		ORDER BY is_canonical_location DESC, created_at ASC
	`, objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var locations []ObjectLocation
	for rows.Next() {
		location, err := scanObjectLocation(rows)
		if err != nil {
			return nil, err
		}
		locations = append(locations, location)
	}
	return locations, rows.Err()
}

func objectSelectSQL() string {
	return `
		SELECT o.object_id, o.object_type, o.slug, o.name, o.owner_actor_id,
		       o.home_scope_id, o.state_class, o.status, o.created_at, o.updated_at,
		       o.metadata
		FROM objects.objects o
	`
}

func objectVersionSelectSQL() string {
	return `
		SELECT object_version_id, object_id, version_number, blob_id, content_hash,
		       source_node_id, source_path, created_by_actor_id, created_at, synced_at,
		       size_bytes, mime_type, status, metadata
		FROM objects.object_versions
	`
}

func blobSelectSQL() string {
	return `
		SELECT blob_id, hash_algorithm, hash_hex, hash_uri, size_bytes, storage_path,
		       mime_type, created_at, verified_at, status, metadata
		FROM files.blobs
	`
}

func fileMetadataSelectSQL() string {
	return `
		SELECT object_id, logical_name, extension, mime_type, source_node_id, source_path,
		       source_mtime, latest_version_id, text_extractable, raw_backup_policy,
		       index_policy, created_at, updated_at, metadata
		FROM files.file_metadata
	`
}

func objectScopeLinkSelectSQL() string {
	return `
		SELECT object_scope_link_id, object_id, scope_id, relationship_type,
		       is_primary, relevance_status, valid_from, valid_until,
		       created_by_actor_id, created_at, metadata
		FROM objects.object_scope_links
	`
}

func objectLocationSelectSQL() string {
	return `
		SELECT object_location_id, object_id, version_id, node_id, location_type,
		       path_or_uri, is_canonical_location, freshness_state, last_verified_at,
		       created_at, metadata
		FROM files.object_locations
	`
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanObject(scanner rowScanner) (Object, error) {
	var object Object
	var slug sql.NullString
	var ownerActorID sql.NullString
	var homeScopeID sql.NullString
	var metadata []byte
	if err := scanner.Scan(
		&object.ObjectID,
		&object.ObjectType,
		&slug,
		&object.Name,
		&ownerActorID,
		&homeScopeID,
		&object.StateClass,
		&object.Status,
		&object.CreatedAt,
		&object.UpdatedAt,
		&metadata,
	); err != nil {
		return Object{}, err
	}
	object.Slug = stringPtr(slug)
	object.OwnerActorID = stringPtr(ownerActorID)
	object.HomeScopeID = stringPtr(homeScopeID)
	object.Metadata = jsonOrEmpty(metadata)
	return object, nil
}

func scanObjectVersion(scanner rowScanner) (ObjectVersion, error) {
	var version ObjectVersion
	var blobID sql.NullString
	var contentHash sql.NullString
	var sourceNodeID sql.NullString
	var sourcePath sql.NullString
	var createdByActorID sql.NullString
	var syncedAt sql.NullTime
	var sizeBytes sql.NullInt64
	var mimeType sql.NullString
	var metadata []byte
	if err := scanner.Scan(
		&version.ObjectVersionID,
		&version.ObjectID,
		&version.VersionNumber,
		&blobID,
		&contentHash,
		&sourceNodeID,
		&sourcePath,
		&createdByActorID,
		&version.CreatedAt,
		&syncedAt,
		&sizeBytes,
		&mimeType,
		&version.Status,
		&metadata,
	); err != nil {
		return ObjectVersion{}, err
	}
	version.BlobID = stringPtr(blobID)
	version.ContentHash = stringPtr(contentHash)
	version.SourceNodeID = stringPtr(sourceNodeID)
	version.SourcePath = stringPtr(sourcePath)
	version.CreatedByActorID = stringPtr(createdByActorID)
	version.SyncedAt = timePtr(syncedAt)
	if sizeBytes.Valid {
		value := sizeBytes.Int64
		version.SizeBytes = &value
	}
	version.MimeType = stringPtr(mimeType)
	version.Metadata = jsonOrEmpty(metadata)
	return version, nil
}

func scanBlob(scanner rowScanner) (Blob, error) {
	var blob Blob
	var mimeType sql.NullString
	var verifiedAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&blob.BlobID,
		&blob.HashAlgorithm,
		&blob.HashHex,
		&blob.HashURI,
		&blob.SizeBytes,
		&blob.StoragePath,
		&mimeType,
		&blob.CreatedAt,
		&verifiedAt,
		&blob.Status,
		&metadata,
	); err != nil {
		return Blob{}, err
	}
	blob.MimeType = stringPtr(mimeType)
	blob.VerifiedAt = timePtr(verifiedAt)
	blob.Metadata = jsonOrEmpty(metadata)
	return blob, nil
}

func scanFileMetadata(scanner rowScanner) (FileMetadata, error) {
	var file FileMetadata
	var extension sql.NullString
	var mimeType sql.NullString
	var sourceNodeID sql.NullString
	var sourcePath sql.NullString
	var sourceMtime sql.NullTime
	var latestVersionID sql.NullString
	var metadata []byte
	if err := scanner.Scan(
		&file.ObjectID,
		&file.LogicalName,
		&extension,
		&mimeType,
		&sourceNodeID,
		&sourcePath,
		&sourceMtime,
		&latestVersionID,
		&file.TextExtractable,
		&file.RawBackupPolicy,
		&file.IndexPolicy,
		&file.CreatedAt,
		&file.UpdatedAt,
		&metadata,
	); err != nil {
		return FileMetadata{}, err
	}
	file.Extension = stringPtr(extension)
	file.MimeType = stringPtr(mimeType)
	file.SourceNodeID = stringPtr(sourceNodeID)
	file.SourcePath = stringPtr(sourcePath)
	file.SourceMtime = timePtr(sourceMtime)
	file.LatestVersionID = stringPtr(latestVersionID)
	file.Metadata = jsonOrEmpty(metadata)
	return file, nil
}

func scanObjectScopeLink(scanner rowScanner) (ObjectScopeLink, error) {
	var link ObjectScopeLink
	var validUntil sql.NullTime
	var createdByActorID sql.NullString
	var metadata []byte
	if err := scanner.Scan(
		&link.ObjectScopeLinkID,
		&link.ObjectID,
		&link.ScopeID,
		&link.RelationshipType,
		&link.IsPrimary,
		&link.RelevanceStatus,
		&link.ValidFrom,
		&validUntil,
		&createdByActorID,
		&link.CreatedAt,
		&metadata,
	); err != nil {
		return ObjectScopeLink{}, err
	}
	link.ValidUntil = timePtr(validUntil)
	link.CreatedByActorID = stringPtr(createdByActorID)
	link.Metadata = jsonOrEmpty(metadata)
	return link, nil
}

func scanObjectLocation(scanner rowScanner) (ObjectLocation, error) {
	var location ObjectLocation
	var versionID sql.NullString
	var lastVerifiedAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&location.ObjectLocationID,
		&location.ObjectID,
		&versionID,
		&location.NodeID,
		&location.LocationType,
		&location.PathOrURI,
		&location.IsCanonicalLocation,
		&location.FreshnessState,
		&lastVerifiedAt,
		&location.CreatedAt,
		&metadata,
	); err != nil {
		return ObjectLocation{}, err
	}
	location.VersionID = stringPtr(versionID)
	location.LastVerifiedAt = timePtr(lastVerifiedAt)
	location.Metadata = jsonOrEmpty(metadata)
	return location, nil
}

func normalizeSourcePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	return abs, nil
}

func detectMIME(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	}

	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && n == 0 {
		return ""
	}
	return http.DetectContentType(buffer[:n])
}

func isTextExtractable(mimeType, extension string) bool {
	if strings.HasPrefix(mimeType, "text/") {
		return true
	}
	switch extension {
	case "md", "markdown", "txt", "json", "csv", "yaml", "yml", "toml", "go", "sql":
		return true
	default:
		return false
	}
}

func validObjectType(value string) bool {
	switch value {
	case "file", "artifact", "project", "note", "report", "decision", "dataset", "script", "workflow", "skill", "repo", "module", "package", "external_resource", "hardware_device", "structured_record":
		return true
	default:
		return false
	}
}

func validStateClass(value string) bool {
	switch value {
	case "canonical", "replica", "derived", "ephemeral", "external":
		return true
	default:
		return false
	}
}

func validRelationship(value string) bool {
	switch value {
	case "primary", "relevant", "visible_in", "owned_by", "source_for", "artifact_for", "temporary", "historical":
		return true
	default:
		return false
	}
}

func normalizeJSON(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return []byte(`{}`), nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("expected object")
	}
	return raw, nil
}

func jsonOrEmpty(value []byte) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(value)
}

func stringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func timePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}
