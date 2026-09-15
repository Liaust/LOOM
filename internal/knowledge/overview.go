package knowledge

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/notesprojection"
	"loom.local/loom/internal/storagecatalog"
)

const (
	NotesFileClassBucketMarkdown    = "markdown"
	NotesFileClassBucketPDF         = "pdf"
	NotesFileClassBucketImage       = "image"
	NotesFileClassBucketText        = "text"
	NotesFileClassBucketOffice      = "office_document"
	NotesFileClassBucketDirectory   = "directory"
	NotesFileClassBucketOther       = "other"
	NotesFileClassBucketUnsupported = "unsupported"
)

const NotesExtractionStatusNotStarted = "not_started"

type NotesOverviewInput struct {
	SourceLifecycle SourceLifecycleFilter `json:"source_lifecycle,omitempty"`
	readTx          *sql.Tx
	SourceCategory  string                  `json:"source_category,omitempty"`
	IncludeInactive bool                    `json:"include_inactive,omitempty"`
	NodeKey         string                  `json:"node_key,omitempty"`
	ProjectID       string                  `json:"project_id,omitempty"`
	Projection      *notesprojection.Status `json:"-"`
}

type NotesOverview struct {
	SourceLifecycle  SourceLifecycleFilter       `json:"source_lifecycle"`
	GeneratedAt      time.Time                   `json:"generated_at"`
	Totals           NotesOverviewTotals         `json:"totals"`
	FileClasses      []NotesFileClassCount       `json:"file_classes"`
	ProcessingStates []NotesProcessingStateCount `json:"processing_states"`
	ExtractionStates []NotesExtractionStateCount `json:"extraction_states"`
	Nodes            []NotesNodeOverview         `json:"nodes"`
	Projection       NotesProjectionOverview     `json:"projection"`
	IndexHealth      NotesIndexHealth            `json:"index_health"`
}

type NotesOverviewTotals struct {
	LifecycleCounts     NotesLifecycleCounts `json:"lifecycle_counts"`
	RootCount           int                  `json:"root_count"`
	ActiveRootCount     int                  `json:"active_root_count"`
	ObjectCount         int                  `json:"object_count"`
	FileCount           int                  `json:"file_count"`
	DirectoryCount      int                  `json:"directory_count"`
	SizeBytes           int64                `json:"size_bytes"`
	SearchDocumentCount int                  `json:"search_document_count"`
}

type NotesFileClassCount struct {
	FileClass string `json:"file_class"`
	Count     int    `json:"count"`
	SizeBytes int64  `json:"size_bytes"`
}

type NotesProcessingStateCount struct {
	ProcessingState string `json:"processing_state"`
	Count           int    `json:"count"`
}

type NotesExtractionStateCount struct {
	ExtractionStatus string `json:"extraction_status"`
	Count            int    `json:"count"`
}

type NotesPipelineStatusCount struct {
	Status        string     `json:"status"`
	Count         int        `json:"count"`
	LastUpdatedAt *time.Time `json:"last_updated_at,omitempty"`
}

type NotesNodeOverview struct {
	NodeID           string                      `json:"node_id,omitempty"`
	NodeKey          string                      `json:"node_key"`
	Totals           NotesOverviewTotals         `json:"totals"`
	ExtractionStates []NotesExtractionStateCount `json:"extraction_states,omitempty"`
	Roots            []NotesRootOverview         `json:"roots"`
	IndexHealth      NotesIndexHealth            `json:"index_health"`
}

