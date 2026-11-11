package knowledge

// Registration membership proves the current receipt before this comparison
// can bridge older published metadata. Policy fields still require equality.
func declarationWatchSourceMatchesSQL(root, published, current string) string {
	return "projects.declaration_watch_source_matches(" + published + "," + current + "," + root + ".project_id," + root + ".node_id," + root + ".backend_root_key)"
}

func notesStatementWatchSourceMatchesSQL(root, published, current string) string {
	return "((" + published + ")=(" + current + ") OR (jsonb_typeof(" + published + ")='object' AND jsonb_typeof(" + current + ")='object' AND ((" + published + ")-'config_hash')=((" + current + ")-'config_hash') AND " + root + ".statement_read_sources @> jsonb_build_array(" + published + ")))"
}
