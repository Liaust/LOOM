package notesworkspacesync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesworkspace"
)

// RuntimeConfig is operator-managed, not client-supplied enrollment. Generation
// is an enrollment token; KnowledgeResolver also binds current source policy.
type RuntimeConfig struct {
	SchemaVersion     string             `json:"schema_version"`
	Command           string             `json:"command"`
	Replica           string             `json:"replica"`
	ReplicaDir        string             `json:"replica_dir"`
	Settings          string             `json:"settings"`
	Scopes            []RuntimeSelection `json:"scopes"`
	WatchSeconds      int                `json:"watch_seconds,omitempty"`
	LayoutPublication bool               `json:"layout_publication,omitempty"`
}

type RuntimeSelection struct {
	Scope
	AdmissionPath    string `json:"admission_path"`
	PathPrefix       string `json:"path_prefix,omitempty"`
	WorkingRoot      string `json:"working_root,omitempty"`
	DesiredRoot      string `json:"desired_root,omitempty"`
	WorkState        string `json:"work_state,omitempty"`
	LayoutFrom       string `json:"layout_from,omitempty"`
	LayoutGeneration string `json:"layout_generation,omitempty"`
	LayoutReadOnly   bool   `json:"layout_read_only,omitempty"`
	Archived         bool   `json:"archived,omitempty"`
}

func (s RuntimeSelection) contains(relative string) bool {
	return s.PathPrefix == "" || strings.HasPrefix(relative, s.PathPrefix+"/")
}

// Apply the same enrollment restriction to exports, edits and both rename paths.
type selectedSources struct {
	notesworkspace.PathResolver
	Selections []RuntimeSelection
}

func (s selectedSources) WithReadDirectoryPaths(ctx context.Context, id string, paths []string, fn func(notesworkspace.Source) error) error {
	reader, ok := s.PathResolver.(notesworkspace.DirectoryReadResolver)
	if !ok {
		return s.WithDirectoryPaths(ctx, id, paths, fn)
	}
	for _, selection := range s.Selections {
		if selection.SourceCollection != id {
			continue
		}
		for _, relative := range paths {
			if relative != selection.PathPrefix && !selection.contains(relative) {
				return notesworkspace.ErrMembership
			}
		}
		return reader.WithReadDirectoryPaths(ctx, id, paths, fn)
	}
	return notesworkspace.ErrMembership
}

func (s selectedSources) WithDirectoryPaths(ctx context.Context, id string, paths []string, fn func(notesworkspace.Source) error) error {
	resolver, ok := s.PathResolver.(notesworkspace.DirectoryResolver)
	if !ok {
		return notesworkspace.ErrMembership
	}
	for _, selection := range s.Selections {
		if selection.SourceCollection != id {
			continue
		}
		for _, relative := range paths {
			if relative != selection.PathPrefix && !selection.contains(relative) {
				return notesworkspace.ErrMembership
			}
		}
		return resolver.WithDirectoryPaths(ctx, id, paths, fn)
	}
	return notesworkspace.ErrMembership
}

func (s selectedSources) WithSource(ctx context.Context, id, relative string, fn func(notesworkspace.Source) error) error {
	return s.WithPaths(ctx, id, []string{relative}, fn)
}

func (s selectedSources) WithRetainedSource(ctx context.Context, id, relative, generation string, fn func(notesworkspace.Source) error) error {
	return s.WithSource(ctx, id, relative, func(src notesworkspace.Source) error {
		if src.Generation != generation {
			approved := false
			for _, selection := range s.Selections {
				if selection.SourceCollection == id && selection.PreviousGeneration == generation {
					approved = true
					break
				}
			}
			if !approved {
				return notesworkspace.ErrMembership
			}
			src.Generation = generation
		}
		return fn(src)
	})
}

func (s selectedSources) WithPaths(ctx context.Context, id string, paths []string, fn func(notesworkspace.Source) error) error {
	for _, selection := range s.Selections {
		if selection.SourceCollection != id {
			continue
		}
		for _, relative := range paths {
			if !selection.contains(relative) {
				return notesworkspace.ErrMembership
			}
		}
		return s.PathResolver.WithPaths(ctx, id, paths, fn)
	}
	return notesworkspace.ErrMembership
}

func LoadRuntimeConfig(path string) (RuntimeConfig, error) {
	var cfg RuntimeConfig
	f, err := os.Open(path)
	if err != nil {
		return cfg, errors.New("Notes workspace configuration unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(raw) > 65536 || strictJSON(raw, &cfg) != nil || cfg.Validate() != nil {
		return RuntimeConfig{}, errors.New("invalid Notes workspace configuration")
	}
	return cfg, nil
}

func (c RuntimeConfig) Validate() error {
	if c.SchemaVersion != "notes_workspace.runtime.v1" || !token(c.Replica) || len(c.Scopes) < 1 || c.WatchSeconds < 0 || c.WatchSeconds > 60 {
		return ErrHeld
	}
	for _, p := range []string{c.Command, c.ReplicaDir, c.Settings} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
			return ErrHeld
		}
	}
	for index, scope := range c.Scopes {
		if scope.PathPrefix != "" && (!textPath(scope.PathPrefix+"/probe.md") || !scope.contains(scope.AdmissionPath)) {
			return ErrHeld
		}
		if !token(scope.Workspace) || !token(scope.Collection) || !token(scope.Generation) || scope.SourceCollection == "" || !textPath(scope.Root+"/probe.md") || !IsSourcePath(scope.AdmissionPath) || scope.PreviousGeneration != "" && !token(scope.PreviousGeneration) {
			return ErrHeld
		}
		for _, previous := range c.Scopes[:index] {
			if previous.SourceCollection == scope.SourceCollection || previous.Collection == scope.Collection || previous.Root == scope.Root || strings.HasPrefix(previous.Root, scope.Root+"/") || strings.HasPrefix(scope.Root, previous.Root+"/") {
				return ErrHeld
			}
		}
	}
	return nil
}

