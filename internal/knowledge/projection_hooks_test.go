package knowledge

import (
	"encoding/json"
	"testing"
	"time"
)

func TestProjectionObjectDirtyOnlyForViewChanges(t *testing.T) {
	old := KnowledgeObject{KnowledgeObjectID: "object", NotesSourceRootID: "root", SourcePath: "/notes/a.md", RelativePath: "a.md", Title: "a", SourceHash: "hash", SourceRevision: "rev", Metadata: json.RawMessage(`{"synced_object":{"blob_storage_path":"/blob/a"}}`)}
	next := old
	next.LastSeenAt = time.Now()
	next.ProcessingState = "indexed"
	next.Metadata = json.RawMessage(`{"observed_at":"later","synced_object":{"blob_storage_path":"/blob/a"}}`)
	if knowledgeProjectionObjectChanged(old, next) {
		t.Fatal("unchanged observation must not create projection work")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*KnowledgeObject)
	}{
		{"bytes", func(o *KnowledgeObject) { o.SourceHash = "new" }},
		{"rename", func(o *KnowledgeObject) { o.RelativePath = "b.md" }},
		{"locator", func(o *KnowledgeObject) {
			o.Metadata = json.RawMessage(`{"synced_object":{"blob_storage_path":"/blob/b"}}`)
		}},
		{"title", func(o *KnowledgeObject) { o.Title = "renamed" }},
		{"withdrawal", func(o *KnowledgeObject) { now := time.Now(); o.DeletedAt = &now }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := old
			tc.mutate(&next)
			if !knowledgeProjectionObjectChanged(old, next) {
				t.Fatal("view change must create projection work")
			}
		})
	}
}

func TestProjectionRootDirtyIgnoresObservationClock(t *testing.T) {
	old := SourceRoot{RootKind: RootKindBoxNotes, NodeKey: "main", SourcePath: "/notes", Status: SourceRootStatusActive,
		AuthorizationMetadata: json.RawMessage(`{"read":true,"generation":9007199254740992}`), Metadata: json.RawMessage(`{"policy":{"enabled":true}}`)}
	next := old
	next.UpdatedAt = time.Now()
	next.AuthorizationMetadata = json.RawMessage(`{"generation":9007199254740992,"read":true}`)
	if knowledgeProjectionRootChanged(old, next) {
		t.Fatal("timestamp and JSON key order are not root changes")
	}
	next.AuthorizationMetadata = json.RawMessage(`{"generation":9007199254740993,"read":true}`)
	if !knowledgeProjectionRootChanged(old, next) {
		t.Fatal("distinct large identity must not compare equal")
	}
	next = old
	next.Status = SourceRootStatusDisabled
	if !knowledgeProjectionRootChanged(old, next) {
		t.Fatal("source policy withdrawal must invalidate projection")
	}
}
