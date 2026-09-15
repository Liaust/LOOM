package knowledge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
)

var (
	ErrInvalid  = errors.New("invalid knowledge input")
	ErrConflict = errors.New("knowledge state conflict")
)

var sha256Pattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var validRootKinds = map[string]struct{}{
	RootKindBoxNotes:        {},
	RootKindProjectNotes:    {},
	RootKindBoxTopics:       {},
	RootKindBoxLibrary:      {},
	RootKindProjectMaterial: {},
}

var validSourceRootStatuses = map[string]struct{}{
	SourceRootStatusActive:   {},
	SourceRootStatusStale:    {},
	SourceRootStatusDisabled: {},
	SourceRootStatusDeleted:  {},
	SourceRootStatusBlocked:  {},
}

var validFileClasses = map[string]struct{}{
	storagecatalog.FileClassMarkdown:          {},
	storagecatalog.FileClassText:              {},
	storagecatalog.FileClassPDF:               {},
	storagecatalog.FileClassImage:             {},
	storagecatalog.FileClassVideo:             {},
	storagecatalog.FileClassAudio:             {},
	storagecatalog.FileClassArchive:           {},
	storagecatalog.FileClassCode:              {},
	storagecatalog.FileClassOfficeDocument:    {},
	storagecatalog.FileClassDirectory:         {},
	storagecatalog.FileClassPackage:           {},
	storagecatalog.FileClassGeneratedMetadata: {},
	storagecatalog.FileClassBinary:            {},
	storagecatalog.FileClassUnknown:           {},
}

var validProcessingStates = map[string]struct{}{
	ProcessingStateMetadataOnly:  {},
	ProcessingStateTextExtracted: {},
	ProcessingStateChunked:       {},
	ProcessingStateIndexed:       {},
	ProcessingStateEmbedded:      {},
	ProcessingStateFailed:        {},
	ProcessingStateStale:         {},
	ProcessingStateDeleted:       {},
}

var validChunkStatuses = map[string]struct{}{
	ChunkStatusCreated:          {},
	ChunkStatusIndexed:          {},
	ChunkStatusStale:            {},
	ChunkStatusFailed:           {},
	ChunkStatusDisabledByPolicy: {},
}

var validPipelineStages = map[string]struct{}{
	PipelineStageMetadata:       {},
	PipelineStageTextExtraction: {},
	PipelineStageChunking:       {},
	PipelineStageBM25:           {},
	PipelineStageProjection:     {},
	PipelineStageEmbedding:      {},
}

var validPipelineStatuses = map[string]struct{}{
	PipelineStatusNotStarted:         {},
	PipelineStatusQueued:             {},
	PipelineStatusProcessing:         {},
	PipelineStatusComplete:           {},
	PipelineStatusStale:              {},
	PipelineStatusFailed:             {},
	PipelineStatusDisabledByPolicy:   {},
	PipelineStatusSkippedUnsupported: {},
}

var validLinkKinds = map[string]struct{}{
	LinkKindMarkdown: {},
	LinkKindWikilink: {},
	LinkKindURL:      {},
	LinkKindFile:     {},
	LinkKindUnknown:  {},
}

var validLinkStatuses = map[string]struct{}{
	LinkStatusUnresolved: {},
	LinkStatusResolved:   {},
	LinkStatusBroken:     {},
	LinkStatusIgnored:    {},
}

var validEmbeddingRuntimes = map[string]struct{}{
	EmbeddingRuntimeOllama: {},
}

var validEmbeddingDistances = map[string]struct{}{
	EmbeddingDistanceCosine: {},
}

var validEmbeddingObjectStatuses = map[string]struct{}{
	EmbeddingObjectStatusNotStarted:         {},
	EmbeddingObjectStatusQueued:             {},
	EmbeddingObjectStatusProcessing:         {},
	EmbeddingObjectStatusComplete:           {},
	EmbeddingObjectStatusStale:              {},
	EmbeddingObjectStatusFailed:             {},
	EmbeddingObjectStatusDisabledByPolicy:   {},
	EmbeddingObjectStatusSkippedUnsupported: {},
}

