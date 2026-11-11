package notesworkspacesync

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/knowledge"
	rootpolicy "loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/notesworkspace"
)

// InventoryScope is an exact admitted owner generation, never a source registry.
// WatchKey identifies an existing watched-root owner; a missing shared hook must
// degrade to bounded repair, rather than create a duplicate recursive watch.
type InventoryScope struct {
	Collection, Generation, Root, Prefix, WatchKey, OwnerNodeKey string
	RepairInterval                                               time.Duration
	WorkspaceIncludes                                            string
}

type InventoryEntry struct {
	Path         string    `json:"path"`
	Kind         string    `json:"kind"`
	Present      bool      `json:"present"`
	Fingerprint  string    `json:"fingerprint"`
	Size         int64     `json:"size_bytes"`
	Sequence     int64     `json:"sequence"`
	Delivered    int64     `json:"delivered_sequence"`
	SeenScan     int64     `json:"seen_scan"`
	Priority     bool      `json:"priority"`
	WaitingSince time.Time `json:"waiting_since"`
	RetryAt      time.Time `json:"retry_at"`
	Attempts     int       `json:"attempts"`
	Reason       string    `json:"reason,omitempty"`
}

type inventoryScan struct {
	Prefix      string
	After       string
	Sequence    int64
	Withdrawing bool
	Again       bool
}

type InventoryState struct {
	CountersReady       bool            `json:"counters_ready"`
	LaneExclusionsReady bool            `json:"lane_exclusions_ready,omitempty"`
	Counts              InventoryCounts `json:"counts"`
	ObservedAt          time.Time       `json:"observed_at"`
	WatchReason         string          `json:"watch_reason,omitempty"`
	WatchDetail         string          `json:"watch_detail,omitempty"`
	Sequence            int64           `json:"sequence"`
	Complete            bool            `json:"complete"`
	Degraded            string          `json:"degraded,omitempty"`
	NextRepair          time.Time       `json:"next_repair"`
	Scans               []inventoryScan `json:"scans,omitempty"`
}

// Page carries explicit completeness: absence from a bounded page never proves
// withdrawal. Sequence/row acknowledgments survive worker checkpoint failures.
type InventoryPage struct {
	Generation string           `json:"generation"`
	Entries    []InventoryEntry `json:"entries"`
	More       bool             `json:"more"`
	Complete   bool             `json:"complete"`
	Degraded   string           `json:"degraded,omitempty"`
}

type InventoryStore interface {
	WithInventory(context.Context, InventoryScope, func(InventoryRecords) error) error
}

type InventoryRecords interface {
	State() *InventoryState
	Get(context.Context, string) (InventoryEntry, error)
	Put(context.Context, InventoryEntry) error
	MarkSeen(context.Context, string, int64) error
	Pending(context.Context, string, time.Time, int) ([]InventoryEntry, error)
	Unseen(context.Context, string, string, int64, int) ([]InventoryEntry, error)
	Waiting(context.Context, string) (*InventoryEntry, *time.Time, error)
}

type SourceInventory struct {
	Store InventoryStore
	// Override only for focused tests; production uses bounded no-follow paging.
	Page    func(context.Context, string, string, string, int) ([]string, bool, error)
	Observe func(string, string) (InventoryEntry, error)
}

func (i SourceInventory) observe(root, relative string) (InventoryEntry, error) {
	if i.Observe != nil {
		return i.Observe(root, relative)
	}
	return observeInventoryPath(root, relative)
}

