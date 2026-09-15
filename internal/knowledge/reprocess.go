package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/storagecatalog"
)

const (
	ReprocessScopeObject    = "object"
	ReprocessScopeRoot      = "root"
	ReprocessScopeProject   = "project"
	ReprocessScopeNode      = "node"
	ReprocessScopeFileClass = "file_class"
	ReprocessScopeStale     = "stale"

	defaultKnowledgeReprocessLimit = 200
	maxKnowledgeReprocessLimit     = 5000
)

type ReprocessInput struct {
	Scope       string `json:"scope,omitempty"`
	ObjectRef   string `json:"object_ref,omitempty"`
	RootRef     string `json:"root_ref,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	ProjectRef  string `json:"project_ref,omitempty"`
	NodeRef     string `json:"node_ref,omitempty"`
	FileClass   string `json:"file_class,omitempty"`
	PipelineKey string `json:"pipeline_key,omitempty"`
	StaleOnly   bool   `json:"stale_only,omitempty"`
	Force       bool   `json:"force,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	Priority    int    `json:"priority,omitempty"`
}

type ReprocessResult struct {
	Scope       string          `json:"scope"`
	PipelineKey string          `json:"pipeline_key"`
	Queued      int             `json:"queued"`
	Skipped     int             `json:"skipped"`
	Items       []ReprocessItem `json:"items"`
}

type ReprocessItem struct {
	KnowledgeObjectID       string         `json:"knowledge_object_id"`
	NotesSourceRootID       string         `json:"notes_source_root_id"`
	SourceNodeKey           string         `json:"source_node_key,omitempty"`
	ProjectID               string         `json:"project_id,omitempty"`
	FileClass               string         `json:"file_class"`
	RelativePath            string         `json:"relative_path"`
	ProcessingState         string         `json:"processing_state"`
	KnowledgePipelineStatus PipelineStatus `json:"knowledge_pipeline_status"`
	PipelineRun             PipelineRun    `json:"pipeline_run"`
	PreviousProcessingState string         `json:"previous_processing_state,omitempty"`
	PreviousExtractionState string         `json:"previous_extraction_state,omitempty"`
	PreviousPipelineKey     string         `json:"previous_pipeline_key,omitempty"`
	PreviousPipelineVersion string         `json:"previous_pipeline_version,omitempty"`
	SkippedReason           string         `json:"skipped_reason,omitempty"`
}