var validChunkEmbeddingStatuses = map[string]struct{}{
	ChunkEmbeddingStatusActive:     {},
	ChunkEmbeddingStatusHistorical: {},
	ChunkEmbeddingStatusReusable:   {},
	ChunkEmbeddingStatusStale:      {},
	ChunkEmbeddingStatusFailed:     {},
}

var validEmbeddingWorkStatuses = map[string]struct{}{
	EmbeddingWorkStatusQueued:             {},
	EmbeddingWorkStatusProcessing:         {},
	EmbeddingWorkStatusComplete:           {},
	EmbeddingWorkStatusStale:              {},
	EmbeddingWorkStatusFailed:             {},
	EmbeddingWorkStatusDisabledByPolicy:   {},
	EmbeddingWorkStatusSkippedUnsupported: {},
}

var validNotesSearchModes = map[string]struct{}{
	NotesSearchModeLexical:  {},
	NotesSearchModeSemantic: {},
	NotesSearchModeHybrid:   {},
}

var validAbsoluteTimeKinds = map[string]struct{}{
	AbsoluteTimeKindCreated:  {},
	AbsoluteTimeKindModified: {},
	AbsoluteTimeKindObserved: {},
}

var validAbsoluteTimeBases = map[string]struct{}{
	AbsoluteTimeBasisSourceFilesystemMtime:     {},
	AbsoluteTimeBasisSourceFilesystemBirthtime: {},
	AbsoluteTimeBasisFrontmatterUpdatedAt:      {},
	AbsoluteTimeBasisFrontmatterCreatedAt:      {},
	AbsoluteTimeBasisEmbeddedModifiedAt:        {},
	AbsoluteTimeBasisEmbeddedCreatedAt:         {},
	AbsoluteTimeBasisSourceObjectMetadata:      {},
	AbsoluteTimeBasisObservedAtFallback:        {},
}

func ValidateAbsoluteTimeCandidate(candidate AbsoluteTimeCandidate) error {
	if _, ok := validAbsoluteTimeKinds[candidate.Kind]; !ok {
		return invalid("absolute-time candidate kind %q is not supported", candidate.Kind)
	}
	if _, ok := validAbsoluteTimeBases[candidate.Basis]; !ok {
		return invalid("absolute-time candidate basis %q is not supported", candidate.Basis)
	}
	validPair := false
	switch candidate.Basis {
	case AbsoluteTimeBasisSourceFilesystemMtime, AbsoluteTimeBasisFrontmatterUpdatedAt, AbsoluteTimeBasisEmbeddedModifiedAt:
		validPair = candidate.Kind == AbsoluteTimeKindModified
	case AbsoluteTimeBasisSourceFilesystemBirthtime, AbsoluteTimeBasisFrontmatterCreatedAt, AbsoluteTimeBasisEmbeddedCreatedAt:
		validPair = candidate.Kind == AbsoluteTimeKindCreated
	case AbsoluteTimeBasisSourceObjectMetadata:
		validPair = candidate.Kind == AbsoluteTimeKindCreated || candidate.Kind == AbsoluteTimeKindModified
	case AbsoluteTimeBasisObservedAtFallback:
		validPair = candidate.Kind == AbsoluteTimeKindObserved
	}
	if !validPair {
		return invalid("absolute-time candidate basis %q does not support kind %q", candidate.Basis, candidate.Kind)
	}
	return nil
}

func ValidateAbsoluteTime(value AbsoluteTime) error {
	if value.RecencyAt.IsZero() {
		return invalid("absolute-time recency_at is required")
	}
	if _, ok := validAbsoluteTimeBases[value.RecencyBasis]; !ok {
		return invalid("absolute-time recency_basis %q is not supported", value.RecencyBasis)
	}
	if value.RecencyBasis == AbsoluteTimeBasisObservedAtFallback && (value.SourceCreatedAt != nil || value.SourceModifiedAt != nil) {
		return invalid("observation fallback cannot be selected when normalized source time is available")
	}
	return nil
}

