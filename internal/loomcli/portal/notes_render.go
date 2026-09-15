package portal

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/loomcli/ui"
)

func renderNotes(builder *strings.Builder, state ScreenState) {
	overview := state.Data.Notes.Overview
	roots := notesVisibleRoots(overview, state.RawDetails)
	summary := notesVisibleSummary(roots)
	records := ScreenRecordItems(state)
	row := renderTopAvailableActions(builder, state, records)
	renderOperationalActionGroups(builder, state, records, &row)

	renderNotesSearchAffordance(builder, state.Data.Notes.Search)
	renderNotesPipelineStatus(builder, state.Data.Notes.Pipelines, state.Data.Notes.PipelinesAvailable, state.RawDetails)
	renderNotesEmbeddingStatus(builder, state.Data.Notes.Embeddings, state.Data.Notes.EmbeddingsAvailable, state.RawDetails)
	renderNotesSearchResults(builder, state, state.Data.Notes.Search, row+len(roots))
	renderNotesAttention(builder, summary.IndexHealth, summary.ExtractionStates, overview.Projection, state.RawDetails)

	renderSummarySection(builder, "Coverage")
	renderMetricLine(builder,
		fmt.Sprintf("roots=%d", summary.Totals.RootCount),
		fmt.Sprintf("active=%d", summary.Totals.ActiveRootCount),
		fmt.Sprintf("files=%d", summary.Totals.FileCount),
		fmt.Sprintf("directories=%d", summary.Totals.DirectoryCount),
		fmt.Sprintf("bytes=%s", storageFormatBytes(summary.Totals.SizeBytes)),
		fmt.Sprintf("search_documents=%d", summary.Totals.SearchDocumentCount),
	)
	if !overview.GeneratedAt.IsZero() {
		renderMetricLine(builder, "generated: "+timeOrDash(overview.GeneratedAt))
	}

	renderNotesFileTypes(builder, summary.FileClasses)
	row = renderNotesAcrossNetwork(builder, state, roots, row)
	renderNotesProcessingAndSearch(builder, summary.ProcessingStates, summary.ExtractionStates, summary.IndexHealth)
	renderNotesProjection(builder, overview.Projection)
}

func renderNotesPipelineStatus(builder *strings.Builder, status knowledge.PipelineOverallStatus, available, raw bool) {
	if !available {
		renderMetricLine(builder, "Pipelines: unavailable (read-only cached overview remains visible)")
		return
	}
	active := len(status.Current)
	parts := []string{fmt.Sprintf("active=%d", active), fmt.Sprintf("waiting_heavy=%d", status.Operations.WaitingHeavy), fmt.Sprintf("lexical=%d", status.Operations.LexicalDocuments), fmt.Sprintf("semantic=%d", status.Operations.ActiveVectors)}
	if status.Operations.Blocked > 0 {
		parts = append(parts, fmt.Sprintf("blocked=%d", status.Operations.Blocked))
	}
	renderMetricLine(builder, append([]string{"Pipelines"}, parts...)...)
	if active > 0 {
		run := status.Current[0]
		renderMetricLine(builder, "current="+firstNonEmpty(run.KnowledgeObjectID, "-"), "stage="+firstNonEmpty(run.CurrentStageKey, "-"), "waiting="+firstNonEmpty(run.Status, "-"))
	}
	renderMetricLine(builder, fmt.Sprintf("Policies: OCR=%t vision=%t embeddings=%t", status.Policy.Policy.PDFOCREnabled, status.Policy.Policy.ImageDescriptionsEnabled, status.Policy.Policy.EmbeddingsEnabled), fmt.Sprintf("heavy=%t capacity=%d active=%d", status.Operations.HeavyExecutorAvailable, status.Operations.HeavyCapacity, status.Operations.HeavyActiveLeases))
	if raw {
		missing := []string{}
		for tool, present := range status.Operations.Tools {
			if !present {
				missing = append(missing, tool)
			}
		}
		sort.Strings(missing)
		missingRuntimes := unavailablePipelineComponents(status.Operations.Runtimes)
		missingModels := unavailablePipelineComponents(status.Operations.Models)
		renderMetricLine(builder, fmt.Sprintf("stale_claims=%d", status.Operations.StaleClaims), fmt.Sprintf("activation_mismatches=%d", status.Operations.ActivationMismatches), fmt.Sprintf("oldest_active_seconds=%d", status.Operations.OldestActiveSeconds), "missing_tools="+strings.Join(missing, ","))
		renderMetricLine(builder, "missing_runtimes="+strings.Join(missingRuntimes, ","), "missing_models="+strings.Join(missingModels, ","))
	}
}

