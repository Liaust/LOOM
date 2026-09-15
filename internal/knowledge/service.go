package knowledge

import (
	"database/sql"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
)

type Clock func() time.Time

type Option func(*Service)

type Service struct {
	store            Store
	now              Clock
	embeddingRuntime EmbeddingRuntime
}

func NewService(db *sql.DB, opts ...Option) *Service {
	service := &Service{
		store: NewStore(db),
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

func WithClock(now Clock) Option {
	return func(service *Service) {
		if now != nil {
			service.now = now
		}
	}
}

func WithEmbeddingRuntime(runtime EmbeddingRuntime) Option {
	return func(service *Service) {
		service.embeddingRuntime = runtime
	}
}

func (s *Service) Store() Store {
	if s == nil {
		return Store{}
	}
	return s.store
}

func (s *Service) PrepareSourceRoot(root SourceRoot) (SourceRoot, error) {
	now := s.currentTime()
	if root.NotesSourceRootID == "" {
		root.NotesSourceRootID = ids.NewNotesSourceRootID()
	}
	if root.Status == "" {
		root.Status = SourceRootStatusActive
	}
	root.AuthorizationMetadata = defaultJSONObject(root.AuthorizationMetadata)
	root.Metadata = defaultJSONObject(root.Metadata)
	if root.CreatedAt.IsZero() {
		root.CreatedAt = now
	}
	if root.UpdatedAt.IsZero() {
		root.UpdatedAt = now
	}
	return root, ValidateSourceRoot(root)
}

func (s *Service) PrepareKnowledgeObject(object KnowledgeObject) (KnowledgeObject, error) {
	now := s.currentTime()
	if object.KnowledgeObjectID == "" {
		object.KnowledgeObjectID = ids.NewKnowledgeObjectID()
	}
	if object.FileClass == "" {
		object.FileClass = storagecatalog.FileClassUnknown
	}
	if object.ProcessingState == "" {
		object.ProcessingState = ProcessingStateMetadataOnly
	}
	object.Metadata = defaultJSONObject(object.Metadata)
	if object.LastSeenAt.IsZero() {
		object.LastSeenAt = now
	}
	if object.RecencyAt.IsZero() {
		absoluteTime, err := ResolveAbsoluteTimeFromMetadata(object.LastSeenAt, object.Metadata)
		if err != nil {
			return KnowledgeObject{}, err
		}
		object.SourceCreatedAt = absoluteTime.SourceCreatedAt
		object.SourceModifiedAt = absoluteTime.SourceModifiedAt
		object.RecencyAt = absoluteTime.RecencyAt
		object.RecencyBasis = absoluteTime.RecencyBasis
		object.AbsoluteTimeMetadata = absoluteTimeMetadataJSON(absoluteTime)
	}
	object.AbsoluteTimeMetadata = defaultJSONObject(object.AbsoluteTimeMetadata)
	if object.CreatedAt.IsZero() {
		object.CreatedAt = now
	}
	if object.UpdatedAt.IsZero() {
		object.UpdatedAt = now
	}
	return object, ValidateKnowledgeObject(object)
}

func (s *Service) PrepareKnowledgeObjectVersion(version KnowledgeObjectVersion) (KnowledgeObjectVersion, error) {
	now := s.currentTime()
	if version.KnowledgeObjectVersionID == "" {
		version.KnowledgeObjectVersionID = ids.NewKnowledgeObjectVersionID()
	}
	if version.FileClass == "" {
		version.FileClass = storagecatalog.FileClassUnknown
	}
	version.Metadata = defaultJSONObject(version.Metadata)
	if version.ObservedAt.IsZero() {
		version.ObservedAt = now
	}
	if version.RecencyAt.IsZero() {
		absoluteTime, err := ResolveAbsoluteTimeFromMetadata(version.ObservedAt, version.Metadata)
		if err != nil {
			return KnowledgeObjectVersion{}, err
		}
		version.SourceCreatedAt = absoluteTime.SourceCreatedAt
		version.SourceModifiedAt = absoluteTime.SourceModifiedAt
		version.RecencyAt = absoluteTime.RecencyAt
		version.RecencyBasis = absoluteTime.RecencyBasis
		version.AbsoluteTimeMetadata = absoluteTimeMetadataJSON(absoluteTime)
	}
	version.AbsoluteTimeMetadata = defaultJSONObject(version.AbsoluteTimeMetadata)
	if version.CreatedAt.IsZero() {
		version.CreatedAt = now
	}
	return version, ValidateKnowledgeObjectVersion(version)
}

func (s *Service) PrepareKnowledgeChunk(chunk KnowledgeChunk) (KnowledgeChunk, error) {
	now := s.currentTime()
	if chunk.KnowledgeChunkID == "" {
		chunk.KnowledgeChunkID = ids.NewKnowledgeChunkID()
	}
	if chunk.Status == "" {
		chunk.Status = ChunkStatusCreated
	}
	chunk.Metadata = defaultJSONObject(chunk.Metadata)
	if chunk.CreatedAt.IsZero() {
		chunk.CreatedAt = now
	}
	return chunk, ValidateKnowledgeChunk(chunk)
}

func (s *Service) PreparePipelineStatus(status PipelineStatus) (PipelineStatus, error) {
	now := s.currentTime()
	if status.KnowledgePipelineStatusID == "" {
		status.KnowledgePipelineStatusID = ids.NewKnowledgePipelineStatusID()
	}
	if status.Status == "" {
		status.Status = PipelineStatusNotStarted
	}
	if status.Priority == 0 {
		status.Priority = 100
	}
	status.Metadata = defaultJSONObject(status.Metadata)
	if status.CreatedAt.IsZero() {
		status.CreatedAt = now
	}
	if status.UpdatedAt.IsZero() {
		status.UpdatedAt = now
	}
	return status, ValidatePipelineStatus(status)
}

func (s *Service) PrepareObjectLink(link ObjectLink) (ObjectLink, error) {
	now := s.currentTime()
	if link.KnowledgeObjectLinkID == "" {
		link.KnowledgeObjectLinkID = ids.NewKnowledgeObjectLinkID()
	}
	if link.LinkKind == "" {
		link.LinkKind = LinkKindUnknown
	}
	if link.Status == "" {
		link.Status = LinkStatusUnresolved
	}
	link.Metadata = defaultJSONObject(link.Metadata)
	if link.CreatedAt.IsZero() {
		link.CreatedAt = now
	}
	if link.UpdatedAt.IsZero() {
		link.UpdatedAt = now
	}
	return link, ValidateObjectLink(link)
}

func (s *Service) currentTime() time.Time {
	if s == nil || s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func defaultJSONObject(raw []byte) []byte {
	if len(raw) == 0 {
		return append([]byte(nil), emptyJSONObject...)
	}
	return raw
}
