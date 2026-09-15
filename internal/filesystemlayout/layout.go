package filesystemlayout

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	DefaultServiceRoot            = "/srv/loom"
	DefaultDataRoot               = "/var/lib/loom"
	DefaultStorageRoot            = "/srv/loom/storage"
	DefaultImportsRoot            = "/srv/loom/storage/imports"
	DefaultUserBackupsRoot        = "/srv/loom/storage/backups"
	DefaultArchiveRoot            = "/srv/loom/storage/archive"
	DefaultGeneratedRoot          = "/var/lib/loom/generated"
	DefaultStorageRetentionRoot   = "/var/lib/loom/storage-retention"
	DefaultOperationalBackupsRoot = "/var/lib/loom/backups/main"
)

// Root is a validated absolute filesystem ownership root.
type Root string

func (r Root) String() string {
	return string(r)
}

// Join resolves a relative path below the root and rejects escapes.
func (r Root) Join(parts ...string) (string, error) {
	root := string(r)
	if root == "" {
		return "", fmt.Errorf("filesystem root is empty")
	}
	for _, part := range parts {
		if filepath.IsAbs(part) {
			return "", fmt.Errorf("path component %q must be relative", part)
		}
	}
	joined := filepath.Join(append([]string{root}, parts...)...)
	if !within(joined, root) {
		return "", fmt.Errorf("path %q escapes root %q", joined, root)
	}
	return joined, nil
}

type Layout struct {
	ServiceRoot             Root
	BoxRoot                 Root
	AgentsRoot              Root
	StorageRoot             Root
	ImportsRoot             Root
	UserBackupsRoot         Root
	ArchiveRoot             Root
	DataRoot                Root
	BoxStateRoot            Root
	GeneratedRoot           Root
	NotesProjectionRoot     Root
	StorageRetentionRoot    Root
	OperationalBackupsRoot  Root
	LegacySplitRoots        bool
	ExternalNotesProjection bool

	// DeprecatedStorageExportRoot and DeprecatedMainDocumentsRoot are migration
	// inputs only. New path selection must use the canonical roots above.
	DeprecatedStorageExportRoot Root
	DeprecatedMainDocumentsRoot Root
}

type Options struct {
	ServiceRoot             string
	BoxRoot                 string
	AgentsRoot              string
	StorageRoot             string
	ImportsRoot             string
	UserBackupsRoot         string
	ArchiveRoot             string
	DataRoot                string
	BoxStateRoot            string
	GeneratedRoot           string
	NotesProjectionRoot     string
	StorageRetentionRoot    string
	OperationalBackupsRoot  string
	LegacySplitRoots        bool
	ExternalNotesProjection bool

	DeprecatedStorageExportRoot string
	DeprecatedMainDocumentsRoot string
}

func Default() Layout {
	layout, err := New(Options{})
	if err != nil {
		panic(err)
	}
	return layout
}

