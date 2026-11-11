package notesworkspacesync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// OutcomeStatus intentionally excludes native payloads, source paths and raw
// exceptions. Runtime can expose these compact states through its existing UI.
type OutcomeStatus struct {
	IntentID, Status, Reason string
	Acknowledged             bool
}
type OutcomePage struct {
	Items []OutcomeStatus
	Next  string
}

func (s *Service) Outcomes(ctx context.Context, after string, limit int) (out OutcomePage, err error) {
	if err = s.valid(); err != nil {
		return
	}
	if limit < 1 || limit > 128 {
		return out, ErrHeld
	}
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		ids, e := r.List(ctx, "operation", after, limit)
		if e != nil {
			return e
		}
		for _, id := range ids {
			j, e := load[Join](ctx, r, "operation", id)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, OutcomeStatus{IntentID: id, Status: j.Status, Reason: j.Reason, Acknowledged: j.AckPublished})
			out.Next = id
		}
		return nil
	})
	return
}

// EvidenceStatus makes stock/missing/invalid-control holds inspectable without
// handing raw note content to a runtime status endpoint.
type EvidenceStatus struct{ ID, NativeID, Revision, Reason string }
type EvidencePage struct {
	Items []EvidenceStatus
	Next  string
}

func (s *Service) Evidence(ctx context.Context, after string, limit int) (out EvidencePage, err error) {
	if err = s.valid(); err != nil {
		return
	}
	if limit < 1 || limit > 128 {
		return out, ErrHeld
	}
	err = s.Store.WithReplica(ctx, s.Replica, s.Epoch, func(r Records) error {
		ids, e := r.List(ctx, "observation", after, limit)
		if e != nil {
			return e
		}
		for _, id := range ids {
			ob, e := load[Observation](ctx, r, "observation", id)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, EvidenceStatus{ID: id, NativeID: ob.Evidence.ID, Revision: ob.Evidence.Revision, Reason: ob.Reason})
			out.Next = id
		}
		return nil
	})
	return
}

// InventoryCounts are maintained by the metadata writer, not corpus-size counts
// on every UI read. Totals remain observational and unknown during catch-up.
type InventoryCounts struct {
	TextPresent   int64 `json:"text_present"`
	AssetPresent  int64 `json:"asset_present"`
	TextPending   int64 `json:"text_pending"`
	AssetPending  int64 `json:"asset_pending"`
	PendingBytes  int64 `json:"pending_bytes"`
	Withdrawn     int64 `json:"withdrawn"`
	Excluded      int64 `json:"excluded"`
	TextExcluded  int64 `json:"text_excluded"`
	AssetExcluded int64 `json:"asset_excluded"`
}

func inventoryTrack(counts *InventoryCounts, old, current InventoryEntry) {
	track := func(e InventoryEntry, delta int64) {
		if e.Sequence == 0 {
			return
		}
		if !e.Present {
			counts.Withdrawn += delta
			if e.Kind == "directory" && e.Sequence > e.Delivered {
				counts.TextPending += delta
			}
			return
		}
		present, pending := &counts.AssetPresent, &counts.AssetPending
		if e.Kind == "text" || e.Kind == "directory" {
			present, pending = &counts.TextPresent, &counts.TextPending
		}
		*present += delta
		if e.Reason == "source_excluded" {
			counts.Excluded += delta
			if e.Kind == "text" || e.Kind == "directory" {
				counts.TextExcluded += delta
			} else {
				counts.AssetExcluded += delta
			}
			return
		}
		if e.Sequence > e.Delivered {
			*pending += delta
			counts.PendingBytes += delta * e.Size
		}
	}
	track(old, -1)
	track(current, 1)
}

