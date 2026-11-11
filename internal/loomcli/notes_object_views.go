package loomcli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/knowledge"
)

type notesObjectSummary struct {
	SchemaVersion     string                                     `json:"schema_version"`
	KnowledgeObjectID string                                     `json:"knowledge_object_id"`
	FileID            string                                     `json:"file_id,omitempty"`
	RootID            string                                     `json:"notes_source_root_id"`
	Node              string                                     `json:"source_node_key,omitempty"`
	ProjectID         *string                                    `json:"project_id,omitempty"`
	Path              string                                     `json:"source_path,omitempty"`
	RelativePath      string                                     `json:"relative_path"`
	FileClass         string                                     `json:"file_class"`
	SourceHash        string                                     `json:"source_hash,omitempty"`
	SourceRevision    string                                     `json:"source_revision,omitempty"`
	ProcessingState   string                                     `json:"processing_state"`
	Extraction        knowledge.KnowledgeObjectExtractionSummary `json:"extraction"`
	LexicalVersionID  string                                     `json:"lexical_version_id,omitempty"`
	SemanticVersionID string                                     `json:"semantic_version_id,omitempty"`
	Lifecycle         knowledge.SourceLifecycle                  `json:"source_lifecycle,omitempty"`
	LastErrorCode     string                                     `json:"last_error_code,omitempty"`
	InspectCommand    string                                     `json:"inspect_command"`
}

func writeNotesObjectList(cmd *cobra.Command, objects []knowledge.KnowledgeObject, details bool) error {
	if details {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(objects)
	}
	items := make([]notesObjectSummary, 0, len(objects))
	for _, o := range objects {
		item := notesObjectSummary{SchemaVersion: "loom.notes.object-summary.v1", KnowledgeObjectID: o.KnowledgeObjectID,
			FileID: o.CanonicalFileID, RootID: o.NotesSourceRootID, Node: o.SourceNodeKey, ProjectID: o.ProjectID,
			Path: o.SourcePath, RelativePath: o.RelativePath, FileClass: o.FileClass, SourceHash: o.SourceHash,
			SourceRevision: o.SourceRevision, ProcessingState: o.ProcessingState, Extraction: knowledge.SummarizeKnowledgeObjectExtraction(o),
			LexicalVersionID: o.LexicalVersionID, SemanticVersionID: o.SemanticVersionID, LastErrorCode: o.LastErrorCode,
			InspectCommand: fmt.Sprintf("loom notes pipelines inspect %q --summary --json", o.KnowledgeObjectID)}
		if o.NotesCustodyContext != nil {
			item.Lifecycle = o.SourceLifecycle
		}
		items = append(items, item)
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(items)
}