func observeInventoryPath(root, relative string) (InventoryEntry, error) {
	directory := strings.HasSuffix(relative, "/")
	canonical := strings.TrimSuffix(relative, "/")
	if !IsSourcePath(relative) && !directoryPath(canonical) {
		return InventoryEntry{}, ErrHeld
	}
	info, err := noFollowInfo(root, canonical)
	out := InventoryEntry{Path: relative, Kind: "asset", Present: err == nil}
	if directory || err == nil && info.IsDir() {
		out.Kind = "directory"
		out.Path = canonical + "/"
	} else if textPath(relative) {
		out.Kind = "text"
	}
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return out, ErrHeld
	}
	if info.Mode().IsRegular() && !IsSourcePath(relative) {
		return out, ErrHeld
	}
	// Metadata observation only, never a content-hash claim. ctime + inode also
	// catch same-size atomic replacement and restored mtime; canonical Read owns bytes.
	stat := info.Sys()
	if stat == nil {
		return out, ErrHeld
	}
	raw, _ := json.Marshal(stat)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	delete(fields, "Atim")
	delete(fields, "Atimespec")
	delete(fields, "Atime")
	delete(fields, "Atimensec")
	raw, _ = json.Marshal(fields)
	out.Fingerprint = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	out.Size = info.Size()
	if out.Kind == "directory" {
		out.Size = 0
	}
	return out, nil
}

func noFollowInfo(root, relative string) (os.FileInfo, error) {
	return notesworkspace.ObserveSourcePath(root, relative)
}
func inventorySourcePage(ctx context.Context, root, prefix, after string, limit int) ([]string, bool, error) {
	return inventorySelectedSourcePage(ctx, root, prefix, after, limit, "")
}

func inventoryPathSelected(encoded, relative string, directory bool) bool {
	if encoded == "" {
		return true
	}
	var include []string
	if json.Unmarshal([]byte(encoded), &include) != nil {
		return false
	}
	if directory {
		return knowledge.NotesWorkspaceDirectoryIncluded(include, strings.TrimSuffix(relative, "/"))
	}
	matched, _ := rootpolicy.MatchAny(include, relative)
	return matched
}

func inventorySelectedSourcePage(ctx context.Context, root, prefix, after string, limit int, selection string) ([]string, bool, error) {
	var include []string
	if selection != "" && json.Unmarshal([]byte(selection), &include) != nil {
		return nil, false, ErrHeld
	}
	return sourcePageWithReader(ctx, root, prefix, after, limit, func(directory string) ([]os.DirEntry, error) {
		relative, err := filepath.Rel(root, directory)
		if err != nil {
			return nil, err
		}
		if relative == "." {
			relative = ""
		}
		entries, err := notesworkspace.ReadSourceDirectory(root, filepath.ToSlash(relative))
		if err != nil || selection == "" {
			return entries, err
		}
		selected := entries[:0]
		for _, entry := range entries {
			candidate := entry.Name()
			if relative != "" {
				candidate = filepath.ToSlash(relative) + "/" + candidate
			}
			matched, _ := rootpolicy.MatchAny(include, candidate)
			if entry.IsDir() {
				matched = knowledge.NotesWorkspaceDirectoryIncluded(include, candidate)
			}
			if matched {
				selected = append(selected, entry)
			}
		}
		return selected, nil
	})
}

func (s *InventoryState) enqueue(prefix string) {
	for index := range s.Scans {
		if s.Scans[index].Prefix == prefix {
			// Only an already-advanced cursor can miss an arrival behind it.
			// Duplicate watch/startup hints before the first page share that pass.
			if s.Scans[index].After != "" || s.Scans[index].Withdrawing {
				s.Scans[index].Again = true
			}
			return
		}
	}
	s.Sequence++
	job := inventoryScan{Prefix: prefix, Sequence: s.Sequence}
	// File hints already receive priority delivery. Discovery and follow-up
	// passes share a FIFO so repeated subtree hints cannot preempt catch-up.
	s.Scans = append(s.Scans, job)
	if len(s.Scans) > 128 {
		common := s.Scans[0].Prefix
		for _, scan := range s.Scans[1:] {
			for common != "" && !inventoryContains(common, scan.Prefix) {
				common = path.Dir(common)
				if common == "." {
					common = ""
				}
			}
		}
		active := s.Scans[0]
		s.Scans = nil
		if active.After != "" || active.Withdrawing {
			// Overflow folds waiting hints, never the active discovery/withdrawal
			// cursor. A later pass still covers arrivals behind that cursor.
			s.Scans = append(s.Scans, active)
		}
		s.enqueue(common)
		s.Degraded = "hint_overflow"
	}
	s.Complete = false
}

