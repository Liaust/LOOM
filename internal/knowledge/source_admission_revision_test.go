package knowledge

import (
	"strings"
	"testing"
)

func TestSourceAuditRevisionRefreshGuards(t *testing.T) {
	node, project := "node", "project"
	root := SourceRoot{NotesSourceRootID: "root", RootKind: RootKindProjectMaterial, Status: SourceRootStatusActive,
		NodeID: &node, NodeKey: "main", ProjectID: &project, BackendRootKey: "key", SourcePath: "/project/notes", RootRelativePath: "notes"}
	metadata := func(hash, pattern string) []byte {
		return mustJSON(t, map[string]any{"registration_metadata": map[string]any{"knowledge_source": map[string]any{
			"schema_version": "project.contract.v0.5", "source_hash": "sha256:" + strings.Repeat(hash, 64), "include": []string{pattern},
		}}})
	}
	root.Metadata = metadata("a", "**/*")
	next := root
	next.Metadata = metadata("b", "**/*")
	if !sourceAuditRefreshEligible(root, next) {
		t.Fatal("audit-only successor did not preserve the binding")
	}
	before, _ := admissionSourceGeneration([]SourceRoot{root})
	after, _ := admissionSourceGeneration([]SourceRoot{next})
	if before != after {
		t.Fatal("audit-only successor restarted corpus admission")
	}
	for _, mutate := range []func(*SourceRoot){
		func(r *SourceRoot) { r.Metadata = metadata("b", "**/*.pdf") },
		func(r *SourceRoot) { r.SourcePath = "/other" },
		func(r *SourceRoot) { r.NodeKey = "other" },
		func(r *SourceRoot) { r.Status = SourceRootStatusDisabled },
	} {
		changed := next
		mutate(&changed)
		if sourceAuditRefreshEligible(root, changed) {
			t.Fatal("real policy or owner drift was treated as an audit update")
		}
	}
}