func unavailablePipelineComponents(values map[string]bool) []string {
	missing := []string{}
	for component, available := range values {
		if !available {
			missing = append(missing, component)
		}
	}
	sort.Strings(missing)
	return missing
}

type notesOverviewSummary struct {
	Totals           knowledge.NotesOverviewTotals
	FileClasses      []knowledge.NotesFileClassCount
	ProcessingStates []knowledge.NotesProcessingStateCount
	ExtractionStates []knowledge.NotesExtractionStateCount
	IndexHealth      knowledge.NotesIndexHealth
}

func notesVisibleSummary(roots []knowledge.NotesRootOverview) notesOverviewSummary {
	summary := notesOverviewSummary{}
	fileClasses := map[string]*knowledge.NotesFileClassCount{}
	processingStates := map[string]*knowledge.NotesProcessingStateCount{}
	extractionStates := map[string]*knowledge.NotesExtractionStateCount{}
	for _, root := range roots {
		notesAddTotals(&summary.Totals, root.Totals)
		if root.Totals.RootCount == 0 {
			summary.Totals.RootCount++
		}
		if notesRootIsActive(root) && root.Totals.ActiveRootCount == 0 {
			summary.Totals.ActiveRootCount++
		}
		for _, count := range root.FileClasses {
			notesAddFileClass(fileClasses, count)
		}
		for _, count := range root.ProcessingStates {
			notesAddProcessingState(processingStates, count)
		}
		for _, count := range root.ExtractionStates {
			notesAddExtractionState(extractionStates, count)
		}
		notesAddIndexHealth(&summary.IndexHealth, root.IndexHealth)
	}
	summary.FileClasses = notesFileClassCounts(fileClasses)
	summary.ProcessingStates = notesProcessingStateCounts(processingStates)
	summary.ExtractionStates = notesExtractionStateCounts(extractionStates)
	return summary
}

func notesAddTotals(target *knowledge.NotesOverviewTotals, add knowledge.NotesOverviewTotals) {
	target.RootCount += add.RootCount
	target.ActiveRootCount += add.ActiveRootCount
	target.ObjectCount += add.ObjectCount
	target.FileCount += add.FileCount
	target.DirectoryCount += add.DirectoryCount
	target.SizeBytes += add.SizeBytes
	target.SearchDocumentCount += add.SearchDocumentCount
}

func notesAddFileClass(target map[string]*knowledge.NotesFileClassCount, add knowledge.NotesFileClassCount) {
	if add.FileClass == "" || add.Count == 0 {
		return
	}
	if existing, ok := target[add.FileClass]; ok {
		existing.Count += add.Count
		existing.SizeBytes += add.SizeBytes
		return
	}
	count := add
	target[add.FileClass] = &count
}

func notesAddProcessingState(target map[string]*knowledge.NotesProcessingStateCount, add knowledge.NotesProcessingStateCount) {
	if add.ProcessingState == "" || add.Count == 0 {
		return
	}
	if existing, ok := target[add.ProcessingState]; ok {
		existing.Count += add.Count
		return
	}
	count := add
	target[add.ProcessingState] = &count
}

func notesAddExtractionState(target map[string]*knowledge.NotesExtractionStateCount, add knowledge.NotesExtractionStateCount) {
	if add.ExtractionStatus == "" || add.Count == 0 {
		return
	}
	if existing, ok := target[add.ExtractionStatus]; ok {
		existing.Count += add.Count
		return
	}
	count := add
	target[add.ExtractionStatus] = &count
}

