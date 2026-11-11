package knowledge

import (
	"bytes"
	"encoding/json"
	"reflect"
)

func knowledgeProjectionObjectChanged(previous, next KnowledgeObject) bool {
	if knowledgeObjectContentChanged(previous, next) ||
		previous.NotesSourceRootID != next.NotesSourceRootID ||
		previous.SourceNodeKey != next.SourceNodeKey ||
		previous.SourcePath != next.SourcePath || previous.RelativePath != next.RelativePath ||
		previous.Title != next.Title || previous.FileClass != next.FileClass || previous.MimeType != next.MimeType ||
		!reflect.DeepEqual(previous.ProjectID, next.ProjectID) ||
		!reflect.DeepEqual(previous.StorageEntryID, next.StorageEntryID) ||
		!reflect.DeepEqual(previous.SizeBytes, next.SizeBytes) {
		return true
	}
	// Admission updates observation/processing metadata independently of payload
	// identity. Only the retained-source locator participates in this projection.
	type locator struct {
		Synced struct {
			BlobPath string `json:"blob_storage_path"`
		} `json:"synced_object"`
	}
	var old, current locator
	if json.Unmarshal(previous.Metadata, &old) != nil || json.Unmarshal(next.Metadata, &current) != nil {
		return true
	}
	return old != current
}

func knowledgeProjectionRootChanged(previous, next SourceRoot) bool {
	return previous.RootKind != next.RootKind || previous.NodeKey != next.NodeKey ||
		previous.BackendRootKey != next.BackendRootKey ||
		previous.DisplayName != next.DisplayName || previous.SourcePath != next.SourcePath ||
		previous.RootRelativePath != next.RootRelativePath || previous.Status != next.Status ||
		!reflect.DeepEqual(previous.ProjectID, next.ProjectID) ||
		!reflect.DeepEqual(previous.BoxWatchRootRegistrationID, next.BoxWatchRootRegistrationID) ||
		!reflect.DeepEqual(previous.ProjectWatchedRootRegistrationID, next.ProjectWatchedRootRegistrationID) ||
		!projectionMetadataEqual(previous.AuthorizationMetadata, next.AuthorizationMetadata) ||
		!projectionMetadataEqual(previous.Metadata, next.Metadata)
}

func projectionMetadataEqual(a, b json.RawMessage) bool {
	decode := func(raw json.RawMessage) (any, error) {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var value any
		err := d.Decode(&value)
		return value, err
	}
	old, oldErr := decode(a)
	current, currentErr := decode(b)
	return oldErr == nil && currentErr == nil && reflect.DeepEqual(old, current)
}
