package storagecatalog

import (
	"encoding/json"
	"testing"
	"time"

	"loom.local/loom/internal/filesystemmeta"
)

func TestLaneCustodyMetadataPreservesOriginTimeInsteadOfDestinationTime(t *testing.T) {
	t.Parallel()

	originMtime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	destinationMtime := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	observation := &filesystemmeta.Observation{
		Kind:                filesystemmeta.ObjectKindRegularFile,
		SourceModifiedAt:    &originMtime,
		SourceModifiedBasis: filesystemmeta.SourceTimeBasisFilesystemMtime,
	}

	laneRaw, err := laneCustodyMetadata(LaneCustodyInput{
		SourceNodeKey:         "macbook",
		BatchID:               "lane_test",
		RelativeLanePath:      "note.md",
		FinalCustodyPath:      "/tmp/destination/note.md",
		AcceptedAt:            destinationMtime,
		FilesystemObservation: observation,
	}, "macbook/Lane/2026-08-17/lane_test/note.md")
	if err != nil {
		t.Fatal(err)
	}
	assertMetadataSourceMtime(t, laneRaw, originMtime)

}

func TestMainDocumentMetadataDistinguishesSourceAndImportTime(t *testing.T) {
	t.Parallel()

	sourceMtime := time.Date(2021, 4, 5, 6, 7, 8, 0, time.FixedZone("source", 2*60*60))
	importedAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	normalized, err := normalizeMainDocumentInput(MainDocumentInput{
		RelativePath:      "note.md",
		PhysicalPath:      "/tmp/note.md",
		ModifiedAt:        sourceMtime,
		ImportedAt:        importedAt,
		FileSizeBytes:     4,
		ChecksumAlgorithm: "sha256",
		ChecksumValue:     "abcd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.SourceModifiedAt == nil || !normalized.SourceModifiedAt.Equal(sourceMtime.UTC()) {
		t.Fatalf("normalized source time = %#v", normalized)
	}
	raw, err := mainDocumentMetadata(normalized)
	if err != nil {
		t.Fatal(err)
	}
	assertMetadataSourceMtime(t, raw, sourceMtime.UTC())
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["imported_at"] == metadata["source_modified_at"] {
		t.Fatalf("import time was relabelled as source time: %s", raw)
	}
}

func TestFilesystemObservationInputRawJSONCarriesSourceTimes(t *testing.T) {
	t.Parallel()

	modifiedAt := time.Date(2024, 2, 3, 4, 5, 6, 0, time.UTC)
	createdAt := modifiedAt.Add(-time.Hour)
	input := FilesystemObservationInputFromMeta(FilesystemObservationInputOptions{LogicalPath: "note.md"}, filesystemmeta.Observation{
		Kind:                filesystemmeta.ObjectKindRegularFile,
		SourceModifiedAt:    &modifiedAt,
		SourceModifiedBasis: filesystemmeta.SourceTimeBasisFilesystemMtime,
		SourceCreatedAt:     &createdAt,
		SourceCreatedBasis:  filesystemmeta.SourceTimeBasisFilesystemBirthtime,
	})
	var raw map[string]any
	if err := json.Unmarshal(input.RawJSON, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["source_modified_at"] == nil || raw["source_created_at"] == nil {
		t.Fatalf("source times missing from raw observation: %s", input.RawJSON)
	}
}

func assertMetadataSourceMtime(t *testing.T, raw json.RawMessage, want time.Time) {
	t.Helper()
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatal(err)
	}
	got, ok := metadata["source_modified_at"].(string)
	if !ok {
		t.Fatalf("source_modified_at missing from %s", raw)
	}
	parsed, err := time.Parse(time.RFC3339Nano, got)
	if err != nil {
		t.Fatalf("parse source_modified_at %q: %v", got, err)
	}
	if !parsed.Equal(want) || metadata["source_modified_basis"] != filesystemmeta.SourceTimeBasisFilesystemMtime {
		t.Fatalf("source time = %s (%v), want %s (%s)", parsed, metadata["source_modified_basis"], want, raw)
	}
}