// DeliveryCursor is additive to RuntimeCursor v1. Each lane advances after
// durable publication/ack; inventory row acknowledgments are the crash-resume
// authority even if a worker dies before saving this summary cursor.
type DeliveryCursor struct {
	Generation    string `json:"generation"`
	TextSequence  int64  `json:"text_sequence,omitempty"`
	AssetSequence int64  `json:"asset_sequence,omitempty"`
	TextAfter     string `json:"text_after,omitempty"`
	AssetAfter    string `json:"asset_after,omitempty"`
	NextLane      string `json:"next_lane,omitempty"`
}

type InventoryScanProgress struct {
	Prefix string `json:"prefix"`
	After  string `json:"after"`
	Phase  string `json:"phase"`
	Again  bool   `json:"again"`
}

type InventoryProgress struct {
	SchemaVersion         string                 `json:"schema_version"`
	Collection            string                 `json:"collection"`
	Generation            string                 `json:"generation"`
	Sequence              int64                  `json:"sequence"`
	Complete              bool                   `json:"complete"`
	CountersKnown         bool                   `json:"counters_known"`
	LaneExclusionsKnown   bool                   `json:"lane_exclusions_known"`
	TotalsKnown           bool                   `json:"totals_known"`
	Degraded              string                 `json:"degraded,omitempty"`
	WatchDetail           string                 `json:"watch_detail,omitempty"`
	ObservedAt            time.Time              `json:"observed_at"`
	NextRepair            time.Time              `json:"next_repair"`
	Counts                InventoryCounts        `json:"counts"`
	TextOldestAgeSeconds  float64                `json:"text_oldest_age_seconds"`
	AssetOldestAgeSeconds float64                `json:"asset_oldest_age_seconds"`
	TextReason            string                 `json:"text_reason,omitempty"`
	AssetReason           string                 `json:"asset_reason,omitempty"`
	TextRetryAt           *time.Time             `json:"text_retry_at,omitempty"`
	AssetRetryAt          *time.Time             `json:"asset_retry_at,omitempty"`
	ActiveScan            *InventoryScanProgress `json:"active_scan,omitempty"`
	QueuedScans           int                    `json:"queued_scans"`
}

// QueuedScans excludes the active head; no waiting scan paths are published.
func (out *InventoryProgress) setScanProgress(active *inventoryScan, queued int) {
	out.ActiveScan = nil
	out.QueuedScans = queued
	if active == nil {
		return
	}
	phase := "discovery"
	if active.Withdrawing {
		phase = "withdrawal"
	}
	out.ActiveScan = &InventoryScanProgress{Prefix: active.Prefix, After: active.After, Phase: phase, Again: active.Again}
}

func (i SourceInventory) Progress(ctx context.Context, scope InventoryScope, now time.Time) (out InventoryProgress, err error) {
	out.SchemaVersion = "notes_workspace.inventory_progress.v1"
	out.Collection = scope.Collection
	out.Generation = scope.Generation
	err = i.Store.WithInventory(ctx, scope, func(r InventoryRecords) error {
		s := r.State()
		out.Sequence = s.Sequence
		out.Complete = s.Complete
		out.CountersKnown = s.CountersReady
		out.LaneExclusionsKnown = s.CountersReady && s.LaneExclusionsReady
		out.TotalsKnown = s.CountersReady && s.Complete && s.Degraded != "source_unavailable"
		out.Degraded = s.Degraded
		out.WatchDetail = s.WatchDetail
		out.ObservedAt = s.ObservedAt
		out.NextRepair = s.NextRepair
		out.Counts = s.Counts
		var active *inventoryScan
		if len(s.Scans) > 0 {
			active = &s.Scans[0]
		}
		out.setScanProgress(active, max(len(s.Scans)-1, 0))
		text, retry, err := r.Waiting(ctx, "text")
		if err != nil {
			return err
		}
		out.TextRetryAt = retry
		if text != nil {
			out.TextReason = text.Reason
			out.TextOldestAgeSeconds = max(0, now.Sub(text.WaitingSince).Seconds())
		}
		asset, retry, err := r.Waiting(ctx, "asset")
		if err != nil {
			return err
		}
		out.AssetRetryAt = retry
		if asset != nil {
			out.AssetReason = asset.Reason
			out.AssetOldestAgeSeconds = max(0, now.Sub(asset.WaitingSince).Seconds())
		}
		return nil
	})
	return
}

