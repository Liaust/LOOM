package portal

import (
	"fmt"
	"strings"

	"loom.local/loom/internal/knowledge"
)

func notesSelectableItems(state ScreenState) []SelectableItem {
	roots := notesVisibleRoots(state.Data.Notes.Overview, state.RawDetails)
	results := state.Data.Notes.Search.ResultSet.Results
	pipelines := state.Data.Notes.Pipelines.Current
	items := make([]SelectableItem, 0, len(pipelines)+len(roots)+len(results))
	for idx, pipeline := range pipelines {
		action := NewNotesPipelineInspectAction(pipeline)
		items = append(items, SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         "Pipeline " + firstNonEmpty(pipeline.KnowledgeObjectID, pipeline.KnowledgePipelineRunID),
			Description:   firstNonEmpty(pipeline.CurrentStageKey, pipeline.Status),
			Screen:        ScreenNotes,
			RowIndex:      idx,
			RecordKind:    "notes_pipeline",
			RecordRef:     pipeline.KnowledgePipelineRunID,
			RecordLabel:   firstNonEmpty(pipeline.KnowledgeObjectID, pipeline.KnowledgePipelineRunID),
			PrimaryAction: actionPtr(action),
		})
	}
	for idx, root := range roots {
		action := NewNotesRootInspectAction(root)
		items = append(items, SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         notesRootLabel(root),
			Description:   notesRootDescription(root),
			Screen:        ScreenNotes,
			RowIndex:      len(pipelines) + idx,
			RecordKind:    "notes_root",
			RecordRef:     firstNonEmpty(root.NotesSourceRootID, root.BackendRootKey, root.SourcePath),
			RecordLabel:   notesRootLabel(root),
			PrimaryAction: actionPtr(action),
		})
	}
	for idx, result := range results {
		action := NewNotesSearchResultInspectAction(result)
		items = append(items, SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         notesSearchResultTitle(result),
			Description:   notesSearchResultDescription(result),
			Screen:        ScreenNotes,
			RowIndex:      len(pipelines) + len(roots) + idx,
			RecordKind:    "notes_search_result",
			RecordRef:     firstNonEmpty(result.KnowledgeChunkID, result.SearchDocumentID, result.KnowledgeObjectID),
			RecordLabel:   notesSearchResultTitle(result),
			PrimaryAction: actionPtr(action),
		})
	}
	return withOperationalActionGroups(state, items)
}

