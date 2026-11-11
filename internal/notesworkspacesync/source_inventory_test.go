package notesworkspacesync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryInventory struct {
	mu     sync.Mutex
	states map[string]*memoryInventoryRecords
}
type memoryInventoryRecords struct {
	state   InventoryState
	entries map[string]InventoryEntry
}

func (m *memoryInventory) WithInventory(ctx context.Context, s InventoryScope, fn func(InventoryRecords) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.states == nil {
		m.states = map[string]*memoryInventoryRecords{}
	}
	id := key(s.Collection, s.Generation)
	r := m.states[id]
	if r == nil {
		r = &memoryInventoryRecords{state: InventoryState{CountersReady: true, LaneExclusionsReady: true}, entries: map[string]InventoryEntry{}}
		m.states[id] = r
	}
	// Snapshot rollback mirrors transaction behavior and checks restart-safe JSON.
	raw, _ := json.Marshal(r.state)
	var state InventoryState
	_ = json.Unmarshal(raw, &state)
	entries := map[string]InventoryEntry{}
	for p, e := range r.entries {
		entries[p] = e
	}
	clone := &memoryInventoryRecords{state: state, entries: entries}
	if err := fn(clone); err != nil {
		return err
	}
	m.states[id] = clone
	return nil
}
func (m *memoryInventoryRecords) State() *InventoryState { return &m.state }
func (m *memoryInventoryRecords) Get(_ context.Context, p string) (InventoryEntry, error) {
	e, ok := m.entries[p]
	if !ok {
		return e, sql.ErrNoRows
	}
	return e, nil
}
func (m *memoryInventoryRecords) Put(_ context.Context, e InventoryEntry) error {
	if m.state.CountersReady {
		inventoryTrack(&m.state.Counts, m.entries[e.Path], e)
	}
	m.entries[e.Path] = e
	return nil
}
func (m *memoryInventoryRecords) MarkSeen(_ context.Context, path string, scan int64) error {
	e, ok := m.entries[path]
	if !ok {
		return sql.ErrNoRows
	}
	e.SeenScan = max(e.SeenScan, scan)
	m.entries[path] = e
	return nil
}
func (m *memoryInventoryRecords) Pending(_ context.Context, kind string, now time.Time, limit int) ([]InventoryEntry, error) {
	var out []InventoryEntry
	for _, e := range m.entries {
		if (e.Present || e.Kind == "directory") && e.Reason != "source_excluded" && e.Sequence > e.Delivered && (kind == "" || e.Kind == kind || kind == "text" && e.Kind == "directory") && !now.Before(e.RetryAt) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Priority != out[b].Priority {
			return out[a].Priority
		}
		if !out[a].WaitingSince.Equal(out[b].WaitingSince) {
			return out[a].WaitingSince.Before(out[b].WaitingSince)
		}
		return out[a].Path < out[b].Path
	})
	var oldest []InventoryEntry
	for _, e := range out {
		if len(oldest) == 0 || e.WaitingSince.Before(oldest[0].WaitingSince) || e.WaitingSince.Equal(oldest[0].WaitingSince) && e.Path < oldest[0].Path {
			oldest = []InventoryEntry{e}
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return inventoryAgeGuard(out, oldest, now, limit), nil
}
func (m *memoryInventoryRecords) Unseen(_ context.Context, prefix, after string, sequence int64, limit int) ([]InventoryEntry, error) {
	var out []InventoryEntry
	for _, e := range m.entries {
		if e.Present && e.Reason != "source_excluded" && e.Path > after && e.SeenScan < sequence && e.Sequence <= sequence && (prefix == "" || len(e.Path) > len(prefix) && inventoryContains(prefix, e.Path)) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Path < out[b].Path })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func inventoryFixture(t *testing.T) (SourceInventory, InventoryScope, time.Time) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return SourceInventory{Store: &memoryInventory{}}, InventoryScope{Collection: "source", Generation: "generation", Root: root}, time.Now().UTC()
}