type NotesRootOverview struct {
	SourceContext
	NotesSourceRootID string                      `json:"notes_source_root_id"`
	RootKind          string                      `json:"root_kind"`
	NodeID            string                      `json:"node_id,omitempty"`
	NodeKey           string                      `json:"node_key"`
	ProjectID         string                      `json:"project_id,omitempty"`
	ProjectSlug       string                      `json:"project_slug,omitempty"`
	ProjectName       string                      `json:"project_name,omitempty"`
	BackendRootKey    string                      `json:"backend_root_key"`
	DisplayName       string                      `json:"display_name,omitempty"`
	SourcePath        string                      `json:"source_path,omitempty"`
	RootRelativePath  string                      `json:"root_relative_path,omitempty"`
	Status            string                      `json:"status"`
	Totals            NotesOverviewTotals         `json:"totals"`
	FileClasses       []NotesFileClassCount       `json:"file_classes"`
	ProcessingStates  []NotesProcessingStateCount `json:"processing_states"`
	ExtractionStates  []NotesExtractionStateCount `json:"extraction_states,omitempty"`
	PipelineStatuses  []NotesPipelineStatusCount  `json:"pipeline_statuses,omitempty"`
	IndexHealth       NotesIndexHealth            `json:"index_health"`
	LastSeenAt        *time.Time                  `json:"last_seen_at,omitempty"`
	LastProcessedAt   *time.Time                  `json:"last_processed_at,omitempty"`
}

type NotesProjectionOverview struct {
	ProjectionRoot     string     `json:"projection_root,omitempty"`
	Exists             bool       `json:"exists"`
	ManifestPath       string     `json:"manifest_path,omitempty"`
	LastRebuildAt      *time.Time `json:"last_rebuild_at,omitempty"`
	Entries            int        `json:"entries"`
	Materialized       int        `json:"materialized"`
	Missing            int        `json:"missing"`
	Skipped            int        `json:"skipped"`
	ReadOnly           bool       `json:"read_only"`
	RawWritesSupported bool       `json:"raw_writes_supported"`
	FindingCount       int        `json:"finding_count"`
	GeneratedAt        time.Time  `json:"generated_at,omitempty"`
}

type NotesIndexHealth struct {
	SearchDocumentCount  int        `json:"search_document_count"`
	Queued               int        `json:"queued"`
	Processing           int        `json:"processing"`
	Complete             int        `json:"complete"`
	Failed               int        `json:"failed"`
	SkippedUnsupported   int        `json:"skipped_unsupported"`
	LastIndexedAt        *time.Time `json:"last_indexed_at,omitempty"`
	LastPipelineUpdateAt *time.Time `json:"last_pipeline_update_at,omitempty"`
	LastFailureAt        *time.Time `json:"last_failure_at,omitempty"`
}

type notesOverviewObjectRow struct {
	SourceContext
	LifecycleCounts     NotesLifecycleCounts
	NotesSourceRootID   string
	RootKind            string
	NodeID              string
	NodeKey             string
	ProjectID           string
	ProjectSlug         string
	ProjectName         string
	BackendRootKey      string
	DisplayName         string
	SourcePath          string
	RootRelativePath    string
	Status              string
	FileClass           string
	ProcessingState     string
	ExtractionStatus    string
	ObjectCount         int
	FileCount           int
	DirectoryCount      int
	SizeBytes           int64
	SearchDocumentCount int
	LastSeenAt          *time.Time
	LastProcessedAt     *time.Time
	LastIndexedAt       *time.Time
}

type notesOverviewPipelineRow struct {
	NotesSourceRootID string
	Status            string
	Count             int
	LastUpdatedAt     *time.Time
	LastFailedAt      *time.Time
}

func (s *Service) GetNotesOverview(ctx context.Context, input NotesOverviewInput) (NotesOverview, error) {
	lifecycle, err := NormalizeSourceLifecycleFilter(input.SourceLifecycle)
	if err != nil {
		return NotesOverview{}, err
	}
	input.SourceLifecycle = lifecycle
	if err := validateSourceCategory(input.SourceCategory); err != nil {
		return NotesOverview{}, err
	}
	if s == nil || s.store.db == nil {
		return NotesOverview{}, fmt.Errorf("knowledge store is not configured")
	}
	input.NodeKey = strings.TrimSpace(input.NodeKey)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	tx, err := s.store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return NotesOverview{}, err
	}
	defer tx.Rollback()
	input.readTx = tx
	rows, err := s.store.ListNotesOverviewRows(ctx, input)
	if err != nil {
		return NotesOverview{}, err
	}
	pipelineRows, err := s.store.ListNotesOverviewPipelineRows(ctx, input)
	if err != nil {
		return NotesOverview{}, err
	}
	out := buildNotesOverview(s.currentTime(), rows, pipelineRows, input.Projection)
	out.SourceLifecycle = lifecycle
	if err := tx.Commit(); err != nil {
		return NotesOverview{}, err
	}
	return out, nil
}