// WorkspaceProgress identifies the committed selection used for native
// publication. Source acceptance and receipt on another device are separate.
type WorkspaceProgress struct {
	CurrentRunID        string                 `json:"current_run_id,omitempty"`
	ReplicaID           *string                `json:"replica_id"`
	SchemaVersion       string                 `json:"schema_version"`
	ObservedAt          time.Time              `json:"observed_at"`
	SelectionScope      string                 `json:"selection_scope"`
	SelectionCurrent    *bool                  `json:"selection_current"`
	WorkerKey           string                 `json:"worker_key"`
	RunID               string                 `json:"run_id,omitempty"`
	RunStatus           string                 `json:"run_status,omitempty"`
	FinishedAt          *time.Time             `json:"finished_at,omitempty"`
	NextRunAfter        *time.Time             `json:"next_run_after,omitempty"`
	CheckpointID        string                 `json:"checkpoint_id,omitempty"`
	CheckpointRunID     string                 `json:"checkpoint_run_id,omitempty"`
	CheckpointAt        *time.Time             `json:"checkpoint_at,omitempty"`
	LastTurn            *RuntimeResult         `json:"last_turn,omitempty"`
	Inventory           []InventoryPublication `json:"inventory"`
	ClientReceived      *bool                  `json:"client_received"`
	SourceAccepted      *bool                  `json:"source_accepted"`
	ProtectedGeneration *string                `json:"protected_generation"`
	ProtectionReceipt   *string                `json:"protection_receipt"`
	UsefulRate          *float64               `json:"useful_rate"`
	ETASeconds          *float64               `json:"eta_seconds"`
	Unknown             map[string]string      `json:"unknown"`
	PartialErrors       []string               `json:"partial_errors,omitempty"`
}

type InventoryPublication struct {
	InventoryProgress
	NativeCollection string            `json:"native_collection"`
	CounterScope     string            `json:"counter_scope"`
	TextSequence     int64             `json:"text_sequence"`
	AssetSequence    int64             `json:"asset_sequence"`
	NextLane         string            `json:"next_lane,omitempty"`
	TextPublished    *int64            `json:"text_published"`
	AssetPublished   *int64            `json:"asset_published"`
	Unknown          map[string]string `json:"unknown,omitempty"`
}

func (publication InventoryPublication) MarshalJSON() ([]byte, error) {
	type readable InventoryPublication
	raw, err := json.Marshal(readable(publication))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if !publication.CountersKnown {
		fields["counts"] = json.RawMessage("null")
	} else if !publication.LaneExclusionsKnown {
		var counts map[string]json.RawMessage
		if err = json.Unmarshal(fields["counts"], &counts); err != nil {
			return nil, err
		}
		counts["text_excluded"], counts["asset_excluded"] = json.RawMessage("null"), json.RawMessage("null")
		if fields["counts"], err = json.Marshal(counts); err != nil {
			return nil, err
		}
	}
	if !publication.CountersKnown || publication.Unknown["text_wait"] != "" {
		fields["text_oldest_age_seconds"] = json.RawMessage("null")
	}
	if !publication.CountersKnown || publication.Unknown["asset_wait"] != "" {
		fields["asset_oldest_age_seconds"] = json.RawMessage("null")
	}
	return json.Marshal(fields)
}