func ValidateSourceRoot(root SourceRoot) error {
	if err := validateID(ids.NotesSourceRootPrefix, root.NotesSourceRootID, "notes_source_root_id"); err != nil {
		return err
	}
	if _, ok := validRootKinds[root.RootKind]; !ok {
		return invalid("root_kind %q is not supported", root.RootKind)
	}
	if root.NodeID == nil && strings.TrimSpace(root.NodeKey) == "" {
		return invalid("node_id or node_key is required")
	}
	if err := validateOptionalID(ids.NodePrefix, root.NodeID, "node_id"); err != nil {
		return err
	}
	if err := validateOptionalID(ids.ProjectPrefix, root.ProjectID, "project_id"); err != nil {
		return err
	}
	if (root.RootKind == RootKindProjectNotes || root.RootKind == RootKindProjectMaterial) && root.ProjectID == nil {
		return invalid("project_id is required for project notes source roots")
	}
	if (root.RootKind == RootKindBoxTopics || root.RootKind == RootKindBoxLibrary) && (root.ProjectID != nil || root.ProjectWatchedRootRegistrationID != nil) {
		return invalid("Box source roots cannot carry project authority")
	}
	if root.RootKind == RootKindProjectMaterial && root.BoxWatchRootRegistrationID != nil {
		return invalid("project material cannot carry Box registration authority")
	}
	if err := validateOptionalID(ids.BoxWatchRootRegistrationPrefix, root.BoxWatchRootRegistrationID, "box_watch_root_registration_id"); err != nil {
		return err
	}
	if err := validateOptionalID(ids.ProjectWatchedRootRegistrationPrefix, root.ProjectWatchedRootRegistrationID, "project_watched_root_registration_id"); err != nil {
		return err
	}
	if strings.TrimSpace(root.BackendRootKey) == "" {
		return invalid("backend_root_key is required")
	}
	if _, ok := validSourceRootStatuses[root.Status]; !ok {
		return invalid("status %q is not supported", root.Status)
	}
	if err := validateJSONObject("authorization_metadata", root.AuthorizationMetadata); err != nil {
		return err
	}
	return validateJSONObject("metadata", root.Metadata)
}

func ValidateKnowledgeObject(object KnowledgeObject) error {
	if err := validateID(ids.KnowledgeObjectPrefix, object.KnowledgeObjectID, "knowledge_object_id"); err != nil {
		return err
	}
	if err := validateID(ids.NotesSourceRootPrefix, object.NotesSourceRootID, "notes_source_root_id"); err != nil {
		return err
	}
	if err := validateOptionalID(ids.StorageEntryPrefix, object.StorageEntryID, "storage_entry_id"); err != nil {
		return err
	}
	if err := validateOptionalID(ids.NodePrefix, object.SourceNodeID, "source_node_id"); err != nil {
		return err
	}
	if err := validateOptionalID(ids.ProjectPrefix, object.ProjectID, "project_id"); err != nil {
		return err
	}
	if strings.TrimSpace(object.RelativePath) == "" {
		return invalid("relative_path is required")
	}
	if _, ok := validFileClasses[object.FileClass]; !ok {
		return invalid("file_class %q is not supported", object.FileClass)
	}
	if object.SizeBytes != nil && *object.SizeBytes < 0 {
		return invalid("size_bytes must be non-negative")
	}
	if err := validateOptionalHash(object.SourceHash, "source_hash"); err != nil {
		return err
	}
	if _, ok := validProcessingStates[object.ProcessingState]; !ok {
		return invalid("processing_state %q is not supported", object.ProcessingState)
	}
	if err := ValidateAbsoluteTime(AbsoluteTime{
		SourceCreatedAt: object.SourceCreatedAt, SourceModifiedAt: object.SourceModifiedAt,
		RecencyAt: object.RecencyAt, RecencyBasis: object.RecencyBasis,
	}); err != nil {
		return err
	}
	if err := validateJSONObject("absolute_time_metadata", object.AbsoluteTimeMetadata); err != nil {
		return err
	}
	return validateJSONObject("metadata", object.Metadata)
}

