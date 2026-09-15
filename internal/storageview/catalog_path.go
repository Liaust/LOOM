package storageview

import (
	"fmt"
	"path"
	"strings"

	"loom.local/loom/internal/storagecatalog"
)

// CatalogViewEntry presents one catalog row using the retired export's stable
// human path vocabulary without constructing a filesystem tree or synthetic
// _System entries.
func CatalogViewEntry(entry storagecatalog.Entry) (ViewEntry, error) {
	viewPath, err := CatalogPathForEntry(entry)
	if err != nil {
		return ViewEntry{}, err
	}
	entryKind := EntryKindFile
	if entry.FileClass == storagecatalog.FileClassDirectory {
		entryKind = EntryKindDirectory
	}
	permissions, writable, reason := PermissionsForPath(viewPath)
	return ViewEntry{
		ViewPath: viewPath, EntryKind: entryKind, StorageEntryID: entry.StorageEntryID,
		StorageClass: entry.StorageClass, SourceArea: entry.SourceArea, OriginNodeKey: entry.OriginNodeKey,
		LogicalPath: entry.LogicalPath, DisplayName: path.Base(viewPath), Permissions: permissions,
		Writable: writable, Generated: false, ReadOnlyReason: reason, SizeBytes: entry.SizeBytes,
		ChecksumAlgorithm: entry.ChecksumAlgorithm, ChecksumHex: entry.ChecksumHex, FileClass: entry.FileClass,
		ProcessingState: entry.ProcessingState, AvailabilityState: entry.AvailabilityState,
		RetentionState: entry.RetentionState, Metadata: entry.Metadata,
	}, nil
}

// CatalogPathForEntry returns the compatibility catalog path for one entry.
// It does not construct a browsing tree or synthesize _System status entries.
// New entries should carry CurrentViewPath as a path relative to their real
// canonical physical root; the legacy mapping remains readable for one compatibility
// release while catalog paths are migrated.
func CatalogPathForEntry(entry storagecatalog.Entry) (string, error) {
	if strings.TrimSpace(entry.CurrentViewPath) != "" {
		viewPath, err := SanitizeRelativePath(entry.CurrentViewPath)
		if err != nil {
			return "", err
		}
		return normalizeAcceptanceCatalogPath(viewPath), nil
	}

	logicalPath, err := SanitizeRelativePath(entry.LogicalPath)
	if err != nil {
		return "", fmt.Errorf("entry %s logical path invalid: %w", entry.StorageEntryID, err)
	}
	logicalPath = normalizeAcceptanceCatalogPath(logicalPath)
	nodeKey := SanitizeLabel(firstCatalogValue(entry.OriginNodeKey, "unknown-node"))
	switch {
	case entry.StorageClass == storagecatalog.StorageClassMainDocument || entry.SourceArea == storagecatalog.SourceAreaMainDocuments:
		return path.Join("main", "Documents", logicalPath), nil
	case entry.StorageClass == storagecatalog.StorageClassArchiveEntry || entry.SourceArea == storagecatalog.SourceAreaMainArchive:
		return path.Join("main", "Archive", logicalPath), nil
	case entry.StorageClass == storagecatalog.StorageClassDropzoneCustody || entry.SourceArea == storagecatalog.SourceAreaDropzone:
		return path.Join(nodeKey, "Dropzone", logicalPath), nil
	case entry.SourceArea == storagecatalog.SourceAreaProjects:
		projectKey := SanitizeLabel(firstCatalogValue(entry.WatchedRootKey, stringCatalogValue(entry.ProjectID), "project"))
		return path.Join(nodeKey, "Backups", "Projects", projectKey, "current", logicalPath), nil
	case entry.SourceArea == storagecatalog.SourceAreaNotes:
		return path.Join(nodeKey, "Backups", "Notes", "current", logicalPath), nil
	case entry.SourceArea == storagecatalog.SourceAreaDocuments:
		return path.Join(nodeKey, "Backups", "Documents", "current", logicalPath), nil
	case entry.SourceArea == storagecatalog.SourceAreaExternalWatchedRoot:
		rootKey := SanitizeLabel(firstCatalogValue(entry.WatchedRootKey, "external-root"))
		return path.Join(nodeKey, "Backups", "External Watched Roots", rootKey, "current", logicalPath), nil
	default:
		return path.Join(nodeKey, "Backups", "Other", "current", logicalPath), nil
	}
}

func normalizeAcceptanceCatalogPath(value string) string {
	value = strings.Trim(strings.ReplaceAll(value, "\\", "/"), "/")
	if value == "" {
		return value
	}
	segments := strings.Split(value, "/")
	for idx, segment := range segments {
		switch strings.ToLower(strings.TrimSpace(segment)) {
		case "loom acceptance", "loom-acceptance":
			segments[idx] = ".loom-acceptance"
		}
	}
	return strings.Join(segments, "/")
}

func firstCatalogValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringCatalogValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