func buildNotesOverview(now time.Time, rows []notesOverviewObjectRow, pipelineRows []notesOverviewPipelineRow, projection *notesprojection.Status) NotesOverview {
	builder := notesOverviewBuilder{
		overview: NotesOverview{
			GeneratedAt: now.UTC(),
			Projection:  notesProjectionOverview(projection),
		},
		nodes: map[string]*notesNodeBuilder{},
		roots: map[string]*notesRootBuilder{},
	}
	for _, row := range rows {
		builder.addObjectRow(row)
	}
	for _, row := range pipelineRows {
		builder.addPipelineRow(row)
	}
	return builder.finish()
}

type notesOverviewBuilder struct {
	overview NotesOverview
	nodes    map[string]*notesNodeBuilder
	roots    map[string]*notesRootBuilder
}

type notesNodeBuilder struct {
	node             NotesNodeOverview
	roots            map[string]*notesRootBuilder
	extractionStates map[string]*NotesExtractionStateCount
}

type notesRootBuilder struct {
	root             NotesRootOverview
	fileClasses      map[string]*NotesFileClassCount
	processingStates map[string]*NotesProcessingStateCount
	extractionStates map[string]*NotesExtractionStateCount
	pipelineStatuses map[string]*NotesPipelineStatusCount
}

func (b *notesOverviewBuilder) addObjectRow(row notesOverviewObjectRow) {
	row = normalizeNotesOverviewObjectRow(row)
	rootBuilder := b.ensureRoot(row)
	if row.ObjectCount <= 0 {
		return
	}
	counts := NotesOverviewTotals{
		LifecycleCounts:     row.LifecycleCounts,
		ObjectCount:         row.ObjectCount,
		FileCount:           row.FileCount,
		DirectoryCount:      row.DirectoryCount,
		SizeBytes:           row.SizeBytes,
		SearchDocumentCount: row.SearchDocumentCount,
	}
	addTotals(&b.overview.Totals, counts)
	nodeBuilder := b.ensureNode(row)
	addTotals(&nodeBuilder.node.Totals, counts)
	addTotals(&rootBuilder.root.Totals, counts)
	b.addFileClass(&b.overview.FileClasses, row.FileClass, row.ObjectCount, row.SizeBytes)
	b.addProcessingState(&b.overview.ProcessingStates, row.ProcessingState, row.ObjectCount)
	b.addExtractionState(&b.overview.ExtractionStates, row.ExtractionStatus, row.ObjectCount)
	addFileClass(rootBuilder.fileClasses, row.FileClass, row.ObjectCount, row.SizeBytes)
	addProcessingState(rootBuilder.processingStates, row.ProcessingState, row.ObjectCount)
	addExtractionState(nodeBuilder.extractionStates, row.ExtractionStatus, row.ObjectCount)
	addExtractionState(rootBuilder.extractionStates, row.ExtractionStatus, row.ObjectCount)
	addIndexHealth(&b.overview.IndexHealth, row.SearchDocumentCount, row.LastIndexedAt)
	addIndexHealth(&nodeBuilder.node.IndexHealth, row.SearchDocumentCount, row.LastIndexedAt)
	addIndexHealth(&rootBuilder.root.IndexHealth, row.SearchDocumentCount, row.LastIndexedAt)
	rootBuilder.root.LastSeenAt = latestTimePtr(rootBuilder.root.LastSeenAt, row.LastSeenAt)
	rootBuilder.root.LastProcessedAt = latestTimePtr(rootBuilder.root.LastProcessedAt, row.LastProcessedAt)
}