func TestInventoryDiscoveryUsesIndependentWorkspaceSelection(t *testing.T) {
	i, scope, now := inventoryFixture(t)
	scope.WorkspaceIncludes = `["paper/*.pdf","Empty"]`
	inventoryWrite(t, scope, "paper/current.pdf", "current")
	inventoryWrite(t, scope, "runs/environment/lib/unused.md", "not selected")
	if err := os.Mkdir(filepath.Join(scope.Root, "Empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := i.Begin(t.Context(), scope, "", now); err != nil {
		t.Fatal(err)
	}
	// Retained exclusions from an older unfiltered scan are not withdrawals or
	// work to stat again. They remain history under this exact generation.
	if err := i.Store.WithInventory(t.Context(), scope, func(r InventoryRecords) error {
		return r.Put(t.Context(), InventoryEntry{Path: "runs/old.md", Kind: "text", Present: true, Sequence: 1, Reason: "source_excluded"})
	}); err != nil {
		t.Fatal(err)
	}
	if err := i.Advance(t.Context(), scope, now, 128); err != nil {
		t.Fatal(err)
	}
	state := inventoryState(t, i, scope)
	if !state.Complete || state.Counts.TextPresent != 3 || state.Counts.AssetPresent != 1 {
		t.Fatal("selection did not bound discovery or preserve empty directories", state)
	}
	if err := i.Store.WithInventory(t.Context(), scope, func(r InventoryRecords) error {
		_, err := r.Get(t.Context(), "runs/environment/lib/unused.md")
		if !missing(err) {
			t.Fatal("unselected environment was traversed", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := state.Sequence
	if err := i.Hint(t.Context(), scope, "runs/environment/lib/unused.md", now); err != nil {
		t.Fatal(err)
	}
	if got := inventoryState(t, i, scope); got.Sequence != before || len(got.Scans) != 0 {
		t.Fatal("unselected hint woke a subtree scan", got)
	}
	if old := inventoryRow(t, i, scope, "runs/old.md"); !old.Present || old.Reason != "source_excluded" {
		t.Fatal("retained exclusion was changed", old)
	}
}

func TestInventoryCoalescesUnstartedScanHints(t *testing.T) {
	var state InventoryState
	state.enqueue("")
	state.enqueue("")
	state.enqueue("")
	if len(state.Scans) != 1 || state.Scans[0].Again {
		t.Fatal("duplicate startup hints scheduled another full scan", state.Scans)
	}
	state.Scans[0].After = "a.md"
	state.enqueue("")
	if !state.Scans[0].Again {
		t.Fatal("a hint behind an active cursor lost its follow-up")
	}
}

func TestInventorySubtreeAgainYieldsToBackgroundScan(t *testing.T) {
	i, scope, now := inventoryFixture(t)
	inventoryWrite(t, scope, "hot/a.md", "A")
	inventoryWrite(t, scope, "hot/b.md", "B")
	inventoryWrite(t, scope, "z.md", "background")
	if err := i.Hint(t.Context(), scope, "hot", now); err != nil {
		t.Fatal(err)
	}
	if err := i.Advance(t.Context(), scope, now, 1); err != nil {
		t.Fatal(err)
	}
	active := inventoryState(t, i, scope).Scans[0]
	if active.Prefix != "hot" || active.After == "" {
		t.Fatal("missing active subtree cursor", active)
	}
	if err := i.Hint(t.Context(), scope, "", now); err != nil {
		t.Fatal(err)
	}
	background := inventoryState(t, i, scope).Scans[1]
	inventoryWrite(t, scope, "other/new.md", "new")
	if err := i.Hint(t.Context(), scope, "other", now); err != nil {
		t.Fatal(err)
	}
	if got := inventoryState(t, i, scope).Scans[0]; got != active {
		t.Fatal("new subtree hint preempted active page", got, active)
	}
	backgroundDone := false
	for turn := 0; turn < 16; turn++ {
		if err := i.Hint(t.Context(), scope, "hot", now); err != nil {
			t.Fatal(err)
		}
		// Each turn resumes only committed state, with no in-memory queue owner.
		i = SourceInventory{Store: i.Store}
		if err := i.Advance(t.Context(), scope, now, 1); err != nil {
			t.Fatal(err)
		}
		pending := false
		for _, scan := range inventoryState(t, i, scope).Scans {
			pending = pending || scan.Sequence == background.Sequence
		}
		if !pending {
			backgroundDone = true
			break
		}
	}
	if !backgroundDone || !inventoryRow(t, i, scope, "z.md").Present {
		t.Fatal("repeated subtree follow-ups starved background scan", inventoryState(t, i, scope))
	}
	for turn := 0; turn < 32 && !inventoryState(t, i, scope).Complete; turn++ {
		if err := i.Advance(t.Context(), scope, now, 1); err != nil {
			t.Fatal(err)
		}
	}
	if state := inventoryState(t, i, scope); !state.Complete || len(state.Scans) != 0 {
		t.Fatal("finite hint burst did not drain", state)
	}
}

func TestInventoryOverflowPreservesActivePage(t *testing.T) {
	for _, prefix := range []string{"Selected", "Selected/hot"} {
		for _, withdrawing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/withdrawing=%t", prefix, withdrawing), func(t *testing.T) {
				active := inventoryScan{Prefix: prefix, After: prefix + "/a.md", Sequence: 7, Withdrawing: withdrawing}
				state := InventoryState{Sequence: 7, Scans: []inventoryScan{active}}
				for n := 0; n < 300; n++ {
					state.enqueue(fmt.Sprintf("Selected/dir-%03d", n))
					if len(state.Scans) > 128 {
						t.Fatal("unbounded scan hints", len(state.Scans))
					}
				}
				got := state.Scans[0]
				if prefix == "Selected" {
					active.Again = true
				}
				if got != active || state.Complete || state.Degraded != "hint_overflow" {
					t.Fatal("overflow discarded active progress or claimed completion", state)
				}
				covered := false
				for _, scan := range state.Scans {
					covered = covered || scan.Prefix == "Selected"
					if !inventoryContains("Selected", scan.Prefix) {
						t.Fatal("overflow expanded admitted prefix", scan)
					}
				}
				if !covered {
					t.Fatal("overflow lost conservative follow-up", state)
				}
			})
		}
	}
}

type observedInventoryRecords struct {
	InventoryRecords
	gets, puts, seen int
	seenError        error
}

func (r *observedInventoryRecords) Get(ctx context.Context, path string) (InventoryEntry, error) {
	r.gets++
	return r.InventoryRecords.Get(ctx, path)
}
func (r *observedInventoryRecords) Put(ctx context.Context, entry InventoryEntry) error {
	r.puts++
	return r.InventoryRecords.Put(ctx, entry)
}
func (r *observedInventoryRecords) MarkSeen(ctx context.Context, path string, scan int64) error {
	r.seen++
	if r.seenError != nil {
		return r.seenError
	}
	return r.InventoryRecords.MarkSeen(ctx, path, scan)
}

func TestInventorySeenScanPreservesPublicationState(t *testing.T) {
	_, _, now := inventoryFixture(t)
	old := InventoryEntry{Path: "a.md", Kind: "text", Present: true, Fingerprint: "unchanged", Size: 10,
		Sequence: 5, Delivered: 3, SeenScan: 2, Priority: true, WaitingSince: now.Add(-time.Minute),
		RetryAt: now.Add(time.Minute), Attempts: 3, Reason: "publication_pending"}
	counts := InventoryCounts{}
	inventoryTrack(&counts, InventoryEntry{}, old)
	records := &memoryInventoryRecords{state: InventoryState{CountersReady: true, Counts: counts, Sequence: 9}, entries: map[string]InventoryEntry{old.Path: old}}
	r := &observedInventoryRecords{InventoryRecords: records}
	observation := InventoryEntry{Path: old.Path, Kind: old.Kind, Present: old.Present, Fingerprint: old.Fingerprint, Size: old.Size}
	if err := inventoryObserve(t.Context(), r, observation, 9, false, now); err != nil {
		t.Fatal(err)
	}
	want := old
	want.SeenScan = 9
	if records.entries[old.Path] != want || records.state.Counts != counts || records.state.Sequence != 9 || r.gets != 1 || r.puts != 0 || r.seen != 1 {
		t.Fatal("unchanged observation rewrote publication state or used full upsert", records, r)
	}
	if err := inventoryObserve(t.Context(), r, observation, 8, false, now); err != nil || r.seen != 1 || records.entries[old.Path] != want {
		t.Fatal("older scan rewrote newer observation", records, r, err)
	}
	r.seenError = errors.New("seen write failed")
	if err := inventoryObserve(t.Context(), r, observation, 10, false, now); !errors.Is(err, r.seenError) || records.entries[old.Path] != want {
		t.Fatal("seen write failure was hidden", records, err)
	}
}

func TestInventoryProgressScanSummary(t *testing.T) {
	backlog := []inventoryScan{{Prefix: "Selected", After: "Selected/a.md", Again: true}}
	for n := 0; n < 127; n++ {
		backlog = append(backlog, inventoryScan{Prefix: fmt.Sprintf("waiting-not-for-publication-%d", n), After: "private-waiting-cursor"})
	}
	for _, tc := range []struct {
		name     string
		scans    []inventoryScan
		complete bool
		active   *InventoryScanProgress
		queued   int
	}{
		{name: "legacy"},
		{name: "idle", complete: true},
		{name: "root discovery", scans: []inventoryScan{{}}, active: &InventoryScanProgress{Phase: "discovery"}},
		{name: "discovery backlog", scans: backlog, active: &InventoryScanProgress{Prefix: "Selected", After: "Selected/a.md", Phase: "discovery", Again: true}, queued: 127},
		{name: "withdrawal", scans: []inventoryScan{{Prefix: "Selected", After: "Selected/gone.md", Withdrawing: true}}, active: &InventoryScanProgress{Prefix: "Selected", After: "Selected/gone.md", Phase: "withdrawal"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i, scope, now := inventoryFixture(t)
			if err := i.Store.WithInventory(t.Context(), scope, func(r InventoryRecords) error {
				r.State().Scans, r.State().Complete = tc.scans, tc.complete
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before := inventoryState(t, i, scope)
			progress, err := i.Progress(t.Context(), scope, now)
			if err != nil || !reflect.DeepEqual(progress.ActiveScan, tc.active) || progress.QueuedScans != tc.queued || progress.Complete != tc.complete {
				t.Fatal("incorrect bounded scan summary", progress, err)
			}
			if after := inventoryState(t, i, scope); !reflect.DeepEqual(before, after) {
				t.Fatal("scan projection changed durable state", before, after)
			}
			raw, err := json.Marshal(inventoryPublication(progress))
			if err != nil {
				t.Fatal(err)
			}
			var publication map[string]json.RawMessage
			if err := json.Unmarshal(raw, &publication); err != nil {
				t.Fatal(err)
			}
			if _, listed := publication["scans"]; listed || strings.Contains(string(raw), "waiting-not-for-publication") || strings.Contains(string(raw), "private-waiting-cursor") {
				t.Fatal("scan summary exposed waiting scan paths", string(raw))
			}
			var active *InventoryScanProgress
			if rawActive, ok := publication["active_scan"]; ok {
				if err := json.Unmarshal(rawActive, &active); err != nil {
					t.Fatal(err)
				}
			}
			var queued int
			if err := json.Unmarshal(publication["queued_scans"], &queued); err != nil || queued != tc.queued || !reflect.DeepEqual(active, tc.active) {
				t.Fatal("publication lost bounded scan summary", string(raw), err)
			}
		})
	}
}

func TestInventoryProgressShowsUnchangedScanAdvancement(t *testing.T) {
	i, scope, now := inventoryFixture(t)
	for _, p := range []string{"a.md", "b.md", "c.md"} {
		inventoryWrite(t, scope, p, "unchanged")
	}
	inventorySeed(t, i, scope, now, 128)
	if err := i.Hint(t.Context(), scope, "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := i.Advance(t.Context(), scope, now.Add(time.Second), 1); err != nil {
		t.Fatal(err)
	}
	first, err := i.Progress(t.Context(), scope, now.Add(time.Second))
	if err != nil || first.ActiveScan == nil || first.ActiveScan.After != "a.md" || first.ActiveScan.Phase != "discovery" || first.ActiveScan.Again {
		t.Fatal("missing first scan position", first, err)
	}
	if err := i.Hint(t.Context(), scope, "", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := i.Advance(t.Context(), scope, now.Add(2*time.Second), 1); err != nil {
		t.Fatal(err)
	}
	second, err := i.Progress(t.Context(), scope, now.Add(2*time.Second))
	if err != nil || second.ActiveScan == nil || second.ActiveScan.After != "b.md" || second.ActiveScan.Phase != "discovery" || !second.ActiveScan.Again {
		t.Fatal("unchanged scan advancement is not visible", second, err)
	}
	if first.Sequence != second.Sequence || first.Counts != second.Counts || !first.ObservedAt.Equal(second.ObservedAt) || second.QueuedScans != 0 {
		t.Fatal("scan advancement fabricated changed inventory or completion", first, second)
	}
}

func inventoryWrite(t *testing.T, s InventoryScope, p, content string) {
	t.Helper()
	full := filepath.Join(s.Root, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func inventoryRow(t *testing.T, i SourceInventory, s InventoryScope, p string) InventoryEntry {
	t.Helper()
	var out InventoryEntry
	if err := i.Store.WithInventory(t.Context(), s, func(r InventoryRecords) error { var err error; out, err = r.Get(t.Context(), p); return err }); err != nil {
		t.Fatal(err)
	}
	return out
}
func inventoryState(t *testing.T, i SourceInventory, s InventoryScope) InventoryState {
	t.Helper()
	var out InventoryState
	if err := i.Store.WithInventory(t.Context(), s, func(r InventoryRecords) error { out = *r.State(); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}
func inventorySeed(t *testing.T, i SourceInventory, s InventoryScope, now time.Time, limit int) {
	t.Helper()
	if err := i.Begin(t.Context(), s, "", now); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 100; n++ {
		if err := i.Advance(t.Context(), s, now, limit); err != nil {
			t.Fatal(err)
		}
		if inventoryState(t, i, s).Complete {
			return
		}
	}
	t.Fatal("inventory failed to finish")
}
func TestInventoryResumeIdleAndGeneration(t *testing.T) {
	i, s, now := inventoryFixture(t)
	for _, p := range []string{"a.md", "b.md", "image.png", "nested/c.md"} {
		inventoryWrite(t, s, p, "bytes")
	}
	if _, err := noFollowInfo(s.Root, ""); err != nil {
		t.Fatal("root metadata", s.Root, err)
	}
	if _, _, err := inventorySourcePage(t.Context(), s.Root, "", "", 1); err != nil {
		t.Fatal("source page", err)
	}
	if _, err := observeInventoryPath(s.Root, "a.md"); err != nil {
		t.Fatal("observation", err)
	}
	calls := 0
	i.Page = func(ctx context.Context, root, prefix, after string, limit int) ([]string, bool, error) {
		calls++
		return inventorySourcePage(ctx, root, prefix, after, limit)
	}
	if err := i.Begin(t.Context(), s, "", now); err != nil {
		t.Fatal(err)
	}
	if err := i.Advance(t.Context(), s, now, 1); err != nil {
		t.Fatal(err)
	}
	first := inventoryState(t, i, s)
	if first.Complete || first.Scans[0].After != "a.md" {
		t.Fatalf("partial %+v", first)
	}
	// Recompose owner and preserve durable continuation instead of re-seeding.
	i = SourceInventory{Store: i.Store, Page: i.Page}
	if err := i.Begin(t.Context(), s, "", now); err != nil {
		t.Fatal(err)
	}
	if got := inventoryState(t, i, s).Scans[0].After; got != "a.md" {
		t.Fatal("reset cursor", got)
	}
	for !inventoryState(t, i, s).Complete {
		if err := i.Advance(t.Context(), s, now, 1); err != nil {
			t.Fatal(err)
		}
	}
	page, err := i.DeliveryPage(t.Context(), s, "", now, 32)
	if err != nil || len(page.Entries) != 5 || !page.Complete {
		t.Fatalf("page %+v %v", page, err)
	}
	for _, entry := range page.Entries {
		if err := i.Finish(t.Context(), s, entry, "", now); err != nil {
			t.Fatal(err)
		}
	}
	previous := calls
	for n := 0; n < 4; n++ {
		if err := i.Advance(t.Context(), s, now.Add(time.Minute), 1); err != nil {
			t.Fatal(err)
		}
	}
	if calls != previous {
		t.Fatal("unchanged transport traversed source")
	}
	page, _ = i.DeliveryPage(t.Context(), s, "", now, 32)
	if len(page.Entries) != 0 {
		t.Fatal("redelivered unchanged rows")
	}
	s.Generation = "new-generation"
	page, _ = i.DeliveryPage(t.Context(), s, "", now, 32)
	if page.Complete || len(page.Entries) != 0 {
		t.Fatal("generation reused former rows")
	}
}
func TestInventoryHealthyRepairDeadline(t *testing.T) {
	i, scope, _ := inventoryFixture(t)
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	inventoryWrite(t, scope, "a.md", "unchanged")
	calls := 0
	i.Page = func(ctx context.Context, root, prefix, after string, limit int) ([]string, bool, error) {
		calls++
		return inventorySourcePage(ctx, root, prefix, after, limit)
	}
	inventorySeed(t, i, scope, now, 32)
	before := inventoryState(t, i, scope)
	interval := before.NextRepair.Sub(now)
	if !before.Complete || before.Degraded != "" || interval < 6*time.Hour || interval > 6*time.Hour+10*time.Minute || calls != 1 {
		t.Fatal("invalid healthy repair deadline", before, interval, calls)
	}
	// Recreate the consumer using only committed state and advance a short clock.
	i = SourceInventory{Store: i.Store, Page: i.Page}
	for _, at := range []time.Time{now.Add(16 * time.Minute), before.NextRepair.Add(-time.Nanosecond)} {
		if err := i.Advance(t.Context(), scope, at, 32); err != nil {
			t.Fatal(err)
		}
		if got := inventoryState(t, i, scope); calls != 1 || !reflect.DeepEqual(got, before) {
			t.Fatal("healthy source scanned before its persisted deadline", at, calls, got)
		}
	}
	if err := i.Advance(t.Context(), scope, before.NextRepair, 32); err != nil {
		t.Fatal(err)
	}
	after := inventoryState(t, i, scope)
	if calls != 2 || !after.Complete || !after.ObservedAt.Equal(before.NextRepair) || !after.NextRepair.Equal(before.NextRepair.Add(interval)) {
		t.Fatal("healthy fallback did not run at its staggered deadline", calls, after)
	}
}

func TestInventoryRepairIntervalStaggerAndDegradedCadence(t *testing.T) {
	scope := InventoryScope{Collection: "source", Generation: "generation", RepairInterval: 3 * time.Minute}
	interval := inventoryRepairInterval(scope, "")
	if interval < 6*time.Hour || interval > 6*time.Hour+10*time.Minute {
		t.Fatal("healthy stagger exceeds bounded six-hour fallback", interval)
	}
	for _, changed := range []InventoryScope{
		{Collection: "other", Generation: scope.Generation},
		{Collection: scope.Collection, Generation: "other"},
	} {
		got := inventoryRepairInterval(changed, "")
		if got == interval || got < 6*time.Hour || got > 6*time.Hour+10*time.Minute {
			t.Fatal("collection/generation did not independently stagger the deadline", changed, got, interval)
		}
	}
	moved := scope
	moved.Root, moved.Prefix, moved.RepairInterval = "/other", "selected", time.Minute
	if got := inventoryRepairInterval(moved, ""); got != interval {
		t.Fatal("healthy stagger changed outside collection/generation identity", got, interval)
	}
	for _, posture := range []string{"watch_unavailable", "source_unavailable", "hint_overflow"} {
		if got := inventoryRepairInterval(scope, posture); got != scope.RepairInterval {
			t.Fatal("degraded source lost configured cadence", posture, got)
		}
		unconfigured := scope
		unconfigured.RepairInterval = 0
		if got := inventoryRepairInterval(unconfigured, posture); got != time.Minute {
			t.Fatal("degraded source lost default cadence", posture, got)
		}
	}
}

func TestInventoryHintsAndCompareAck(t *testing.T) {
	i, s, now := inventoryFixture(t)
	inventoryWrite(t, s, "a.md", "old")
	inventorySeed(t, i, s, now, 1)
	old := inventoryRow(t, i, s, "a.md")
	replacement := filepath.Join(s.Root, "replacement")
	if err := os.WriteFile(replacement, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, filepath.Join(s.Root, "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := i.Hint(t.Context(), s, "a.md", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	changed := inventoryRow(t, i, s, "a.md")
	if changed.Sequence <= old.Sequence || !changed.Priority {
		t.Fatal("atomic save not priority", changed)
	}
	if err := i.Finish(t.Context(), s, old, "", now); err != nil {
		t.Fatal(err)
	}
	if got := inventoryRow(t, i, s, "a.md"); got.Delivered == got.Sequence {
		t.Fatal("concurrent change lost by old ack")
	}
	if err := i.Finish(t.Context(), s, changed, "publication_pending", now); err != nil {
		t.Fatal(err)
	}
	inventoryWrite(t, s, "z.md", "other")
	if err := i.Hint(t.Context(), s, "z.md", now); err != nil {
		t.Fatal(err)
	}
	page, _ := i.DeliveryPage(t.Context(), s, "text", now, 32)
	if len(page.Entries) != 1 || page.Entries[0].Path != "z.md" {
		t.Fatal("retry blocked other paths", page)
	}
	page, _ = i.DeliveryPage(t.Context(), s, "text", now.Add(2*time.Second), 32)
	if len(page.Entries) != 2 {
		t.Fatal("retry deadline not selected", page)
	}
}
func TestInventorySubtreeWithdrawalAndUnavailableRoot(t *testing.T) {
	i, s, now := inventoryFixture(t)
	inventoryWrite(t, s, "dir/a.md", "A")
	inventoryWrite(t, s, "b.md", "B")
	inventorySeed(t, i, s, now, 1)
	if err := os.RemoveAll(filepath.Join(s.Root, "dir")); err != nil {
		t.Fatal(err)
	}
	if err := i.Hint(t.Context(), s, "dir", now); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 8 && !inventoryState(t, i, s).Complete; n++ {
		if err := i.Advance(t.Context(), s, now, 1); err != nil {
			t.Fatal(err)
		}
	}
	if inventoryRow(t, i, s, "dir/a.md").Present || inventoryRow(t, i, s, "dir/").Present || !inventoryRow(t, i, s, "b.md").Present {
		t.Fatal("subtree absence affected wrong rows")
	}
	oldRoot := s.Root + "-moved"
	if err := os.Rename(s.Root, oldRoot); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(oldRoot, s.Root)
	if err := i.Hint(t.Context(), s, "", now); err != nil {
		t.Fatal(err)
	}
	if err := i.Advance(t.Context(), s, now, 1); err != nil {
		t.Fatal(err)
	}
	if !inventoryRow(t, i, s, "b.md").Present || inventoryState(t, i, s).Degraded != "source_unavailable" {
		t.Fatal("missing root treated as deletion")
	}
}
func TestInventoryMissedEventSafetyAndNoFollow(t *testing.T) {
	i, s, now := inventoryFixture(t)
	inventoryWrite(t, s, "a.md", "A")
	inventorySeed(t, i, s, now, 32)
	first := inventoryRow(t, i, s, "a.md")
	inventoryWrite(t, s, "a.md", "B")
	inventoryWrite(t, s, "dir/new.md", "new")
	if err := i.Advance(t.Context(), s, inventoryState(t, i, s).NextRepair, 32); err != nil {
		t.Fatal(err)
	}
	if inventoryRow(t, i, s, "a.md").Sequence <= first.Sequence || !inventoryRow(t, i, s, "dir/new.md").Present {
		t.Fatal("missed change not repaired")
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.Root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := observeInventoryPath(s.Root, "link/secret.md"); err == nil {
		t.Fatal("followed symlink")
	}
	page, _, err := inventorySourcePage(t.Context(), s.Root, "", "", 32)
	if err != nil || !reflect.DeepEqual(page, []string{"a.md", "dir/", "dir/new.md"}) {
		t.Fatal(page, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := i.Advance(ctx, s, now, 32); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation", err)
	}
}

func (m *memoryInventoryRecords) Waiting(_ context.Context, kind string) (*InventoryEntry, *time.Time, error) {
	var oldest *InventoryEntry
	var retry *time.Time
	for _, e := range m.entries {
		if (e.Kind == kind || kind == "text" && e.Kind == "directory") && (e.Present || e.Kind == "directory") && e.Reason != "source_excluded" && e.Sequence > e.Delivered {
			if oldest == nil || e.WaitingSince.Before(oldest.WaitingSince) {
				copy := e
				oldest = &copy
			}
			if retry == nil || e.RetryAt.Before(*retry) {
				copy := e.RetryAt
				retry = &copy
			}
		}
	}
	return oldest, retry, nil
}

func TestInventoryExcludedIsNotDeliveredOrRetried(t *testing.T) {
	i, scope, now := inventoryFixture(t)
	inventoryWrite(t, scope, "report.json", "{}")
	inventorySeed(t, i, scope, now, 32)
	entry := inventoryRow(t, i, scope, "report.json")
	if err := i.Finish(t.Context(), scope, entry, "source_excluded", now); err != nil {
		t.Fatal(err)
	}
	row := inventoryRow(t, i, scope, entry.Path)
	if row.Delivered != entry.Delivered || row.Attempts != 0 || row.Reason != "source_excluded" {
		t.Fatal("exclusion masqueraded as delivery or failure", row)
	}
	page, err := i.DeliveryPage(t.Context(), scope, "asset", now.Add(time.Hour), 32)
	if err != nil || len(page.Entries) != 0 {
		t.Fatal("excluded path retried", page, err)
	}
	progress, err := i.Progress(t.Context(), scope, now)
	if err != nil || progress.Counts.Excluded != 1 || progress.Counts.AssetPending != 0 || progress.Counts.PendingBytes != 0 || progress.AssetRetryAt != nil {
		t.Fatal("incorrect exclusion progress", progress, err)
	}
	inventoryWrite(t, scope, entry.Path, "{\"updated\":true}")
	if err := i.Hint(t.Context(), scope, entry.Path, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	page, err = i.DeliveryPage(t.Context(), scope, "asset", now.Add(time.Second), 32)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Reason != "" {
		t.Fatal("changed observation not rechecked", page, err)
	}
	progress, err = i.Progress(t.Context(), scope, now)
	if err != nil || progress.Counts.Excluded != 0 || progress.Counts.AssetPending != 1 {
		t.Fatal("re-admission counters", progress, err)
	}
}

func TestInventoryPublicationLaneExclusions(t *testing.T) {
	counts := InventoryCounts{}
	text := InventoryEntry{Path: "a.md", Kind: "text", Present: true, Sequence: 1, Delivered: 1}
	asset := InventoryEntry{Path: "a.pdf", Kind: "asset", Present: true, Sequence: 1}
	inventoryTrack(&counts, InventoryEntry{}, text)
	inventoryTrack(&counts, InventoryEntry{}, asset)
	excluded := asset
	excluded.Reason = "source_excluded"
	inventoryTrack(&counts, asset, excluded)
	progress := InventoryProgress{CountersKnown: true, LaneExclusionsKnown: true, Counts: counts}
	publication := inventoryPublication(progress)
	if publication.TextPublished == nil || *publication.TextPublished != 1 || publication.AssetPublished == nil || *publication.AssetPublished != 0 || counts.AssetExcluded != 1 || counts.TextExcluded != 0 {
		t.Fatalf("excluded assets hid unrelated text publication: %+v", publication)
	}
	// A kind change must move the exclusion between lanes exactly once.
	changed := excluded
	changed.Kind = "text"
	inventoryTrack(&counts, excluded, changed)
	if counts.AssetExcluded != 0 || counts.TextExcluded != 1 || counts.Excluded != 1 {
		t.Fatal("kind transition lost exclusions", counts)
	}
	withdrawn := changed
	withdrawn.Present = false
	inventoryTrack(&counts, changed, withdrawn)
	if counts.TextExcluded != 0 || counts.Excluded != 0 || counts.Withdrawn != 1 {
		t.Fatal("withdrawal left exclusions", counts)
	}
	progress.LaneExclusionsKnown = false
	publication = inventoryPublication(progress)
	if publication.TextPublished != nil || publication.AssetPublished != nil {
		t.Fatal("legacy exclusion split was fabricated")
	}
	raw, err := json.Marshal(publication)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Counts map[string]*int64 `json:"counts"`
	}
	if err = json.Unmarshal(raw, &payload); err != nil || payload.Counts["text_excluded"] != nil || payload.Counts["asset_excluded"] != nil {
		t.Fatal("legacy lane exclusions reported as zero", string(raw), err)
	}
}

func TestInventoryMappingIndexCatchupDoesNotEscalateFailureBackoff(t *testing.T) {
	i, scope, now := inventoryFixture(t)
	inventoryWrite(t, scope, "a.md", "A")
	inventorySeed(t, i, scope, now, 32)
	for n := 0; n < 3; n++ {
		entry := inventoryRow(t, i, scope, "a.md")
		if err := i.Finish(t.Context(), scope, entry, "binding_index_pending", now); err != nil {
			t.Fatal(err)
		}
		row := inventoryRow(t, i, scope, "a.md")
		if row.Attempts != 0 || !row.RetryAt.Equal(now.Add(2*time.Second)) || row.Delivered != 0 {
			t.Fatal("catch-up escalated or acknowledged publication", row)
		}
	}
}

func TestInventoryFailedPageAndOverflowKeepExactScope(t *testing.T) {
	i, scope, now := inventoryFixture(t)
	scope.Prefix = "Selected"
	inventoryWrite(t, scope, "Selected/a.md", "A")
	inventorySeed(t, i, scope, now, 32)
	old := inventoryRow(t, i, scope, "Selected/a.md")
	i.Page = func(context.Context, string, string, string, int) ([]string, bool, error) {
		return nil, false, os.ErrPermission
	}
	if err := i.Hint(t.Context(), scope, "", now); err != nil {
		t.Fatal(err)
	}
	if err := i.Advance(t.Context(), scope, now, 32); err != nil {
		t.Fatal(err)
	}
	if row := inventoryRow(t, i, scope, "Selected/a.md"); !row.Present || row.Sequence != old.Sequence {
		t.Fatal("failed page withdrew a retained path", row)
	}
	if progress, err := i.Progress(t.Context(), scope, now); err != nil || progress.TotalsKnown || progress.Degraded != "source_unavailable" {
		t.Fatal("failed page claimed complete total", progress, err)
	}
	if err := i.Store.WithInventory(t.Context(), scope, func(r InventoryRecords) error {
		r.State().Scans = nil
		for n := 0; n < 129; n++ {
			r.State().enqueue(fmt.Sprintf("Selected/dir-%d", n))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state := inventoryState(t, i, scope)
	if len(state.Scans) != 1 || state.Scans[0].Prefix != "Selected" {
		t.Fatal("overflow expanded admitted prefix", state)
	}
}

func TestInventoryObservationFailureKeepsCursorAndRetry(t *testing.T) {
	i, scope, now := inventoryFixture(t)
	for _, p := range []string{"a.md", "b.md", "c.md"} {
		inventoryWrite(t, scope, p, "retained")
	}
	inventorySeed(t, i, scope, now, 128)
	before := inventoryRow(t, i, scope, "b.md")
	failedAt := now.Add(time.Second)
	if err := i.Hint(t.Context(), scope, "", failedAt); err != nil {
		t.Fatal(err)
	}
	calls := 0
	i.Observe = func(root, path string) (InventoryEntry, error) {
		calls++
		if path == "b.md" {
			return InventoryEntry{}, os.ErrPermission
		}
		return observeInventoryPath(root, path)
	}
	if err := i.Advance(t.Context(), scope, failedAt, 128); err != nil {
		t.Fatal(err)
	}
	state := inventoryState(t, i, scope)
	if state.Complete || state.Degraded != "source_unavailable" || state.Scans[0].After != "a.md" || state.Scans[0].Withdrawing || !state.NextRepair.After(failedAt) || !state.ObservedAt.Equal(now) {
		t.Fatal("I/O failure lost committed page progress or claimed completion", state)
	}
	if got := inventoryRow(t, i, scope, "b.md"); got != before {
		t.Fatal("I/O failure changed retained entry", got, before)
	}
	if err := i.Advance(t.Context(), scope, failedAt.Add(time.Second), 128); err != nil || calls != 2 {
		t.Fatal("I/O failure ignored retry deadline", calls, err)
	}
	i = SourceInventory{Store: i.Store}
	var resumed []string
	i.Observe = func(root, path string) (InventoryEntry, error) {
		resumed = append(resumed, path)
		return observeInventoryPath(root, path)
	}
	if err := i.Advance(t.Context(), scope, state.NextRepair, 128); err != nil {
		t.Fatal(err)
	}
	if state := inventoryState(t, i, scope); !state.Complete || state.Degraded != "" || !reflect.DeepEqual(resumed, []string{"b.md", "c.md"}) {
		t.Fatal("repair replayed committed entries or failed to finish", state, resumed)
	}
}

func TestInventoryFingerprintIgnoresReadAccessAndCatchesReplacement(t *testing.T) {
	_, scope, _ := inventoryFixture(t)
	inventoryWrite(t, scope, "a.md", "old")
	before, err := observeInventoryPath(scope.Root, "a.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(filepath.Join(scope.Root, "a.md")); err != nil {
		t.Fatal(err)
	}
	same, err := observeInventoryPath(scope.Root, "a.md")
	if err != nil || same.Fingerprint != before.Fingerprint {
		t.Fatal("read access dirtied source", same, err)
	}
	info, err := os.Stat(filepath.Join(scope.Root, "a.md"))
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(scope.Root, "replace")
	if err := os.WriteFile(replacement, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, filepath.Join(scope.Root, "a.md")); err != nil {
		t.Fatal(err)
	}
	changed, err := observeInventoryPath(scope.Root, "a.md")
	if err != nil || changed.Fingerprint == before.Fingerprint {
		t.Fatal("same-size restored-mtime replacement missed", changed, err)
	}
}

func TestInventoryLegacyCountersRemainUnknown(t *testing.T) {
	i, scope, now := inventoryFixture(t)
	if err := i.Store.WithInventory(t.Context(), scope, func(r InventoryRecords) error { r.State().Complete = true; r.State().CountersReady = false; return nil }); err != nil {
		t.Fatal(err)
	}
	progress, err := i.Progress(t.Context(), scope, now)
	if err != nil || progress.CountersKnown || progress.TotalsKnown {
		t.Fatal("legacy metadata invented current totals", progress, err)
	}
}