func notesAddIndexHealth(target *knowledge.NotesIndexHealth, add knowledge.NotesIndexHealth) {
	target.SearchDocumentCount += add.SearchDocumentCount
	target.Queued += add.Queued
	target.Processing += add.Processing
	target.Complete += add.Complete
	target.Failed += add.Failed
	target.SkippedUnsupported += add.SkippedUnsupported
	target.LastIndexedAt = latestPortalTimePtr(target.LastIndexedAt, add.LastIndexedAt)
	target.LastPipelineUpdateAt = latestPortalTimePtr(target.LastPipelineUpdateAt, add.LastPipelineUpdateAt)
	target.LastFailureAt = latestPortalTimePtr(target.LastFailureAt, add.LastFailureAt)
}

func notesFileClassCounts(input map[string]*knowledge.NotesFileClassCount) []knowledge.NotesFileClassCount {
	out := make([]knowledge.NotesFileClassCount, 0, len(input))
	for _, value := range input {
		out = append(out, *value)
	}
	sort.Slice(out, func(i, j int) bool {
		return notesFileClassRank(out[i].FileClass) < notesFileClassRank(out[j].FileClass)
	})
	return out
}

func notesProcessingStateCounts(input map[string]*knowledge.NotesProcessingStateCount) []knowledge.NotesProcessingStateCount {
	out := make([]knowledge.NotesProcessingStateCount, 0, len(input))
	for _, value := range input {
		out = append(out, *value)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ProcessingState < out[j].ProcessingState
	})
	return out
}

func notesExtractionStateCounts(input map[string]*knowledge.NotesExtractionStateCount) []knowledge.NotesExtractionStateCount {
	out := make([]knowledge.NotesExtractionStateCount, 0, len(input))
	for _, value := range input {
		out = append(out, *value)
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := notesExtractionStateRank(out[i].ExtractionStatus), notesExtractionStateRank(out[j].ExtractionStatus)
		if left == right {
			return out[i].ExtractionStatus < out[j].ExtractionStatus
		}
		return left < right
	})
	return out
}

func renderNotesFileTypes(builder *strings.Builder, classes []knowledge.NotesFileClassCount) {
	renderSummarySection(builder, "File Types")
	if len(classes) == 0 {
		renderEmpty(builder, "No indexed note files.")
		return
	}
	for _, class := range classes {
		renderMetricLine(builder, fmt.Sprintf("%s=%d (%s)", class.FileClass, class.Count, storageFormatBytes(class.SizeBytes)))
	}
}

func renderNotesSearchAffordance(builder *strings.Builder, search NotesSearchData) {
	label := "Search notes..."
	if strings.TrimSpace(search.Query) != "" {
		label = fmt.Sprintf("Search notes... last=%q results=%d", search.Query, search.ResultSet.ResultCount)
	}
	renderMetricLine(builder, "s notes search", label)
}

func renderNotesEmbeddingStatus(builder *strings.Builder, status knowledge.EmbeddingStatus, available bool, raw bool) {
	if !available {
		renderMetricLine(builder, "Embeddings: unavailable")
		return
	}
	state := "OFF"
	if status.Settings.Enabled {
		state = "ON"
	}
	renderMetricLine(builder,
		"Embeddings: "+state,
		"runtime="+firstNonEmpty(status.Settings.RuntimeKey, "-"),
		"model="+firstNonEmpty(status.Settings.ModelKey, "-"),
		fmt.Sprintf("queued=%d", status.Queue.Queued),
		fmt.Sprintf("ready=%d", status.Queue.Ready),
		fmt.Sprintf("processing=%d", status.Queue.Processing),
		fmt.Sprintf("active=%d", status.ActiveVectors),
		fmt.Sprintf("failed=%d", status.Queue.Failed+status.Objects.Failed),
	)
	if raw {
		renderMetricLine(builder,
			fmt.Sprintf("dimensions=%d", status.Settings.Dimensions),
			fmt.Sprintf("historical=%d", status.Historical),
			fmt.Sprintf("reusable=%d", status.ReusableVectors),
			fmt.Sprintf("objects_queued=%d", status.Objects.Queued),
			fmt.Sprintf("objects_processing=%d", status.Objects.Processing),
			fmt.Sprintf("objects_complete=%d", status.Objects.Complete),
			fmt.Sprintf("objects_failed=%d", status.Objects.Failed),
			"generated="+timeOrDash(status.GeneratedAt),
		)
	}
}

