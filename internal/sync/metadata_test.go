package sync

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
)

func metadataTestItem() PushBatchItem {
	p := MetadataObservation{SchemaVersion: MetadataObservationSchema, LocalObjectRef: ids.NewObjectID(), LocalVersionRef: ids.NewObjectVersionID(),
		ObjectID: ids.NewObjectID(), VersionID: ids.NewObjectVersionID(), HashURI: "sha256:" + strings.Repeat("a", 64),
		SourcePath: "watched-root://notes/cadence.md", SourceMtime: time.Date(2026, 9, 8, 19, 16, 16, 44448000, time.UTC)}
	return PushBatchItem{ItemKind: ItemKindObjectMetadata, StreamName: StreamObjectMetadata, LocalRef: ids.NewLocalOutboxID(), LocalSequence: 1, PayloadJSON: mustJSON(p)}
}

func TestSourceMetadataClosedContract(t *testing.T) {
	item := metadataTestItem()
	if _, err := decodeMetadataObservation(item); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*PushBatchItem){
		func(i *PushBatchItem) { i.StreamName = StreamEvents },
		func(i *PushBatchItem) { i.LocalSequence = 0 },
		func(i *PushBatchItem) { i.EventType = "arbitrary" },
		func(i *PushBatchItem) { i.PayloadJSON = json.RawMessage(`{"unknown":true}`) },
		func(i *PushBatchItem) { i.PayloadJSON = append(i.PayloadJSON, []byte(` {}`)...) },
		func(i *PushBatchItem) {
			i.PayloadJSON = json.RawMessage(strings.Replace(string(i.PayloadJSON), `{`, `{"schema_version":"duplicate",`, 1))
		},
		func(i *PushBatchItem) {
			i.PayloadJSON = json.RawMessage(strings.Replace(string(i.PayloadJSON), `"source_mtime"`, `"SOURCE_MTIME"`, 1))
		},
		func(i *PushBatchItem) {
			i.PayloadJSON = json.RawMessage(strings.Replace(string(i.PayloadJSON), `{`, `{"SCHEMA_VERSION":"ambiguous",`, 1))
		},
		func(i *PushBatchItem) {
			var p map[string]any
			json.Unmarshal(i.PayloadJSON, &p)
			p["content_base64"] = "YWJj"
			i.PayloadJSON = mustJSON(p)
		},
		func(i *PushBatchItem) {
			var p map[string]any
			json.Unmarshal(i.PayloadJSON, &p)
			p["object_id"] = "not-an-id"
			i.PayloadJSON = mustJSON(p)
		},
		func(i *PushBatchItem) {
			var p map[string]any
			json.Unmarshal(i.PayloadJSON, &p)
			p["hash_uri"] = "sha256:" + strings.Repeat("A", 64)
			i.PayloadJSON = mustJSON(p)
		},
	} {
		i := item
		i.PayloadJSON = append(json.RawMessage(nil), item.PayloadJSON...)
		change(&i)
		if _, err := decodeMetadataObservation(i); err == nil {
			t.Fatalf("accepted %#v", i)
		}
		if _, err := normalizePushBatchInput(PushBatchInput{Items: []PushBatchItem{i}}); err == nil {
			t.Fatalf("batch normalization erased invalid input: %#v", i)
		}
	}
	result := PushBatchResult{Items: []SyncItemResult{{LocalRef: item.LocalRef, LocalSequence: item.LocalSequence, ItemKind: item.ItemKind, StreamName: item.StreamName, PayloadHash: hashJSON(item.PayloadJSON)}}}
	if !metadataBatchReplayMatches(PushBatchInput{Items: []PushBatchItem{item}}, result) {
		t.Fatal("exact replay rejected")
	}
	item.LocalSequence++
	if metadataBatchReplayMatches(PushBatchInput{Items: []PushBatchItem{item}}, result) {
		t.Fatal("changed sequence accepted")
	}
}
