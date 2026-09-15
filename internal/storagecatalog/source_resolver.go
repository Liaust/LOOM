package storagecatalog

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	PhysicalRefClassCurrentSource    = "current_source"
	PhysicalRefClassCanonicalCustody = "canonical_custody"
	PhysicalRefClassRetentionCopy    = "retention_copy"
	PhysicalRefClassArchiveCopy      = "archive_copy"
	PhysicalRefClassBackupArtifact   = "backup_artifact"
	PhysicalRefClassCloudCopy        = "cloud_copy"
)

type ResolvePhysicalRefOptions struct {
	LocalOnly    bool
	ExcludeKinds []string
}

func ReadablePhysicalRefKinds() []string {
	return []string{
		PhysicalRefKindLocalPath,
		PhysicalRefKindObjectBlob,
		PhysicalRefKindBackupArtifact,
		PhysicalRefKindDropzoneFile,
		PhysicalRefKindLaneFile,
		PhysicalRefKindRetentionPayload,
		PhysicalRefKindArchiveFile,
	}
}

func IsAvailablePhysicalRef(ref PhysicalRef) bool {
	return PhysicalRefAvailable(ref)
}

func IsFilesystemPhysicalRefKind(kind string) bool {
	switch kind {
	case PhysicalRefKindLocalPath,
		PhysicalRefKindObjectBlob,
		PhysicalRefKindDropzoneFile,
		PhysicalRefKindLaneFile,
		PhysicalRefKindRetentionPayload,
		PhysicalRefKindArchiveFile:
		return true
	default:
		return false
	}
}

func BackupArtifactContentMember(ref PhysicalRef) string {
	metadata := map[string]any{}
	if len(ref.Metadata) > 0 {
		_ = json.Unmarshal(ref.Metadata, &metadata)
	}
	if value, ok := metadata["content_member"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return "content"
}

// PreferredReadablePhysicalRefs is retained for existing catalog consumers.
// It now shares the canonical availability and class resolver.
func PreferredReadablePhysicalRefs(refs []PhysicalRef) []PhysicalRef {
	return ResolvePhysicalRefs(refs, ResolvePhysicalRefOptions{LocalOnly: true})
}

// ClassifyPhysicalRef maps every catalog ref kind to its storage role. The
// mapping is deliberately centralized so readers do not invent their own
// custody/copy preference order.
func ClassifyPhysicalRef(ref PhysicalRef) (string, bool) {
	switch ref.RefKind {
	case PhysicalRefKindLocalPath, PhysicalRefKindExternalURI:
		return PhysicalRefClassCurrentSource, true
	case PhysicalRefKindDropzoneFile, PhysicalRefKindLaneFile:
		return PhysicalRefClassCanonicalCustody, true
	case PhysicalRefKindObjectBlob, PhysicalRefKindRetentionPayload:
		return PhysicalRefClassRetentionCopy, true
	case PhysicalRefKindArchiveFile:
		return PhysicalRefClassArchiveCopy, true
	case PhysicalRefKindBackupArtifact:
		return PhysicalRefClassBackupArtifact, true
	case PhysicalRefKindCloudObject:
		return PhysicalRefClassCloudCopy, true
	default:
		return "", false
	}
}

func PhysicalRefAvailable(ref PhysicalRef) bool {
	return ref.Status == "" || ref.Status == PhysicalRefStatusAvailable
}

// ResolvePhysicalRefs returns deterministic, best-first candidates. It does
// not touch the filesystem; consumers remain responsible for proving that a
// candidate is readable for their operation.
func ResolvePhysicalRefs(refs []PhysicalRef, opts ResolvePhysicalRefOptions) []PhysicalRef {
	excluded := make(map[string]struct{}, len(opts.ExcludeKinds))
	for _, kind := range opts.ExcludeKinds {
		excluded[kind] = struct{}{}
	}
	resolved := make([]PhysicalRef, 0, len(refs))
	for _, ref := range refs {
		if _, skip := excluded[ref.RefKind]; skip {
			continue
		}
		if !PhysicalRefAvailable(ref) {
			continue
		}
		if _, ok := ClassifyPhysicalRef(ref); !ok {
			continue
		}
		if opts.LocalOnly {
			if _, ok := PhysicalRefLocalPath(ref); !ok {
				continue
			}
		}
		resolved = append(resolved, ref)
	}
	sort.SliceStable(resolved, func(i, j int) bool {
		left := physicalRefPriority(resolved[i])
		right := physicalRefPriority(resolved[j])
		if left != right {
			return left < right
		}
		if !resolved[i].UpdatedAt.Equal(resolved[j].UpdatedAt) {
			return resolved[i].UpdatedAt.After(resolved[j].UpdatedAt)
		}
		return resolved[i].StoragePhysicalRefID < resolved[j].StoragePhysicalRefID
	})
	return resolved
}

func PhysicalRefLocalPath(ref PhysicalRef) (string, bool) {
	if ref.RefKind == PhysicalRefKindExternalURI || ref.RefKind == PhysicalRefKindCloudObject {
		return "", false
	}
	raw := ref.URI
	if strings.TrimSpace(raw) == "" {
		return "", false
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw), true
	}
	if strings.HasPrefix(raw, "file://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host != "" || strings.TrimSpace(parsed.Path) == "" {
			return "", false
		}
		pathValue, err := url.PathUnescape(parsed.Path)
		if err != nil || !filepath.IsAbs(pathValue) {
			return "", false
		}
		return filepath.Clean(pathValue), true
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", false
		}
		return filepath.Clean(filepath.Join(home, strings.TrimPrefix(raw, "~/"))), true
	}
	return "", false
}

func PhysicalRefRetainsPayload(entry Entry, ref PhysicalRef) bool {
	if !PhysicalRefAvailable(ref) {
		return false
	}
	class, ok := ClassifyPhysicalRef(ref)
	if !ok || class == PhysicalRefClassCurrentSource {
		return ref.RefKind == PhysicalRefKindLocalPath && entry.StorageClass != StorageClassMainDocument && entry.SourceArea != SourceAreaMainDocuments
	}
	return class != PhysicalRefClassCloudCopy || ref.RefKind == PhysicalRefKindCloudObject
}

func physicalRefPriority(ref PhysicalRef) int {
	class, _ := ClassifyPhysicalRef(ref)
	switch class {
	case PhysicalRefClassCurrentSource:
		return 0
	case PhysicalRefClassCanonicalCustody:
		return 1
	case PhysicalRefClassRetentionCopy:
		return 2
	case PhysicalRefClassArchiveCopy:
		return 3
	case PhysicalRefClassBackupArtifact:
		return 4
	case PhysicalRefClassCloudCopy:
		return 5
	default:
		return 6
	}
}
