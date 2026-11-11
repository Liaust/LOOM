package storagearchive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/lane"
	"loom.local/loom/internal/storagecatalog"
)

func (s RetainedRelocationService) ProveCommittedLane(ctx context.Context, ref WorkspacePathRef, bound string, inventory NoFollowInventory) (RetainedCustodyProof, error) {
	original := ref.AbsolutePath
	var custody, current string
	err := s.Catalog.DB.QueryRowContext(ctx, `SELECT custody_id,original_prefix,current_prefix FROM storage.custody_locations WHERE current_prefix=$1`, ref.AbsolutePath).Scan(&custody, &original, &current)
	if err != nil && err != sql.ErrNoRows {
		return RetainedCustodyProof{}, err
	}
	if err == nil && (bound != "" && bound != custody || !strings.HasPrefix(custody, "lane:")) {
		return RetainedCustodyProof{}, fmt.Errorf("committed Lane custody identity changed")
	}
	rel, err := filepath.Rel(s.Roots.StorageRoot, original)
	if err != nil {
		return RetainedCustodyProof{}, err
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 5 || parts[0] != "imports" {
		return RetainedCustodyProof{}, fmt.Errorf("exact original committed Lane collection required")
	}
	source, date, batch := parts[1], parts[2], parts[3]
	member := strings.Join(parts[4:], "/")
	id := storagecatalog.LaneCustodyIdentity(source, batch)
	if bound != "" && bound != id || custody != "" && custody != id {
		return RetainedCustodyProof{}, fmt.Errorf("committed Lane owner mismatch")
	}
	root := strings.Join(parts[:4], "/")
	meta, err := openHeldDirectoryChain(s.Roots.StorageRoot, root, nil)
	if err != nil {
		return RetainedCustodyProof{}, err
	}
	defer meta.Close()
	raw, present, err := readFileAtNoFollow(meta.ParentFD(), ".loom-lane-custody.json", 256<<20)
	if err != nil || !present {
		return RetainedCustodyProof{}, fmt.Errorf("original committed Lane manifest is unavailable: %v", err)
	}
	var manifest lane.CommittedCustodyManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(new(any)) != io.EOF || manifest.SchemaVersion != "loom.lane.custody_manifest.v1" || manifest.SourceNodeKey != source || manifest.BatchID != batch || manifest.AcceptedDate != date {
		return RetainedCustodyProof{}, fmt.Errorf("committed Lane manifest identity is invalid")
	}
	var canonical bytes.Buffer
	if err = json.NewEncoder(&canonical).Encode(manifest.Entries); err != nil || sha256Digest(canonical.Bytes()) != manifest.InventoryHash {
		return RetainedCustodyProof{}, fmt.Errorf("committed Lane manifest inventory hash differs")
	}
	if bound == "" {
		if _, _, err = lane.VerifyCommittedCustody(ctx, filepath.Join(s.Roots.StorageRoot, filepath.FromSlash(root)), source, batch, date, s.Catalog); err != nil {
			return RetainedCustodyProof{}, err
		}
	}
	// A prepared operation authenticates the saved member inventory directly;
	// ordinary member readers correctly refuse that in-flight custody identity.
	if err = verifyLaneCollectionInventory(manifest, member, inventory); err != nil {
		return RetainedCustodyProof{}, err
	}
	return RetainedCustodyProof{ID: id, Original: original, Digest: sha256Digest(raw)}, nil
}

func verifyLaneCollectionInventory(manifest lane.CommittedCustodyManifest, member string, inventory NoFollowInventory) error {
	if validateRetainedInventory(inventory) != nil {
		return fmt.Errorf("complete retained inventory required")
	}
	want := map[string]lane.CommittedCustodyEntry{}
	for _, item := range manifest.Entries {
		if item.RelativePath == member || strings.HasPrefix(item.RelativePath, member+"/") {
			want[strings.TrimPrefix(strings.TrimPrefix(item.RelativePath, member), "/")] = item
		}
	}
	root, ok := want[""]
	if !ok || root.Kind != "directory" || root.Mode != inventory.RootIdentity.Mode&0777 || root.ModifiedAt.UnixNano() != inventory.RootIdentity.ModifiedUnixNS || len(want) != len(inventory.Entries)+1 {
		return fmt.Errorf("choose a complete committed Lane directory; split ancestors need a narrower selection")
	}
	delete(want, "")
	for _, item := range inventory.Entries {
		original, ok := want[item.RelativePath]
		if !ok || original.Mode != item.Mode&0777 || original.ModifiedAt.UnixNano() != item.ModifiedUnixNS {
			return fmt.Errorf("Lane member metadata differs from original commit")
		}
		switch item.Kind {
		case InventoryEntryDirectory:
			if original.Kind != "directory" {
				return fmt.Errorf("Lane directory kind differs")
			}
		case InventoryEntryFile:
			if original.Kind != "regular_file" || original.SizeBytes != item.SizeBytes || "sha256:"+original.SHA256 != item.ContentDigest {
				return fmt.Errorf("Lane file differs from original commit")
			}
		case InventoryEntrySymlink:
			if original.Kind != "symlink" || original.LinkTarget != item.SymlinkTarget {
				return fmt.Errorf("Lane link differs from original commit")
			}
		default:
			return fmt.Errorf("unsupported Lane member kind")
		}
		delete(want, item.RelativePath)
	}
	if len(want) != 0 {
		return fmt.Errorf("Lane committed collection is incomplete")
	}
	return nil
}

func (s RetainedRelocationService) ProveSupported(ctx context.Context, workspace *WorkspaceMoveService, ref WorkspacePathRef, bound string, inventory NoFollowInventory) (RetainedCustodyProof, error) {
	if bound == "" {
		err := s.Catalog.DB.QueryRowContext(ctx, `SELECT custody_id FROM storage.custody_locations WHERE current_prefix=$1`, ref.AbsolutePath).Scan(&bound)
		if err != nil && err != sql.ErrNoRows {
			return RetainedCustodyProof{}, err
		}
	}
	if strings.HasPrefix(bound, "workspace:") || strings.HasPrefix(ref.RelativePath, "archive/projects/") {
		if workspace == nil {
			return RetainedCustodyProof{}, fmt.Errorf("workspace archive owner unavailable")
		}
		return s.ProveWorkspaceArchive(ctx, *workspace, ref, bound, inventory)
	}
	if strings.HasPrefix(bound, "lane:") {
		return s.ProveCommittedLane(ctx, ref, bound, inventory)
	}
	parts := strings.Split(ref.RelativePath, "/")
	if bound == "" && len(parts) >= 5 && parts[0] == "imports" {
		meta, err := openHeldDirectoryChain(s.Roots.StorageRoot, strings.Join(parts[:4], "/"), nil)
		if err != nil {
			return RetainedCustodyProof{}, err
		}
		present, err := namePresentNoFollow(meta.ParentFD(), ".loom-lane-custody.json")
		meta.Close()
		if err != nil {
			return RetainedCustodyProof{}, err
		}
		if present {
			return s.ProveCommittedLane(ctx, ref, bound, inventory)
		}
	}
	return s.ProveLegacyImport(ctx, ref, bound, inventory)
}
