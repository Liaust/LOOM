package notesworkspacesync

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesworkspace"
)

// RuntimeConfig is operator-managed, not client-supplied enrollment. Generation
// is an enrollment token; KnowledgeResolver also binds current source policy.
type RuntimeConfig struct {
	SchemaVersion string             `json:"schema_version"`
	Command       string             `json:"command"`
	Replica       string             `json:"replica"`
	ReplicaDir    string             `json:"replica_dir"`
	Settings      string             `json:"settings"`
	Scopes        []RuntimeSelection `json:"scopes"`
	WatchSeconds  int                `json:"watch_seconds,omitempty"`
}

type RuntimeSelection struct {
	Scope
	AdmissionPath string `json:"admission_path"`
	PathPrefix    string `json:"path_prefix,omitempty"`
}

func (s RuntimeSelection) contains(relative string) bool {
	return s.PathPrefix == "" || strings.HasPrefix(relative, s.PathPrefix+"/")
}

// Apply the same enrollment restriction to exports, edits and both rename paths.
type selectedSources struct {
	notesworkspace.PathResolver
	Selections []RuntimeSelection
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
	if c.SchemaVersion != "notes_workspace.runtime.v1" || !token(c.Replica) || len(c.Scopes) < 1 || len(c.Scopes) > 8 || c.WatchSeconds < 0 || c.WatchSeconds > 60 {
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
	Config      RuntimeConfig
	DB          *sql.DB
	Knowledge   *knowledge.Service
	NodeKey     string
	sourceHints map[string]sourceHint
	mu          sync.Mutex
}

type sourceHint struct {
	info     os.FileInfo
	verified time.Time
}

type RuntimeCursor struct {
	Collection      string `json:"collection,omitempty"`
	After           string `json:"after,omitempty"`
	RetainedAfter   string `json:"retained_after,omitempty"`
	EnrollmentAfter string `json:"enrollment_after,omitempty"`
}

type RuntimeResult struct {
	SchemaVersion string        `json:"schema_version"`
	Imported      int           `json:"imported"`
	Processed     int           `json:"processed"`
	Held          int           `json:"held"`
	Exported      int           `json:"exported"`
	Skipped       int           `json:"skipped"`
	Reconciled    int           `json:"reconciled"`
	Recoveries    int           `json:"recoveries"`
	Replication   string        `json:"replication"`
	Cursor        RuntimeCursor `json:"-"`
}

// Run opens only the operator-selected native replica. The existing worker owns
// cadence and concurrency; the child owns native replication, not source writes.
func (r *Runtime) Run(ctx context.Context, cursor RuntimeCursor) (RuntimeResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := RuntimeResult{SchemaVersion: "notes_workspace.result.v1", Cursor: cursor}
	if r.Config.Validate() != nil || r.DB == nil || r.Knowledge == nil || r.NodeKey == "" {
		return out, errors.New("Notes workspace runtime is not configured")
	}
	lock, acquired, err := lockReplica(r.Config.ReplicaDir)
	if err != nil {
		return out, errors.New("Notes recovery fence unavailable")
	}
	if !acquired {
		out.Replication = "recovery_paused"
		return out, nil
	}
	defer lock.Close()
	resolver := notesworkspace.KnowledgeResolver{
		Knowledge: r.Knowledge, NodeKey: r.NodeKey,
		WithSelection: func(_ context.Context, id string, fn func(string) error) error {
			for _, scope := range r.Config.Scopes {
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
	source := notesworkspace.Service{Store: notesworkspace.SQLStore{DB: r.DB}, Sources: selectedSources{PathResolver: resolver, Selections: r.Config.Scopes}}
	var scopes []Scope
	roots := map[string]string{}
	prefixes := map[string]string{}
	for _, selection := range r.Config.Scopes {
		scope := selection.Scope
		err := source.Sources.WithSource(ctx, scope.SourceCollection, selection.AdmissionPath, func(src notesworkspace.Source) error {
			scope.Generation = src.Generation
			roots[scope.Collection] = src.Path
			prefixes[scope.Collection] = selection.PathPrefix
			return nil
		})
		if err != nil {
			out.Skipped++ // withdrawn/archived sources do not authorize deletion
			continue
		}
		scopes = append(scopes, scope)
	}
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
	}
	out.Cursor = cursor
	child, native, err := startNative(ctx, r.Config)
	if err != nil {
		return out, err
	}
	defer child()
	var status NativeStatus
	if err := native.Call(ctx, map[string]any{"method": "status"}, &status); err != nil {
		return out, errors.New("Notes native startup unavailable")
	}
	bridge := Service{Replica: r.Config.Replica, Epoch: status.Epoch, Store: SQLStore{DB: r.DB}, Source: NotesSource{Service: source}, Native: native, Scopes: scopes}
	if err := bridge.ready(ctx); err != nil {
		return out, errors.New("Notes native identity or encryption unavailable")
	}
	// Bind epoch before replication, including on runs with no writable scopes.
	if err := bridge.Store.WithReplica(ctx, bridge.Replica, bridge.Epoch, func(Records) error { return nil }); err != nil {
		return out, errors.New("Notes replica identity unavailable")
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
	if err := sync(); err != nil {
		return out, err
	}
	continuous := func(method string) error {
		var result struct{ Status, Outcome string }
		if err := bridge.call(ctx, method, nil, &result); err != nil || result.Status != "replication" || result.Outcome != "completed" {
			return errors.New("Notes continuous replication unavailable")
		}
		return nil
	}
	if r.Config.WatchSeconds > 0 {
		if err := continuous("sync.start"); err != nil {
			return out, err
		}
	}
	until := time.Now().Add(time.Duration(r.Config.WatchSeconds) * time.Second)
	nextSources := time.Time{}
	if r.sourceHints == nil {
		r.sourceHints = map[string]sourceHint{}
	}
	for k, hint := range r.sourceHints {
		if time.Since(hint.verified) >= time.Minute {
			delete(r.sourceHints, k)
		}
	}
	for {
		progress, err := bridge.Step(ctx, 128)
		if err != nil {
			return out, errors.New("Notes source reconciliation incomplete")
		}
		out.Imported += progress.Imported
		out.Processed += progress.Processed
		out.Held = progress.Held
		if len(scopes) > 0 && !time.Now().Before(nextSources) {
			// Existing clients must finish their enrollment transition before a
			// large new attachment tree delays their next ordinary source sweep.
			mappings, after, err := bridge.enrollmentExports(ctx, cursor.EnrollmentAfter, 32)
			if err != nil {
				return out, errors.New("Notes enrollment discovery incomplete")
			}
			cursor.EnrollmentAfter = after
			for _, mapping := range mappings {
				file, err := source.Store.File(ctx, mapping.SourceFileID)
				if err != nil || file.Deleted || file.CollectionID != mapping.Scope.SourceCollection {
					out.Skipped++
					continue
				}
				read, err := source.Read(ctx, file.CollectionID, file.RelativePath)
				if err != nil || !read.Base.Exists {
					out.Skipped++
				} else if _, err := bridge.Export(ctx, mapping.Scope.Collection, read.File.ID, read.Base.ID); err != nil {
					out.Held++
				} else {
					out.Exported++
				}
			}
			index := 0
			for i, scope := range scopes {
				if scope.Collection == cursor.Collection {
					index = i
					break
				}
			}
			scope := scopes[index]
			if cursor.Collection != scope.Collection {
				cursor.Collection, cursor.After = scope.Collection, ""
			}
			paths, more, err := sourcePageWithin(ctx, roots[scope.Collection], prefixes[scope.Collection], cursor.After, 32)
			if err != nil {
				return out, errors.New("Notes source enumeration incomplete")
			}
			references := 0
			deferred := false
			resumeAfter := cursor.After
			for _, relative := range paths {
				path := filepath.Join(roots[scope.Collection], filepath.FromSlash(relative))
				info, statErr := os.Lstat(path)
				hintKey := key(scope.SourceCollection, scope.Generation, path)
				hint, known := r.sourceHints[hintKey]
				if statErr == nil && known && hint.matches(info, time.Now()) {
					cursor.After = relative
					if !deferred {
						resumeAfter = relative
					}
					continue
				}
				// Keep attachment hashing/publication bounded between text admission
				// passes. Skipped references are revisited by the next lexical sweep.
				if !textPath(relative) {
					if references >= 1 {
						deferred = true
						continue
					}
					references++
				}
				read, err := source.Read(ctx, scope.SourceCollection, relative)
				delete(r.sourceHints, hintKey)
				if err != nil || !read.Base.Exists {
					out.Skipped++
				} else if _, err := bridge.Export(ctx, scope.Collection, read.File.ID, read.Base.ID); err != nil {
					out.Held++
				} else {
					out.Exported++
					if statErr == nil && len(r.sourceHints) < 10000 {
						r.sourceHints[hintKey] = sourceHint{info: info, verified: time.Now()}
					}
				}
				cursor.After = relative
				if !deferred {
					resumeAfter = relative
				}
			}
			if deferred {
				cursor.After = resumeAfter
			} else if !more {
				cursor.Collection, cursor.After = scopes[(index+1)%len(scopes)].Collection, ""
			}
			out.Cursor = cursor
			nextSources = time.Now().Add(2 * time.Second)
		}
		if r.Config.WatchSeconds == 0 || !time.Now().Before(until) {
			break
		}
		var wake struct{ Status string }
		if err := bridge.call(ctx, "changes.wait", map[string]any{"since": progress.NextSequence, "timeoutMs": 1000}, &wake); err != nil || (wake.Status != "idle" && wake.Status != "changed") {
			return out, errors.New("Notes change notification unavailable")
		}
	}
	if r.Config.WatchSeconds > 0 {
		if err := continuous("sync.stop"); err != nil {
			return out, err
		}
	}
	return out, sync()
}

func (h sourceHint) matches(info os.FileInfo, now time.Time) bool {
	return h.info != nil && info.Mode().IsRegular() && os.SameFile(info, h.info) &&
		info.Size() == h.info.Size() && info.Mode() == h.info.Mode() &&
		info.ModTime().Equal(h.info.ModTime()) && now.Sub(h.verified) < time.Minute
}

func sourcePage(ctx context.Context, root, after string, limit int) ([]string, bool, error) {
	return sourcePageWithin(ctx, root, "", after, limit)
}

func sourcePageWithin(ctx context.Context, root, prefix, after string, limit int) ([]string, bool, error) {
	if limit < 1 || limit > 100 {
		return nil, false, ErrHeld
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
	err := filepath.WalkDir(start, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == start {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return ErrHeld
			}
			return nil
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "credentials" || entry.Name() == "private_no_index" || entry.Name() == "LOOM-Control-v1" {
				return filepath.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type().IsRegular() && relative > after && IsSourcePath(relative) {
			// WalkDir's directory-first traversal is not global path order.
			// Keep only the next bounded lexical page, including a lookahead.
			index := sort.SearchStrings(paths, relative)
			paths = append(paths, "")
			copy(paths[index+1:], paths[index:])
			paths[index] = relative
			if len(paths) > limit+1 {
				paths = paths[:limit+1]
			}
		}
		return nil
	})
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