type Runtime struct {
	Config                 RuntimeConfig
	DB                     *sql.DB
	Knowledge              *knowledge.Service
	Archives               *knowledge.WorkspaceArchiveSourceReader
	NodeKey                string
	OpenSourceWatch        OpenSourceWatch
	SourceRepairInterval   func(InventoryScope) time.Duration
	Wake                   func()
	Lifetime               context.Context
	session                *nativeSession
	nativeRevision         atomic.Uint64
	dependencyRevision     atomic.Uint64
	seenDependencyRevision uint64
	layoutDependencies     layoutDependencyCache
	seenNativeRevision     uint64
	seenSourceRevision     uint64
	nextSafety             time.Time
	nextRetained           time.Time
	retainedInitialized    bool
	nextSourceMove         time.Time
	nextLayout             time.Time
	nextArchive            time.Time
	nextEnrollment         time.Time
	inventories            map[string]*runtimeInventory
	archiveSelections      map[string]archiveSelectionCache
	mu                     sync.Mutex
	hintMu                 sync.Mutex
	drainPending           bool
	sourceRevision         atomic.Uint64
	wakeReason             string
}

func (r *Runtime) InvalidateEnrollment() { r.sourceRevision.Add(1) }

type RuntimeCursor struct {
	Delivery           map[string]DeliveryCursor `json:"delivery,omitempty"`
	Collection         string                    `json:"collection,omitempty"`
	After              string                    `json:"after,omitempty"`
	RetainedAfter      string                    `json:"retained_after,omitempty"`
	RetainedAuditAt    time.Time                 `json:"retained_audit_at,omitempty"`
	EnrollmentAfter    string                    `json:"enrollment_after,omitempty"`
	EnrollmentContext  string                    `json:"enrollment_context,omitempty"`
	EnrollmentComplete bool                      `json:"enrollment_complete,omitempty"`
	EnrollmentRetryAt  time.Time                 `json:"enrollment_retry_at,omitempty"`
}

type RuntimeResult struct {
	WakeReason            string                `json:"wake_reason,omitempty"`
	DrainPending          bool                  `json:"drain_pending"`
	Enrollment            *EnrollmentInventory  `json:"enrollment,omitempty"`
	TextExported          int                   `json:"text_exported"`
	AssetExported         int                   `json:"asset_exported"`
	Adopted               int                   `json:"adopted"`
	DeliveryBytes         int64                 `json:"delivery_bytes"`
	DeliveryAttempted     int                   `json:"delivery_attempted"`
	ReferenceStagedBytes  int64                 `json:"reference_staged_bytes"`
	ReferenceMetadataHeld int                   `json:"reference_metadata_held"`
	ReferenceReadBytes    int64                 `json:"reference_read_bytes"`
	ReferenceReadAttempts int                   `json:"reference_read_attempts"`
	ReferenceReadMS       int64                 `json:"reference_read_ms"`
	DeliveryPressure      string                `json:"delivery_pressure,omitempty"`
	LayoutPublished       int                   `json:"layout_published"`
	LayoutHeld            int                   `json:"layout_held"`
	ArchivePublished      int                   `json:"archive_published"`
	ArchiveHeld           int                   `json:"archive_held"`
	Inventory             []InventoryProgress   `json:"inventory,omitempty"`
	SchemaVersion         string                `json:"schema_version"`
	Imported              int                   `json:"imported"`
	Processed             int                   `json:"processed"`
	Applied               int                   `json:"applied"`
	PendingOperations     int                   `json:"pending_operations"`
	RetainedHolds         int                   `json:"retained_holds"`
	Held                  int                   `json:"held"`
	Exported              int                   `json:"exported"`
	Skipped               int                   `json:"skipped"`
	Reconciled            int                   `json:"reconciled"`
	Recoveries            int                   `json:"recoveries"`
	LayoutInspected       int                   `json:"layout_inspected"`
	LayoutMetadataProbes  int                   `json:"layout_metadata_probes"`
	LayoutPressure        string                `json:"layout_pressure,omitempty"`
	LayoutAttempts        []LayoutAttemptDetail `json:"layout_attempts,omitempty"`
	LayoutHoldReasons     map[string]int        `json:"layout_hold_reasons,omitempty"`
	ElapsedMS             map[string]int64      `json:"elapsed_ms,omitempty"`
	Replication           string                `json:"replication"`
	Cursor                RuntimeCursor         `json:"-"`
}