func inventoryPublication(progress InventoryProgress) InventoryPublication {
	out := InventoryPublication{InventoryProgress: progress, CounterScope: "source_generation_native_publication", Unknown: map[string]string{}}
	if !progress.CountersKnown {
		out.Unknown["counters"] = "generation counters have not been initialized"
	}
	if progress.TextReason == "waiting_outside_bounded_window" {
		out.Unknown["text_wait"] = "oldest eligible entry lies outside the bounded lane window"
	}
	if progress.AssetReason == "waiting_outside_bounded_window" {
		out.Unknown["asset_wait"] = "oldest eligible entry lies outside the bounded lane window"
	}
	if progress.CountersKnown && progress.Counts.TextPending > 0 && progress.TextRetryAt == nil {
		out.Unknown["text_retry"] = "deadline lies outside the bounded lane window"
	}
	if progress.CountersKnown && progress.Counts.AssetPending > 0 && progress.AssetRetryAt == nil {
		out.Unknown["asset_retry"] = "deadline lies outside the bounded lane window"
	}
	if !progress.TotalsKnown {
		out.Unknown["totals"] = "enumeration incomplete or source unavailable"
	}
	if progress.CountersKnown && (progress.LaneExclusionsKnown || progress.Counts.Excluded == 0) {
		c := progress.Counts
		text, asset := c.TextPresent-c.TextExcluded-c.TextPending, c.AssetPresent-c.AssetExcluded-c.AssetPending
		if text >= 0 && asset >= 0 && c.TextExcluded >= 0 && c.AssetExcluded >= 0 && c.TextExcluded+c.AssetExcluded == c.Excluded {
			out.TextPublished, out.AssetPublished = &text, &asset
		} else {
			out.Unknown["published"] = "generation counters are inconsistent"
		}
	} else if progress.CountersKnown {
		out.Unknown["published"] = "per-lane excluded counts unavailable; excluded paths are not publication"
	} else {
		out.Unknown["published"] = "generation counters unavailable"
	}
	return out
}

