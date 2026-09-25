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
		result += " AND (" + chunk + ".knowledge_object_version_id IS NULL OR " + current + ")"
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
		"'current',(" + version + ".knowledge_object_version_id IS NULL OR (" + version + ".source_hash=" + object + ".source_hash AND " + version + ".source_revision=" + object + ".source_revision))," +
		"'indexed_source_revision',COALESCE(" + version + ".source_revision," + object + ".source_revision)," +
		"'indexed_source_hash',COALESCE(" + version + ".source_hash," + object + ".source_hash)," +
		"'latest_source_revision'," + object + ".source_revision,'latest_source_hash'," + object + ".source_hash," +
		"'published_at'," + object + "." + publication + ",'semantic_lag',(" + object + ".lexical_version_id IS DISTINCT FROM " + object + ".semantic_version_id)," +
		"'refresh_state',COALESCE((SELECT CASE WHEN (pr.source_revision<>" + object + ".source_revision OR pr.source_hash<>" + object + ".source_hash) AND pr.status IN ('complete','complete_with_warnings','failed','blocked_manual_action','stale') THEN 'pending' ELSE pr.status END FROM knowledge.pipeline_runs pr WHERE pr.knowledge_object_id=" + object + ".knowledge_object_id ORDER BY pr.generation DESC LIMIT 1),'not_scheduled')))"
}