func New(opts Options) (Layout, error) {
	serviceRoot := firstNonEmpty(opts.ServiceRoot, DefaultServiceRoot)
	dataRoot := firstNonEmpty(opts.DataRoot, DefaultDataRoot)
	storageRoot := firstNonEmpty(opts.StorageRoot, filepath.Join(serviceRoot, "storage"))
	generatedRoot := firstNonEmpty(opts.GeneratedRoot, filepath.Join(dataRoot, "generated"))

	layout := Layout{
		ServiceRoot:                 Root(serviceRoot),
		BoxRoot:                     Root(firstNonEmpty(opts.BoxRoot, filepath.Join(serviceRoot, "box"))),
		AgentsRoot:                  Root(firstNonEmpty(opts.AgentsRoot, filepath.Join(serviceRoot, "agents"))),
		StorageRoot:                 Root(storageRoot),
		ImportsRoot:                 Root(firstNonEmpty(opts.ImportsRoot, filepath.Join(storageRoot, "imports"))),
		UserBackupsRoot:             Root(firstNonEmpty(opts.UserBackupsRoot, filepath.Join(storageRoot, "backups"))),
		ArchiveRoot:                 Root(firstNonEmpty(opts.ArchiveRoot, filepath.Join(storageRoot, "archive"))),
		DataRoot:                    Root(dataRoot),
		BoxStateRoot:                Root(firstNonEmpty(opts.BoxStateRoot, filepath.Join(dataRoot, "box-state"))),
		GeneratedRoot:               Root(generatedRoot),
		NotesProjectionRoot:         Root(firstNonEmpty(opts.NotesProjectionRoot, filepath.Join(generatedRoot, "notes"))),
		StorageRetentionRoot:        Root(firstNonEmpty(opts.StorageRetentionRoot, filepath.Join(dataRoot, "storage-retention"))),
		OperationalBackupsRoot:      Root(firstNonEmpty(opts.OperationalBackupsRoot, filepath.Join(dataRoot, "backups", "main"))),
		LegacySplitRoots:            opts.LegacySplitRoots,
		ExternalNotesProjection:     opts.ExternalNotesProjection,
		DeprecatedStorageExportRoot: Root(strings.TrimSpace(opts.DeprecatedStorageExportRoot)),
		DeprecatedMainDocumentsRoot: Root(strings.TrimSpace(opts.DeprecatedMainDocumentsRoot)),
	}
	if err := layout.Validate(); err != nil {
		return Layout{}, err
	}
	return layout, nil
}

func (l Layout) Validate() error {
	paths := map[string]Root{
		"service_root":             l.ServiceRoot,
		"box_root":                 l.BoxRoot,
		"agents_root":              l.AgentsRoot,
		"storage_root":             l.StorageRoot,
		"imports_root":             l.ImportsRoot,
		"user_backups_root":        l.UserBackupsRoot,
		"archive_root":             l.ArchiveRoot,
		"data_root":                l.DataRoot,
		"box_state_root":           l.BoxStateRoot,
		"generated_root":           l.GeneratedRoot,
		"notes_projection_root":    l.NotesProjectionRoot,
		"storage_retention_root":   l.StorageRetentionRoot,
		"operational_backups_root": l.OperationalBackupsRoot,
	}
	for name, root := range paths {
		if err := validateRoot(name, root); err != nil {
			return err
		}
	}
	for name, root := range map[string]Root{
		"deprecated_storage_export_root": l.DeprecatedStorageExportRoot,
		"deprecated_main_documents_root": l.DeprecatedMainDocumentsRoot,
	} {
		if root != "" {
			if err := validateRoot(name, root); err != nil {
				return err
			}
		}
	}

	if !l.LegacySplitRoots {
		if err := requireWithin("imports_root", l.ImportsRoot, "storage_root", l.StorageRoot); err != nil {
			return err
		}
		if err := requireWithin("user_backups_root", l.UserBackupsRoot, "storage_root", l.StorageRoot); err != nil {
			return err
		}
		if err := requireWithin("archive_root", l.ArchiveRoot, "storage_root", l.StorageRoot); err != nil {
			return err
		}
	}
	legacyBoxStateRoot := filepath.Join(l.BoxRoot.String(), ".loom", "state")
	if !l.LegacySplitRoots || l.BoxStateRoot.String() != legacyBoxStateRoot {
		if err := requireWithin("box_state_root", l.BoxStateRoot, "data_root", l.DataRoot); err != nil {
			return err
		}
	}
	if err := requireWithin("generated_root", l.GeneratedRoot, "data_root", l.DataRoot); err != nil {
		return err
	}
	if !l.LegacySplitRoots && !l.ExternalNotesProjection {
		if err := requireWithin("notes_projection_root", l.NotesProjectionRoot, "generated_root", l.GeneratedRoot); err != nil {
			return err
		}
	}
	if err := requireWithin("operational_backups_root", l.OperationalBackupsRoot, "data_root", l.DataRoot); err != nil {
		return err
	}

	if err := disjoint(map[string]Root{
		"box_root":     l.BoxRoot,
		"agents_root":  l.AgentsRoot,
		"storage_root": l.StorageRoot,
		"data_root":    l.DataRoot,
	}); err != nil {
		return err
	}
	if err := disjoint(map[string]Root{
		"imports_root":      l.ImportsRoot,
		"user_backups_root": l.UserBackupsRoot,
		"archive_root":      l.ArchiveRoot,
	}); err != nil {
		return err
	}
	if err := disjoint(map[string]Root{
		"box_state_root":           l.BoxStateRoot,
		"generated_root":           l.GeneratedRoot,
		"storage_retention_root":   l.StorageRetentionRoot,
		"operational_backups_root": l.OperationalBackupsRoot,
	}); err != nil {
		return err
	}

	if legacy := l.DeprecatedStorageExportRoot.String(); legacy != "" {
		for name, root := range paths {
			if name == "data_root" || name == "service_root" {
				continue
			}
			if within(root.String(), legacy) {
				return fmt.Errorf("%s %q must not be inside deprecated generated export %q", name, root, legacy)
			}
		}
	}
	return nil
}