// Begin registers the restart catch-up without resetting a durable active page.
func (i SourceInventory) Begin(ctx context.Context, scope InventoryScope, posture string, now time.Time) error {
	return i.beginWatch(ctx, scope, posture, "", now)
}

func (i SourceInventory) beginWatch(ctx context.Context, scope InventoryScope, posture, detail string, now time.Time) error {
	return i.Store.WithInventory(ctx, scope, func(r InventoryRecords) error {
		s := r.State()
		s.Degraded = posture
		s.WatchReason = posture
		s.WatchDetail = detail
		s.enqueue(scope.Prefix)
		s.NextRepair = now
		return nil
	})
}

// Hint persists validated metadata or a bounded reconciliation request before
// the caller wakes transport. Hints carry the exact scope generation.
func (i SourceInventory) Hint(ctx context.Context, scope InventoryScope, relative string, now time.Time) error {
	return i.Store.WithInventory(ctx, scope, func(r InventoryRecords) error {
		if relative == "" {
			r.State().enqueue(scope.Prefix)
			return nil
		}
		if _, err := noFollowInfo(scope.Root, scope.Prefix); err != nil {
			r.State().Degraded = "source_unavailable"
			return nil
		}
		if !inventoryContains(scope.Prefix, relative) || !inventoryPath(strings.TrimSuffix(relative, "/")) {
			return ErrHeld
		}
		if !inventoryPathSelected(scope.WorkspaceIncludes, relative, false) && !inventoryPathSelected(scope.WorkspaceIncludes, relative, true) {
			return nil
		}
		if !strings.HasSuffix(relative, "/") {
			if old, e := r.Get(ctx, relative+"/"); e == nil && old.Kind == "directory" {
				relative += "/"
			}
		}
		entry, err := i.observe(scope.Root, relative)
		if err == nil {
			if entry.Kind == "directory" {
				r.State().enqueue(strings.TrimSuffix(entry.Path, "/"))
			}
			if !entry.Present {
				if _, err := noFollowInfo(scope.Root, scope.Prefix); err != nil {
					r.State().Degraded = "source_unavailable"
					return nil
				}
			}
			return inventoryObserve(ctx, r, entry, 0, true, now)
		}
		info, statErr := noFollowInfo(scope.Root, relative)
		if statErr == nil && info.IsDir() {
			r.State().enqueue(relative)
			return nil
		}
		if errors.Is(statErr, os.ErrNotExist) {
			// A missing directory is reconciled against its own persisted prefix, not
			// inferred from a partial full scan. Individual missing file rows are explicit.
			if _, getErr := r.Get(ctx, relative); getErr == nil && IsSourcePath(relative) {
				entry = InventoryEntry{Path: relative, Kind: "asset"}
				if textPath(relative) {
					entry.Kind = "text"
				}
				if err := inventoryObserve(ctx, r, entry, 0, true, now); err != nil {
					return err
				}
			}
			r.State().enqueue(relative)
			return nil
		}
		r.State().Degraded = "source_unavailable"
		return nil // unknown is not withdrawal
	})
}

func inventoryObserve(ctx context.Context, r InventoryRecords, e InventoryEntry, scan int64, priority bool, now time.Time) error {
	old, err := r.Get(ctx, e.Path)
	if err != nil && !missing(err) {
		return err
	}
	if err == nil && old.Present == e.Present && old.Fingerprint == e.Fingerprint && old.Kind == e.Kind {
		if scan > old.SeenScan {
			return r.MarkSeen(ctx, old.Path, scan)
		}
		return nil
	}
	s := r.State()
	s.Sequence++
	e.Sequence = s.Sequence
	e.SeenScan = scan
	e.Priority = priority
	e.WaitingSince = now
	e.RetryAt = now
	if err == nil {
		e.Delivered = old.Delivered
		if old.Sequence > old.Delivered {
			e.WaitingSince = old.WaitingSince
		}
	}
	if !e.Present && e.Kind != "directory" {
		e.Reason = "source_withdrawn"
	}
	return r.Put(ctx, e)
}

