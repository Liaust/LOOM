package projectexport

import (
	"encoding/json"
	"fmt"
)

const (
	ArchiveManifestPath    = ".loom/export/registrations.json"
	archiveManifestVersion = "loom.project_export.registrations.v1"
	maxManifestBytes       = 1 << 20
)

// RegistrationReferences is deliberately reference-only. It must never contain
// credential values; pass:// values remain in their source contracts unchanged.
type RegistrationReferences struct {
	ProjectRef             string   `json:"project_ref,omitempty"`
	ProjectID              string   `json:"project_id,omitempty"`
	ProjectScopeID         string   `json:"project_scope_id,omitempty"`
	HomeNodeID             string   `json:"home_node_id,omitempty"`
	ContractRegistrationID string   `json:"contract_registration_id,omitempty"`
	ContractHash           string   `json:"contract_hash,omitempty"`
	RegistrationRefs       []string `json:"registration_refs,omitempty"`
}

type archivalManifest struct {
	SchemaVersion string                 `json:"schema_version"`
	Project       RegistrationReferences `json:"project"`
}

func buildArchivalManifest(refs RegistrationReferences) ([]byte, error) {
	payload, err := json.MarshalIndent(archivalManifest{
		SchemaVersion: archiveManifestVersion,
		Project:       refs,
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode project registration references: %w", err)
	}
	payload = append(payload, '\n')
	if len(payload) > maxManifestBytes {
		return nil, fmt.Errorf("project registration reference manifest exceeds %d bytes", maxManifestBytes)
	}
	return payload, nil
}