func ValidateKnowledgeObjectVersion(version KnowledgeObjectVersion) error {
	if err := validateID(ids.KnowledgeObjectVersionPrefix, version.KnowledgeObjectVersionID, "knowledge_object_version_id"); err != nil {
		return err
	}
	if err := validateID(ids.KnowledgeObjectPrefix, version.KnowledgeObjectID, "knowledge_object_id"); err != nil {
		return err
	}
	if version.VersionNumber <= 0 {
		return invalid("version_number must be greater than zero")
	}
	if err := validateOptionalID(ids.StorageEntryPrefix, version.StorageEntryID, "storage_entry_id"); err != nil {
		return err
	}
	if err := validateOptionalHash(version.SourceHash, "source_hash"); err != nil {
		return err
	}
	if _, ok := validFileClasses[version.FileClass]; !ok {
		return invalid("file_class %q is not supported", version.FileClass)
	}
	if version.SizeBytes != nil && *version.SizeBytes < 0 {
		return invalid("size_bytes must be non-negative")
	}
	if err := ValidateAbsoluteTime(AbsoluteTime{
		SourceCreatedAt: version.SourceCreatedAt, SourceModifiedAt: version.SourceModifiedAt,
		RecencyAt: version.RecencyAt, RecencyBasis: version.RecencyBasis,
	}); err != nil {
		return err
	}
	if err := validateJSONObject("absolute_time_metadata", version.AbsoluteTimeMetadata); err != nil {
		return err
	}
	return validateJSONObject("metadata", version.Metadata)
}

func ValidateKnowledgeChunk(chunk KnowledgeChunk) error {
	if err := validateID(ids.KnowledgeChunkPrefix, chunk.KnowledgeChunkID, "knowledge_chunk_id"); err != nil {
		return err
	}
	if err := validateID(ids.KnowledgeObjectPrefix, chunk.KnowledgeObjectID, "knowledge_object_id"); err != nil {
		return err
	}
	if err := validateOptionalID(ids.KnowledgeObjectVersionPrefix, chunk.KnowledgeObjectVersionID, "knowledge_object_version_id"); err != nil {
		return err
	}
	if chunk.ChunkIndex <= 0 {
		return invalid("chunk_index must be greater than zero")
	}
	if strings.TrimSpace(chunk.ChunkText) == "" {
		return invalid("chunk_text is required")
	}
	if !sha256Pattern.MatchString(chunk.ChunkHash) {
		return invalid("chunk_hash must be a sha256 content address")
	}
	if strings.TrimSpace(chunk.ChunkerVersion) == "" {
		return invalid("chunker_version is required")
	}
	if chunk.StartOffset != nil && *chunk.StartOffset < 0 {
		return invalid("start_offset must be non-negative")
	}
	if chunk.EndOffset != nil {
		if *chunk.EndOffset < 0 {
			return invalid("end_offset must be non-negative")
		}
		if chunk.StartOffset != nil && *chunk.EndOffset < *chunk.StartOffset {
			return invalid("end_offset must be greater than or equal to start_offset")
		}
	}
	if chunk.TokenCountEstimate != nil && *chunk.TokenCountEstimate < 0 {
		return invalid("token_count_estimate must be non-negative")
	}
	if _, ok := validChunkStatuses[chunk.Status]; !ok {
		return invalid("status %q is not supported", chunk.Status)
	}
	return validateJSONObject("metadata", chunk.Metadata)
}