func (b *notesOverviewBuilder) ensureNode(row notesOverviewObjectRow) *notesNodeBuilder {
	key := notesNodeKey(row)
	if node, ok := b.nodes[key]; ok {
		return node
	}
	node := &notesNodeBuilder{
		node: NotesNodeOverview{
			NodeID:  row.NodeID,
			NodeKey: row.NodeKey,
		},
		roots:            map[string]*notesRootBuilder{},
		extractionStates: map[string]*NotesExtractionStateCount{},
	}
	b.nodes[key] = node
	return node
}

func (b *notesOverviewBuilder) ensureRoot(row notesOverviewObjectRow) *notesRootBuilder {
	if root, ok := b.roots[row.NotesSourceRootID]; ok {
		return root
	}
	node := b.ensureNode(row)
	root := &notesRootBuilder{
		root: NotesRootOverview{
			SourceContext:     row.SourceContext,
			NotesSourceRootID: row.NotesSourceRootID,
			RootKind:          row.RootKind,
			NodeID:            row.NodeID,
			NodeKey:           row.NodeKey,
			ProjectID:         row.ProjectID,
			ProjectSlug:       row.ProjectSlug,
			ProjectName:       row.ProjectName,
			BackendRootKey:    row.BackendRootKey,
			DisplayName:       row.DisplayName,
			SourcePath:        row.SourcePath,
			RootRelativePath:  row.RootRelativePath,
			Status:            row.Status,
		},
		fileClasses:      map[string]*NotesFileClassCount{},
		processingStates: map[string]*NotesProcessingStateCount{},
		extractionStates: map[string]*NotesExtractionStateCount{},
		pipelineStatuses: map[string]*NotesPipelineStatusCount{},
	}
	b.roots[row.NotesSourceRootID] = root
	node.roots[row.NotesSourceRootID] = root
	b.overview.Totals.RootCount++
	node.node.Totals.RootCount++
	root.root.Totals.RootCount = 1
	if row.Status == SourceRootStatusActive {
		b.overview.Totals.ActiveRootCount++
		node.node.Totals.ActiveRootCount++
		root.root.Totals.ActiveRootCount = 1
	}
	return root
}

func (b *notesOverviewBuilder) addFileClass(target *[]NotesFileClassCount, fileClass string, count int, sizeBytes int64) {
	class := normalizeNotesFileClassBucket(fileClass)
	for i := range *target {
		if (*target)[i].FileClass == class {
			(*target)[i].Count += count
			(*target)[i].SizeBytes += sizeBytes
			return
		}
	}
	*target = append(*target, NotesFileClassCount{FileClass: class, Count: count, SizeBytes: sizeBytes})
}

func (b *notesOverviewBuilder) addProcessingState(target *[]NotesProcessingStateCount, state string, count int) {
	state = normalizeNotesProcessingState(state)
	for i := range *target {
		if (*target)[i].ProcessingState == state {
			(*target)[i].Count += count
			return
		}
	}
	*target = append(*target, NotesProcessingStateCount{ProcessingState: state, Count: count})
}

func (b *notesOverviewBuilder) addExtractionState(target *[]NotesExtractionStateCount, status string, count int) {
	status = normalizeNotesExtractionStatus(status, "")
	for i := range *target {
		if (*target)[i].ExtractionStatus == status {
			(*target)[i].Count += count
			return
		}
	}
	*target = append(*target, NotesExtractionStateCount{ExtractionStatus: status, Count: count})
}

