package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/ids"
	loomsync "loom.local/loom/internal/sync"
)

type SyncRepairPlan struct {
	Digest         string   `json:"digest"`
	MissingRecords []string `json:"missing_records"`
	MissingOutbox  []string `json:"missing_outbox"`
	Applied        bool     `json:"applied"`
}

func newSyncRepairCommand(opts *rootOptions) *cobra.Command {
	var apply, yes bool
	var digest string
	cmd := &cobra.Command{
		Use:   "repair-deletion-pairs",
		Short: "Plan recovery of existing local deletion-request queue pairs without submitting requests",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if apply && (!yes || digest == "") {
				return errors.New("apply requires --yes and --plan-hash")
			}
			if !apply && (yes || digest != "") {
				return errors.New("confirmation flags require --apply")
			}
			store, config, state, err := opts.loadAll()
			if err != nil {
				return err
			}
			plan, err := store.RepairSyncDeletionPairs(cmd.Context(), config, state, apply, digest)
			if err != nil {
				return err
			}
			return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), plan))
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "Apply the exact reviewed repair (default: plan only)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm local queue metadata repair")
	cmd.Flags().StringVar(&digest, "plan-hash", "", "Exact dry-run digest")
	return cmd
}

// Recovery reads only retained metadata. It never samples current source bytes
// to invent historical evidence, changes a target, or submits a remote action.
func (s Store) RepairSyncDeletionPairs(ctx context.Context, config Config, state State, apply bool, digest string) (SyncRepairPlan, error) {
	plan := SyncRepairPlan{MissingRecords: []string{}, MissingOutbox: []string{}}
	s, unlock, err := s.lockLocalSync(ctx)
	if err != nil {
		return plan, err
	}
	defer unlock()
	if state.NodeID == "" || config.NodeKey == "" {
		return plan, errors.New("repair requires the configured node identity")
	}
	root, err := filepath.EvalSymlinks(s.syncRoot())
	if err != nil || root != s.syncRoot() {
		return plan, errors.New("sync repair root must be real and canonical")
	}
	files := []string{localSyncObjectsFile, localSyncDeletionsFile, localSyncOutboxFile, localSyncCursorsFile}
	before := make(map[string][]byte, len(files))
	for _, name := range files {
		raw, err := readSyncRepairFile(filepath.Join(root, name))
		if err != nil {
			return plan, err
		}
		before[name] = raw
	}
	var objects []LocalSyncObject
	var deletions []LocalSyncDeletionRequest
	var outbox []LocalSyncOutboxItem
	var cursors []LocalSyncCursor
	if err := json.Unmarshal(before[localSyncObjectsFile], &objects); err != nil {
		return plan, err
	}
	if err := json.Unmarshal(before[localSyncDeletionsFile], &deletions); err != nil {
		return plan, err
	}
	if err := json.Unmarshal(before[localSyncOutboxFile], &outbox); err != nil {
		return plan, err
	}
	if err := json.Unmarshal(before[localSyncCursorsFile], &cursors); err != nil {
		return plan, err
	}
	var queued int64
	for _, cursor := range cursors {
		if cursor.StreamName == loomsync.StreamDeletionRequests {
			queued = cursor.LastQueuedSequence
		}
	}
	identity, _ := json.Marshal(struct {
		NodeID, NodeKey, Root string
		Files                 map[string][]byte
	}{state.NodeID, config.NodeKey, root, before})
	plan.Digest = rawBytesHashHex(identity)
	byRef := map[string]LocalSyncDeletionRequest{}
	for _, d := range deletions {
		if d.LocalSequence > queued {
			return plan, errors.New("deletion cursor is behind retained evidence")
		}
		if _, exists := byRef[d.LocalDeletionID]; exists {
			return plan, errors.New("duplicate deletion identity")
		}
		byRef[d.LocalDeletionID] = d
	}
	paired := map[string]bool{}
	ids := map[string]bool{}
	for _, item := range outbox {
		if ids[item.LocalOutboxID] {
			return plan, errors.New("duplicate outbox identity")
		}
		ids[item.LocalOutboxID] = true
		if item.ItemKind != loomsync.ItemKindDeletionRequest {
			continue
		}
		if item.LocalSequence > queued {
			return plan, errors.New("deletion cursor is behind retained outbox evidence")
		}
		if paired[item.LocalRef] {
			return plan, errors.New("duplicate deletion outbox relation")
		}
		paired[item.LocalRef] = true
		if d, exists := byRef[item.LocalRef]; exists {
			if item.PayloadHash != rawJSONHash(item.PayloadJSON) || !syncRepairPayloadEqual(item.PayloadJSON, localDeletionPayload(d)) || item.LocalSequence != d.LocalSequence {
				return plan, errors.New("existing deletion pair evidence differs")
			}
			continue
		}
		if item.Status != localSyncStatusPending || item.StreamName != loomsync.StreamDeletionRequests || item.SyncedAt != nil || item.GlobalRef != "" || item.CreatedAt.IsZero() || item.LocalSequence <= 0 || item.PayloadHash != rawJSONHash(item.PayloadJSON) {
			return plan, errors.New("orphan outbox is not an intact unaccepted deletion request")
		}
		d := LocalSyncDeletionRequest{
			LocalDeletionID: item.LocalRef, LocalSequence: item.LocalSequence,
			RootKey: metadataString(item.PayloadJSON, "root_key"), RelativePath: metadataString(item.PayloadJSON, "relative_path"),
			ContentHashURI: metadataString(item.PayloadJSON, "content_hash_uri"),
			TargetKind:     metadataString(item.PayloadJSON, "target_kind"), TargetRef: metadataString(item.PayloadJSON, "target_ref"),
			RequestedAction: metadataString(item.PayloadJSON, "requested_action"), Reason: metadataString(item.PayloadJSON, "reason"),
			CreatedAt: item.CreatedAt, SyncStatus: localSyncStatusPending,
		}
		if !syncRepairPayloadEqual(item.PayloadJSON, localDeletionPayload(d)) {
			return plan, errors.New("orphan payload is not the exact canonical request")
		}
		object, err := syncRepairMatchingObject(objects, config.NodeKey, d)
		if err != nil {
			return plan, err
		}
		d.LocalObjectID, d.MainObjectID = object.LocalObjectID, object.MainObjectID
		d.Metadata = objectJSON(map[string]any{"source": "loom-node-agent", "node_key": config.NodeKey, "slice": "10_part_1", "watched_root": d.RootKey, "relative_path": d.RelativePath})
		deletions = append(deletions, d)
		byRef[d.LocalDeletionID] = d
		plan.MissingRecords = append(plan.MissingRecords, d.LocalDeletionID)
	}
	for _, d := range deletions {
		if paired[d.LocalDeletionID] || d.SyncStatus != localSyncStatusPending {
			continue
		}
		object, err := syncRepairMatchingObject(objects, config.NodeKey, d)
		if err != nil {
			return plan, err
		}
		if d.LocalObjectID != object.LocalObjectID || d.MainObjectID != object.MainObjectID || d.CreatedAt.IsZero() || d.LocalSequence <= 0 || d.SyncedAt != nil || d.DeletionRequestID != "" {
			return plan, errors.New("orphan deletion record identity differs")
		}
		// The deletion ULID gives a stable local envelope ID; remote idempotency
		// remains the original node/root/path/content identity, not this envelope.
		id := "local_outbox_" + strings.TrimPrefix(d.LocalDeletionID, "deletion_request_")
		if ids[id] {
			return plan, errors.New("recovered outbox identity collides")
		}
		ids[id] = true
		payload := localDeletionPayload(d)
		outbox = append(outbox, LocalSyncOutboxItem{LocalOutboxID: id, LocalRef: d.LocalDeletionID, ItemKind: loomsync.ItemKindDeletionRequest, StreamName: loomsync.StreamDeletionRequests, LocalSequence: d.LocalSequence, PayloadHash: rawJSONHash(payload), Status: localSyncStatusPending, CreatedAt: d.CreatedAt, PayloadJSON: payload})
		plan.MissingOutbox = append(plan.MissingOutbox, d.LocalDeletionID)
	}
	if !apply {
		return plan, nil
	}
	if digest != plan.Digest {
		return plan, errors.New("sync repair plan changed; review a fresh dry-run")
	}
	if len(plan.MissingRecords)+len(plan.MissingOutbox) == 0 {
		return plan, nil
	}
	// Preserve all small before-images before either write. A partial I/O failure
	// remains an error; a new reviewed plan can complete only the missing half.
	evidence := filepath.Join(root, "repair-evidence", plan.Digest)
	if err := os.MkdirAll(evidence, 0o700); err != nil {
		return plan, err
	}
	resolved, err := filepath.EvalSymlinks(evidence)
	if err != nil || resolved != evidence {
		return plan, errors.New("repair evidence path is not canonical")
	}
	for _, name := range files {
		if err := preserveSyncRepairFile(filepath.Join(evidence, name), before[name]); err != nil {
			return plan, err
		}
		current, err := readSyncRepairFile(filepath.Join(root, name))
		if err != nil || !bytes.Equal(current, before[name]) {
			return plan, errors.New("sync repair input changed after review")
		}
	}
	if len(plan.MissingRecords) > 0 {
		if err := s.SaveSyncDeletions(deletions); err != nil {
			return plan, err
		}
	}
	if len(plan.MissingOutbox) > 0 {
		if err := s.SaveSyncOutbox(outbox); err != nil {
			return plan, err
		}
	}
	plan.Applied = true
	return plan, nil
}

