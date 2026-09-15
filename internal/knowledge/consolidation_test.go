package knowledge

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestConsolidatedSegmentsPreservePageCitationsAndRejectDrift(t *testing.T) {
	one, two := "First page: caf\u00e9.", "Second page: contention."
	result := ConsolidateCurrentArtifacts([]DerivedArtifact{
		{KnowledgeDerivedArtifactID: "a2", ArtifactKind: ArtifactKindEmbeddedText, SourceLocator: "page:2", TextContent: &two, Active: true},
		{KnowledgeDerivedArtifactID: "a1", ArtifactKind: ArtifactKindEmbeddedText, SourceLocator: "page:1", TextContent: &one, Active: true},
	}, 1000)
	metadata, _ := json.Marshal(map[string]any{"segments": result.Segments})
	artifact := DerivedArtifact{TextContent: &result.Text, Metadata: metadata}
	chunks, err := chunkInputsFromConsolidatedArtifact(artifact)
	if err != nil || len(chunks) != 2 {
		t.Fatalf("chunks=%#v err=%v", chunks, err)
	}
	for i, chunk := range chunks {
		segment := result.Segments[i]
		if chunk.StructuralPath != segment.SourceLocator || !reflect.DeepEqual(chunk.ArtifactIDs, []string{segment.ArtifactID}) || !reflect.DeepEqual(chunk.SourceLocators, []string{segment.SourceLocator}) || result.Text[chunk.StartOffset:chunk.EndOffset] != chunk.Text {
			t.Fatalf("lost page provenance: %#v", chunk)
		}
	}
	again, err := chunkInputsFromConsolidatedArtifact(artifact)
	if err != nil || !reflect.DeepEqual(again, chunks) {
		t.Fatal("segment replay changed")
	}
	for _, mutation := range []func([]ConsolidatedSegment){
		func(s []ConsolidatedSegment) { s[1].StartByte-- },
		func(s []ConsolidatedSegment) { s[1].EndByte++ },
		func(s []ConsolidatedSegment) { s[1].EndByte-- },
		func(s []ConsolidatedSegment) {
			s[0].EndByte = strings.Index(result.Text, "\u00e9") + 1
			s[1].StartByte = s[0].EndByte + 2
		},
		func(s []ConsolidatedSegment) { s[1].SourceLocator = "" },
	} {
		segments := append([]ConsolidatedSegment(nil), result.Segments...)
		mutation(segments)
		artifact.Metadata, _ = json.Marshal(map[string]any{"segments": segments})
		if _, err := chunkInputsFromConsolidatedArtifact(artifact); err == nil {
			t.Fatal("accepted changed segment boundaries or provenance")
		}
	}
	artifact.Metadata = json.RawMessage(`{"artifact_ids":["a1","a2"],"source_locators":["page:1","page:2"]}`)
	legacy, err := chunkInputsFromConsolidatedArtifact(artifact)
	if err != nil || len(legacy) != 1 || len(legacy[0].ArtifactIDs) != 2 {
		t.Fatalf("historical whole-document contract changed: %#v %v", legacy, err)
	}
}

func TestConsolidateCurrentArtifactsOrdersPagesAndDeduplicatesOCR(t *testing.T) {
	one, two, duplicate := "embedded one", "embedded two", " embedded one "
	result := ConsolidateCurrentArtifacts([]DerivedArtifact{{KnowledgeDerivedArtifactID: "a2", ArtifactKind: ArtifactKindEmbeddedText, SourceLocator: "page:2", TextContent: &two, Active: true}, {KnowledgeDerivedArtifactID: "o1", ArtifactKind: ArtifactKindOCRText, SourceLocator: "page:1", TextContent: &duplicate, Active: true}, {KnowledgeDerivedArtifactID: "a1", ArtifactKind: ArtifactKindEmbeddedText, SourceLocator: "page:1", TextContent: &one, Active: true}}, 1000)
	if result.Text != "embedded one\n\nembedded two" || result.Omitted != 1 {
		t.Fatalf("result=%#v", result)
	}
}
func TestConsolidateCurrentArtifactsBoundsOutput(t *testing.T) {
	value := "12345"
	result := ConsolidateCurrentArtifacts([]DerivedArtifact{{KnowledgeDerivedArtifactID: "a", ArtifactKind: ArtifactKindEmbeddedText, SourceLocator: "document", TextContent: &value, Active: true}}, 4)
	if result.Text != "" || result.Omitted != 1 {
		t.Fatalf("result=%#v", result)
	}
}