func (b *notesOverviewBuilder) addPipelineRow(row notesOverviewPipelineRow) {
	if row.Count <= 0 {
		return
	}
	root, ok := b.roots[strings.TrimSpace(row.NotesSourceRootID)]
	if !ok {
		return
	}
	status := normalizeNotesPipelineStatus(row.Status)
	addPipelineStatus(root.pipelineStatuses, status, row.Count, row.LastUpdatedAt)
	addPipelineHealth(&root.root.IndexHealth, status, row.Count, row.LastUpdatedAt, row.LastFailedAt)
	if node, ok := b.nodes[notesNodeKeyValues(root.root.NodeKey, root.root.NodeID)]; ok {
		addPipelineHealth(&node.node.IndexHealth, status, row.Count, row.LastUpdatedAt, row.LastFailedAt)
	}
	addPipelineHealth(&b.overview.IndexHealth, status, row.Count, row.LastUpdatedAt, row.LastFailedAt)
}

func (b *notesOverviewBuilder) finish() NotesOverview {
	sortFileClassCounts(b.overview.FileClasses)
	sortProcessingStateCounts(b.overview.ProcessingStates)
	sortExtractionStateCounts(b.overview.ExtractionStates)
	for _, node := range b.nodes {
		for _, root := range node.roots {
			root.root.FileClasses = fileClassCountsFromMap(root.fileClasses)
			root.root.ProcessingStates = processingStateCountsFromMap(root.processingStates)
			root.root.ExtractionStates = extractionStateCountsFromMap(root.extractionStates)
			root.root.PipelineStatuses = pipelineStatusCountsFromMap(root.pipelineStatuses)
			node.node.Roots = append(node.node.Roots, root.root)
		}
		node.node.ExtractionStates = extractionStateCountsFromMap(node.extractionStates)
		sort.Slice(node.node.Roots, func(i, j int) bool {
			return notesRootSortKey(node.node.Roots[i]) < notesRootSortKey(node.node.Roots[j])
		})
		b.overview.Nodes = append(b.overview.Nodes, node.node)
	}
	sort.Slice(b.overview.Nodes, func(i, j int) bool {
		return b.overview.Nodes[i].NodeKey < b.overview.Nodes[j].NodeKey
	})
	return b.overview
}

func addFileClass(target map[string]*NotesFileClassCount, fileClass string, count int, sizeBytes int64) {
	class := normalizeNotesFileClassBucket(fileClass)
	if existing, ok := target[class]; ok {
		existing.Count += count
		existing.SizeBytes += sizeBytes
		return
	}
	target[class] = &NotesFileClassCount{FileClass: class, Count: count, SizeBytes: sizeBytes}
}

func addProcessingState(target map[string]*NotesProcessingStateCount, state string, count int) {
	state = normalizeNotesProcessingState(state)
	if existing, ok := target[state]; ok {
		existing.Count += count
		return
	}
	target[state] = &NotesProcessingStateCount{ProcessingState: state, Count: count}
}

func addExtractionState(target map[string]*NotesExtractionStateCount, status string, count int) {
	status = normalizeNotesExtractionStatus(status, "")
	if existing, ok := target[status]; ok {
		existing.Count += count
		return
	}
	target[status] = &NotesExtractionStateCount{ExtractionStatus: status, Count: count}
}

func addPipelineStatus(target map[string]*NotesPipelineStatusCount, status string, count int, updatedAt *time.Time) {
	if existing, ok := target[status]; ok {
		existing.Count += count
		existing.LastUpdatedAt = latestTimePtr(existing.LastUpdatedAt, updatedAt)
		return
	}
	target[status] = &NotesPipelineStatusCount{Status: status, Count: count, LastUpdatedAt: copyTimePtr(updatedAt)}
}

func addTotals(target *NotesOverviewTotals, add NotesOverviewTotals) {
	target.LifecycleCounts.Active += add.LifecycleCounts.Active
	target.LifecycleCounts.Archived += add.LifecycleCounts.Archived
	target.ObjectCount += add.ObjectCount
	target.FileCount += add.FileCount
	target.DirectoryCount += add.DirectoryCount
	target.SizeBytes += add.SizeBytes
	target.SearchDocumentCount += add.SearchDocumentCount
}

