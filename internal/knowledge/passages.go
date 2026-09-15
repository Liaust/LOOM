package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/ids"
)

const MaxNotesPassageTextBytes = 64 * 1024

// NotesPassageInput requires the entire citation, never a path or latest alias.
type NotesPassageInput struct {
	SourceLifecycle          SourceLifecycleFilter `json:"source_lifecycle,omitempty"`
	KnowledgeObjectID        string                `json:"knowledge_object_id"`
	KnowledgeObjectVersionID string                `json:"knowledge_object_version_id"`
	KnowledgeChunkID         string                `json:"knowledge_chunk_id"`
	SourceHash               string                `json:"source_hash"`
}

type NotesPassage struct {
	NotesPassageInput
	Custody              NotesCustodyContext `json:"custody"`
	SchemaVersion        string              `json:"schema_version"`
	Text                 string              `json:"text"`
	ChunkHash            string              `json:"chunk_hash"`
	StructuralPath       string              `json:"structural_path"`
	Historical           bool                `json:"historical"`
	CurrentSourceContext SourceContext       `json:"current_source_context"`
}

func ValidateNotesPassageInput(input NotesPassageInput) error {
	if _, err := NormalizeSourceLifecycleFilter(input.SourceLifecycle); err != nil {
		return err
	}
	for _, item := range []struct{ prefix, value string }{
		{ids.KnowledgeObjectPrefix, input.KnowledgeObjectID},
		{ids.KnowledgeObjectVersionPrefix, input.KnowledgeObjectVersionID},
		{ids.KnowledgeChunkPrefix, input.KnowledgeChunkID},
	} {
		if err := ids.Validate(item.prefix, item.value); err != nil {
			return invalid("passage requires an exact %s ID", item.prefix)
		}
	}
	if strings.TrimSpace(input.SourceHash) == "" {
		return invalid("passage source_hash is required")
	}
	return validateOptionalHash(input.SourceHash, "source_hash")
}

