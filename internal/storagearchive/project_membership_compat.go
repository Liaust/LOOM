package storagearchive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"loom.local/loom/internal/boxregistry"
)

// Reconciliation adds attribution to an existing Personal project row, not a
// new archive lineage. Call only on canonical comparison copies; signed plans
// and current project records retain their original bytes and timestamps.
func normalizeLegacyProjectMembership(archive ProjectPhysicalArchivePlan, roots TrustedWorkspaceRoots, wantMetadata json.RawMessage, wantUpdatedAt, wantRepositoryUpdatedAt time.Time, metadata *json.RawMessage, updatedAt, repositoryUpdatedAt *time.Time) error {
	if err := validateWorkspaceBoxBinding(archive.Workspace.BoxID, roots); err != nil {
		return err
	}
	if bytes.Equal(*metadata, wantMetadata) {
		return nil
	}
	before, err := projectMembershipMetadata(wantMetadata)
	if err != nil {
		return err
	}
	after, err := projectMembershipMetadata(*metadata)
	if err != nil {
		return err
	}
	_, hadHome := before["home_box_id"]
	_, hadRevision := before["box_membership_revision"]
	home, homeOK := after["home_box_id"].(string)
	revision, revisionOK := after["box_membership_revision"].(string)
	if hadHome || hadRevision || !homeOK || !revisionOK || len(after) != len(before)+2 || !sha256DigestPattern.MatchString(revision) {
		return fmt.Errorf("project metadata changed outside additive legacy Box attribution")
	}
	for key, value := range before {
		actual, exists := after[key]
		if !exists || !reflect.DeepEqual(value, actual) {
			return fmt.Errorf("project metadata changed outside additive legacy Box attribution")
		}
	}
	if !roots.Primary || !boxregistry.IsBoxRef("box:"+roots.BoxID) || home != roots.BoxID {
		return fmt.Errorf("legacy Box attribution requires the independently bound Personal owner")
	}
	source := archive.Workspace.Source.Path
	if source.Root != WorkspaceRootBox || source.RelativePath == "" || source.RelativePath == "." || path.Clean(source.RelativePath) != source.RelativePath || path.IsAbs(source.RelativePath) || strings.Contains(source.RelativePath, "\\") || source.RelativePath == ".." || strings.HasPrefix(source.RelativePath, "../") || source.AbsolutePath != filepath.Join(roots.BoxRoot, filepath.FromSlash(source.RelativePath)) {
		return fmt.Errorf("legacy Box attribution does not match the signed Personal source path")
	}
	if err := ValidateProjectPhysicalArchivePlanEnvelope(archive, roots); err != nil {
		return fmt.Errorf("legacy Box attribution lacks exact signed Personal custody: %w", err)
	}
	// Both readers project the same projects row. A stale/mixed timestamp pair
	// cannot establish the metadata-only reconciliation accepted here.
	if updatedAt.IsZero() || !updatedAt.Equal(*repositoryUpdatedAt) || updatedAt.Before(wantUpdatedAt) || repositoryUpdatedAt.Before(wantRepositoryUpdatedAt) {
		return fmt.Errorf("legacy Box attribution has inconsistent project update evidence")
	}
	*metadata = append(json.RawMessage(nil), wantMetadata...)
	*updatedAt, *repositoryUpdatedAt = wantUpdatedAt, wantRepositoryUpdatedAt
	return nil
}

func projectMembershipMetadata(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid project metadata for legacy Box attribution: %w", err)
	}
	return value, nil
}