func renderNotesAttention(builder *strings.Builder, health knowledge.NotesIndexHealth, extractionStates []knowledge.NotesExtractionStateCount, projection knowledge.NotesProjectionOverview, rawDetails bool) {
	lines := []string{}
	if health.Failed > 0 {
		lines = append(lines, fmt.Sprintf("index failed=%d", health.Failed))
	}
	extractionAttention := notesExtractionAttentionParts(extractionStates)
	if len(extractionAttention) > 0 {
		lines = append(lines, "extraction "+strings.Join(extractionAttention, " "))
	}
	if health.Queued > 0 || health.Processing > 0 {
		lines = append(lines, fmt.Sprintf("index queued=%d processing=%d", health.Queued, health.Processing))
	}
	if projection.FindingCount > 0 || projection.Missing > 0 {
		lines = append(lines, fmt.Sprintf("projection findings=%d missing=%d", projection.FindingCount, projection.Missing))
	}
	if !projection.Exists && projection.ProjectionRoot != "" {
		lines = append(lines, "projection root is missing")
	}
	if len(lines) == 0 {
		return
	}
	renderAttentionSection(builder, "Notes Attention")
	renderEmpty(builder, fmt.Sprintf("Doctor owns %d notes issue(s). Open Doctor or search #doctor notes for full triage.", len(lines)))
	if rawDetails {
		for _, line := range lines {
			renderEmpty(builder, line)
		}
	} else {
		renderEmpty(builder, "Press tab for local notes indexing and projection counters.")
	}
}

func renderNotesProcessingAndSearch(builder *strings.Builder, states []knowledge.NotesProcessingStateCount, extractionStates []knowledge.NotesExtractionStateCount, health knowledge.NotesIndexHealth) {
	renderDetailsSection(builder, "Processing And Search Health")
	stateParts := make([]string, 0, len(states))
	for _, state := range states {
		stateParts = append(stateParts, fmt.Sprintf("%s=%d", state.ProcessingState, state.Count))
	}
	if len(stateParts) == 0 {
		stateParts = append(stateParts, "states=0")
	}
	renderMetricLine(builder, stateParts...)
	extractionParts := make([]string, 0, len(extractionStates))
	for _, state := range extractionStates {
		extractionParts = append(extractionParts, fmt.Sprintf("%s=%d", state.ExtractionStatus, state.Count))
	}
	if len(extractionParts) == 0 {
		extractionParts = append(extractionParts, "extraction=0")
	}
	renderMetricLine(builder, extractionParts...)
	renderMetricLine(builder,
		fmt.Sprintf("queued=%d", health.Queued),
		fmt.Sprintf("processing=%d", health.Processing),
		fmt.Sprintf("complete=%d", health.Complete),
		fmt.Sprintf("failed=%d", health.Failed),
		fmt.Sprintf("skipped_unsupported=%d", health.SkippedUnsupported),
	)
	renderMetricLine(builder,
		fmt.Sprintf("search_documents=%d", health.SearchDocumentCount),
		"last_indexed="+timePtrOrDash(health.LastIndexedAt),
		"last_pipeline_update="+timePtrOrDash(health.LastPipelineUpdateAt),
	)
}

func notesExtractionAttentionParts(states []knowledge.NotesExtractionStateCount) []string {
	parts := []string{}
	for _, state := range states {
		if state.Count == 0 {
			continue
		}
		switch state.ExtractionStatus {
		case knowledge.ExtractionStatusFailed,
			knowledge.ExtractionStatusTooLarge,
			knowledge.ExtractionStatusPasswordRequired,
			knowledge.ExtractionStatusNoEmbeddedText,
			knowledge.ExtractionStatusOCRDeferred,
			knowledge.ExtractionStatusSourceUnavailable:
			parts = append(parts, fmt.Sprintf("%s=%d", state.ExtractionStatus, state.Count))
		}
	}
	return parts
}