func addIndexHealth(target *NotesIndexHealth, searchDocuments int, indexedAt *time.Time) {
	target.SearchDocumentCount += searchDocuments
	target.LastIndexedAt = latestTimePtr(target.LastIndexedAt, indexedAt)
}

func addPipelineHealth(target *NotesIndexHealth, status string, count int, updatedAt *time.Time, failedAt *time.Time) {
	switch status {
	case PipelineStatusQueued:
		target.Queued += count
	case PipelineStatusProcessing:
		target.Processing += count
	case PipelineStatusComplete:
		target.Complete += count
	case PipelineStatusFailed:
		target.Failed += count
		target.LastFailureAt = latestTimePtr(target.LastFailureAt, failedAt)
	case PipelineStatusSkippedUnsupported:
		target.SkippedUnsupported += count
	}
	target.LastPipelineUpdateAt = latestTimePtr(target.LastPipelineUpdateAt, updatedAt)
}

func fileClassCountsFromMap(input map[string]*NotesFileClassCount) []NotesFileClassCount {
	out := make([]NotesFileClassCount, 0, len(input))
	for _, value := range input {
		out = append(out, *value)
	}
	sortFileClassCounts(out)
	return out
}

func processingStateCountsFromMap(input map[string]*NotesProcessingStateCount) []NotesProcessingStateCount {
	out := make([]NotesProcessingStateCount, 0, len(input))
	for _, value := range input {
		out = append(out, *value)
	}
	sortProcessingStateCounts(out)
	return out
}

func extractionStateCountsFromMap(input map[string]*NotesExtractionStateCount) []NotesExtractionStateCount {
	out := make([]NotesExtractionStateCount, 0, len(input))
	for _, value := range input {
		out = append(out, *value)
	}
	sortExtractionStateCounts(out)
	return out
}

func pipelineStatusCountsFromMap(input map[string]*NotesPipelineStatusCount) []NotesPipelineStatusCount {
	out := make([]NotesPipelineStatusCount, 0, len(input))
	for _, value := range input {
		out = append(out, *value)
	}
	sort.Slice(out, func(i, j int) bool {
		return pipelineStatusSortRank(out[i].Status) < pipelineStatusSortRank(out[j].Status)
	})
	return out
}

func sortFileClassCounts(counts []NotesFileClassCount) {
	sort.Slice(counts, func(i, j int) bool {
		left, right := fileClassBucketRank(counts[i].FileClass), fileClassBucketRank(counts[j].FileClass)
		if left == right {
			return counts[i].FileClass < counts[j].FileClass
		}
		return left < right
	})
}

func sortProcessingStateCounts(counts []NotesProcessingStateCount) {
	sort.Slice(counts, func(i, j int) bool {
		left, right := processingStateRank(counts[i].ProcessingState), processingStateRank(counts[j].ProcessingState)
		if left == right {
			return counts[i].ProcessingState < counts[j].ProcessingState
		}
		return left < right
	})
}

func sortExtractionStateCounts(counts []NotesExtractionStateCount) {
	sort.Slice(counts, func(i, j int) bool {
		left, right := extractionStatusRank(counts[i].ExtractionStatus), extractionStatusRank(counts[j].ExtractionStatus)
		if left == right {
			return counts[i].ExtractionStatus < counts[j].ExtractionStatus
		}
		return left < right
	})
}