func NewNotesPipelineWorkerAction(workerKey, label string) PortalAction {
	action := PortalAction{
		ID:           "notes.pipeline.process." + safeActionID(workerKey),
		Label:        label,
		Description:  "Run one bounded unified Notes pipeline worker pass.",
		Domain:       "notes",
		SourceScreen: ScreenNotes,
		TargetKind:   "worker",
		TargetRef:    workerKey,
		TargetLabel:  label,
		Risk:         ActionRiskSafeRun,
		State:        ActionAvailable,
		Executor: PortalActionExecutor{
			Kind:    PortalExecutorWorkerRunOnce,
			Target:  workerKey,
			Payload: map[string]string{"worker_key": workerKey},
		},
		RawCommand:    []string{"loom", "notes", "run", strings.TrimPrefix(workerKey, "main.knowledge_"), "--once"},
		RefreshScreen: ScreenNotes,
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewNotesPipelineStatusAction(status knowledge.PipelineOverallStatus) PortalAction {
	action := NewPortalRecordInspectAction("notes", ScreenNotes, "notes_pipeline_status", "unified", "Unified Notes pipelines", map[string]string{
		"active":                fmt.Sprintf("%d", len(status.Current)),
		"waiting_heavy":         fmt.Sprintf("%d", status.Operations.WaitingHeavy),
		"blocked":               fmt.Sprintf("%d", status.Operations.Blocked),
		"lexical_documents":     fmt.Sprintf("%d", status.Operations.LexicalDocuments),
		"active_vectors":        fmt.Sprintf("%d", status.Operations.ActiveVectors),
		"stale_claims":          fmt.Sprintf("%d", status.Operations.StaleClaims),
		"activation_mismatches": fmt.Sprintf("%d", status.Operations.ActivationMismatches),
	})
	action.ID = "notes.pipeline.inspect.status"
	action.Label = "Inspect Pipeline Status"
	action.Description = "Inspect unified pipeline health and bounded diagnostics."
	action.RawCommand = []string{"loom", "notes", "pipelines", "status"}
	return action
}

func NewNotesPipelineInspectAction(run knowledge.PipelineRun) PortalAction {
	action := NewPortalRecordInspectAction("notes", ScreenNotes, "notes_pipeline", run.KnowledgePipelineRunID, firstNonEmpty(run.KnowledgeObjectID, run.KnowledgePipelineRunID), map[string]string{
		"pipeline_run_id": run.KnowledgePipelineRunID,
		"status":          run.Status,
		"stage":           run.CurrentStageKey,
		"execution_class": run.CurrentExecutionClass,
		"generation":      fmt.Sprintf("%d", run.Generation),
	})
	action.ID = "notes.pipeline.inspect." + safeActionID(run.KnowledgePipelineRunID)
	action.Label = "Inspect Pipeline"
	action.Description = "Inspect stage progress and bounded pipeline metadata."
	action.RawCommand = []string{"loom", "notes", "pipelines", "inspect", run.KnowledgePipelineRunID}
	return action
}

func NewNotesPipelinePolicyInspectAction(status knowledge.PipelinePolicyStatus) PortalAction {
	action := NewPortalRecordInspectAction("notes", ScreenNotes, "notes_pipeline_policy", "unified", "Notes pipeline policy", map[string]string{
		"pdf_ocr_enabled":            fmt.Sprintf("%t", status.Policy.PDFOCREnabled),
		"image_descriptions_enabled": fmt.Sprintf("%t", status.Policy.ImageDescriptionsEnabled),
		"embeddings_enabled":         fmt.Sprintf("%t", status.Policy.EmbeddingsEnabled),
	})
	action.ID = "notes.pipeline.policy.inspect"
	action.Label = "Inspect Pipeline Policies"
	action.Description = "Inspect OCR, image-description, and embedding policy."
	action.RawCommand = []string{"loom", "notes", "pipelines", "policy", "status"}
	return action
}

func NewNotesPipelineFailuresAction(blocked int) PortalAction {
	action := NewPortalRecordInspectAction("notes", ScreenNotes, "notes_pipeline_failures", "unified", "Notes pipeline failures", map[string]string{
		"blocked": fmt.Sprintf("%d", blocked),
	})
	action.ID = "notes.pipeline.repair.failures"
	action.Label = "Inspect Pipeline Failures"
	action.Description = "Inspect failed or blocked pipelines before selecting a retry."
	action.RawCommand = []string{"loom", "notes", "pipelines", "failures"}
	return action
}

func NewNotesEmbeddingsToggleAction(status knowledge.EmbeddingStatus) PortalAction {
	enable := !status.Settings.Enabled
	stateLabel := "enable"
	label := "Enable Embeddings"
	description := "Enable semantic embeddings for all eligible notes."
	if !enable {
		stateLabel = "disable"
		label = "Disable Embeddings"
		description = "Disable semantic embedding work for notes."
	}
	action := PortalAction{
		ID:           "notes.embeddings." + stateLabel,
		Label:        label,
		Description:  description,
		Domain:       "notes",
		SourceScreen: ScreenNotes,
		TargetKind:   "notes_embeddings",
		TargetRef:    knowledge.EmbeddingSettingsID,
		TargetLabel:  "Notes embeddings",
		Risk:         ActionRiskSensitive,
		State:        ActionAvailable,
		Executor: PortalActionExecutor{
			Kind:    PortalExecutorNotesEmbeddingsToggle,
			Target:  knowledge.EmbeddingSettingsID,
			Payload: map[string]string{"enabled": fmt.Sprintf("%t", enable)},
		},
		RawCommand:    []string{"loom", "notes", "embeddings", stateLabel, "--yes"},
		RawDetails:    notesEmbeddingsActionPayload(status),
		RefreshScreen: ScreenNotes,
	}
	action.ConfirmationPolicy = ConfirmationPolicy{
		Required: true,
		Strength: ConfirmationStrengthNormal,
		Prompt:   "Confirm changing the global notes embedding state.",
	}
	return action
}

func NewNotesRootInspectAction(root knowledge.NotesRootOverview) PortalAction {
	ref := firstNonEmpty(root.NotesSourceRootID, root.BackendRootKey, root.SourcePath)
	action := NewPortalRecordInspectAction("notes", ScreenNotes, "notes_root", ref, notesRootLabel(root), notesRootPayload(root))
	action.Label = "Inspect Notes Root"
	action.Description = "Inspect this notes source root."
	action.RawCommand = []string{"loom", "notes", "overview"}
	return action
}

func notesEmbeddingsActionPayload(status knowledge.EmbeddingStatus) map[string]string {
	return map[string]string{
		"enabled":          fmt.Sprintf("%t", status.Settings.Enabled),
		"runtime":          status.Settings.RuntimeKey,
		"model":            status.Settings.ModelKey,
		"dimensions":       fmt.Sprintf("%d", status.Settings.Dimensions),
		"queued":           fmt.Sprintf("%d", status.Queue.Queued),
		"ready":            fmt.Sprintf("%d", status.Queue.Ready),
		"processing":       fmt.Sprintf("%d", status.Queue.Processing),
		"active_vectors":   fmt.Sprintf("%d", status.ActiveVectors),
		"historical":       fmt.Sprintf("%d", status.Historical),
		"reusable_vectors": fmt.Sprintf("%d", status.ReusableVectors),
		"generated_at":     timeOrDash(status.GeneratedAt),
	}
}

func NewNotesSearchResultInspectAction(result knowledge.NotesSearchResult) PortalAction {
	ref := firstNonEmpty(result.KnowledgeChunkID, result.SearchDocumentID, result.KnowledgeObjectID)
	action := NewPortalRecordInspectAction("notes", ScreenNotes, "notes_search_result", ref, notesSearchResultTitle(result), notesSearchResultPayload(result))
	action.Label = "Inspect Note Result"
	action.Description = "Inspect this notes search result."
	action.RawCommand = []string{"loom", "notes", "search", result.Citation.Label}
	if result.KnowledgeObjectID != "" {
		action.RawCommand = []string{"loom", "notes", "objects", "show", result.KnowledgeObjectID}
	}
	if result.SourceLifecycle != "" {
		action.RawCommand = append(action.RawCommand, "--source-lifecycle", string(result.SourceLifecycle))
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This search result does not include an inspectable reference."
	}
	return action
}

func notesVisibleRoots(overview knowledge.NotesOverview, raw bool) []knowledge.NotesRootOverview {
	roots := []knowledge.NotesRootOverview{}
	for _, node := range overview.Nodes {
		for _, root := range node.Roots {
			if !raw && !notesRootIsActive(root) && root.Totals.LifecycleCounts.Active == 0 {
				continue
			}
			roots = append(roots, root)
		}
	}
	return roots
}

func notesRootIsActive(root knowledge.NotesRootOverview) bool {
	return root.Status == "" || root.Status == knowledge.SourceRootStatusActive
}

func notesRootLabel(root knowledge.NotesRootOverview) string {
	if root.RootKind == knowledge.RootKindProjectNotes {
		project := firstNonEmpty(root.ProjectSlug, root.ProjectName, root.ProjectID)
		if project != "" {
			return project + " / notes"
		}
	}
	return firstNonEmpty(root.DisplayName, titleFromToken(root.RootKind), root.BackendRootKey, root.NotesSourceRootID)
}

func notesRootDescription(root knowledge.NotesRootOverview) string {
	return fmt.Sprintf("%s files=%d objects=%d search=%d", firstNonEmpty(root.Status, "-"), root.Totals.FileCount, root.Totals.ObjectCount, root.Totals.SearchDocumentCount)
}

func notesRootPayload(root knowledge.NotesRootOverview) map[string]string {
	extractionParts := []string{}
	for _, state := range root.ExtractionStates {
		extractionParts = append(extractionParts, fmt.Sprintf("%s=%d", state.ExtractionStatus, state.Count))
	}
	return map[string]string{
		"notes_source_root_id": root.NotesSourceRootID,
		"root_kind":            root.RootKind,
		"node":                 firstNonEmpty(root.NodeKey, root.NodeID),
		"project":              firstNonEmpty(root.ProjectSlug, root.ProjectName, root.ProjectID),
		"backend_root_key":     root.BackendRootKey,
		"source_path":          root.SourcePath,
		"root_relative_path":   root.RootRelativePath,
		"status":               root.Status,
		"files":                fmt.Sprintf("%d", root.Totals.FileCount),
		"objects":              fmt.Sprintf("%d", root.Totals.ObjectCount),
		"search_documents":     fmt.Sprintf("%d", root.Totals.SearchDocumentCount),
		"extraction_states":    strings.Join(extractionParts, " "),
	}
}

func notesSearchResultTitle(result knowledge.NotesSearchResult) string {
	return firstNonEmpty(result.Title, result.RelativePath, result.Citation.Label, result.KnowledgeObjectID, result.SearchDocumentID)
}

func notesSearchResultDescription(result knowledge.NotesSearchResult) string {
	return fmt.Sprintf("%s %s %s", firstNonEmpty(result.SourceNodeKey, "-"), firstNonEmpty(result.FileClass, "-"), firstNonEmpty(result.RelativePath, "-"))
}

func notesSearchResultPayload(result knowledge.NotesSearchResult) map[string]string {
	return map[string]string{
		"source_lifecycle":             string(result.SourceLifecycle),
		"original_path":                result.OriginalPath,
		"canonical_path":               result.CanonicalPath,
		"archive_operation_id":         result.ArchiveOperationID,
		"archived_at":                  timePtrOrDash(result.ArchivedAt),
		"workspace_kind":               result.WorkspaceKind,
		"workspace_object_id":          result.WorkspaceObjectID,
		"workspace_lifecycle_event_id": result.WorkspaceLifecycleEventID,
		"search_document_id":           result.SearchDocumentID,
		"knowledge_object_id":          result.KnowledgeObjectID,
		"knowledge_object_version_id":  result.KnowledgeObjectVersionID,
		"knowledge_chunk_id":           result.KnowledgeChunkID,
		"notes_source_root_id":         result.NotesSourceRootID,
		"root_kind":                    result.RootKind,
		"node":                         result.SourceNodeKey,
		"project":                      result.ProjectID,
		"relative_path":                result.RelativePath,
		"source_path":                  result.SourcePath,
		"title":                        result.Title,
		"file_class":                   result.FileClass,
		"text_source":                  notesSearchResultSourceLabel(result),
		"extraction_status":            notesSearchResultExtractionLabel(result),
		"metadata_only":                fmt.Sprintf("%t", result.MetadataOnly),
		"structural_path":              result.StructuralPath,
		"citation_label":               result.Citation.Label,
		"citation_source_ref":          result.Citation.SourceRef,
		"source_kind":                  result.SourceKind,
		"rank_score":                   fmt.Sprintf("%.4f", result.RankScore),
		"bm25_score":                   fmt.Sprintf("%.4f", result.BM25Score),
		"fts_score":                    fmt.Sprintf("%.4f", result.FTSScore),
		"boost_score":                  fmt.Sprintf("%.4f", result.BoostScore),
		"final_score":                  fmt.Sprintf("%.4f", result.FinalScore),
		"match_reasons":                strings.Join(result.MatchReasons, ","),
		"source_created_at":            timePtrOrDash(result.SourceCreatedAt),
		"source_modified_at":           timePtrOrDash(result.SourceModifiedAt),
		"recency_at":                   timeOrDash(result.RecencyAt),
		"recency_basis":                result.RecencyBasis,
		"recency_score":                fmt.Sprintf("%.4f", result.RecencyScore),
		"recency_rank":                 fmt.Sprintf("%d", result.RecencyRank),
		"observed_at":                  timeOrDash(result.ObservedAt),
		"indexed_at":                   timeOrDash(result.IndexedAt),
	}
}