func ValidatePipelineStatus(status PipelineStatus) error {
	if err := validateID(ids.KnowledgePipelineStatusPrefix, status.KnowledgePipelineStatusID, "knowledge_pipeline_status_id"); err != nil {
		return err
	}
	if err := validateID(ids.KnowledgeObjectPrefix, status.KnowledgeObjectID, "knowledge_object_id"); err != nil {
		return err
	}
	if err := validateOptionalID(ids.KnowledgeObjectVersionPrefix, status.KnowledgeObjectVersionID, "knowledge_object_version_id"); err != nil {
		return err
	}
	if strings.TrimSpace(status.PipelineKey) == "" {
		return invalid("pipeline_key is required")
	}
	if strings.TrimSpace(status.PipelineVersion) == "" {
		return invalid("pipeline_version is required")
	}
	if _, ok := validPipelineStages[status.Stage]; !ok {
		return invalid("stage %q is not supported", status.Stage)
	}
	if _, ok := validPipelineStatuses[status.Status]; !ok {
		return invalid("status %q is not supported", status.Status)
	}
	if status.AttemptCount < 0 {
		return invalid("attempt_count must be non-negative")
	}
	if status.Priority < 0 {
		return invalid("priority must be non-negative")
	}
	return validateJSONObject("metadata", status.Metadata)
}

func ValidateObjectLink(link ObjectLink) error {
	if err := validateID(ids.KnowledgeObjectLinkPrefix, link.KnowledgeObjectLinkID, "knowledge_object_link_id"); err != nil {
		return err
	}
	if err := validateID(ids.KnowledgeObjectPrefix, link.SourceKnowledgeObjectID, "source_knowledge_object_id"); err != nil {
		return err
	}
	if err := validateOptionalID(ids.KnowledgeObjectPrefix, link.TargetKnowledgeObjectID, "target_knowledge_object_id"); err != nil {
		return err
	}
	if err := validateOptionalID(ids.KnowledgeChunkPrefix, link.ChunkID, "chunk_id"); err != nil {
		return err
	}
	if _, ok := validLinkKinds[link.LinkKind]; !ok {
		return invalid("link_kind %q is not supported", link.LinkKind)
	}
	if strings.TrimSpace(link.RawTarget) == "" {
		return invalid("raw_target is required")
	}
	if _, ok := validLinkStatuses[link.Status]; !ok {
		return invalid("status %q is not supported", link.Status)
	}
	return validateJSONObject("metadata", link.Metadata)
}

func IsRootKind(kind string) bool {
	_, ok := validRootKinds[kind]
	return ok
}

func IsProcessingState(state string) bool {
	_, ok := validProcessingStates[state]
	return ok
}

func IsPipelineStage(stage string) bool {
	_, ok := validPipelineStages[stage]
	return ok
}

func IsPipelineStatus(status string) bool {
	_, ok := validPipelineStatuses[status]
	return ok
}

func IsLinkKind(kind string) bool {
	_, ok := validLinkKinds[kind]
	return ok
}

func IsEmbeddingRuntime(runtime string) bool {
	_, ok := validEmbeddingRuntimes[runtime]
	return ok
}

func IsEmbeddingDistance(distance string) bool {
	_, ok := validEmbeddingDistances[distance]
	return ok
}

func IsEmbeddingObjectStatus(status string) bool {
	_, ok := validEmbeddingObjectStatuses[status]
	return ok
}

func IsChunkEmbeddingStatus(status string) bool {
	_, ok := validChunkEmbeddingStatuses[status]
	return ok
}

func IsEmbeddingWorkStatus(status string) bool {
	_, ok := validEmbeddingWorkStatuses[status]
	return ok
}

func IsNotesSearchMode(mode string) bool {
	_, ok := validNotesSearchModes[mode]
	return ok
}

