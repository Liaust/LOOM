package storagecatalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

func LaneCustodyIdentity(source, batch string) string {
	raw, _ := json.Marshal([]string{source, batch})
	sum := sha256.Sum256(raw)
	return "lane:" + hex.EncodeToString(sum[:])
}

type laneCustodyRootKey struct{}

type laneCustodyRoot struct {
	db                  *sql.DB
	source, batch, root string
}

// PrepareLaneCustodyResolution shares only the immutable original root within
// one verification. Current locations and in-flight moves are still read live.
func (s Service) PrepareLaneCustodyResolution(ctx context.Context, source, batch string) (context.Context, error) {
	if s.DB == nil || source == "" || batch == "" {
		return nil, fmt.Errorf("exact committed Lane batch required")
	}
	root, err := s.laneOriginalRoot(ctx, source, batch)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, laneCustodyRootKey{}, laneCustodyRoot{s.DB, source, batch, root}), nil
}

func (s Service) laneOriginalRoot(ctx context.Context, source, batch string) (string, error) {
	if cached, ok := ctx.Value(laneCustodyRootKey{}).(laneCustodyRoot); ok && cached.db == s.DB && cached.source == source && cached.batch == batch {
		return cached.root, nil
	}
	var original, relative string
	err := s.DB.QueryRowContext(ctx, `SELECT metadata->>'final_custody_path',metadata->>'relative_lane_path' FROM storage.storage_entries WHERE storage_class='lane_custody' AND origin_node_key=$1 AND metadata->>'batch_id'=$2 ORDER BY metadata->>'relative_lane_path',storage_entry_id LIMIT 1`, source, batch).Scan(&original, &relative)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !fs.ValidPath(relative) || relative == "." || !filepath.IsAbs(original) || filepath.Clean(original) != original || !strings.HasSuffix(original, "/"+relative) {
		return "", fmt.Errorf("immutable Lane custody root is invalid")
	}
	return strings.TrimSuffix(original, "/"+relative), nil
}

func (s Service) ResolveLaneCustodyMember(ctx context.Context, source, batch, relative string) (string, error) {
	if s.DB == nil || source == "" || batch == "" || !fs.ValidPath(relative) || relative == "." {
		return "", fmt.Errorf("exact committed Lane member required")
	}
	root, err := s.laneOriginalRoot(ctx, source, batch)
	if err != nil || root == "" {
		return "", err
	}
	return s.ResolveCustodyPath(ctx, LaneCustodyIdentity(source, batch), filepath.Join(root, filepath.FromSlash(relative)))
}

// A directory mtime may change when a child is renamed out. Only the exact
// append-only relocation chain can qualify it; arbitrary drift stays an error.
func (s Service) VerifyLaneCustodyDirectoryMutation(ctx context.Context, source, batch, relative string, original, observed time.Time) error {
	root, err := s.laneOriginalRoot(ctx, source, batch)
	if err != nil || root == "" {
		return fmt.Errorf("Lane parent origin unavailable: %v", err)
	}
	current, err := s.ResolveLaneCustodyMember(ctx, source, batch, relative)
	if err != nil {
		return err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT r.plan_json#>'{source_ancestors,-1,identity}',e.evidence->'SourceParent' FROM storage.retained_relocations r JOIN storage.retained_relocation_events e USING(relocation_id) WHERE r.custody_id=$1 AND r.state='complete' AND e.phase='complete' AND (regexp_replace(r.source_prefix,'/[^/]+$','')=$2 OR regexp_replace(r.source_prefix,'/[^/]+$','')=$3) ORDER BY e.event_id`, LaneCustodyIdentity(source, batch), filepath.Join(root, filepath.FromSlash(relative)), current)
	if err != nil {
		return err
	}
	defer rows.Close()
	stamp := original.UnixNano()
	count := 0
	for rows.Next() {
		var beforeRaw, afterRaw []byte
		if err = rows.Scan(&beforeRaw, &afterRaw); err != nil {
			return err
		}
		var before, after struct {
			Device   uint64 `json:"device_id"`
			Inode    uint64 `json:"inode"`
			Mode     uint32 `json:"mode"`
			Modified int64  `json:"modified_unix_ns"`
		}
		if json.Unmarshal(beforeRaw, &before) != nil || json.Unmarshal(afterRaw, &after) != nil || before.Modified != stamp || before.Device == 0 || before.Inode == 0 || before.Device != after.Device || before.Inode != after.Inode || before.Mode != after.Mode {
			return fmt.Errorf("Lane directory mutation history does not form an exact identity chain")
		}
		stamp = after.Modified
		count++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count == 0 || stamp != observed.UnixNano() {
		return fmt.Errorf("Lane directory timestamp drift has no matching completed relocation")
	}
	return nil
}

func (s Service) inspectLaneMemberIdentity(ctx context.Context, input LaneCustodyInput) (EntryDetail, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT storage_entry_id FROM storage.storage_entries WHERE storage_class='lane_custody' AND origin_node_key=$1 AND metadata->>'batch_id'=$2 AND metadata->>'relative_lane_path'=$3 LIMIT 2`, input.SourceNodeKey, input.BatchID, input.RelativeLanePath)
	if err != nil {
		return EntryDetail{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return EntryDetail{}, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return EntryDetail{}, err
	}
	if len(ids) == 0 {
		return EntryDetail{}, sql.ErrNoRows
	}
	if len(ids) != 1 {
		return EntryDetail{}, fmt.Errorf("Lane member has ambiguous catalog identity")
	}
	return s.InspectEntry(ctx, ids[0])
}