func (s *Service) QueueReprocess(ctx context.Context, input ReprocessInput) (ReprocessResult, error) {
	if s == nil || s.store.db == nil {
		return ReprocessResult{}, fmt.Errorf("knowledge store is not configured")
	}
	input, err := s.resolveReprocessInput(ctx, input)
	if err != nil {
		return ReprocessResult{}, err
	}
	objects, err := s.listReprocessCandidates(ctx, input)
	if err != nil {
		return ReprocessResult{}, err
	}
	result := ReprocessResult{
		Scope:       input.Scope,
		PipelineKey: input.PipelineKey,
		Items:       []ReprocessItem{},
	}
	if len(objects) == 0 {
		return result, nil
	}
	policy, err := s.GetPipelinePolicy(ctx)
	if err != nil {
		return ReprocessResult{}, err
	}

	for _, object := range objects {
		previousState := object.ProcessingState
		previousKey := object.PipelineKey
		previousVersion := object.PipelineVersion
		run, created, err := s.ensurePipelineRun(ctx, object, policy.Policy, input.Force, input.Priority)
		if err != nil {
			return result, err
		}
		if created {
			result.Queued++
		} else {
			result.Skipped++
		}
		item := reprocessItemForObject(object, PipelineStatus{}, "")
		item.PipelineRun = run
		if !created {
			item.SkippedReason = "current_pipeline_already_exists"
		}
		item.PreviousProcessingState = previousState
		item.PreviousPipelineKey = previousKey
		item.PreviousPipelineVersion = previousVersion
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func NormalizeReprocessInput(input ReprocessInput) (ReprocessInput, error) {
	input.Scope = strings.TrimSpace(input.Scope)
	input.ObjectRef = strings.TrimSpace(input.ObjectRef)
	input.RootRef = strings.TrimSpace(input.RootRef)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.ProjectRef = strings.TrimSpace(input.ProjectRef)
	input.NodeRef = strings.TrimSpace(input.NodeRef)
	input.FileClass = strings.TrimSpace(input.FileClass)
	input.PipelineKey = strings.TrimSpace(input.PipelineKey)
	if input.PipelineKey == "" || input.PipelineKey == KnowledgeObjectPipelineMarkdownText {
		input.PipelineKey = KnowledgeObjectPipelineNotesFileExtraction
	}
	if input.PipelineKey != KnowledgeObjectPipelineNotesFileExtraction {
		return ReprocessInput{}, fmt.Errorf("%w: pipeline %q is not supported for notes reprocessing", ErrInvalid, input.PipelineKey)
	}
	if input.Scope == "" {
		input.Scope = inferReprocessScope(input)
	}
	switch input.Scope {
	case ReprocessScopeObject:
		if input.ObjectRef == "" {
			return ReprocessInput{}, fmt.Errorf("%w: object_ref is required", ErrInvalid)
		}
	case ReprocessScopeRoot:
		if input.RootRef == "" {
			return ReprocessInput{}, fmt.Errorf("%w: root_ref is required", ErrInvalid)
		}
	case ReprocessScopeProject:
		if input.ProjectID == "" && input.ProjectRef == "" {
			return ReprocessInput{}, fmt.Errorf("%w: project_ref or project_id is required", ErrInvalid)
		}
	case ReprocessScopeNode:
		if input.NodeRef == "" {
			return ReprocessInput{}, fmt.Errorf("%w: node_ref is required", ErrInvalid)
		}
	case ReprocessScopeFileClass:
		if input.FileClass == "" {
			return ReprocessInput{}, fmt.Errorf("%w: file_class is required", ErrInvalid)
		}
	case ReprocessScopeStale:
		input.StaleOnly = true
	default:
		return ReprocessInput{}, fmt.Errorf("%w: reprocess scope %q is not supported", ErrInvalid, input.Scope)
	}
	if input.FileClass != "" && !isReprocessableFileClass(input.FileClass) {
		return ReprocessInput{}, fmt.Errorf("%w: file_class %q is not supported by notes reprocessing", ErrInvalid, input.FileClass)
	}
	if input.Limit <= 0 {
		input.Limit = defaultKnowledgeReprocessLimit
	}
	if input.Limit > maxKnowledgeReprocessLimit {
		input.Limit = maxKnowledgeReprocessLimit
	}
	if input.Priority < 0 {
		return ReprocessInput{}, fmt.Errorf("%w: priority must be non-negative", ErrInvalid)
	}
	if input.Priority == 0 {
		input.Priority = 100
	}
	return input, nil
}

func (s *Service) resolveReprocessInput(ctx context.Context, input ReprocessInput) (ReprocessInput, error) {
	input, err := NormalizeReprocessInput(input)
	if err != nil {
		return ReprocessInput{}, err
	}
	if input.Scope == ReprocessScopeRoot {
		root, err := s.store.ResolveSourceRootRef(ctx, input.RootRef)
		if err != nil {
			return ReprocessInput{}, fmt.Errorf("resolve notes source root: %w", err)
		}
		input.RootRef = root.NotesSourceRootID
	}
	if input.ProjectID == "" && input.ProjectRef != "" {
		project, err := projects.NewService(s.store.db).ResolveProjectRef(ctx, input.ProjectRef)
		if err != nil {
			return ReprocessInput{}, fmt.Errorf("resolve project: %w", err)
		}
		input.ProjectID = project.ProjectID
	}
	return input, nil
}

func inferReprocessScope(input ReprocessInput) string {
	switch {
	case input.ObjectRef != "":
		return ReprocessScopeObject
	case input.RootRef != "":
		return ReprocessScopeRoot
	case input.ProjectID != "" || input.ProjectRef != "":
		return ReprocessScopeProject
	case input.NodeRef != "":
		return ReprocessScopeNode
	case input.FileClass != "":
		return ReprocessScopeFileClass
	case input.StaleOnly:
		return ReprocessScopeStale
	default:
		return ReprocessScopeStale
	}
}

func (s *Service) listReprocessCandidates(ctx context.Context, input ReprocessInput) ([]KnowledgeObject, error) {
	clauses := []string{
		"o.deleted_at IS NULL",
		"o.file_class IN ('markdown', 'text', 'code', 'pdf', 'office_document', 'image')",
		visibleNotesKnowledgeRelativePathSQL("o.relative_path"),
	}
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(condition, len(args)))
	}
	switch input.Scope {
	case ReprocessScopeObject:
		object, err := s.store.GetKnowledgeObject(ctx, input.ObjectRef)
		if err != nil {
			return nil, err
		}
		add("o.knowledge_object_id = $%d", object.KnowledgeObjectID)
	case ReprocessScopeRoot:
		add("o.notes_source_root_id = $%d", input.RootRef)
	case ReprocessScopeProject:
		add("o.project_id = $%d", input.ProjectID)
	case ReprocessScopeNode:
		args = append(args, input.NodeRef)
		n := len(args)
		clauses = append(clauses, fmt.Sprintf("(o.source_node_key = $%d OR o.source_node_id = $%d)", n, n))
	case ReprocessScopeFileClass:
		add("o.file_class = $%d", input.FileClass)
	case ReprocessScopeStale:
		// No scope-specific predicate; stale filtering below applies globally.
	}
	if input.FileClass != "" && input.Scope != ReprocessScopeFileClass {
		add("o.file_class = $%d", input.FileClass)
	}
	if input.StaleOnly {
		args = append(args, input.PipelineKey, KnowledgeFileExtractionPipelineVersion)
		pipelineArg := len(args) - 1
		versionArg := len(args)
		clauses = append(clauses, fmt.Sprintf(`(
			o.pipeline_key <> $%d
			OR o.pipeline_version <> $%d
			OR o.processing_state IN ('metadata_only', 'stale', 'failed')
			OR EXISTS (
				SELECT 1
				FROM knowledge.pipeline_statuses ps
				WHERE ps.knowledge_object_id = o.knowledge_object_id
				  AND ps.pipeline_key = $%d
				  AND (
					ps.pipeline_version <> $%d
					OR ps.status IN ('queued', 'stale', 'failed')
				  )
			)
		)`, pipelineArg, versionArg, pipelineArg, versionArg))
	}
	args = append(args, input.Limit)
	query := `SELECT ` + knowledgeObjectColumns() + `
		FROM knowledge.knowledge_objects o
		WHERE ` + strings.Join(clauses, " AND ") + fmt.Sprintf(`
		ORDER BY o.updated_at ASC, o.relative_path ASC, o.knowledge_object_id ASC
		LIMIT $%d`, len(args))
	rows, err := s.store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := []KnowledgeObject{}
	for rows.Next() {
		object, err := scanKnowledgeObject(rows)
		if err != nil {
			return nil, err
		}
		objects = append(objects, object)
	}
	return objects, rows.Err()
}

