package sync

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
)

func TestFileObservationContract(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	p := FileObservation{SchemaVersion: FileObservationSchema, LocalObjectRef: ids.NewObjectID(),
		RootKey: "notes", RelativePath: "study/note.md", Generation: "root-generation", Presence: "present",
		ObservedAt: now, SourceMtime: &now, SizeBytes: 20}
	item := PushBatchItem{LocalRef: ids.NewLocalOutboxID(), ItemKind: ItemKindObjectMetadata,
		StreamName: StreamObjectMetadata, LocalSequence: 1, PayloadJSON: mustJSON(p)}
	if _, err := normalizePushBatchInput(PushBatchInput{Items: []PushBatchItem{item}}); err != nil {
		t.Fatal(err)
	}
	for _, presence := range []string{"missing", "unavailable"} {
		p.Presence, p.SourceMtime = presence, nil
		if _, err := decodeFileObservation(PushBatchItem{LocalRef: item.LocalRef, StreamName: item.StreamName,
			LocalSequence: 2, PayloadJSON: mustJSON(p)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, change := range []func(*PushBatchItem){
		func(i *PushBatchItem) { i.StreamName = StreamEvents },
		func(i *PushBatchItem) { i.LocalSequence = 0 },
		func(i *PushBatchItem) { i.PayloadJSON = append(i.PayloadJSON, []byte(` {}`)...) },
		func(i *PushBatchItem) {
			i.PayloadJSON = json.RawMessage(strings.Replace(string(i.PayloadJSON), `{`, `{"presence":"missing",`, 1))
		},
		func(i *PushBatchItem) {
			i.PayloadJSON = json.RawMessage(strings.Replace(string(i.PayloadJSON), `"presence"`, `"PRESENCE"`, 1))
		},
		func(i *PushBatchItem) {
			var value map[string]any
			_ = json.Unmarshal(i.PayloadJSON, &value)
			value["relative_path"] = "../escape.md"
			i.PayloadJSON = mustJSON(value)
		},
		func(i *PushBatchItem) {
			var value map[string]any
			_ = json.Unmarshal(i.PayloadJSON, &value)
			value["content_base64"] = "not allowed"
			i.PayloadJSON = mustJSON(value)
		},
	} {
		i := item
		i.PayloadJSON = append(json.RawMessage(nil), item.PayloadJSON...)
		change(&i)
		if _, err := normalizePushBatchInput(PushBatchInput{Items: []PushBatchItem{i}}); err == nil {
			t.Fatalf("accepted invalid file observation: %s", i.PayloadJSON)
		}
	}
}