func renderNotesProjection(builder *strings.Builder, projection knowledge.NotesProjectionOverview) {
	renderDetailsSection(builder, "Projection")
	renderMetricLine(builder,
		"root="+firstNonEmpty(projection.ProjectionRoot, "-"),
		fmt.Sprintf("exists=%t", projection.Exists),
		fmt.Sprintf("read_only=%t", projection.ReadOnly),
		fmt.Sprintf("raw_writes=%t", projection.RawWritesSupported),
	)
	renderMetricLine(builder,
		fmt.Sprintf("entries=%d", projection.Entries),
		fmt.Sprintf("materialized=%d", projection.Materialized),
		fmt.Sprintf("missing=%d", projection.Missing),
		fmt.Sprintf("skipped=%d", projection.Skipped),
		fmt.Sprintf("findings=%d", projection.FindingCount),
		"last_rebuild="+timePtrOrDash(projection.LastRebuildAt),
	)
}

func renderNotesAcrossNetwork(builder *strings.Builder, state ScreenState, roots []knowledge.NotesRootOverview, row int) int {
	renderPrimarySection(builder, "Notes Across Network")
	if len(roots) == 0 {
		renderEmpty(builder, "No active notes roots.")
		return row
	}
	grouped := notesRootsByNode(roots)
	for _, group := range grouped {
		fmt.Fprintf(builder, "  %s\n", firstNonEmpty(group.NodeKey, "-"))
		for _, root := range group.Roots {
			line := fmt.Sprintf("%s - %d files", notesRootLabel(root), root.Totals.FileCount)
			if state.RawDetails {
				line += fmt.Sprintf("  status=%s id=%s", firstNonEmpty(root.Status, "-"), firstNonEmpty(root.NotesSourceRootID, "-"))
			}
			fmt.Fprintf(builder, "  %s %s\n", renderSelectedMarker(row, state.SelectedIndex), line)
			row++
		}
	}
	return row
}

func renderNotesSearchResults(builder *strings.Builder, state ScreenState, search NotesSearchData, row int) {
	renderPrimarySection(builder, "Search Results")
	if search.Status == ScreenLoadLoading {
		renderLoading(builder, "Notes Search")
		return
	}
	if search.Status == ScreenLoadFailed {
		fmt.Fprintf(builder, "  %s %s\n", renderStatus("failed"), firstNonEmpty(search.Error, "Search failed."))
		return
	}
	if strings.TrimSpace(search.Query) == "" {
		renderEmpty(builder, "Press s to search indexed notes. Filters: lifecycle:active|archived|all project:<slug> node:<key> tag:<tag> path:<path> after:<date> before:<date> sort:<order>.")
		return
	}
	renderNotesSearchResultSetHeader(builder, search)
	if len(search.ResultSet.Results) == 0 {
		renderEmpty(builder, "No indexed notes matched the search.")
		return
	}
	for index, result := range search.ResultSet.Results {
		renderNotesLifecycleGroup(builder, search.ResultSet, index)
		renderNotesSearchResultRow(builder, row, state.SelectedIndex, result, state.RawDetails)
		row++
	}
}