func ValidatePipelineRun(run PipelineRun) error {
	if err := validateID(ids.KnowledgePipelineRunPrefix, run.KnowledgePipelineRunID, "knowledge_pipeline_run_id"); err != nil {
		return err
	}
	if err := validateID(ids.KnowledgeObjectPrefix, run.KnowledgeObjectID, "knowledge_object_id"); err != nil {
		return err
	}
	if err := validateID(ids.KnowledgeObjectVersionPrefix, run.KnowledgeObjectVersionID, "knowledge_object_version_id"); err != nil {
		return err
	}
	if strings.TrimSpace(run.PipelineDefinitionKey) == "" || strings.TrimSpace(run.PipelineDefinitionVersion) == "" {
		return invalid("pipeline definition key and version are required")
	}
	if run.Generation <= 0 || run.Priority < 0 || run.WarningCount < 0 || run.ClaimGeneration < 0 {
		return invalid("pipeline generation and counters are invalid")
	}
	if !isFilePipelineStatus(run.Status) {
		return invalid("pipeline status %q is not supported", run.Status)
	}
	if run.CurrentExecutionClass != "" && !isPipelineExecutionClass(run.CurrentExecutionClass) {
		return invalid("pipeline execution class %q is not supported", run.CurrentExecutionClass)
	}
	if err := validateOptionalHash(run.SourceHash, "source_hash"); err != nil {
		return err
	}
	if run.ClaimedByWorkerRunID == "" && run.ClaimExpiresAt != nil {
		return invalid("claim expiry requires a worker run claim")
	}
	if run.ClaimedByWorkerRunID != "" && (run.ClaimGeneration <= 0 || run.ClaimExpiresAt == nil) {
		return invalid("worker run claim requires a positive generation and expiry")
	}
	if err := validateJSONObject("plan_snapshot", run.PlanSnapshot); err != nil {
		return err
	}
	if err := validateJSONObject("resource_totals", run.ResourceTotals); err != nil {
		return err
	}
	return validateJSONObject("metadata", run.Metadata)
}

func ValidatePipelineStageRun(stage PipelineStageRun) error {
	if err := validateID(ids.KnowledgePipelineStageRunPrefix, stage.KnowledgePipelineStageRunID, "knowledge_pipeline_stage_run_id"); err != nil {
		return err
	}
	if err := validateID(ids.KnowledgePipelineRunPrefix, stage.KnowledgePipelineRunID, "knowledge_pipeline_run_id"); err != nil {
		return err
	}
	if !isFilePipelineStage(stage.StageKey) || !isPipelineExecutionClass(stage.ExecutionClass) || !isPipelineStageRunStatus(stage.Status) {
		return invalid("pipeline stage key, execution class, or status is invalid")
	}
	if strings.TrimSpace(stage.StageContractVersion) == "" || stage.Ordinal <= 0 || stage.AttemptCount < 0 || stage.ProgressCompleted < 0 || stage.ProgressTotal < 0 || stage.ProgressCompleted > stage.ProgressTotal {
		return invalid("pipeline stage contract, ordinal, attempts, or progress is invalid")
	}
	if err := validateOptionalHash(stage.InputHash, "input_hash"); err != nil {
		return err
	}
	if err := validateJSONArray("dependency_snapshot", stage.DependencySnapshot); err != nil {
		return err
	}
	for name, raw := range map[string]json.RawMessage{"resource_request": stage.ResourceRequest, "resource_usage": stage.ResourceUsage, "error": stage.Error, "metadata": stage.Metadata} {
		if err := validateJSONObject(name, raw); err != nil {
			return err
		}
	}
	return validateJSONArray("warnings", stage.Warnings)
}