// Run opens only the operator-selected native replica. The existing worker owns
// cadence and concurrency; the child owns native replication, not source writes.
func (r *Runtime) Run(ctx context.Context, cursor RuntimeCursor) (RuntimeResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	persistent := r.Lifetime != nil
	startedAt := time.Now()
	out := RuntimeResult{SchemaVersion: "notes_workspace.result.v1", Cursor: cursor, WakeReason: r.wakeReason, ElapsedMS: map[string]int64{}, LayoutHoldReasons: map[string]int{}}
	defer func() { out.ElapsedMS["total"] = time.Since(startedAt).Milliseconds() }()
	if !r.retainedInitialized {
		r.nextRetained, r.retainedInitialized = cursor.RetainedAuditAt, true
	}
	if r.Config.Validate() != nil || r.DB == nil || r.Knowledge == nil || r.NodeKey == "" {
		return out, errors.New("Notes workspace runtime is not configured")
	}
	if persistent && recoveryRequested(r.Config.ReplicaDir) {
		r.closeNative()
		out.Replication = "recovery_paused"
		return out, nil
	}
	var lock *os.File
	r.drainPending = false
	var acquired bool
	var err error
	if persistent && r.session != nil {
		acquired = true
	} else {
		lock, acquired, err = lockReplica(r.Config.ReplicaDir)
	}
	if err != nil {
		return out, errors.New("Notes recovery fence unavailable")
	}
	if !acquired {
		out.Replication = "recovery_paused"
		return out, nil
	}
	defer func() {
		if lock != nil {
			lock.Close()
		}
	}()
	selections, previous, err := r.enrollmentSelections(ctx)
	if err != nil {
		return out, errors.New("Notes automatic enrollment unavailable")
	}
	enrollment := EnrollmentInventory{SchemaVersion: enrollmentSchema, Replica: r.Config.Replica,
		NodeKey: r.NodeKey, ConfigSHA256: enrollmentConfigHash(r.Config), Collections: []EnrollmentCollection{}}
	resolver := notesworkspace.KnowledgeResolver{
		Knowledge: r.Knowledge, NodeKey: r.NodeKey,
		WithSelection: func(_ context.Context, id string, fn func(string) error) error {
			for _, scope := range selections {
				if scope.SourceCollection == id {
					if scope.PathPrefix != "" {
						return fn(key(scope.Generation, scope.PathPrefix))
					}
					return fn(scope.Generation)
				}
			}
			return notesworkspace.ErrMembership
		},
	}
	source := notesworkspace.Service{Store: notesworkspace.SQLStore{DB: r.DB}, Sources: selectedSources{PathResolver: resolver, Selections: selections}}
	var scopes []Scope
	var liveScopes []Scope
	archives := map[string]knowledge.WorkspaceArchiveSource{}
	inventoryScopes := map[string]InventoryScope{}
	watchKeys := map[string]string{}
	intakeStages := map[string]string{}
	workspaceIncludes := map[string]string{}
	resolver.ObserveRoot = func(src notesworkspace.Source, root knowledge.SourceRoot) {
		watchKeys[src.CollectionID] = root.BackendRootKey
		intakeStages[src.CollectionID] = root.SourceIntakeStage
		if policy, err := knowledge.NotesWorkspacePolicy(root); err == nil && policy != nil {
			raw, _ := json.Marshal(policy.Include)
			workspaceIncludes[src.CollectionID] = string(raw)
		}
	}
	source.Sources = selectedSources{PathResolver: resolver, Selections: selections}
	var unavailableScopes []Scope
	for _, selection := range selections {
		scope := selection.Scope
		entry := EnrollmentCollection{RuntimeSelection: selection, Status: "held"}
		if selection.Archived && r.Archives != nil && r.Config.LayoutPublication {
			err := r.withArchiveSelection(ctx, scope.SourceCollection, func(archive knowledge.WorkspaceArchiveSource) error {
				scope.Generation, scope.ReadOnly = notesworkspace.ArchiveGeneration(archive.Generation), true
				if selection.LayoutGeneration != "" && selection.LayoutGeneration != scope.Generation {
					scope.PreviousGeneration = selection.LayoutGeneration
				}
				for _, old := range previous.Collections {
					if old.SourceCollection == scope.SourceCollection && old.SourceGeneration != scope.Generation {
						scope.PreviousGeneration = old.SourceGeneration
					}
				}
				entry.Status, entry.SourceGeneration = "reference", scope.Generation
				entry.ArchiveOperation, entry.ArchiveManifest = archive.ArchiveOperationID, archive.ManifestDigest
				selected := archive.Files[:0:0]
				for _, file := range archive.Files {
					if selection.contains(file.RelativePath) {
						selected = append(selected, file)
					}
				}
				archive.Files = selected
				entry.ReferenceFiles = len(selected)
				for _, file := range selected {
					entry.ReferenceBytes += file.SizeBytes
				}
				archives[scope.SourceCollection] = archive
				return nil
			})
			if err != nil {
				entry.Status = "reference_held"
				enrollment.Holds = append(enrollment.Holds, EnrollmentHold{SourceCollection: scope.SourceCollection, Reason: "archive_reference_unverified"})
				out.Skipped++
			} else {
				scopes = append(scopes, scope)
			}
			enrollment.Collections = append(enrollment.Collections, entry)
			continue
		}
		err := resolver.WithRoot(ctx, scope.SourceCollection, func(src notesworkspace.Source) error {
			scope.Generation = src.Generation
			if selection.LayoutGeneration != "" && selection.LayoutGeneration != scope.Generation {
				scope.PreviousGeneration = selection.LayoutGeneration
			}
			entry.SourcePath, entry.SourceGeneration, entry.Status = src.Path, src.Generation, "active"
			entry.SourceIntakeStage = intakeStages[src.CollectionID]
			inventoryScopes[scope.Collection] = InventoryScope{Collection: src.CollectionID, Generation: src.Generation, Root: src.Path, Prefix: selection.PathPrefix, WatchKey: watchKeys[src.CollectionID], OwnerNodeKey: r.NodeKey, WorkspaceIncludes: workspaceIncludes[src.CollectionID]}
			if r.SourceRepairInterval != nil {
				value := inventoryScopes[scope.Collection]
				value.RepairInterval = r.SourceRepairInterval(value)
				inventoryScopes[scope.Collection] = value
			}
			return nil
		})
		if err != nil {
			entry.Status, entry.SourcePath, entry.SourceGeneration = "held", "", ""
			unavailableScopes = append(unavailableScopes, scope)
			delete(inventoryScopes, scope.Collection)
			enrollment.Holds = append(enrollment.Holds, enrollmentAdmissionHold(scope.SourceCollection, err))
			enrollment.Collections = append(enrollment.Collections, entry)
			out.Skipped++ // withdrawn/archived sources do not authorize deletion
			continue
		}
		enrollment.Collections = append(enrollment.Collections, entry)
		scopes = append(scopes, scope)
		liveScopes = append(liveScopes, scope)
	}
	// Keep retired selection identities for future re-enrollment. They carry no
	// current write authority or live source-path recovery claim.
	current := map[string]bool{}
	for _, c := range enrollment.Collections {
		current[c.SourceCollection] = true
	}
	for _, c := range previous.Collections {
		if !current[c.SourceCollection] {
			c.Status, c.SourcePath, c.SourceGeneration = "withdrawn", "", ""
			enrollment.Collections = append(enrollment.Collections, c)
		}
	}
	if err := writeEnrollment(enrollmentPath(r.Config), enrollment); err != nil {
		return out, errors.New("Notes enrollment inventory publication unavailable")
	}
	out.Enrollment = &enrollment
	seenSource, seenNative := r.sourceRevision.Load(), r.nativeRevision.Load()
	seenDependency := r.dependencyRevision.Load()
	out.ElapsedMS["enrollment"] = time.Since(startedAt).Milliseconds()
	phaseAt := time.Now()
	if r.retainedReconciliationDue(persistent, cursor.RetainedAfter, time.Now()) {
		retained, err := (notesworkspace.SQLStore{DB: r.DB}).RetainedOperationIDs(ctx, cursor.RetainedAfter, 32)
		if err != nil {
			return out, errors.New("Notes retained-operation discovery unavailable")
		}
		for _, id := range retained.IDs {
			recovery, err := source.Reconcile(ctx, id)
			if err != nil {
				return out, errors.New("Notes retained-operation reconciliation incomplete")
			}
			out.Reconciled++
			if recovery.State != "clean" {
				out.Recoveries++
			}
		}
		cursor.RetainedAfter = retained.Next
		if len(retained.IDs) < 32 {
			cursor.RetainedAfter = ""
			r.nextRetained = time.Now().Add(24 * time.Hour)
			cursor.RetainedAuditAt = r.nextRetained
		}
	}
	out.ElapsedMS["retained_audit"] = time.Since(phaseAt).Milliseconds()
	out.Cursor = cursor
	var native *JSONLClient
	keepSession := false
	started := !persistent || r.session == nil
	if persistent {
		if r.session == nil {
			r.session, err = startNativeSession(r.Lifetime, r.Config, r.nativeHint)
			if err != nil {
				return out, err
			}
			r.session.lock, lock = lock, nil
		}
		native = r.session.client
		defer func() {
			if !keepSession || ctx.Err() != nil || native.Poisoned() {
				r.closeNative()
			}
		}()
	} else {
		child, client, startErr := startNative(ctx, r.Config)
		if startErr != nil {
			return out, startErr
		}
		defer child()
		native = client
	}
	var status NativeStatus
	if err := native.Call(ctx, map[string]any{"method": "status"}, &status); err != nil {
		return out, errors.New("Notes native startup unavailable")
	}
	// Source hints wake inventory only. New intent/publication evidence and
	// enrollment changes already invalidate the exact operation's retry state.
	sources := runtimeSources{NotesSource: NotesSource{Service: source}, Archived: archives, Selections: selections}
	if r.Archives != nil {
		sources.Archive = &notesworkspace.ArchiveReader{Reader: *r.Archives, Store: source.Store}
	}
	bridge := Service{Replica: r.Config.Replica, Epoch: status.Epoch, AdoptExisting: true, LayoutPublication: r.Config.LayoutPublication, Store: SQLStore{DB: r.DB}, Source: sources, Native: native, Scopes: scopes, UnavailableScopes: unavailableScopes,
		ReferenceSpool: filepath.Join(r.Config.ReplicaDir, "reference-spool"), layoutDependencies: &r.layoutDependencies}
	r.layoutDependencies.retainScopes(scopes)
	if err := bridge.ready(ctx); err != nil {
		return out, errors.New("Notes native identity or encryption unavailable")
	}
	// Bind epoch before replication, including on runs with no writable scopes.
	if err := bridge.Store.WithReplica(ctx, bridge.Replica, bridge.Epoch, func(Records) error { return nil }); err != nil {
		return out, errors.New("Notes replica identity unavailable")
	}
	if err := bridge.prepareLayouts(ctx, selections); err != nil {
		return out, errors.New("Notes layout predecessor publication unavailable")
	}
	sync := func() error {
		var result struct{ Status, Outcome string }
		if err := bridge.call(ctx, "sync", nil, &result); err != nil || result.Status != "replication" || result.Outcome != "completed" {
			out.Replication = "incomplete"
			return errors.New("Notes native replication incomplete")
		}
		out.Replication = "completed"
		return nil
	}
	if started {
		if err := sync(); err != nil {
			if persistent {
				r.closeNative()
			}
			return out, err
		}
	} else {
		out.Replication = "completed"
	}
	continuous := func(method string) error {
		var result struct{ Status, Outcome string }
		if err := bridge.call(ctx, method, nil, &result); err != nil || result.Status != "replication" || result.Outcome != "completed" {
			return errors.New("Notes continuous replication unavailable")
		}
		return nil
	}
	if (r.Config.WatchSeconds > 0 || persistent) && started {
		if err := continuous("sync.start"); err != nil {
			if persistent {
				r.closeNative()
			}
			return out, err
		}
	}
	until := time.Now().Add(time.Duration(r.Config.WatchSeconds) * time.Second)
	if persistent {
		until = time.Now().Add(deliveryAdmissionWindow)
	}
	idleCollections := 0
	seenSourceRevision := r.sourceRevision.Load()
	inventory := SourceInventory{Store: SQLStore{DB: r.DB}}
	for id := range cursor.Delivery {
		if _, ok := inventoryScopes[id]; !ok {
			delete(cursor.Delivery, id)
		}
	}
	if err := r.prepareInventories(ctx, inventory, inventoryScopes); err != nil {
		return out, errors.New("Notes inventory startup unavailable")
	}
	if err := bridge.publishScopes(ctx); err != nil {
		return out, errors.New("Notes collection admission publication incomplete")
	}
	referenceScopes := referenceWakeScopes(liveScopes, selections, r.Config.LayoutPublication)

	for {
		bridge.SourceMovesQueued = false
		// Recover accepted native move publication before admitting old-path
		// intents after a crash. Pending IPC outcomes remain exact replays.
		moveMore, movePublished, moveHeld, err := bridge.advanceSourceMoves(ctx, 32)
		if err != nil {
			return out, errors.New("Notes reviewed source move incomplete")
		}
		r.nextSourceMove = bridge.MoveRetryAt
		progress, err := bridge.Step(ctx, 128)
		if err != nil {
			return out, errors.New("Notes source reconciliation incomplete")
		}
		out.Imported += progress.Imported
		out.Processed += progress.Processed
		out.Applied += progress.Applied
		out.Held += progress.Held
		if err := retryReferenceEvidence(ctx, inventory, inventoryScopes, referenceScopes, progress.ReferencePaths, r.layoutDependencies.referenceHints(), time.Now()); err != nil {
			return out, errors.New("Notes reference dependency retry unavailable")
		}
		r.drainPending = progress.MoreWork || moveMore
		out.LayoutPublished += movePublished
		out.LayoutHeld += moveHeld
		phaseAt = time.Now()
		layoutMore, layoutMoved, layoutHeld, err := bridge.advanceLayouts(ctx, 32)
		if err != nil {
			return out, errors.New("Notes layout relocation incomplete")
		}
		r.nextLayout = bridge.LayoutRetryAt
		r.drainPending = r.drainPending || layoutMore
		out.Exported += layoutMoved
		out.Held += layoutHeld
		out.LayoutPublished += layoutMoved
		out.LayoutHeld += layoutHeld
		out.ElapsedMS["layout"] += time.Since(phaseAt).Milliseconds()
		out.LayoutInspected += bridge.LayoutInspected
		out.LayoutMetadataProbes += bridge.LayoutMetadataProbes
		if bridge.LayoutPressure != "" {
			out.LayoutPressure = bridge.LayoutPressure
		}
		for reason, count := range bridge.LayoutHoldReasons {
			out.LayoutHoldReasons[reason] += count
		}
		if revision := r.sourceRevision.Load(); revision != seenSourceRevision || progress.Incoming > 0 || progress.Applied > 0 {
			idleCollections = 0
			seenSourceRevision = revision
		}
		if len(liveScopes) > 0 {
			moveIndexMore, err := bridge.indexMoveBindings(ctx, 128)
			if err != nil {
				return out, errors.New("Notes source move metadata index incomplete")
			}
			r.drainPending = r.drainPending || moveIndexMore
			if err := bridge.indexRetainedMappings(ctx, 128); err != nil {
				return out, errors.New("Notes retained mapping index incomplete")
			}
			r.drainPending = r.drainPending || bridge.MappingIndexPending
			// Existing clients must finish their enrollment transition before a
			// large new attachment tree delays their next ordinary source sweep.
			var mappings []FileMapping
			if enrollmentScanDue(&cursor, bridge.Scopes, time.Now()) {
				var after string
				mappings, after, err = bridge.enrollmentExports(ctx, cursor.EnrollmentAfter, 32)
				if err != nil {
					return out, errors.New("Notes enrollment discovery incomplete")
				}
				cursor.EnrollmentAfter = after
				cursor.EnrollmentComplete = after == ""
				if cursor.EnrollmentComplete {
					cursor.EnrollmentRetryAt = time.Now().Add(6 * time.Hour)
				}
			}
			r.nextEnrollment = cursor.EnrollmentRetryAt
			r.drainPending = r.drainPending || cursor.EnrollmentAfter != ""
			for _, mapping := range mappings {
				file, err := source.Store.File(ctx, mapping.SourceFileID)
				if err != nil || file.Deleted || file.CollectionID != mapping.Scope.SourceCollection {
					out.Skipped++
					continue
				}
				if file.EntryKind == "directory" {
					read, err := sources.ReadDirectory(ctx, file.CollectionID, file.RelativePath)
					if err != nil || !read.Base.Exists {
						out.Skipped++
					} else if _, err := bridge.ExportDirectory(ctx, mapping.Scope.Collection, read.File.ID, read.Base.ID); err != nil {
						out.Held++
					} else {
						out.Exported++
					}
					continue
				}
				var read notesworkspace.ReadResult
				var snapshot string
				var attempt referenceAttemptInput
				if notesworkspace.IsReferencePath(file.RelativePath) {
					observation, e := bridge.fileObservation(ctx, file)
					if e != nil {
						out.Skipped++
						continue
					}
					attempt.Fingerprint = observation.Fingerprint
					read, snapshot, err = bridge.readExportReference(ctx, mapping.Scope, file.RelativePath, attempt)
				} else {
					read, snapshot, err = readExportSource(ctx, sources, file.CollectionID, file.RelativePath, bridge.ReferenceSpool)
				}
				if err != nil || !read.Base.Exists {
					out.Skipped++
				} else if _, err := bridge.export(ctx, mapping.Scope.Collection, read.File.ID, read.Base.ID, snapshot, attempt); err != nil {
					out.Held++
				} else {
					out.Exported++
				}
				if snapshot != "" {
					os.Remove(snapshot)
				}
			}
			index := 0
			for i, scope := range liveScopes {
				if scope.Collection == cursor.Collection {
					index = i
					break
				}
			}
			scope := liveScopes[index]
			if cursor.Collection != scope.Collection {
				cursor.Collection, cursor.After = scope.Collection, ""
			}
			inventoryScope := inventoryScopes[scope.Collection]
			if err := inventory.Advance(ctx, inventoryScope, time.Now().UTC(), 128); err != nil {
				return out, errors.New("Notes inventory catch-up incomplete")
			}
			if cursor.Delivery == nil {
				cursor.Delivery = map[string]DeliveryCursor{}
			}
			laneCursor := cursor.Delivery[scope.Collection]
			if laneCursor.Generation != scope.Generation {
				laneCursor = DeliveryCursor{Generation: scope.Generation, TextAfter: cursor.After, AssetAfter: cursor.After}
			}
			turn, err := deliverInventoryTurn(ctx, inventory, inventoryScope, laneCursor, func(entry InventoryEntry) (deliveryPublication, error) {
				if entry.Kind == "directory" {
					path := strings.TrimSuffix(entry.Path, "/")
					if reserved, err := bridge.directoryReserved(ctx, scope, path); err != nil || reserved {
						return deliveryPublication{Reason: "directory_operation_pending"}, ErrHeld
					}
					read, err := source.ReadDirectory(ctx, scope.SourceCollection, path)
					if errors.Is(err, knowledge.ErrNotesPathExcluded) {
						return deliveryPublication{Reason: "source_excluded"}, ErrHeld
					}
					if err != nil {
						return deliveryPublication{Reason: "directory_source_unavailable"}, ErrHeld
					}
					_, err = bridge.ExportDirectory(ctx, scope.Collection, read.File.ID, read.Base.ID)
					return deliveryPublication{Reason: "directory_publication_pending"}, err
				}
				if notesworkspace.IsReferencePath(entry.Path) {
					attempt := referenceAttemptInput{Fingerprint: entry.Fingerprint, PriorReason: entry.Reason}
					read, snapshot, err := bridge.readExportReference(ctx, scope, entry.Path, attempt)
					if errors.Is(err, knowledge.ErrNotesPathExcluded) {
						return deliveryPublication{Reason: "source_excluded"}, ErrHeld
					}
					if errors.Is(err, notesworkspace.ErrReferenceLimit) {
						return deliveryPublication{Reason: "reference_size_limit", MetadataOnly: true}, ErrHeld
					}
					if err != nil {
						return deliveryPublication{Reason: deliveryFailureReason(err, "source_unavailable"), MetadataOnly: metadataOnlyReferenceFailure(err)}, ErrHeld
					}
					defer os.Remove(snapshot)
					_, err = bridge.export(ctx, scope.Collection, read.File.ID, read.Base.ID, snapshot, attempt)
					return deliveryPublication{Bytes: read.Base.ReferenceSize, Reason: deliveryFailureReason(err, "publication_pending")}, err
				}
				read, err := source.Read(ctx, scope.SourceCollection, entry.Path)
				if errors.Is(err, knowledge.ErrNotesPathExcluded) {
					return deliveryPublication{Reason: "source_excluded"}, ErrHeld
				}
				if err != nil || !read.Base.Exists {
					return deliveryPublication{Reason: "source_unavailable"}, ErrHeld
				}
				_, adopted, err := bridge.ExportInventory(ctx, scope.Collection, read.File.ID, read.Base.ID)
				if errors.Is(err, errMappingIndexPending) {
					return deliveryPublication{Reason: "binding_index_pending"}, err
				}
				return deliveryPublication{Bytes: int64(len(read.Base.Content)), Adopted: adopted, Reason: "publication_pending"}, err
			}, time.Now)
			if err != nil {
				return out, errors.New("Notes inventory delivery checkpoint unavailable")
			}
			cursor.Delivery[scope.Collection] = turn.Cursor
			out.TextExported += turn.TextExported
			out.AssetExported += turn.AssetExported
			out.Adopted += turn.Adopted
			out.Exported += turn.TextExported + turn.AssetExported
			out.DeliveryBytes += turn.Bytes
			out.DeliveryAttempted += turn.Attempted
			out.Held += turn.Held
			out.DeliveryPressure = turn.Pressure
			// A collection gets one finite turn even while its seed is backlogged.
			cursor.Collection = liveScopes[(index+1)%len(liveScopes)].Collection
			cursor.After = ""
			progress, err := inventory.Progress(ctx, inventoryScope, time.Now().UTC())
			if err != nil {
				return out, errors.New("Notes inventory progress unavailable")
			}
			// Last observation per collection, bounded by explicitly enrolled scopes.
			replaced := false
			for index := range out.Inventory {
				if out.Inventory[index].Collection == inventoryScope.Collection {
					out.Inventory[index] = progress
					replaced = true
					break
				}
			}
			if !replaced {
				out.Inventory = append(out.Inventory, progress)
			}

			out.Cursor = cursor
			if turn.Attempted > 0 {
				idleCollections = 0
			} else {
				idleCollections++
			}
		}
		if !progress.MoreWork {
			archiveMore, archiveMoved, archiveHeld, err := bridge.advanceArchives(ctx, archives, 8)
			r.nextArchive = bridge.ArchiveRetryAt
			if err != nil {
				return out, errors.New("Notes archived reference publication incomplete")
			}
			r.drainPending = r.drainPending || archiveMore
			out.Exported += archiveMoved
			out.Held += archiveHeld
			out.ArchivePublished += archiveMoved
			out.ArchiveHeld += archiveHeld
		}
		if (!persistent && r.Config.WatchSeconds == 0) || !time.Now().Before(until) {
			r.drainPending = r.drainPending || bridge.SourceMovesQueued
			break
		}
		r.drainPending = r.drainPending || bridge.SourceMovesQueued
		if len(liveScopes) > 0 && idleCollections < len(liveScopes) {
			continue
		}
		if persistent {
			break
		}
		var wake struct{ Status string }
		if err := bridge.call(ctx, "changes.wait", map[string]any{"since": progress.NextSequence, "timeoutMs": 1000}, &wake); err != nil || (wake.Status != "idle" && wake.Status != "changed") {
			return out, errors.New("Notes change notification unavailable")
		}
	}
	out.ReferenceStagedBytes, out.ReferenceMetadataHeld = bridge.ReferenceStagedBytes, bridge.ReferenceMetadataHeld
	out.ReferenceReadBytes, out.ReferenceReadAttempts, out.ReferenceReadMS = bridge.ReferenceReadBytes, bridge.ReferenceReadAttempts, bridge.ReferenceReadMS
	out.LayoutAttempts = bridge.LayoutAttempts
	if err := r.DB.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER (WHERE record->>'Status'='held') FROM notes_workspace.sync_records WHERE replica_id=$1 AND kind='operation' AND COALESCE(record->>'AckPublished','false')<>'true'`, r.Config.Replica).Scan(&out.PendingOperations, &out.RetainedHolds); err != nil {
		return out, errors.New("Notes pending operation status unavailable")
	}
	if persistent {
		r.seenSourceRevision, r.seenNativeRevision = seenSource, seenNative
		r.seenDependencyRevision = seenDependency
		r.drainPending = r.drainPending || cursor.RetainedAfter != ""
		out.DrainPending = r.drainPending
		r.nextSafety = time.Now().Add(6 * time.Hour)
		keepSession = true
		return out, nil
	}
	if r.Config.WatchSeconds > 0 {
		if err := continuous("sync.stop"); err != nil {
			return out, err
		}
	}
	return out, sync()
}

func retryReferenceEvidence(ctx context.Context, inventory SourceInventory, inventoryScopes map[string]InventoryScope, scopes []Scope, nativePaths []string, hints []referenceRetryHint, now time.Time) error {
	selected := map[referenceRetryHint]bool{}
	for _, hint := range hints {
		selected[hint] = true
	}
	for _, path := range nativePaths {
		for _, scope := range scopes {
			if relative, ok := strings.CutPrefix(path, scope.Root+"/"); ok {
				selected[referenceRetryHint{scope.SourceCollection, relative}] = true
			}
		}
	}
	for hint := range selected {
		for _, scope := range inventoryScopes {
			if scope.Collection == hint.Collection && (scope.Prefix == "" || strings.HasPrefix(hint.Path, scope.Prefix+"/")) {
				if err := inventory.RetryReferenceEvidence(ctx, scope, hint.Path, now); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func referenceWakeScopes(scopes []Scope, selections []RuntimeSelection, layouts bool) []Scope {
	out := append([]Scope(nil), scopes...)
	if layouts {
		for _, scope := range scopes {
			for _, selection := range selections {
				if selection.Collection != scope.Collection || selection.SourceCollection != scope.SourceCollection {
					continue
				}
				for _, root := range []string{selection.WorkingRoot, selection.LayoutFrom} {
					if root != "" && root != scope.Root && textPath(root+"/probe.md") {
						alias := scope
						alias.Root = root
						out = append(out, alias)
					}
				}
			}
		}
	}
	return out
}

func sourcePage(ctx context.Context, root, after string, limit int) ([]string, bool, error) {
	return sourcePageWithin(ctx, root, "", after, limit)
}

func sourcePageWithin(ctx context.Context, root, prefix, after string, limit int) ([]string, bool, error) {
	return sourcePageWithReader(ctx, root, prefix, after, limit, os.ReadDir)
}

func sourcePageWithReader(ctx context.Context, root, prefix, after string, limit int, readDir func(string) ([]os.DirEntry, error)) ([]string, bool, error) {
	if limit < 1 || limit > 128 {
		return nil, false, ErrHeld
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	start := root
	if prefix != "" {
		if !textPath(prefix + "/probe.md") {
			return nil, false, ErrHeld
		}
		for _, part := range strings.Split(prefix, "/") {
			start = filepath.Join(start, part)
			info, err := os.Lstat(start)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, false, ErrHeld
			}
		}
	}
	var paths []string
	if prefix != "" && prefix+"/" > after {
		if directoryPath(prefix) {
			paths = append(paths, prefix+"/")
		}
	}
	var visit func(string, string) error
	visit = func(directory, relativeDir string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := os.Lstat(directory)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrHeld
		}
		entries, err := readDir(directory)
		if err != nil {
			return err
		}
		orderKey := func(entry os.DirEntry) string {
			if entry.IsDir() {
				return entry.Name() + "/"
			}
			return entry.Name()
		}
		// Sorting directory prefixes with their slash preserves global path
		// order (a.md before a/note.md) without walking every later subtree.
		sort.Slice(entries, func(i, j int) bool { return orderKey(entries[i]) < orderKey(entries[j]) })
		for _, entry := range entries {
			if len(paths) > limit {
				break
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			relative := entry.Name()
			if relativeDir != "" {
				relative = relativeDir + "/" + relative
			}
			if entry.IsDir() {
				if !directoryPath(relative) || entry.Name() == "credentials" || entry.Name() == "private_no_index" || entry.Name() == "LOOM-Control-v1" {
					continue
				}
				branch := relative + "/"
				if branch > after {
					paths = append(paths, branch)
				}
				if len(paths) > limit {
					break
				}
				if branch <= after && !strings.HasPrefix(after, branch) {
					continue
				}
				if err := visit(filepath.Join(directory, entry.Name()), relative); err != nil {
					return err
				}
			} else if entry.Type().IsRegular() && relative > after && IsSourcePath(relative) {
				paths = append(paths, relative)
			}
		}
		return nil
	}
	err := visit(start, prefix)
	if err != nil {
		return nil, false, err
	}
	if len(paths) > limit {
		return paths[:limit], true, nil
	}
	return paths, false, nil
}

func startNative(ctx context.Context, cfg RuntimeConfig) (func(), *JSONLClient, error) {
	info, err := os.Stat(cfg.ReplicaDir)
	if err != nil || !info.IsDir() {
		return nil, nil, errors.New("Notes replica directory unavailable")
	}
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.ReplicaDir, "--settings", cfg.Settings)
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, nil, err
	}
	cmd.Stderr = io.Discard // native diagnostics can contain private settings
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, nil, errors.New("Notes native process unavailable")
	}
	stop := func() {
		stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		stdout.Close()
	}
	return stop, &JSONLClient{Input: stdin, Output: stdout}, nil
}

type runtimeInventory struct {
	Scope   InventoryScope
	Watch   SourceWatch
	RetryAt time.Time
	Healthy bool
}

func (r *Runtime) prepareInventories(ctx context.Context, inventory SourceInventory, scopes map[string]InventoryScope) error {
	if r.inventories == nil {
		r.inventories = map[string]*runtimeInventory{}
	}
	for id, existing := range r.inventories {
		scope, ok := scopes[id]
		if !ok || scope != existing.Scope {
			if existing.Watch != nil {
				existing.Watch.Close()
			}
			delete(r.inventories, id)
		}
	}
	for id, scope := range scopes {
		if existing := r.inventories[id]; existing != nil {
			if existing.Watch == nil {
				if r.OpenSourceWatch == nil || time.Now().Before(existing.RetryAt) {
					continue
				}
			} else if existing.Watch.Healthy() || sourceWatchObserving(existing.Watch) {
				healthy := existing.Watch.Healthy()
				if existing.Healthy != healthy {
					posture, detail := "", ""
					if !healthy {
						posture, detail = "watch_unavailable", sourceWatchLeaseDetail(existing.Watch)
					}
					if err := inventory.beginWatch(ctx, scope, posture, detail, time.Now().UTC()); err != nil {
						return err
					}
					existing.Healthy = healthy
				}
				continue
			}
			if existing.Watch != nil {
				existing.Watch.Close()
			}
			delete(r.inventories, id)
		}
		posture := "watch_unconnected"
		detail := ""
		var watch SourceWatch
		if r.OpenSourceWatch != nil {
			var err error
			watch, err = r.OpenSourceWatch(scope, func(hintCtx context.Context, path string) error { return r.SourceHint(hintCtx, scope, path) })
			if err == nil && watch != nil && watch.Healthy() {
				posture = ""
			} else {
				posture = "watch_unavailable"
				detail = sourceWatchDetail(err)
				if err == nil {
					detail = "watch_lease_missing"
					if watch != nil {
						detail = sourceWatchLeaseDetail(watch)
					}
				}
				if watch != nil && (err != nil || !sourceWatchObserving(watch)) {
					watch.Close()
					watch = nil
				}
			}
		}
		if err := inventory.beginWatch(ctx, scope, posture, detail, time.Now().UTC()); err != nil {
			if watch != nil {
				watch.Close()
			}
			return err
		}
		r.inventories[id] = &runtimeInventory{Scope: scope, Watch: watch, Healthy: posture == "", RetryAt: time.Now().Add(inventoryRepairInterval(scope, posture))}
	}
	return nil
}

// CloseSourceWatches is the existing daemon shutdown/config-owner hook.
func (r *Runtime) CloseSourceWatches() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeNative()
	for id, inventory := range r.inventories {
		if inventory.Watch != nil {
			inventory.Watch.Close()
		}
		delete(r.inventories, id)
	}
}

const deliverySoftBytes int64 = 128 << 20
const deliveryAdmissionWindow = 4 * time.Second

type deliveryPublication struct {
	Bytes        int64
	Adopted      bool
	Reason       string
	MetadataOnly bool
}
type inventoryTurn struct {
	Cursor                                     DeliveryCursor
	TextExported, AssetExported, Adopted, Held int
	Bytes                                      int64
	Pressure                                   string
	Attempted                                  int
}

func deliverInventoryTurn(ctx context.Context, inventory SourceInventory, scope InventoryScope, cursor DeliveryCursor, publish func(InventoryEntry) (deliveryPublication, error), now func() time.Time) (out inventoryTurn, err error) {
	if cursor.Generation != scope.Generation {
		cursor = DeliveryCursor{Generation: scope.Generation}
	}
	out.Cursor = cursor
	started := now()
	text, err := inventory.DeliveryPage(ctx, scope, "text", started, 128)
	if err != nil {
		return out, err
	}
	assets, err := inventory.DeliveryPage(ctx, scope, "asset", started, 16)
	if err != nil {
		return out, err
	}
	lanes := map[string][]InventoryEntry{"text": text.Entries, "asset": assets.Entries}
	lane := cursor.NextLane
	if lane != "text" && lane != "asset" {
		lane = "text"
		if len(assets.Entries) > 0 && started.Sub(assets.Entries[0].WaitingSince) >= 30*time.Second && (len(text.Entries) == 0 || assets.Entries[0].WaitingSince.Before(text.Entries[0].WaitingSince)) {
			lane = "asset"
		}
	}
	opposite := func(lane string) string {
		if lane == "text" {
			return "asset"
		}
		return "text"
	}
	var admittedBytes int64
	admitted := 0
	blocked := map[string]bool{}
	preferredNext := ""
	for len(lanes["text"])+len(lanes["asset"]) > 0 {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if admitted > 0 && now().Sub(started) >= deliveryAdmissionWindow {
			out.Pressure = "admission_window"
			if len(lanes["asset"]) > 0 && out.AssetExported == 0 {
				preferredNext = "asset"
			} else {
				preferredNext = lane
			}
			break
		}
		if len(lanes[lane]) == 0 || blocked[lane] {
			lane = opposite(lane)
			if len(lanes[lane]) == 0 || blocked[lane] {
				break
			}
		}
		entry := lanes[lane][0]
		if admittedBytes > 0 && admittedBytes+entry.Size > deliverySoftBytes {
			blocked[lane] = true
			out.Pressure = "soft_byte_budget"
			if entry.Size > deliverySoftBytes {
				out.Pressure = "oversized_asset_dedicated"
				preferredNext = lane
			}
			lane = opposite(lane)
			continue
		}
		lanes[lane] = lanes[lane][1:]
		admitted++
		out.Attempted++
		publication, publishErr := publish(entry)
		spent := publication.Bytes
		if publishErr != nil && spent == 0 && !publication.MetadataOnly {
			spent = entry.Size
		}
		admittedBytes += spent
		dedicated := spent > deliverySoftBytes
		reason := ""
		if publishErr != nil {
			reason = publication.Reason
			if reason == "" {
				reason = "publication_pending"
			}
			out.Held++
		} else {
			if publication.Adopted {
				out.Adopted++
			} else {
				out.Bytes += publication.Bytes
				if lane == "text" {
					out.TextExported++
				} else {
					out.AssetExported++
				}
			}
			// Re-observe after canonical read/publication: a missed concurrent hint must
			// not let this acknowledgment hide changed bytes behind an old fingerprint.
			observed, observeErr := inventory.observe(scope.Root, entry.Path)
			if observeErr != nil {
				reason = "source_unavailable"
			} else if observed.Fingerprint != entry.Fingerprint || observed.Present != entry.Present {
				if err := inventory.Hint(ctx, scope, entry.Path, now()); err != nil {
					return out, err
				}
			}
			if lane == "text" {
				out.Cursor.TextSequence = max(out.Cursor.TextSequence, entry.Sequence)
				out.Cursor.TextAfter = entry.Path
			} else {
				out.Cursor.AssetSequence = max(out.Cursor.AssetSequence, entry.Sequence)
				out.Cursor.AssetAfter = entry.Path
			}
		}
		if err := inventory.Finish(ctx, scope, entry, reason, now()); err != nil {
			return out, err
		}
		out.Cursor.NextLane = opposite(lane)
		if dedicated {
			out.Pressure = "oversized_asset_dedicated"
			break
		}
		lane = opposite(lane)
	}
	if preferredNext != "" {
		out.Cursor.NextLane = preferredNext
	}
	return out, nil
}