func renderNotesSearchResultRow(builder *strings.Builder, row int, selected int, result knowledge.NotesSearchResult, raw bool) {
	title := firstNonEmpty(result.Title, result.RelativePath, result.Citation.Label, result.KnowledgeObjectID, result.SearchDocumentID)
	snippet := compactPortalText(result.Snippet, usableWidth(portalRenderContext().Width, 12))
	location := notesSearchResultLocation(result)
	contextParts := []string{
		"lifecycle=" + firstNonEmpty(string(result.SourceLifecycle), "active"),
		"match=" + notesSearchMatchLabel(result),
		"date=" + notesSearchSelectedDateLabel(result),
		"node=" + firstNonEmpty(result.SourceNodeKey, "-"),
		"project=" + firstNonEmpty(result.ProjectID, "-"),
		"root=" + firstNonEmpty(result.NotesSourceRootID, "-"),
		"class=" + firstNonEmpty(result.FileClass, "-"),
		"source=" + notesSearchResultSourceLabel(result),
		"extraction=" + notesSearchResultExtractionLabel(result),
	}
	fmt.Fprintf(builder, "  %s %s\n",
		renderSelectedMarker(row, selected),
		trimForWidth(title, usableWidth(portalRenderContext().Width, 32)),
	)
	if location != "" {
		fmt.Fprintf(builder, "      %s\n", portalRenderContext().Styles.Muted.Render(location))
	}
	fmt.Fprintf(builder, "      %s\n", portalRenderContext().Styles.Muted.Render(strings.Join(contextParts, "  ")))
	if snippet != "" {
		fmt.Fprintf(builder, "      %s\n", portalRenderContext().Styles.Muted.Render(snippet))
	}
	if result.Citation.Label != "" || result.Citation.SourceRef != "" {
		fmt.Fprintf(builder, "      %s\n", portalRenderContext().Styles.Muted.Render("citation="+firstNonEmpty(result.Citation.Label, "-")+" source="+firstNonEmpty(result.Citation.SourceRef, "-")))
	}
	if result.SourceLifecycle == knowledge.SourceLifecycleArchived {
		fmt.Fprintf(builder, "      %s\n", portalRenderContext().Styles.Muted.Render(trimForWidth("archived="+timePtrOrDash(result.ArchivedAt)+" canonical="+result.CanonicalPath, usableWidth(portalRenderContext().Width, 8))))
	}
	if raw {
		fmt.Fprintf(builder, "      document=%s object=%s chunk=%s source=%s text_source=%s extraction=%s metadata_only=%t source_created=%s source_modified=%s recency=%s recency_basis=%s observed=%s indexed=%s scores=final:%.4f bm25:%.4f fts:%.4f boost:%.4f recency:%.4f lexical_rank=%d semantic_rank=%d recency_rank=%d semantic_distance=%.4f semantic_score=%.4f\n",
			firstNonEmpty(result.SearchDocumentID, "-"),
			firstNonEmpty(result.KnowledgeObjectID, "-"),
			firstNonEmpty(result.KnowledgeChunkID, "-"),
			firstNonEmpty(result.SourceKind, "-"),
			notesSearchResultSourceLabel(result),
			notesSearchResultExtractionLabel(result),
			result.MetadataOnly,
			timePtrOrDash(result.SourceCreatedAt),
			timePtrOrDash(result.SourceModifiedAt),
			timeOrDash(result.RecencyAt),
			firstNonEmpty(result.RecencyBasis, "-"),
			timeOrDash(result.ObservedAt),
			timeOrDash(result.IndexedAt),
			result.FinalScore,
			result.BM25Score,
			result.FTSScore,
			result.BoostScore,
			result.RecencyScore,
			result.LexicalRank,
			result.SemanticRank,
			result.RecencyRank,
			result.SemanticDistance,
			result.SemanticScore,
		)
	}
}

func notesSearchResultSourceLabel(result knowledge.NotesSearchResult) string {
	if strings.TrimSpace(result.TextSource) != "" {
		return result.TextSource
	}
	if result.MetadataOnly {
		return knowledge.TextSourceMetadataText
	}
	return "-"
}

func notesSearchSelectedDateLabel(result knowledge.NotesSearchResult) string {
	if result.RecencyAt.IsZero() {
		return "-"
	}
	return result.RecencyAt.UTC().Format("2006-01-02")
}

func notesSearchResultExtractionLabel(result knowledge.NotesSearchResult) string {
	status := strings.TrimSpace(result.ExtractionStatus)
	if status == "" && result.MetadataOnly {
		status = knowledge.ExtractionStatusMetadataOnly
	}
	if status == "" {
		return "-"
	}
	if result.MetadataOnly && status != knowledge.ExtractionStatusMetadataOnly {
		return status + ":metadata"
	}
	return status
}

func renderNotesSearchResultSetHeader(builder *strings.Builder, search NotesSearchData) {
	query := firstNonEmpty(search.ResultSet.Query, search.Query)
	parts := []string{
		fmt.Sprintf("query=%q", query),
		fmt.Sprintf("results=%d", search.ResultSet.ResultCount),
	}
	if strings.TrimSpace(search.ResultSet.Mode) != "" {
		parts = append(parts, "mode="+search.ResultSet.Mode)
	}
	if strings.TrimSpace(search.ResultSet.RequestedMode) != "" && search.ResultSet.RequestedMode != search.ResultSet.Mode {
		parts = append(parts, "requested="+search.ResultSet.RequestedMode)
	}
	if strings.TrimSpace(search.ResultSet.FallbackReason) != "" {
		parts = append(parts, "fallback="+search.ResultSet.FallbackReason)
	}
	parts = append(parts, "lifecycle="+firstNonEmpty(string(search.ResultSet.SourceLifecycle), "active"),
		fmt.Sprintf("archived_omitted=%d truncated=%t", search.ResultSet.ArchivedMatchesOmitted, search.ResultSet.ArchivedMatchesOmittedTruncated))
	fmt.Fprintf(builder, "  %s\n", strings.Join(parts, " "))
}