func normalizeNotesOverviewObjectRow(row notesOverviewObjectRow) notesOverviewObjectRow {
	row.NotesSourceRootID = strings.TrimSpace(row.NotesSourceRootID)
	row.RootKind = strings.TrimSpace(row.RootKind)
	row.NodeID = strings.TrimSpace(row.NodeID)
	row.NodeKey = strings.TrimSpace(row.NodeKey)
	row.ProjectID = strings.TrimSpace(row.ProjectID)
	row.ProjectSlug = strings.TrimSpace(row.ProjectSlug)
	row.ProjectName = strings.TrimSpace(row.ProjectName)
	row.BackendRootKey = strings.TrimSpace(row.BackendRootKey)
	row.DisplayName = strings.TrimSpace(row.DisplayName)
	row.SourcePath = strings.TrimSpace(row.SourcePath)
	row.RootRelativePath = strings.TrimSpace(row.RootRelativePath)
	row.Status = strings.TrimSpace(row.Status)
	if row.Status == "" {
		row.Status = SourceRootStatusActive
	}
	row.FileClass = normalizeNotesFileClassBucket(row.FileClass)
	row.ProcessingState = normalizeNotesProcessingState(row.ProcessingState)
	row.ExtractionStatus = normalizeNotesExtractionStatus(row.ExtractionStatus, row.ProcessingState)
	if row.FileCount == 0 && row.ObjectCount > 0 && row.FileClass != NotesFileClassBucketDirectory {
		row.FileCount = row.ObjectCount
	}
	if row.DirectoryCount == 0 && row.ObjectCount > 0 && row.FileClass == NotesFileClassBucketDirectory {
		row.DirectoryCount = row.ObjectCount
	}
	return row
}

func normalizeNotesFileClassBucket(fileClass string) string {
	switch strings.TrimSpace(fileClass) {
	case storagecatalog.FileClassMarkdown:
		return NotesFileClassBucketMarkdown
	case storagecatalog.FileClassPDF:
		return NotesFileClassBucketPDF
	case storagecatalog.FileClassImage:
		return NotesFileClassBucketImage
	case storagecatalog.FileClassText, storagecatalog.FileClassCode:
		return NotesFileClassBucketText
	case storagecatalog.FileClassOfficeDocument:
		return NotesFileClassBucketOffice
	case storagecatalog.FileClassDirectory:
		return NotesFileClassBucketDirectory
	case storagecatalog.FileClassVideo, storagecatalog.FileClassAudio, storagecatalog.FileClassArchive, storagecatalog.FileClassPackage:
		return NotesFileClassBucketOther
	case storagecatalog.FileClassGeneratedMetadata, storagecatalog.FileClassBinary, storagecatalog.FileClassUnknown, "":
		return NotesFileClassBucketUnsupported
	default:
		return NotesFileClassBucketOther
	}
}

func normalizeNotesProcessingState(state string) string {
	state = strings.TrimSpace(state)
	if state == "" {
		return ProcessingStateMetadataOnly
	}
	if _, ok := validProcessingStates[state]; ok {
		return state
	}
	return ProcessingStateFailed
}

func normalizeNotesPipelineStatus(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return PipelineStatusNotStarted
	}
	if _, ok := validPipelineStatuses[status]; ok {
		return status
	}
	return PipelineStatusFailed
}

func normalizeNotesExtractionStatus(status string, processingState string) string {
	if strings.TrimSpace(status) == "" && strings.TrimSpace(processingState) == "" {
		return NotesExtractionStatusNotStarted
	}
	switch strings.TrimSpace(status) {
	case ExtractionStatusExtracted,
		ExtractionStatusMetadataOnly,
		ExtractionStatusTooLarge,
		ExtractionStatusSourceUnavailable,
		ExtractionStatusUnsupportedBodyExtraction,
		ExtractionStatusPasswordRequired,
		ExtractionStatusNoEmbeddedText,
		ExtractionStatusOCRDeferred,
		ExtractionStatusFailed:
		return strings.TrimSpace(status)
	}
	switch normalizeNotesProcessingState(processingState) {
	case ProcessingStateTextExtracted, ProcessingStateChunked, ProcessingStateIndexed, ProcessingStateEmbedded:
		return ExtractionStatusExtracted
	case ProcessingStateMetadataOnly:
		return ExtractionStatusMetadataOnly
	case ProcessingStateFailed:
		return ExtractionStatusFailed
	default:
		return NotesExtractionStatusNotStarted
	}
}