// ReadWorkspaceProgress performs only SELECTs. WithInventory is intentionally
// not used: that producer API creates missing rows and takes a writer lock.
func ReadWorkspaceProgress(ctx context.Context, db *sql.DB, now time.Time) (WorkspaceProgress, error) {
	out := WorkspaceProgress{SchemaVersion: "notes_workspace.publication_progress.v1", ObservedAt: now,
		SelectionScope: "last_committed_worker_checkpoint", WorkerKey: "main.notes_workspace_sync", Inventory: []InventoryPublication{},
		Unknown: map[string]string{
			"selection_current": "last committed worker selection; current enrollment/owner generation is not attested by this checkpoint",
			"client_received":   "native publication is not a per-device receipt",
			"source_accepted":   "no exact selected-generation acceptance aggregate",
			"protection":        "no exact selected-generation backup receipt; publication does not establish protection",
			"useful_rate":       "turn counters do not establish rolling generation throughput",
			"eta":               "stable denominator and observed throughput unavailable",
			"replica_scope":     "checkpoint identifies publication generations but has no per-device receipt identity"}}
	if db == nil {
		return out, fmt.Errorf("Notes workspace store is not configured")
	}
	var finished, next, checkpointAt sql.NullTime
	var summary, cursorRaw, selectionSummary []byte
	var schema string
	err := db.QueryRowContext(ctx, `SELECT COALESCE(i.current_run_id,''),COALESCE(r.worker_run_id,''),COALESCE(r.run_status,''),r.finished_at,i.next_run_after,
	 COALESCE(c.worker_checkpoint_id,''),COALESCE(c.updated_by_run_id,''),c.updated_at,COALESCE(c.schema_version,''),r.result_summary_json,c.checkpoint_json,cr.result_summary_json
	 FROM workers.worker_instances i LEFT JOIN workers.worker_runs r ON r.worker_run_id=i.last_run_id
	 LEFT JOIN workers.worker_checkpoints c ON c.worker_instance_id=i.worker_instance_id AND c.checkpoint_key='sources'
	 LEFT JOIN workers.worker_runs cr ON cr.worker_run_id=c.updated_by_run_id
	 WHERE i.worker_key='main.notes_workspace_sync'`).Scan(&out.CurrentRunID, &out.RunID, &out.RunStatus, &finished, &next, &out.CheckpointID, &out.CheckpointRunID, &checkpointAt, &schema, &summary, &cursorRaw, &selectionSummary)
	if err != nil {
		return out, err
	}
	if finished.Valid {
		out.FinishedAt = &finished.Time
	}
	if next.Valid {
		out.NextRunAfter = &next.Time
	}
	if checkpointAt.Valid {
		out.CheckpointAt = &checkpointAt.Time
	}
	if len(summary) > 0 {
		var turn RuntimeResult
		if json.Unmarshal(summary, &turn) != nil {
			out.PartialErrors = append(out.PartialErrors, "worker_result_invalid")
		} else if turn.SchemaVersion == "notes_workspace.result.v1" {
			// Only live generation reads below report inventory, never an old turn's snapshot.
			turn.Inventory = nil
			out.LastTurn = &turn
		}
	}
	if out.LastTurn == nil {
		out.Unknown["last_turn"] = "compatible last-finished worker result unavailable"
	}
	var selectionResult RuntimeResult
	if json.Unmarshal(selectionSummary, &selectionResult) != nil || selectionResult.SchemaVersion != "notes_workspace.result.v1" {
		out.Unknown["source_mapping"] = "checkpoint run has no compatible exact-generation inventory identities"
	}
	var cursor RuntimeCursor
	if schema != "notes_workspace.cursor.v1" || json.Unmarshal(cursorRaw, &cursor) != nil {
		out.Unknown["selection"] = "committed delivery checkpoint unavailable or incompatible"
		return out, nil
	}
	collections := make([]string, 0, len(cursor.Delivery))
	for collection := range cursor.Delivery {
		collections = append(collections, collection)
	}
	sort.Strings(collections)
	if len(collections) > 32 {
		collections = collections[:32]
		out.PartialErrors = append(out.PartialErrors, "selection_window_full")
	}
	if len(collections) == 0 {
		out.Unknown["selection"] = "checkpoint has no selected delivery generations"
	}
	for _, collection := range collections {
		generation := cursor.Delivery[collection].Generation
		if generation == "" {
			out.PartialErrors = append(out.PartialErrors, "generation_unavailable:"+collection)
			continue
		}
		source, ok := inventorySourceForGeneration(selectionResult.Inventory, generation)
		if !ok {
			out.PartialErrors = append(out.PartialErrors, "source_mapping_unknown:"+collection)
			continue
		}
		progress, err := ReadInventoryProgress(ctx, db, source, generation, now)
		if err != nil {
			out.PartialErrors = append(out.PartialErrors, "inventory_unavailable:"+collection)
			continue
		}
		publication := inventoryPublication(progress)
		publication.NativeCollection = collection
		publication.TextSequence, publication.AssetSequence, publication.NextLane = cursor.Delivery[collection].TextSequence, cursor.Delivery[collection].AssetSequence, cursor.Delivery[collection].NextLane
		out.Inventory = append(out.Inventory, publication)
	}
	return out, nil
}