func notesSearchMatchLabel(result knowledge.NotesSearchResult) string {
	if len(result.MatchReasons) == 0 {
		return "-"
	}
	return strings.Join(result.MatchReasons, ",")
}

func notesSearchResultLocation(result knowledge.NotesSearchResult) string {
	location := strings.TrimSpace(result.RelativePath)
	if strings.TrimSpace(result.StructuralPath) != "" {
		if location == "" {
			return result.StructuralPath
		}
		return location + " > " + result.StructuralPath
	}
	return location
}

func RenderNotesSearchWithSelection(mode ui.Mode, query string, data NotesSearchData, selected int, width int, height int) string {
	ctx := renderContext{Mode: mode, Styles: NewPortalStyles(mode), Width: width, Height: height, Logo: SelectPortalLogo(width, nil)}
	restore := setActiveRenderContext(ctx)
	defer restore()

	var builder strings.Builder
	renderTitle(&builder, mode, "Notes Search")
	renderNotesSearchBox(&builder, query, width)
	renderSection(&builder, "Search Results")
	switch data.Status {
	case ScreenLoadLoading:
		renderLoading(&builder, "Notes Search")
	case ScreenLoadFailed:
		fmt.Fprintf(&builder, "  %s %s\n", renderStatus("failed"), firstNonEmpty(data.Error, "Search failed."))
	default:
		if strings.TrimSpace(data.Query) == "" {
			renderEmpty(&builder, "Type a query and press enter. Filters: lifecycle:active|archived|all project:<slug> node:<key> tag:<tag> path:<path> after:<date> before:<date> sort:<order>.")
		} else if len(data.ResultSet.Results) == 0 {
			renderNotesSearchResultSetHeader(&builder, data)
			renderEmpty(&builder, "No indexed notes matched the search.")
		} else {
			renderNotesSearchResultSetHeader(&builder, data)
			for idx, result := range data.ResultSet.Results {
				renderNotesLifecycleGroup(&builder, data.ResultSet, idx)
				renderNotesSearchResultRow(&builder, idx, selected, result, false)
			}
		}
	}
	renderNotesSearchFooter(&builder)
	return builder.String()
}

func renderNotesSearchBox(builder *strings.Builder, query string, width int) {
	if width <= 0 {
		width = 72
	}
	boxWidth := width
	if boxWidth > 80 {
		boxWidth = 80
	}
	if boxWidth < 32 {
		boxWidth = 32
	}
	innerWidth := boxWidth - 4
	label := " Notes Search "
	topFill := innerWidth - len(label)
	if topFill < 0 {
		topFill = 0
	}
	top := "+" + label + strings.Repeat("-", topFill) + "+"
	searchValue := strings.TrimSpace(query)
	if searchValue == "" {
		searchValue = portalRenderContext().Styles.Muted.Render("search indexed notes")
	}
	line := " Query  " + searchValue
	line = trimForWidth(line, innerWidth)
	padding := innerWidth - len(stripANSI(line))
	if padding < 0 {
		padding = 0
	}
	bottom := "+" + strings.Repeat("-", innerWidth) + "+"
	border := portalRenderContext().Styles.BorderFocus
	builder.WriteString(border.Render(top))
	builder.WriteByte('\n')
	builder.WriteString(border.Render("| "))
	builder.WriteString(line)
	builder.WriteString(strings.Repeat(" ", padding))
	builder.WriteString(border.Render(" |"))
	builder.WriteByte('\n')
	builder.WriteString(border.Render(bottom))
	builder.WriteString("\n\n")
}

func renderNotesSearchFooter(builder *strings.Builder) {
	builder.WriteString("\n")
	builder.WriteString(portalRenderContext().Styles.Muted.Render("Keys: enter search/open  esc close  j/k move  trackpad/pgup/pgdn scroll"))
	builder.WriteString("\n")
}

