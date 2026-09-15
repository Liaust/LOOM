package provenance

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed testdata/source_snapshot.json
var sourceSnapshotJSON []byte

type SourceFileDigest struct {
	Version int    `json:"version,omitempty"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Reason  string `json:"reason,omitempty"`
}

type UpstreamSnapshot struct {
	SchemaVersion         int                `json:"schema_version"`
	Repository            string             `json:"repository"`
	Commit                string             `json:"commit"`
	SourcePosture         string             `json:"source_posture"`
	LicenseDeclaration    string             `json:"license_declaration"`
	IncludedMigrations    []SourceFileDigest `json:"included_migrations"`
	IncludedFixtures      []SourceFileDigest `json:"included_fixtures"`
	ExcludedMigrations    []SourceFileDigest `json:"excluded_migrations"`
	PostgreSQLAdaptations []string           `json:"postgresql_adaptations"`
}

func LoadUpstreamSnapshot() (UpstreamSnapshot, error) {
	var snapshot UpstreamSnapshot
	if err := json.Unmarshal(sourceSnapshotJSON, &snapshot); err != nil {
		return UpstreamSnapshot{}, fmt.Errorf("decode embedded upstream snapshot: %w", err)
	}
	if snapshot.Commit != "93d3d94a76059802d00dbbe818cb188babcb18f4" {
		return UpstreamSnapshot{}, fmt.Errorf("unexpected upstream commit %q", snapshot.Commit)
	}
	return snapshot, nil
}

func UpstreamSnapshotJSON() []byte {
	return append([]byte(nil), sourceSnapshotJSON...)
}
