package knowledge

import (
	"strings"
	"testing"
)

func TestNotesCurrentSourceUsesSeparatePathLookups(t *testing.T) {
	query := notesSyncedCurrentSourceSQL("scope", "object", "version", "file")
	if strings.Count(query, "NOT EXISTS (") != 2 || strings.Contains(query, " OR ") {
		t.Fatal("explicit and legacy paths must not collapse into one broad OR lookup")
	}
	for _, guard := range []string{
		"FROM objects.object_versions newer_version",
		"newer_file.latest_version_id = newer_version.object_version_id",
		"newer_version.object_id = newer_object.object_id",
		"newer_scope.scope_id = scope.scope_id",
		"newer_scope.relevance_status = 'active'",
		"IS NOT DISTINCT FROM COALESCE(version.source_node_id, file.source_node_id)",
		"(newer_version.created_at, newer_version.object_version_id) > (version.created_at, version.object_version_id)",
	} {
		if strings.Count(query, guard) != 2 {
			t.Errorf("each branch must retain %q", guard)
		}
	}
	for _, guard := range []string{
		"newer_version.source_path = COALESCE(version.source_path, file.source_path, object.metadata->>'source_path', '')",
		"newer_version.source_path IS NULL AND COALESCE(newer_file.source_path, newer_object.metadata->>'source_path', '') = COALESCE(version.source_path, file.source_path, object.metadata->>'source_path', '')",
	} {
		if !strings.Contains(query, guard) {
			t.Errorf("missing source identity branch %q", guard)
		}
	}
}

func TestNotesRecoveryJournalPathsAreNotKnowledge(t *testing.T) {
	journal := ".loom-notes-" + strings.Repeat("a", 64)
	for _, name := range []string{journal, journal + "/original", "nested/" + journal + "/displaced", `nested\` + journal + `\intent`} {
		if !ignoredNotesKnowledgeRelativePath(name) {
			t.Errorf("journal path admitted: %s", name)
		}
	}
	for _, name := range []string{"notes.md", ".loom-notes-guide.md", "nested/loom-notes-" + strings.Repeat("a", 64) + "/original", journal + "x/original"} {
		if ignoredNotesKnowledgeRelativePath(name) {
			t.Errorf("ordinary path excluded: %s", name)
		}
	}
}
