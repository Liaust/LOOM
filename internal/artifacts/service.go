package artifacts

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/search"
)

type Artifact struct {
	ArtifactID      string          `json:"artifact_id"`
	ObjectID        string          `json:"object_id"`
	ObjectVersionID *string         `json:"object_version_id,omitempty"`
	BlobID          *string         `json:"blob_id,omitempty"`
	JobID           string          `json:"job_id"`
	JobAttemptID    *string         `json:"job_attempt_id,omitempty"`
	ScriptID        *string         `json:"script_id,omitempty"`
	ScriptVersionID *string         `json:"script_version_id,omitempty"`
	ScopeID         *string         `json:"scope_id,omitempty"`
	SourceObjectID  *string         `json:"source_object_id,omitempty"`
	ArtifactType    string          `json:"artifact_type"`
	Title           string          `json:"title"`
	HashURI         *string         `json:"hash_uri,omitempty"`
	MimeType        *string         `json:"mime_type,omitempty"`
	SizeBytes       *int64          `json:"size_bytes,omitempty"`
	Status          string          `json:"status"`
	CreatedAt       time.Time       `json:"created_at"`
	Metadata        json.RawMessage `json:"metadata"`
}

type ArtifactDetail struct {
	Artifact Artifact             `json:"artifact"`
	Object   objects.ObjectDetail `json:"object"`
	Index    *search.IndexResult  `json:"index,omitempty"`
	EventIDs []string             `json:"event_ids,omitempty"`
}