func isReprocessableFileClass(fileClass string) bool {
	switch strings.TrimSpace(fileClass) {
	case storagecatalog.FileClassMarkdown,
		storagecatalog.FileClassText,
		storagecatalog.FileClassCode,
		storagecatalog.FileClassPDF,
		storagecatalog.FileClassOfficeDocument,
		storagecatalog.FileClassImage:
		return true
	default:
		return false
	}
}

func reprocessItemForObject(object KnowledgeObject, status PipelineStatus, skippedReason string) ReprocessItem {
	projectID := ""
	if object.ProjectID != nil {
		projectID = *object.ProjectID
	}
	return ReprocessItem{
		KnowledgeObjectID:       object.KnowledgeObjectID,
		NotesSourceRootID:       object.NotesSourceRootID,
		SourceNodeKey:           object.SourceNodeKey,
		ProjectID:               projectID,
		FileClass:               object.FileClass,
		RelativePath:            object.RelativePath,
		ProcessingState:         object.ProcessingState,
		KnowledgePipelineStatus: status,
		PreviousExtractionState: KnowledgeObjectExtractionStatus(object),
		SkippedReason:           skippedReason,
	}
}

func reprocessStatusMetadata(object KnowledgeObject, input ReprocessInput) json.RawMessage {
	payload, err := json.Marshal(map[string]any{
		"schema_version":    "knowledge.reprocess_queue.v0.8",
		"scope":             input.Scope,
		"pipeline_key":      input.PipelineKey,
		"pipeline_version":  KnowledgeFileExtractionPipelineVersion,
		"file_class":        object.FileClass,
		"relative_path":     object.RelativePath,
		"notes_source_root": object.NotesSourceRootID,
		"source_node_key":   object.SourceNodeKey,
		"force":             input.Force,
	})
	if err != nil {
		return emptyJSONObject
	}
	return payload
}