func ValidatePipelineStageUnit(unit PipelineStageUnit) error {
	if err := validateID(ids.KnowledgePipelineStageUnitPrefix, unit.KnowledgePipelineStageUnitID, "knowledge_pipeline_stage_unit_id"); err != nil {
		return err
	}
	if err := validateID(ids.KnowledgePipelineStageRunPrefix, unit.KnowledgePipelineStageRunID, "knowledge_pipeline_stage_run_id"); err != nil {
		return err
	}
	if strings.TrimSpace(unit.UnitKey) == "" || unit.AttemptCount < 0 || !sha256Pattern.MatchString(unit.UnitInputHash) || !isPipelineStageUnitStatus(unit.Status) {
		return invalid("pipeline stage unit identity, status, or attempts are invalid")
	}
	claimed := strings.TrimSpace(unit.ClaimedByWorkerRunID) != "" || unit.ClaimGeneration > 0
	if (unit.Status == PipelineStageStatusProcessing) != claimed || unit.ClaimGeneration < 0 {
		return invalid("pipeline stage unit claim fence is invalid")
	}
	if unit.PageNumber != nil && *unit.PageNumber <= 0 {
		return invalid("page_number must be greater than zero")
	}
	if unit.Confidence != nil && (*unit.Confidence < 0 || *unit.Confidence > 1) {
		return invalid("confidence must be between zero and one")
	}
	if err := validateOptionalID(ids.KnowledgeDerivedArtifactPrefix, unit.OutputArtifactID, "output_artifact_id"); err != nil {
		return err
	}
	for name, raw := range map[string]json.RawMessage{"resource_usage": unit.ResourceUsage, "error": unit.Error, "metadata": unit.Metadata} {
		if err := validateJSONObject(name, raw); err != nil {
			return err
		}
	}
	return validateJSONArray("warnings", unit.Warnings)
}

func ValidateDerivedArtifact(artifact DerivedArtifact) error {
	if err := validateID(ids.KnowledgeDerivedArtifactPrefix, artifact.KnowledgeDerivedArtifactID, "knowledge_derived_artifact_id"); err != nil {
		return err
	}
	for prefix, pair := range map[string][2]string{
		ids.KnowledgeObjectPrefix:           {artifact.KnowledgeObjectID, "knowledge_object_id"},
		ids.KnowledgeObjectVersionPrefix:    {artifact.KnowledgeObjectVersionID, "knowledge_object_version_id"},
		ids.KnowledgePipelineRunPrefix:      {artifact.KnowledgePipelineRunID, "knowledge_pipeline_run_id"},
		ids.KnowledgePipelineStageRunPrefix: {artifact.KnowledgePipelineStageRunID, "knowledge_pipeline_stage_run_id"},
	} {
		if err := validateID(prefix, pair[0], pair[1]); err != nil {
			return err
		}
	}
	if artifact.Generation <= 0 || !isArtifactKind(artifact.ArtifactKind) || !isArtifactState(artifact.State) || strings.TrimSpace(artifact.SourceLocator) == "" {
		return invalid("derived artifact generation, kind, state, or source locator is invalid")
	}
	if (artifact.TextContent == nil) == (strings.TrimSpace(artifact.PayloadRef) == "") {
		return invalid("derived artifact must contain exactly one of text_content or payload_ref")
	}
	if !sha256Pattern.MatchString(artifact.ContentHash) || !sha256Pattern.MatchString(artifact.InputHash) {
		return invalid("derived artifact hashes must be sha256 content addresses")
	}
	if strings.TrimSpace(artifact.GeneratorKey) == "" || strings.TrimSpace(artifact.GeneratorVersion) == "" {
		return invalid("derived artifact generator key and version are required")
	}
	if artifact.Active && artifact.State != ArtifactStateActive {
		return invalid("active derived artifact must have active state")
	}
	if artifact.Confidence != nil && (*artifact.Confidence < 0 || *artifact.Confidence > 1) {
		return invalid("confidence must be between zero and one")
	}
	if err := ValidateAbsoluteTime(AbsoluteTime{SourceCreatedAt: artifact.SourceCreatedAt, SourceModifiedAt: artifact.SourceModifiedAt, RecencyAt: artifact.RecencyAt, RecencyBasis: artifact.RecencyBasis}); err != nil {
		return err
	}
	return validateJSONObject("metadata", artifact.Metadata)
}

