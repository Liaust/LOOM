package artifacts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/objectstore"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

const MaximumContentBytes = 64 << 20
const DefaultTextBytes = 64 << 10

var ErrReadDenied = errors.New("artifact_read_denied")
var ErrContentUnavailable = errors.New("artifact_content_unavailable")
var ErrBinaryContent = errors.New("artifact_binary_content")
var ErrInvalidRead = errors.New("artifact_read_invalid")

type ReadResult struct {
	ArtifactID      string `json:"artifact_id"`
	ObjectVersionID string `json:"object_version_id"`
	Hash            string `json:"hash"`
	MimeType        string `json:"mime_type"`
	SizeBytes       int64  `json:"size_bytes"`
	Content         string `json:"content,omitempty"`
	Payload         []byte `json:"payload,omitempty"`
	Truncated       bool   `json:"truncated"`
}

func (s Service) ReadContent(ctx context.Context, req requestctx.Context, ref string, maxBytes int, download bool) (ReadResult, error) {
	if ids.Validate(ids.ActorPrefix, req.ActorID) != nil || ids.Validate(ids.NodePrefix, req.OriginNodeID) != nil {
		return ReadResult{}, ErrReadDenied
	}
	if maxBytes == 0 {
		maxBytes = DefaultTextBytes
	}
	if maxBytes < 1 || maxBytes > MaximumContentBytes {
		return ReadResult{}, ErrInvalidRead
	}
	a, err := s.resolveArtifact(ctx, ref)
	if err != nil {
		return ReadResult{}, err
	}
	if a.ScopeID == nil || a.ObjectVersionID == nil || a.BlobID == nil {
		return ReadResult{}, ErrContentUnavailable
	}
	if err = s.authorizeContent(ctx, req, *a.ScopeID); err != nil {
		return ReadResult{}, err
	}
	source, err := s.Objects.ReadObjectVersionSource(ctx, a.ObjectID, *a.ObjectVersionID)
	if err != nil || source.HomeScopeID == nil || *source.HomeScopeID != *a.ScopeID || source.BlobID != *a.BlobID ||
		a.HashURI == nil || *a.HashURI != source.Blob.HashURI || a.SizeBytes == nil || *a.SizeBytes != source.Blob.SizeBytes {
		return ReadResult{}, ErrContentUnavailable
	}
	body, err := s.Objects.Store.ReadVerified(ctx, source.Blob, MaximumContentBytes)
	if err != nil {
		return ReadResult{}, err
	}
	result := ReadResult{ArtifactID: a.ArtifactID, ObjectVersionID: source.ObjectVersionID, Hash: source.Blob.HashURI, MimeType: stringValue(a.MimeType), SizeBytes: source.Blob.SizeBytes}
	if download {
		result.Payload = body
		return result, nil
	}
	if !utf8.Valid(body) || strings.IndexByte(string(body), 0) >= 0 {
		return ReadResult{}, ErrBinaryContent
	}
	if len(body) > maxBytes {
		body = body[:maxBytes]
		for len(body) > 0 && !utf8.Valid(body) {
			body = body[:len(body)-1]
		}
		result.Truncated = true
	}
	result.Content = string(body)
	return result, nil
}

func (s Service) authorizeContent(ctx context.Context, req requestctx.Context, scopeID string) error {
	var projectID, targetNodeID, owner sql.NullString
	var status string
	err := s.DB.QueryRowContext(ctx, `SELECT p.project_id,coalesce(p.home_node_id,ss.home_node_id),ss.owner_actor_id,ss.status
	 FROM scopes.scopes ss LEFT JOIN projects.projects p ON p.project_scope_id=ss.scope_id WHERE ss.scope_id=$1`, scopeID).Scan(&projectID, &targetNodeID, &owner, &status)
	if err != nil || status != "active" {
		return ErrReadDenied
	}
	if projectID.Valid {
		if !targetNodeID.Valid {
			return ErrReadDenied
		}
		authority, err := projects.NewService(s.DB).GetDeclarationAuthority(ctx, req, projectID.String, targetNodeID.String)
		if err != nil || !authority.CanRead {
			return ErrReadDenied
		}
	} else if !owner.Valid || owner.String != req.ActorID {
		return ErrReadDenied
	}
	return nil
}

func ContentErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrReadDenied):
		return "artifact.read_denied"
	case errors.Is(err, ErrBinaryContent):
		return "artifact.binary_content"
	case errors.Is(err, objectstore.ErrReadLimit):
		return "artifact.content_too_large"
	case errors.Is(err, ErrInvalidRead):
		return "artifact.invalid_read"
	default:
		return "artifact.content_unavailable"
	}
}