func notesProjectionOverview(status *notesprojection.Status) NotesProjectionOverview {
	if status == nil {
		return NotesProjectionOverview{ReadOnly: true}
	}
	return NotesProjectionOverview{
		ProjectionRoot:     status.ProjectionRoot,
		Exists:             status.Exists,
		ManifestPath:       status.ManifestPath,
		LastRebuildAt:      copyTimePtr(status.LastRebuildAt),
		Entries:            status.Counts.Entries,
		Materialized:       status.Counts.Materialized,
		Missing:            status.Counts.Missing,
		Skipped:            status.Counts.Skipped,
		ReadOnly:           status.ReadOnly,
		RawWritesSupported: status.RawWritesSupported,
		FindingCount:       len(status.Findings),
		GeneratedAt:        status.GeneratedAt,
	}
}

func notesNodeKey(row notesOverviewObjectRow) string {
	return notesNodeKeyValues(row.NodeKey, row.NodeID)
}

func notesNodeKeyValues(nodeKey string, nodeID string) string {
	if nodeKey != "" {
		return nodeKey
	}
	if nodeID != "" {
		return nodeID
	}
	return "unknown"
}

func notesRootSortKey(root NotesRootOverview) string {
	return strings.Join([]string{
		root.NodeKey,
		root.RootKind,
		root.ProjectSlug,
		root.ProjectID,
		root.BackendRootKey,
		root.NotesSourceRootID,
	}, "\x00")
}

func fileClassBucketRank(fileClass string) int {
	switch fileClass {
	case NotesFileClassBucketMarkdown:
		return 0
	case NotesFileClassBucketPDF:
		return 1
	case NotesFileClassBucketImage:
		return 2
	case NotesFileClassBucketText:
		return 3
	case NotesFileClassBucketOffice:
		return 4
	case NotesFileClassBucketDirectory:
		return 5
	case NotesFileClassBucketOther:
		return 6
	case NotesFileClassBucketUnsupported:
		return 7
	default:
		return 99
	}
}

func processingStateRank(state string) int {
	switch state {
	case ProcessingStateMetadataOnly:
		return 0
	case ProcessingStateTextExtracted:
		return 1
	case ProcessingStateChunked:
		return 2
	case ProcessingStateIndexed:
		return 3
	case ProcessingStateEmbedded:
		return 4
	case ProcessingStateFailed:
		return 5
	case ProcessingStateStale:
		return 6
	case ProcessingStateDeleted:
		return 7
	default:
		return 99
	}
}

func extractionStatusRank(status string) int {
	switch status {
	case ExtractionStatusExtracted:
		return 0
	case ExtractionStatusMetadataOnly:
		return 1
	case ExtractionStatusTooLarge:
		return 2
	case ExtractionStatusPasswordRequired:
		return 3
	case ExtractionStatusNoEmbeddedText:
		return 4
	case ExtractionStatusOCRDeferred:
		return 5
	case ExtractionStatusSourceUnavailable:
		return 6
	case ExtractionStatusUnsupportedBodyExtraction:
		return 7
	case ExtractionStatusFailed:
		return 8
	case NotesExtractionStatusNotStarted:
		return 9
	default:
		return 99
	}
}

func pipelineStatusSortRank(status string) int {
	switch status {
	case PipelineStatusQueued:
		return 0
	case PipelineStatusProcessing:
		return 1
	case PipelineStatusComplete:
		return 2
	case PipelineStatusFailed:
		return 3
	case PipelineStatusSkippedUnsupported:
		return 4
	case PipelineStatusStale:
		return 5
	case PipelineStatusDisabledByPolicy:
		return 6
	case PipelineStatusNotStarted:
		return 7
	default:
		return 99
	}
}

func latestTimePtr(left *time.Time, right *time.Time) *time.Time {
	switch {
	case left == nil:
		return copyTimePtr(right)
	case right == nil:
		return copyTimePtr(left)
	case right.After(*left):
		return copyTimePtr(right)
	default:
		return copyTimePtr(left)
	}
}

func copyTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	out := value.UTC()
	return &out
}