func isFilePipelineStage(value string) bool {
	switch value {
	case FilePipelineStageMetadata, FilePipelineStageNativeText, FilePipelineStagePDFPageAnalysis,
		FilePipelineStagePDFOCR, FilePipelineStageImageDescription, FilePipelineStageConsolidateText,
		FilePipelineStageChunk, FilePipelineStageLexicalIndex, FilePipelineStageEmbedding, FilePipelineStageFinalize:
		return true
	default:
		return false
	}
}

func isFilePipelineStatus(value string) bool {
	switch value {
	case FilePipelineStatusQueued, FilePipelineStatusWaitingQuietWindow, FilePipelineStatusWaitingCoordinator,
		FilePipelineStatusWaitingHeavy, FilePipelineStatusProcessing, FilePipelineStatusComplete,
		FilePipelineStatusCompleteWithWarning, FilePipelineStatusBlockedManual, FilePipelineStatusFailed,
		FilePipelineStatusStale, FilePipelineStatusCancelled:
		return true
	default:
		return false
	}
}

func isPipelineExecutionClass(value string) bool {
	return value == PipelineExecutionCoordinator || value == PipelineExecutionHeavy
}

func isPipelineStageRunStatus(value string) bool {
	switch value {
	case PipelineStageStatusPlanned, PipelineStageStatusWaitingDependency, PipelineStageStatusWaitingQuietWindow,
		PipelineStageStatusReady, PipelineStageStatusProcessing, PipelineStageStatusComplete,
		PipelineStageStatusCompleteWithWarning, PipelineStageStatusSkippedNotApplicable,
		PipelineStageStatusSkippedByPolicy, PipelineStageStatusFailedRetryable, PipelineStageStatusBlockedManual,
		PipelineStageStatusStale, PipelineStageStatusCancelled:
		return true
	default:
		return false
	}
}

func isPipelineStageUnitStatus(value string) bool {
	switch value {
	case PipelineStageStatusPlanned, PipelineStageStatusReady, PipelineStageStatusProcessing,
		PipelineStageStatusComplete, PipelineStageStatusCompleteWithWarning,
		PipelineStageStatusSkippedNotApplicable, PipelineStageStatusFailedRetryable,
		PipelineStageStatusBlockedManual, PipelineStageStatusStale, PipelineStageStatusCancelled:
		return true
	default:
		return false
	}
}

func isArtifactKind(value string) bool {
	switch value {
	case ArtifactKindMetadataText, ArtifactKindEmbeddedText, ArtifactKindStructuredText,
		ArtifactKindOCRText, ArtifactKindVisionDescription, ArtifactKindConsolidatedText:
		return true
	default:
		return false
	}
}

func isArtifactState(value string) bool {
	return value == ArtifactStateActive || value == ArtifactStateHistorical || value == ArtifactStateReusable || value == ArtifactStateStale
}

func validateID(prefix, value, field string) error {
	if strings.TrimSpace(value) == "" {
		return invalid("%s is required", field)
	}
	if err := ids.Validate(prefix, value); err != nil {
		return invalid("%s is invalid: %v", field, err)
	}
	return nil
}

func validateOptionalID(prefix string, value *string, field string) error {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	if err := ids.Validate(prefix, *value); err != nil {
		return invalid("%s is invalid: %v", field, err)
	}
	return nil
}

func validateOptionalHash(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if !sha256Pattern.MatchString(value) {
		return invalid("%s must be a sha256 content address", field)
	}
	return nil
}

func validateJSONObject(field string, raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return invalid("%s is required", field)
	}

	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return invalid("%s must be valid JSON: %v", field, err)
	}
	if _, ok := decoded.(map[string]any); !ok {
		return invalid("%s must be a JSON object", field)
	}
	return nil
}

func validateJSONArray(field string, raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return invalid("%s is required", field)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return invalid("%s must be valid JSON: %v", field, err)
	}
	if _, ok := decoded.([]any); !ok {
		return invalid("%s must be a JSON array", field)
	}
	return nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}
