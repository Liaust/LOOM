package storagearchive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"loom.local/loom/internal/workspaceactions"
)

// Legacy imports predate Lane's committed-batch manifest. Their first explicit
// relocation adopts the complete current inventory, without inventing an old
// signed receipt. Later moves bind that same custody and inventory.
func (s RetainedRelocationService) ProveLegacyImport(ctx context.Context, ref WorkspacePathRef, bound string, inventory NoFollowInventory) (RetainedCustodyProof, error) {
	var proof RetainedCustodyProof
	related, err := workspaceactions.RelatedProjects(ctx, s.Catalog.DB, ref.AbsolutePath, "")
	if err != nil {
		return proof, err
	}
	if len(related) > 0 {
		return proof, fmt.Errorf("registered project content requires its lifecycle/restore owner")
	}
	var operation, current string
	err = s.Catalog.DB.QueryRowContext(ctx, `SELECT custody_id,original_prefix,current_prefix,relocation_id FROM storage.custody_locations WHERE current_prefix=$1`, ref.AbsolutePath).Scan(&proof.ID, &proof.Original, &current, &operation)
	if err == nil {
		if !strings.HasPrefix(proof.ID, "legacy-import:") || bound != "" && bound != proof.ID {
			return proof, fmt.Errorf("retained collection custody mismatch")
		}
		record, e := s.Catalog.RetainedRelocation(ctx, operation)
		if e != nil {
			return proof, e
		}
		var previous RetainedRelocationPlan
		if json.Unmarshal(record.Plan, &previous) != nil || record.State != "complete" || previous.Digest != record.Digest || relocationDigest(previous) != record.Digest || !retainedInventoriesMatch(previous.Inventory, inventory) {
			return proof, fmt.Errorf("retained collection differs from its completed relocation inventory")
		}
		proof.Digest = previous.OwnerDigest
		return proof, nil
	}
	if err != sql.ErrNoRows {
		return proof, err
	}
	parts := strings.Split(ref.RelativePath, "/")
	if len(parts) < 5 || parts[0] != "imports" {
		return proof, fmt.Errorf("only legacy import collections are admitted by this owner")
	}
	batch := strings.Join(parts[:4], "/")
	parent, err := openHeldDirectoryChain(s.Roots.StorageRoot, batch, nil)
	if err != nil {
		return proof, err
	}
	defer parent.Close()
	if present, err := namePresentNoFollow(parent.ParentFD(), ".loom-lane-custody.json"); err != nil {
		return proof, err
	} else if present {
		return proof, fmt.Errorf("committed Lane batches require their member-aware relocation reader")
	}
	if validateRetainedInventory(inventory) != nil {
		return proof, fmt.Errorf("complete current inventory required")
	}
	identity, _ := json.Marshal([]any{"legacy-import", ref.AbsolutePath, inventory.RootIdentity.DeviceID, inventory.RootIdentity.Inode})
	proof.Original = ref.AbsolutePath
	proof.Digest = sha256Digest(identity)
	proof.ID = "legacy-import:" + strings.TrimPrefix(proof.Digest, "sha256:")
	if bound != "" && bound != proof.ID {
		return proof, fmt.Errorf("legacy import identity changed")
	}
	return proof, nil
}

func retainedInventoriesMatch(previous, current NoFollowInventory) bool {
	if reflect.DeepEqual(previous, current) {
		return true
	}
	if previous.SchemaVersion != NoFollowInventorySchemaVersion || current.SchemaVersion != retainedInventorySchema {
		return false
	}
	current.SchemaVersion = NoFollowInventorySchemaVersion
	var err error
	current.Digest, err = noFollowInventoryDigest(current)
	return err == nil && ValidateNoFollowInventory(current) == nil && reflect.DeepEqual(previous, current)
}