// Advance performs at most one discovery/withdrawal page. Healthy idle calls
// only read durable state. Permission/root failure cannot tombstone old rows.
func (i SourceInventory) Advance(ctx context.Context, scope InventoryScope, now time.Time, limit int) error {
	if limit < 1 || limit > 128 {
		return ErrHeld
	}
	page := i.Page
	if page == nil {
		page = func(ctx context.Context, root, prefix, after string, limit int) ([]string, bool, error) {
			return inventorySelectedSourcePage(ctx, root, prefix, after, limit, scope.WorkspaceIncludes)
		}
	}
	return i.Store.WithInventory(ctx, scope, func(r InventoryRecords) error {
		s := r.State()
		if s.Degraded == "source_unavailable" && now.Before(s.NextRepair) {
			return nil
		}
		if len(s.Scans) == 0 {
			if now.Before(s.NextRepair) {
				return nil
			}
			s.enqueue(scope.Prefix)
		}
		if _, err := noFollowInfo(scope.Root, scope.Prefix); err != nil {
			s.Degraded = "source_unavailable"
			s.NextRepair = now.Add(inventoryRepairInterval(scope, s.Degraded))
			return nil
		}
		s.Degraded = s.WatchReason
		scan := &s.Scans[0]
		if !inventoryContains(scope.Prefix, scan.Prefix) {
			return ErrHeld
		}
		if !scan.Withdrawing {
			paths, more, err := page(ctx, scope.Root, scan.Prefix, scan.After, limit)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// Only a directly verified missing affected subtree may prove absence.
				info, checkErr := noFollowInfo(scope.Root, scan.Prefix)
				_ = info
				if scan.Prefix != scope.Prefix && errors.Is(checkErr, os.ErrNotExist) {
					scan.Withdrawing = true
				} else {
					s.Degraded = "source_unavailable"
					s.NextRepair = now.Add(inventoryRepairInterval(scope, s.Degraded))
					return nil
				}
			} else {
				for _, relative := range paths {
					entry, err := i.observe(scope.Root, relative)
					if err != nil {
						s.Degraded = "source_unavailable"
						s.NextRepair = now.Add(inventoryRepairInterval(scope, s.Degraded))
						return nil
					}
					if !entry.Present {
						if _, err := noFollowInfo(scope.Root, scope.Prefix); err != nil {
							s.Degraded = "source_unavailable"
							s.NextRepair = now.Add(inventoryRepairInterval(scope, s.Degraded))
							return nil
						}
					}
					if err := inventoryObserve(ctx, r, entry, scan.Sequence, false, now); err != nil {
						return err
					}
					scan.After = relative
				}
				if more {
					return nil
				}
				scan.Withdrawing = true
				scan.After = ""
			}
		}
		rows, err := r.Unseen(ctx, scan.Prefix, scan.After, scan.Sequence, limit)
		if err != nil {
			return err
		}
		for _, entry := range rows {
			if _, err := noFollowInfo(scope.Root, scope.Prefix); err != nil {
				s.Degraded = "source_unavailable"
				s.NextRepair = now.Add(inventoryRepairInterval(scope, s.Degraded))
				return nil
			}
			observed, err := i.observe(scope.Root, entry.Path)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					s.Degraded = "source_unavailable"
					s.NextRepair = now.Add(inventoryRepairInterval(scope, s.Degraded))
					return nil
				}
				observed = InventoryEntry{Path: entry.Path, Kind: entry.Kind}
			}
			if !observed.Present {
				if _, err := noFollowInfo(scope.Root, scope.Prefix); err != nil {
					s.Degraded = "source_unavailable"
					s.NextRepair = now.Add(inventoryRepairInterval(scope, s.Degraded))
					return nil
				}
			}
			if err := inventoryObserve(ctx, r, observed, scan.Sequence, true, now); err != nil {
				return err
			}
			scan.After = entry.Path
		}
		if len(rows) == limit {
			return nil
		}
		prefix, again := scan.Prefix, scan.Again
		s.Scans = s.Scans[1:]
		if again {
			s.enqueue(prefix)
		}
		s.Complete = len(s.Scans) == 0
		s.ObservedAt = now
		s.NextRepair = now.Add(inventoryRepairInterval(scope, s.Degraded))
		return nil
	})
}

