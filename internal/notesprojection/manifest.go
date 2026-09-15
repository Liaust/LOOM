package notesprojection

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

func NewManifest(root string, generatedAt time.Time, entries []ProjectionEntry, findings []Finding) Manifest {
	entries = append([]ProjectionEntry(nil), entries...)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ProjectedPath == entries[j].ProjectedPath {
			return entries[i].KnowledgeObjectID < entries[j].KnowledgeObjectID
		}
		return entries[i].ProjectedPath < entries[j].ProjectedPath
	})
	return Manifest{
		SchemaVersion:  ManifestSchemaVersion,
		ProjectionRoot: root,
		GeneratedAt:    generatedAt.UTC(),
		ReadOnly:       true,
		Counts:         CountEntries(entries),
		Entries:        entries,
		Findings:       append([]Finding(nil), findings...),
	}
}

func CountEntries(entries []ProjectionEntry) Counts {
	counts := Counts{Entries: len(entries)}
	for _, entry := range entries {
		switch entry.Status {
		case ProjectionStatusMaterialized:
			counts.Materialized++
		case ProjectionStatusMissing:
			counts.Missing++
		case ProjectionStatusSkipped:
			counts.Skipped++
		}
	}
	return counts
}

func ReadManifest(root string) (Manifest, bool, error) {
	payload, err := os.ReadFile(manifestPath(root))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Manifest{}, false, nil
		}
		return Manifest{}, false, err
	}
	var manifest Manifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return Manifest{}, false, err
	}
	return manifest, true, nil
}

func writeManifest(root string, manifest Manifest) error {
	if err := prepareProjectionDir(root, filepath.Join(root, ".loom")); err != nil {
		return fmt.Errorf("create projection metadata directory: %w", err)
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode projection manifest: %w", err)
	}
	payload = append(payload, '\n')
	if err := writeFileAtomic(root, manifestPath(root), payload, 0o444); err != nil {
		return fmt.Errorf("write projection manifest: %w", err)
	}
	if err := writeFileAtomic(root, readmePath(root), []byte(readmeText()), 0o444); err != nil {
		return fmt.Errorf("write projection readme: %w", err)
	}
	return nil
}

func manifestPath(root string) string {
	return filepath.Join(root, filepath.FromSlash(ManifestRelativePath))
}

func readmePath(root string) string {
	return filepath.Join(root, filepath.FromSlash(ReadmeRelativePath))
}

func readmeText() string {
	return `LOOM notes projection

This folder is generated from LOOM knowledge index source objects.
Do not edit files here directly. Raw filesystem edits are unsupported and may be overwritten.

Use LOOM notes/project operations to change authoritative notes at their source.
`
}