// One exact-generation statement reads counters, the scan head/queue length and
// indexed lane deadlines. No corpus scan/count, locks or missing-row write.
func ReadInventoryProgress(ctx context.Context, db *sql.DB, collection, generation string, now time.Time) (InventoryProgress, error) {
	out := InventoryProgress{SchemaVersion: "notes_workspace.inventory_progress.v1", Collection: collection, Generation: generation}
	if db == nil || collection == "" || generation == "" {
		return out, ErrHeld
	}
	var raw, activeRaw []byte
	var scanCount int
	var textAge, assetAge, textRetry, assetRetry sql.NullTime
	var textReason, assetReason sql.NullString
	err := db.QueryRowContext(ctx, `SELECT g.record-'scans',g.record->'scans'->0,COALESCE(jsonb_array_length(g.record->'scans'),0),t.waiting_since,t.reason,a.waiting_since,a.reason,tr.retry_at,ar.retry_at
	 FROM notes_workspace.source_inventory_generations g
	 LEFT JOIN LATERAL (`+inventoryProgressLaneSQL("text", "waiting_since,reason", "waiting_since,relative_path")+`) t ON true
	 LEFT JOIN LATERAL (`+inventoryProgressLaneSQL("asset", "waiting_since,reason", "waiting_since,relative_path")+`) a ON true
	 LEFT JOIN LATERAL (`+inventoryProgressLaneSQL("text", "retry_at", "retry_at")+`) tr ON true
	 LEFT JOIN LATERAL (`+inventoryProgressLaneSQL("asset", "retry_at", "retry_at")+`) ar ON true
	 WHERE g.collection_id=$1 AND g.generation=$2`, collection, generation).Scan(&raw, &activeRaw, &scanCount, &textAge, &textReason, &assetAge, &assetReason, &textRetry, &assetRetry)
	if err != nil {
		return out, err
	}
	var state InventoryState
	if err = json.Unmarshal(raw, &state); err != nil {
		return out, err
	}
	var active *inventoryScan
	if len(activeRaw) > 0 {
		if err = json.Unmarshal(activeRaw, &active); err != nil {
			return out, err
		}
	}
	out.setScanProgress(active, max(scanCount-1, 0))
	out.Sequence, out.Complete, out.CountersKnown = state.Sequence, state.Complete, state.CountersReady
	out.LaneExclusionsKnown = state.CountersReady && state.LaneExclusionsReady
	out.TotalsKnown = state.CountersReady && state.Complete && state.Degraded != "source_unavailable"
	out.Degraded, out.ObservedAt, out.NextRepair, out.Counts = state.Degraded, state.ObservedAt, state.NextRepair, state.Counts
	out.WatchDetail = state.WatchDetail
	out.TextReason, out.AssetReason = textReason.String, assetReason.String
	if out.CountersKnown && out.Counts.TextPending > 0 && !textAge.Valid {
		out.TextReason = "waiting_outside_bounded_window"
	}
	if out.CountersKnown && out.Counts.AssetPending > 0 && !assetAge.Valid {
		out.AssetReason = "waiting_outside_bounded_window"
	}
	if textAge.Valid {
		out.TextOldestAgeSeconds = max(0, now.Sub(textAge.Time).Seconds())
	}
	if assetAge.Valid {
		out.AssetOldestAgeSeconds = max(0, now.Sub(assetAge.Time).Seconds())
	}
	if textRetry.Valid {
		out.TextRetryAt = &textRetry.Time
	}
	if assetRetry.Valid {
		out.AssetRetryAt = &assetRetry.Time
	}
	return out, nil
}

// Existing indexes include excluded entries. Bound that indexed window before
// filtering them; a missing eligible row is unknown, never proof of no pending
// work. Maintained zero pending avoids even the lane lookup.
func inventoryProgressLaneSQL(kind, columns, order string) string {
	lane := "entry_kind='" + kind + "'"
	if kind == "text" {
		lane = "entry_kind IN ('text','directory')"
	}
	return `SELECT ` + columns + ` FROM (SELECT waiting_since,reason,retry_at,relative_path FROM notes_workspace.source_inventory
	 WHERE collection_id=g.collection_id AND generation=g.generation AND ` + lane + ` AND (present OR entry_kind='directory') AND sequence>delivered_sequence
	 AND COALESCE((g.record->'counts'->>'` + kind + `_pending')::bigint,0)>0
	 ORDER BY ` + order + ` LIMIT 128) pending WHERE reason<>'source_excluded' ORDER BY ` + order + ` LIMIT 1`
}

func inventorySourceForGeneration(inventory []InventoryProgress, generation string) (string, bool) {
	source := ""
	for _, item := range inventory {
		if item.Generation != generation || item.Collection == "" {
			continue
		}
		if source != "" && source != item.Collection {
			return "", false
		}
		source = item.Collection
	}
	return source, source != ""
}