func inventoryRepairInterval(scope InventoryScope, posture string) time.Duration {
	if posture == "" {
		// Stable owner-generation staggering survives restart without delaying
		// degraded recovery or synchronizing healthy collection inventories.
		const maxStagger = 10 * time.Minute
		digest := sha256.Sum256([]byte(scope.Collection + "\x00" + scope.Generation))
		stagger := time.Duration(binary.BigEndian.Uint64(digest[:8]) % uint64(maxStagger))
		return 6*time.Hour + stagger
	}
	if scope.RepairInterval > 0 {
		return scope.RepairInterval
	}
	return time.Minute
}

func (i SourceInventory) DeliveryPage(ctx context.Context, scope InventoryScope, kind string, now time.Time, limit int) (out InventoryPage, err error) {
	if limit < 1 || limit > 128 || (kind != "" && kind != "text" && kind != "asset") {
		return out, ErrHeld
	}
	out.Generation = scope.Generation
	err = i.Store.WithInventory(ctx, scope, func(r InventoryRecords) error {
		s := r.State()
		out.Complete = s.Complete
		out.Degraded = s.Degraded
		entries, err := r.Pending(ctx, kind, now, limit+1)
		if err != nil {
			return err
		}
		out.More = len(entries) > limit
		if out.More {
			entries = entries[:limit]
		}
		out.Entries = entries
		return nil
	})
	return
}

// Finish is a compare-and-ack fence: an event arriving during publication stays
// pending. Failure deadlines do not prevent later eligible paths from progressing.
func (i SourceInventory) Finish(ctx context.Context, scope InventoryScope, entry InventoryEntry, reason string, now time.Time) error {
	return i.Store.WithInventory(ctx, scope, func(r InventoryRecords) error {
		current, err := r.Get(ctx, entry.Path)
		if err != nil {
			return err
		}
		if current.Sequence != entry.Sequence {
			return nil
		}
		if reason == "" {
			current.Delivered = current.Sequence
			current.Priority = false
			current.Reason = ""
			current.Attempts = 0
		} else if reason == "source_excluded" {
			current.Reason = reason
			current.Priority = false
			current.Attempts = 0
			current.RetryAt = time.Time{}
		} else if reason == "binding_index_pending" {
			// Bounded index catch-up is not another failed publication attempt.
			current.Reason = reason
			current.RetryAt = now.Add(2 * time.Second)
		} else if stableReferenceHold(reason) {
			current.Attempts++
			current.Reason = reason
			current.RetryAt = now.Add(layoutEvidenceRetry)
		} else {
			current.Attempts++
			delay := time.Second << min(current.Attempts, 9)
			current.RetryAt = now.Add(delay)
			current.Reason = reason
		}
		return r.Put(ctx, current)
	})
}

// Evidence changed, not source bytes. Retain the original pending identity and
// history while making only this already-held reference eligible for recheck.
func (i SourceInventory) RetryReferenceEvidence(ctx context.Context, scope InventoryScope, path string, now time.Time) error {
	if !referencePath(path) {
		return nil
	}
	return i.Store.WithInventory(ctx, scope, func(r InventoryRecords) error {
		entry, err := r.Get(ctx, path)
		if missing(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.Present || entry.Sequence <= entry.Delivered || !referenceEvidenceHold(entry.Reason) || !entry.RetryAt.After(now) {
			return nil
		}
		entry.RetryAt = now
		return r.Put(ctx, entry)
	})
}

func inventoryAgeGuard(entries, oldest []InventoryEntry, now time.Time, limit int) []InventoryEntry {
	if len(oldest) == 0 || now.Sub(oldest[0].WaitingSince) < 30*time.Second {
		return entries
	}
	first := oldest[0]
	out := []InventoryEntry{first}
	for _, e := range entries {
		if e.Path != first.Path && len(out) < limit {
			out = append(out, e)
		}
	}
	return out
}
