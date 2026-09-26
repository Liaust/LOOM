package knowledge

import "time"

type NotesSearchFreshness struct {
	Current               bool       `json:"current"`
	IndexedSourceRevision string     `json:"indexed_source_revision"`
	IndexedSourceHash     string     `json:"indexed_source_hash"`
	LatestSourceRevision  string     `json:"latest_source_revision"`
	LatestSourceHash      string     `json:"latest_source_hash"`
	PublishedAt           *time.Time `json:"published_at,omitempty"`
	RefreshState          string     `json:"refresh_state"`
	SemanticLag           bool       `json:"semantic_lag"`
}

// Objects can accept a revision before the Notes admission worker observes it.
// Read freshness from that live identity; this does not grant source access.
func notesLatestSourceSQL(object, field string) string {
	value := "'object_version:' || latest_version.object_version_id"
	if field == "source_hash" {
		value = "COALESCE(NULLIF(latest_version.content_hash,''),latest_blob.hash_uri)"
	}
	return "COALESCE((SELECT " + value + ` FROM files.file_metadata latest_file
	 JOIN objects.object_versions latest_version ON latest_version.object_version_id=latest_file.latest_version_id
	 AND latest_version.object_id=latest_file.object_id
	 LEFT JOIN files.blobs latest_blob ON latest_blob.blob_id=latest_version.blob_id
	 WHERE ` + object + `.storage_entry_id IS NULL AND latest_file.object_id=` + object + `.metadata->'synced_object'->>'object_id'),` + object + "." + field + ")"
}

func notesPublicationCurrentSQL(object, version string) string {
	return "(COALESCE(" + version + ".source_revision," + object + ".source_revision)=" + notesLatestSourceSQL(object, "source_revision") +
		" AND COALESCE(" + version + ".source_hash," + object + ".source_hash)=" + notesLatestSourceSQL(object, "source_hash") + ")"
}

func notesPublishedVersionSQL(object, chunk, version string, semantic, strict bool) string {
	column := "lexical_version_id"
	if semantic {
		column = "semantic_version_id"
	}
	current := "(" + version + ".source_hash=" + object + ".source_hash AND " + version + ".source_revision=" + object + ".source_revision)"
	// Legacy writers retain current-revision search until unified processing owns it.
	result := "(" + chunk + ".knowledge_object_version_id IS NULL OR (" + chunk + ".knowledge_object_version_id=" + object + "." + column +
		" OR (" + object + "." + column + " IS NULL AND " + current + " AND NOT EXISTS (SELECT 1 FROM knowledge.pipeline_runs pr WHERE pr.knowledge_object_id=" + object + ".knowledge_object_id))))"
	if strict {
		result += " AND " + notesPublicationCurrentSQL(object, version)
	}
	if semantic {
		result += " AND (" + object + ".semantic_version_id IS NULL OR (ce.runtime_key=" + object + ".semantic_runtime_key AND ce.model_key=" + object + ".semantic_model_key AND ce.dimensions=" + object + ".semantic_dimensions))"
	}
	return result
}

func notesHybridVersionSQL(mode string) string {
	if mode != NotesSearchModeHybrid {
		return "TRUE"
	}
	return "(ko.lexical_version_id IS NULL OR kc.knowledge_object_version_id=ko.lexical_version_id)"
}

func notesSearchReadContextSQL(root, object, version string, semantic bool) string {
	publication := "lexical_published_at"
	if semantic {
		publication = "semantic_published_at"
	}
	return "(" + notesReadContextSQL(root, object) + ") || jsonb_build_object('freshness',jsonb_build_object(" +
		"'current'," + notesPublicationCurrentSQL(object, version) + "," +
		"'indexed_source_revision',COALESCE(" + version + ".source_revision," + object + ".source_revision)," +
		"'indexed_source_hash',COALESCE(" + version + ".source_hash," + object + ".source_hash)," +
		"'latest_source_revision'," + notesLatestSourceSQL(object, "source_revision") + ",'latest_source_hash'," + notesLatestSourceSQL(object, "source_hash") + "," +
		"'published_at'," + object + "." + publication + ",'semantic_lag',(" + object + ".lexical_version_id IS DISTINCT FROM " + object + ".semantic_version_id)," +
		"'refresh_state',CASE WHEN " + notesLatestSourceSQL(object, "source_revision") + "<>" + object + ".source_revision OR " + notesLatestSourceSQL(object, "source_hash") + "<>" + object + ".source_hash THEN 'pending' ELSE COALESCE((SELECT CASE WHEN (pr.source_revision<>" + object + ".source_revision OR pr.source_hash<>" + object + ".source_hash) AND pr.status IN ('complete','complete_with_warnings','failed','blocked_manual_action','stale') THEN 'pending' ELSE pr.status END FROM knowledge.pipeline_runs pr WHERE pr.knowledge_object_id=" + object + ".knowledge_object_id ORDER BY pr.generation DESC LIMIT 1),'not_scheduled') END))"
}