func (s *Service) GetNotesPassage(ctx context.Context, input NotesPassageInput) (NotesPassage, error) {
	if err := ValidateNotesPassageInput(input); err != nil {
		return NotesPassage{}, err
	}
	if s.store.db == nil {
		return NotesPassage{}, fmt.Errorf("knowledge store is not configured")
	}
	var out NotesPassage
	var index int
	var relativePath string
	var root SourceRoot
	var metadata json.RawMessage
	var contextRoot []byte
	var sourcePath string
	var currentSourcePath, currentBackendRoot, currentLogicalName string
	var syncedSource bool
	// A single statement sees citation identity and current access evidence in
	// one database snapshot. No source file, latest body or diagnostic fallback.
	// Consolidated publication numbers rows across segments, but its chunk hash
	// uses the segment-local index. Historical whole-document chunks share one
	// provenance group. Recover that index without re-reading or rehashing sources.
	err := s.store.db.QueryRowContext(ctx, `SELECT c.chunk_text,c.chunk_hash,
	 CASE WHEN c.metadata->>'text_source'='consolidated_text' THEN
	 (SELECT count(*) FROM knowledge.knowledge_chunks peer
	 WHERE peer.knowledge_object_version_id=c.knowledge_object_version_id
	 AND peer.chunk_index<=c.chunk_index
	 AND peer.metadata->'artifact_ids' IS NOT DISTINCT FROM c.metadata->'artifact_ids'
	 AND peer.metadata->'source_locators' IS NOT DISTINCT FROM c.metadata->'source_locators')
	 ELSE c.chunk_index END,c.structural_path,
	 (v.source_hash<>o.source_hash OR v.source_revision<>o.source_revision OR
	 EXISTS (SELECT 1 FROM knowledge.knowledge_object_versions newer
	 WHERE newer.knowledge_object_id=o.knowledge_object_id AND newer.version_number>v.version_number)),
	 o.relative_path,o.metadata,`+notesReadContextSQL("r", "o")+`,o.source_path,
	 COALESCE(current_version.source_path,''),r.backend_root_key,COALESCE(current_file.logical_name,''),o.storage_entry_id IS NULL
	 FROM knowledge.knowledge_chunks c
	 JOIN knowledge.knowledge_object_versions v ON v.knowledge_object_version_id=c.knowledge_object_version_id
	 JOIN knowledge.knowledge_objects o ON o.knowledge_object_id=c.knowledge_object_id AND o.knowledge_object_id=v.knowledge_object_id
	 JOIN knowledge.notes_source_roots r ON r.notes_source_root_id=o.notes_source_root_id
	 JOIN nodes.nodes n ON n.node_id=r.node_id AND n.node_id=o.source_node_id AND n.node_key=o.source_node_key
	 LEFT JOIN files.file_metadata current_file
	 ON current_file.object_id=o.metadata->'synced_object'->>'object_id'
	 AND current_file.latest_version_id=o.metadata->'synced_object'->>'object_version_id'
	 LEFT JOIN objects.object_versions current_version
	 ON current_version.object_version_id=current_file.latest_version_id
	 AND current_version.object_id=current_file.object_id
	 WHERE o.knowledge_object_id=$1 AND v.knowledge_object_version_id=$2
	 AND c.knowledge_chunk_id=$3 AND v.source_hash=$4
	 AND o.deleted_at IS NULL AND n.status='active'
	 AND c.status IN ('created','indexed')
	 AND COALESCE(c.metadata->>'text_source','') <> 'metadata_text'
	 AND octet_length(c.chunk_text) BETWEEN 1 AND $5
	 AND octet_length(c.structural_path)<=4096
	 AND `+visibleNotesCustodyObjectSQL("o", false)+`
	 AND `+notesLifecycleSelectionSQL("o", input.SourceLifecycle)+`
	 LIMIT 1`, input.KnowledgeObjectID, input.KnowledgeObjectVersionID, input.KnowledgeChunkID, input.SourceHash, MaxNotesPassageTextBytes).
		Scan(&out.Text, &out.ChunkHash, &index, &out.StructuralPath, &out.Historical,
			&relativePath, &metadata, &contextRoot, &sourcePath, &currentSourcePath, &currentBackendRoot, &currentLogicalName, &syncedSource)
	if err != nil {
		return NotesPassage{}, err
	}
	if err := json.Unmarshal(contextRoot, &root); err != nil {
		return NotesPassage{}, err
	}
	out.Custody, err = decodeNotesReadCustody(contextRoot, sourcePath)
	if err != nil {
		return NotesPassage{}, err
	}
	if !validNotesPassage(out.Text, out.ChunkHash, index, out.StructuralPath) || !notesPassageSourceAllowed(root, relativePath, metadata) {
		return NotesPassage{}, sql.ErrNoRows
	}
	if syncedSource {
		// Admission selects this exact current Objects version, while the saved
		// citation may name older text. Check current path policy without changing
		// retained response identity or requiring equality with its original path.
		currentRoot := root
		currentRoot.BackendRootKey = currentBackendRoot
		currentEntry := SyncedObjectEntry{SourcePath: currentSourcePath}
		if (root.RootKind == RootKindProjectNotes || root.RootKind == RootKindProjectMaterial) && !strings.HasPrefix(currentSourcePath, "watched-root://") {
			// Legacy project admission can use the authoritative file logical name
			// for non-watched sources. Never use it to rescue a watched-root path.
			if !notesCustodyText(currentLogicalName, 4096) {
				return NotesPassage{}, sql.ErrNoRows
			}
			currentEntry.LogicalName = currentLogicalName
		}
		currentRelative, reason := relativePathFromSyncedObject(currentRoot, currentEntry)
		if !notesCustodyText(currentSourcePath, 4096) || !notesCustodyText(currentBackendRoot, 4096) || reason != "" || !notesPassageSourceAllowed(currentRoot, currentRelative, metadata) {
			return NotesPassage{}, sql.ErrNoRows
		}
	}
	out.NotesPassageInput, out.SchemaVersion = input, "loom.notes.passage.v1"
	out.CurrentSourceContext = sourceContextForRoot(root, relativePath)
	contextBytes, err := json.Marshal(out.CurrentSourceContext)
	if err != nil || len(contextBytes) > 4096 || out.CurrentSourceContext.SourceCategory == "" {
		return NotesPassage{}, sql.ErrNoRows
	}
	return out, nil
}

func validNotesPassage(text, hash string, index int, locator string) bool {
	return len(text) <= MaxNotesPassageTextBytes && strings.TrimSpace(text) != "" && utf8.ValidString(text) &&
		index >= 0 && hash == hashChunkText(index, text) && len(locator) <= 4096 && utf8.ValidString(locator)
}

func notesPassageSourceAllowed(root SourceRoot, relativePath string, metadata json.RawMessage) bool {
	if ignoredNotesKnowledgeRelativePath(relativePath) || filepolicy.IsIndexingExcludedPath(relativePath) {
		return false
	}
	for _, part := range strings.Split(strings.ToLower(relativePath), "/") {
		if part == ".hermes" || part == ".codex" || part == ".orca" || part == ".repo" || part == "credentials" || part == "private_no_index" || strings.HasPrefix(part, ".env.") {
			return false
		}
	}
	m := jsonObject(metadata)
	for _, key := range []string{"storage_metadata", "file_metadata", "object_metadata", "version_metadata"} {
		value := m[key]
		if value == nil {
			continue
		}
		policy, ok := value.(map[string]any)
		if !ok || policy["private_no_index"] == true || policy["index_policy"] == "private_no_index" {
			return false
		}
	}
	return !expandedSourceExcluded(root, relativePath, metadata)
}