func (l Layout) SafeFields() map[string]string {
	return map[string]string{
		"service_root":                            l.ServiceRoot.String(),
		"box_root":                                l.BoxRoot.String(),
		"agents_root":                             l.AgentsRoot.String(),
		"storage_root":                            l.StorageRoot.String(),
		"imports_root":                            l.ImportsRoot.String(),
		"user_backups_root":                       l.UserBackupsRoot.String(),
		"archive_root":                            l.ArchiveRoot.String(),
		"box_state_root":                          l.BoxStateRoot.String(),
		"generated_root":                          l.GeneratedRoot.String(),
		"notes_projection_root":                   l.NotesProjectionRoot.String(),
		"storage_retention_root":                  l.StorageRetentionRoot.String(),
		"operational_backups_root":                l.OperationalBackupsRoot.String(),
		"legacy_split_roots":                      strconv.FormatBool(l.LegacySplitRoots),
		"external_notes_projection_compatibility": strconv.FormatBool(l.ExternalNotesProjection),
		"deprecated_storage_export_root":          l.DeprecatedStorageExportRoot.String(),
		"deprecated_main_documents_root":          l.DeprecatedMainDocumentsRoot.String(),
		"storage_export_migration_input_only":     fmt.Sprintf("%t", l.DeprecatedStorageExportRoot != ""),
		"main_documents_migration_input_only":     fmt.Sprintf("%t", l.DeprecatedMainDocumentsRoot != ""),
		"legacy_box_state_compatibility_active":   fmt.Sprintf("%t", l.LegacySplitRoots && l.BoxStateRoot.String() == filepath.Join(l.BoxRoot.String(), ".loom", "state")),
	}
}

func validateRoot(name string, root Root) error {
	raw := root.String()
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if strings.ContainsAny(raw, "\x00\r\n") {
		return fmt.Errorf("%s contains characters unsafe for installed configuration", name)
	}
	if !filepath.IsAbs(raw) {
		return fmt.Errorf("%s %q must be absolute", name, raw)
	}
	if filepath.Clean(raw) != raw {
		return fmt.Errorf("%s %q must be clean", name, raw)
	}
	return nil
}

func requireWithin(childName string, child Root, parentName string, parent Root) error {
	if child == parent || !within(child.String(), parent.String()) {
		return fmt.Errorf("%s %q must be a proper descendant of %s %q", childName, child, parentName, parent)
	}
	return nil
}

func disjoint(paths map[string]Root) error {
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, leftName := range names {
		for _, rightName := range names[i+1:] {
			left := paths[leftName].String()
			right := paths[rightName].String()
			if within(left, right) || within(right, left) {
				return fmt.Errorf("filesystem ownership roots overlap: %s=%q %s=%q", leftName, left, rightName, right)
			}
		}
	}
	return nil
}

func within(path string, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