func syncRepairPayloadEqual(a, b json.RawMessage) bool {
	canonical := func(raw json.RawMessage) []byte {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) != nil {
			return nil
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return nil
		}
		encoded, _ := json.Marshal(value)
		return encoded
	}
	x, y := canonical(a), canonical(b)
	return x != nil && y != nil && bytes.Equal(x, y)
}

func syncRepairMatchingObject(objects []LocalSyncObject, nodeKey string, d LocalSyncDeletionRequest) (LocalSyncObject, error) {
	var match LocalSyncObject
	if ids.Validate("deletion_request", d.LocalDeletionID) != nil || d.RootKey == "" || d.RelativePath == "" || d.TargetKind != "object" || d.RequestedAction != "tombstone" || !strings.HasPrefix(d.ContentHashURI, "sha256:") || len(d.ContentHashURI) != 71 {
		return match, errors.New("deletion repair requires exact existing object/root/path/hash identity")
	}
	for _, object := range objects {
		if object.MainObjectID != d.TargetRef || object.HashURI != d.ContentHashURI || object.SourcePath != "watched-root://"+d.RootKey+"/"+d.RelativePath || metadataString(object.Metadata, "node_key") != nodeKey || metadataString(object.Metadata, "watched_root") != d.RootKey || metadataString(object.Metadata, "relative_path") != d.RelativePath {
			continue
		}
		if object.LocalObjectID == "" || object.MainObjectID == "" || (match.LocalObjectID != "" && match.LocalObjectID != object.LocalObjectID) {
			return match, errors.New("deletion repair object evidence is ambiguous")
		}
		match = object
	}
	if match.LocalObjectID == "" {
		return match, errors.New("deletion repair has no matching retained object evidence")
	}
	return match, nil
}

func readSyncRepairFile(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size > 16<<20 {
		return nil, errors.New("sync repair input must be bounded regular single-link metadata")
	}
	return io.ReadAll(io.LimitReader(f, (16<<20)+1))
}

func preserveSyncRepairFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		old, readErr := readSyncRepairFile(path)
		if readErr != nil || !bytes.Equal(old, data) {
			return errors.New("sync repair before-image conflict")
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("persist repair evidence: %w", err)
	}
	return nil
}