func notesSearchSelectedBodyLine(data NotesSearchData, selected int) int {
	if len(data.ResultSet.Results) == 0 {
		return 0
	}
	selected = clampIndex(selected, len(data.ResultSet.Results))
	line := 3
	for idx := 0; idx < selected; idx++ {
		if notesLifecycleGroupAt(data.ResultSet, idx) != "" {
			line++
		}
		line += notesSearchResultLineCount(data.ResultSet.Results[idx], false)
	}
	if notesLifecycleGroupAt(data.ResultSet, selected) != "" {
		line++
	}
	return line
}

func notesLifecycleGroupAt(results knowledge.NotesSearchResultSet, index int) string {
	for _, group := range results.LifecycleGroups {
		if group.Offset == index && group.ResultCount > 0 {
			return string(group.SourceLifecycle)
		}
	}
	return ""
}
func renderNotesLifecycleGroup(builder *strings.Builder, results knowledge.NotesSearchResultSet, index int) {
	if lifecycle := notesLifecycleGroupAt(results, index); lifecycle != "" {
		fmt.Fprintf(builder, "  %s Notes\n", lifecycle)
	}
}

func notesSearchResultLineCount(result knowledge.NotesSearchResult, raw bool) int {
	count := 2
	if result.SourceLifecycle == knowledge.SourceLifecycleArchived {
		count++
	}
	if strings.TrimSpace(notesSearchResultLocation(result)) != "" {
		count++
	}
	if strings.TrimSpace(result.Snippet) != "" {
		count++
	}
	if result.Citation.Label != "" || result.Citation.SourceRef != "" {
		count++
	}
	if raw {
		count++
	}
	return count
}

type notesNodeRootGroup struct {
	NodeKey string
	Roots   []knowledge.NotesRootOverview
}

func notesRootsByNode(roots []knowledge.NotesRootOverview) []notesNodeRootGroup {
	byNode := map[string][]knowledge.NotesRootOverview{}
	for _, root := range roots {
		node := firstNonEmpty(root.NodeKey, root.NodeID, "unknown")
		byNode[node] = append(byNode[node], root)
	}
	keys := make([]string, 0, len(byNode))
	for key := range byNode {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]notesNodeRootGroup, 0, len(keys))
	for _, key := range keys {
		out = append(out, notesNodeRootGroup{NodeKey: key, Roots: byNode[key]})
	}
	return out
}

func notesFileClassRank(fileClass string) int {
	switch fileClass {
	case knowledge.NotesFileClassBucketMarkdown:
		return 0
	case knowledge.NotesFileClassBucketPDF:
		return 1
	case knowledge.NotesFileClassBucketImage:
		return 2
	case knowledge.NotesFileClassBucketText:
		return 3
	case knowledge.NotesFileClassBucketDirectory:
		return 4
	case knowledge.NotesFileClassBucketOther:
		return 5
	case knowledge.NotesFileClassBucketUnsupported:
		return 6
	default:
		return 99
	}
}

func notesExtractionStateRank(status string) int {
	switch status {
	case knowledge.ExtractionStatusExtracted:
		return 0
	case knowledge.ExtractionStatusMetadataOnly:
		return 1
	case knowledge.ExtractionStatusTooLarge:
		return 2
	case knowledge.ExtractionStatusPasswordRequired:
		return 3
	case knowledge.ExtractionStatusNoEmbeddedText:
		return 4
	case knowledge.ExtractionStatusOCRDeferred:
		return 5
	case knowledge.ExtractionStatusSourceUnavailable:
		return 6
	case knowledge.ExtractionStatusUnsupportedBodyExtraction:
		return 7
	case knowledge.ExtractionStatusFailed:
		return 8
	default:
		return 99
	}
}

func latestPortalTimePtr(left *time.Time, right *time.Time) *time.Time {
	switch {
	case left == nil:
		return copyPortalTimePtr(right)
	case right == nil:
		return copyPortalTimePtr(left)
	case right.After(*left):
		return copyPortalTimePtr(right)
	default:
		return copyPortalTimePtr(left)
	}
}

func copyPortalTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	out := value.UTC()
	return &out
}