type CreateFromJobFileInput struct {
	JobID           string          `json:"job_id"`
	JobAttemptID    string          `json:"job_attempt_id,omitempty"`
	ScriptID        string          `json:"script_id,omitempty"`
	ScriptVersionID string          `json:"script_version_id,omitempty"`
	ScopeID         string          `json:"scope_id,omitempty"`
	SourceObjectID  string          `json:"source_object_id,omitempty"`
	ArtifactDir     string          `json:"artifact_dir"`
	Path            string          `json:"path"`
	ArtifactType    string          `json:"artifact_type"`
	Title           string          `json:"title,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

type ListFilter struct {
	Limit     int
	JobRef    string
	ScopeRef  string
	ObjectRef string
}

type Service struct {
	DB      *sql.DB
	Objects objects.Service
	Search  search.Service
}

func NewService(db *sql.DB, objects objects.Service, search search.Service) Service {
	return Service{DB: db, Objects: objects, Search: search}
}

func (s Service) CreateArtifactFromJobFile(ctx context.Context, req requestctx.Context, input CreateFromJobFileInput) (ArtifactDetail, error) {
	jobID := strings.TrimSpace(input.JobID)
	if jobID == "" {
		return ArtifactDetail{}, fmt.Errorf("job_id is required")
	}
	artifactDir := strings.TrimSpace(input.ArtifactDir)
	if artifactDir == "" {
		return ArtifactDetail{}, fmt.Errorf("artifact_dir is required")
	}
	artifactPath, err := resolveArtifactPath(artifactDir, input.Path)
	if err != nil {
		return ArtifactDetail{}, err
	}
	info, err := os.Stat(artifactPath)
	if err != nil {
		return ArtifactDetail{}, fmt.Errorf("stat artifact file: %w", err)
	}
	if info.IsDir() {
		return ArtifactDetail{}, fmt.Errorf("artifact path is a directory")
	}

	artifactType := strings.TrimSpace(input.ArtifactType)
	if artifactType == "" {
		artifactType = "file"
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = filepath.Base(artifactPath)
	}

	metadata, err := normalizeJSONObject(input.Metadata)
	if err != nil {
		return ArtifactDetail{}, fmt.Errorf("artifact metadata must be a JSON object: %w", err)
	}

	ingest, err := s.Objects.IngestFile(ctx, req, objects.IngestFileInput{
		Path:             artifactPath,
		ScopeRef:         strings.TrimSpace(input.ScopeID),
		Name:             title,
		RelationshipType: "artifact_for",
		ObjectType:       "artifact",
		StateClass:       "derived",
		CreatedByJobID:   jobID,
		Metadata:         metadata,
	})
	if err != nil {
		return ArtifactDetail{}, err
	}

	latest := ingest.Object.LatestVersion
	if latest == nil {
		return ArtifactDetail{}, fmt.Errorf("artifact ingest did not create an object version")
	}

	status := StatusCreated
	var indexResult *search.IndexResult
	index, indexErr := s.Search.EnqueueObjectVersion(ctx, req, search.IndexEnqueueInput{
		ObjectID:        ingest.Object.Object.ObjectID,
		ObjectVersionID: latest.ObjectVersionID,
	})
	if indexErr == nil {
		indexResult = &search.IndexResult{
			ObjectID:        index.ObjectID,
			ObjectVersionID: index.ObjectVersionID,
			Status:          index.Status,
			Statuses:        []search.IndexStatus{index.IndexStatus},
		}
		if index.Status == "indexed" || index.Status == "already_indexed" {
			status = StatusIndexed
		}
	}

	artifactID := ids.NewArtifactID()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ArtifactDetail{}, err
	}
	defer tx.Rollback()

	artifact, err := insertArtifactTx(ctx, tx, artifactID, input, ingest.Object, artifactType, title, status)
	if err != nil {
		return ArtifactDetail{}, err
	}
	event, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeArtifactCreated,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    strings.TrimSpace(input.ScopeID),
		TargetKind: "artifact",
		TargetID:   artifactID,
		JobID:      jobID,
		Status:     artifact.Status,
		Result:     "ok",
		Payload: map[string]any{
			"artifact_id":       artifact.ArtifactID,
			"artifact_type":     artifact.ArtifactType,
			"object_id":         artifact.ObjectID,
			"object_version_id": stringValue(artifact.ObjectVersionID),
			"blob_id":           stringValue(artifact.BlobID),
			"job_id":            artifact.JobID,
			"script_id":         stringValue(artifact.ScriptID),
			"script_version_id": stringValue(artifact.ScriptVersionID),
			"source_object_id":  stringValue(artifact.SourceObjectID),
			"status":            artifact.Status,
		},
		VisibilityClass: "internal",
	})
	if err != nil {
		return ArtifactDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return ArtifactDetail{}, err
	}

	eventIDs := append([]string{}, ingest.EventIDs...)
	if indexResult != nil {
		eventIDs = append(eventIDs, indexResult.EventIDs...)
	}
	eventIDs = append(eventIDs, event.EventID)

	if indexErr != nil {
		return ArtifactDetail{
			Artifact: artifact,
			Object:   ingest.Object,
			EventIDs: eventIDs,
		}, fmt.Errorf("artifact created but indexing failed: %w", indexErr)
	}
	return ArtifactDetail{
		Artifact: artifact,
		Object:   ingest.Object,
		Index:    indexResult,
		EventIDs: eventIDs,
	}, nil
}

func (s Service) GetArtifact(ctx context.Context, ref string) (ArtifactDetail, error) {
	artifact, err := s.resolveArtifact(ctx, ref)
	if err != nil {
		return ArtifactDetail{}, err
	}
	object, err := s.Objects.GetObject(ctx, artifact.ObjectID)
	if err != nil {
		return ArtifactDetail{}, err
	}
	return ArtifactDetail{Artifact: artifact, Object: object}, nil
}

func (s Service) ListArtifacts(ctx context.Context, filter ListFilter) ([]Artifact, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	query := artifactSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if strings.TrimSpace(filter.JobRef) != "" {
		add("a.job_id =", strings.TrimSpace(filter.JobRef))
	}
	if strings.TrimSpace(filter.ScopeRef) != "" {
		add("a.scope_id =", strings.TrimSpace(filter.ScopeRef))
	}
	if strings.TrimSpace(filter.ObjectRef) != "" {
		add("a.object_id =", strings.TrimSpace(filter.ObjectRef))
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY a.created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var artifacts []Artifact
	for rows.Next() {
		artifact, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, rows.Err()
}

func (s Service) resolveArtifact(ctx context.Context, ref string) (Artifact, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Artifact{}, fmt.Errorf("artifact ref is required")
	}
	row := s.DB.QueryRowContext(ctx, artifactSelectSQL()+`
		WHERE a.artifact_id = $1 OR a.object_id = $1
		LIMIT 1
	`, ref)
	return scanArtifact(row)
}

func insertArtifactTx(ctx context.Context, tx *sql.Tx, artifactID string, input CreateFromJobFileInput, detail objects.ObjectDetail, artifactType, title, status string) (Artifact, error) {
	versionID := ""
	blobID := ""
	hashURI := ""
	mimeType := ""
	var sizeBytes any
	if detail.LatestVersion != nil {
		versionID = detail.LatestVersion.ObjectVersionID
		if detail.LatestVersion.BlobID != nil {
			blobID = *detail.LatestVersion.BlobID
		}
		if detail.LatestVersion.ContentHash != nil {
			hashURI = *detail.LatestVersion.ContentHash
		}
		if detail.LatestVersion.MimeType != nil {
			mimeType = *detail.LatestVersion.MimeType
		}
		if detail.LatestVersion.SizeBytes != nil {
			sizeBytes = *detail.LatestVersion.SizeBytes
		}
	}
	if sizeBytes == nil {
		sizeBytes = nil
	}

	row := tx.QueryRowContext(ctx, `
		INSERT INTO jobs.artifacts (
			artifact_id, object_id, object_version_id, blob_id, job_id, job_attempt_id,
			script_id, script_version_id, scope_id, source_object_id, artifact_type,
			title, hash_uri, mime_type, size_bytes, status, metadata
		)
		VALUES ($1, $2, nullif($3, ''), nullif($4, ''), $5, nullif($6, ''),
		        nullif($7, ''), nullif($8, ''), nullif($9, ''), nullif($10, ''),
		        $11, $12, nullif($13, ''), nullif($14, ''), $15, $16,
		        '{"slice":"5","part":"2"}'::jsonb)
		RETURNING `+artifactReturningColumns()+`
	`, artifactID, detail.Object.ObjectID, versionID, blobID, strings.TrimSpace(input.JobID),
		strings.TrimSpace(input.JobAttemptID), strings.TrimSpace(input.ScriptID),
		strings.TrimSpace(input.ScriptVersionID), strings.TrimSpace(input.ScopeID),
		strings.TrimSpace(input.SourceObjectID), artifactType, title, hashURI, mimeType,
		sizeBytes, status)
	return scanArtifact(row)
}

func resolveArtifactPath(root, relPath string) (string, error) {
	root = filepath.Clean(root)
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return "", fmt.Errorf("artifact path is required")
	}
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("artifact path must be relative")
	}
	cleanRel := filepath.Clean(relPath)
	if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact path must stay inside artifact dir")
	}
	fullPath := filepath.Join(root, cleanRel)
	evalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve artifact dir: %w", err)
	}
	evalPath, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		return "", fmt.Errorf("resolve artifact path: %w", err)
	}
	rel, err := filepath.Rel(evalRoot, evalPath)
	if err != nil {
		return "", fmt.Errorf("validate artifact path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("artifact path escapes artifact dir")
	}
	return evalPath, nil
}

func artifactSelectSQL() string {
	return `SELECT ` + artifactColumns() + ` FROM jobs.artifacts a`
}

func artifactColumns() string {
	return `
		a.artifact_id, a.object_id, a.object_version_id, a.blob_id, a.job_id,
		a.job_attempt_id, a.script_id, a.script_version_id, a.scope_id,
		a.source_object_id, a.artifact_type, a.title, a.hash_uri, a.mime_type,
		a.size_bytes, a.status, a.created_at, a.metadata`
}

func artifactReturningColumns() string {
	return `
		artifact_id, object_id, object_version_id, blob_id, job_id, job_attempt_id,
		script_id, script_version_id, scope_id, source_object_id, artifact_type,
		title, hash_uri, mime_type, size_bytes, status, created_at, metadata`
}

type artifactScanner interface {
	Scan(dest ...any) error
}

func scanArtifact(scanner artifactScanner) (Artifact, error) {
	var artifact Artifact
	var objectVersionID sql.NullString
	var blobID sql.NullString
	var jobAttemptID sql.NullString
	var scriptID sql.NullString
	var scriptVersionID sql.NullString
	var scopeID sql.NullString
	var sourceObjectID sql.NullString
	var hashURI sql.NullString
	var mimeType sql.NullString
	var sizeBytes sql.NullInt64
	var metadata []byte
	if err := scanner.Scan(
		&artifact.ArtifactID,
		&artifact.ObjectID,
		&objectVersionID,
		&blobID,
		&artifact.JobID,
		&jobAttemptID,
		&scriptID,
		&scriptVersionID,
		&scopeID,
		&sourceObjectID,
		&artifact.ArtifactType,
		&artifact.Title,
		&hashURI,
		&mimeType,
		&sizeBytes,
		&artifact.Status,
		&artifact.CreatedAt,
		&metadata,
	); err != nil {
		return Artifact{}, err
	}
	artifact.ObjectVersionID = stringPtr(objectVersionID)
	artifact.BlobID = stringPtr(blobID)
	artifact.JobAttemptID = stringPtr(jobAttemptID)
	artifact.ScriptID = stringPtr(scriptID)
	artifact.ScriptVersionID = stringPtr(scriptVersionID)
	artifact.ScopeID = stringPtr(scopeID)
	artifact.SourceObjectID = stringPtr(sourceObjectID)
	artifact.HashURI = stringPtr(hashURI)
	artifact.MimeType = stringPtr(mimeType)
	if sizeBytes.Valid {
		value := sizeBytes.Int64
		artifact.SizeBytes = &value
	}
	artifact.Metadata = jsonOrEmpty(metadata)
	return artifact, nil
}

func normalizeJSONObject(raw json.RawMessage) ([]byte, error) {
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

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func jsonOrEmpty(value []byte) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(value)
}
