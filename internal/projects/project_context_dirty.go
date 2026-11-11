package projects

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"loom.local/loom/internal/ids"
)

const ProjectContextSafetyInterval = 15 * time.Minute

type ProjectContextChangeSource struct {
	ProjectID  string
	Root       string
	Generation string
	Archived   bool
}

// These are scheduling observations only. The ordinary context reader still
// validates source ownership, archive evidence and every file it reads.
type ProjectContextObservation struct {
	Generation  string    `json:"generation"`
	AttemptedAt time.Time `json:"attempted_at"`
	RefreshedAt time.Time `json:"refreshed_at,omitempty"`
	Status      string    `json:"status"`
}

type ProjectContextChange struct {
	ProjectID  string `json:"project_id"`
	Generation string `json:"generation"`
	Reason     string `json:"reason"`
}

func (s Service) ListProjectContextChangeSources(ctx context.Context, owner, after string, limit int) ([]ProjectContextChangeSource, error) {
	if owner == "" || limit < 1 || limit > 20 || (after != "" && ids.Validate(ids.ProjectPrefix, after) != nil) {
		return nil, fmt.Errorf("invalid project context change page")
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT p.project_id,s.project_root,p.status='archived',
	 md5(jsonb_build_array(p.slug,p.name,p.status,p.archive_state,s.source_revision,
	 s.semantic_digest,s.location_digest,
	 (SELECT jsonb_agg(jsonb_build_array(f.facet_key,f.folder,f.enabled,f.present,f.placeholder,f.facet_status) ORDER BY f.facet_key)
	  FROM projects.project_contract_facets f WHERE f.project_id=p.project_id),
	 (SELECT jsonb_agg(jsonb_build_array(m.repository_id,m.repository_owner_project_id,m.member_key,m.member_path,m.role,m.state_root,m.lifecycle_status,m.metadata) ORDER BY m.repository_id)
	  FROM projects.project_repository_memberships m WHERE m.project_id=p.project_id))::text || COALESCE(p.metadata->>'work_state',''))
	 FROM projects.projects p JOIN projects.project_repository_sources s USING(project_id)
	 WHERE s.source_snapshot_json->'location'->>'owner_node'=$1 AND p.project_id>$2
	 ORDER BY p.project_id LIMIT $3`, owner, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ProjectContextChangeSource{}
	for rows.Next() {
		var source ProjectContextChangeSource
		if err := rows.Scan(&source.ProjectID, &source.Root, &source.Archived, &source.Generation); err != nil {
			return nil, err
		}
		result = append(result, source)
	}
	return result, rows.Err()
}

type projectContextSourceLister interface {
	ListProjectContextChangeSources(context.Context, string, string, int) ([]ProjectContextChangeSource, error)
}

// Native notifications are hints, not a new journal. Registered source rows,
// the actual files and the worker's completed-generation checkpoint are durable.
// A fresh listener requests catch-up; missed notifications get a safety pass.
type ProjectContextChanges struct {
	Sources projectContextSourceLister
	Owner   string
	Wake    func()

	mu          sync.Mutex
	watcher     *fsnotify.Watcher
	session     int64
	epochs      map[string]uint64
	sources     map[string]ProjectContextChangeSource
	watched     map[string]map[string]bool
	nextCatalog time.Time
	degraded    bool
}

func NewProjectContextChanges(sources projectContextSourceLister, owner string, wake func()) *ProjectContextChanges {
	return &ProjectContextChanges{Sources: sources, Owner: owner, Wake: wake,
		session: time.Now().UnixNano(), epochs: map[string]uint64{},
		sources: map[string]ProjectContextChangeSource{}, watched: map[string]map[string]bool{}}
}

func (c *ProjectContextChanges) Start(ctx context.Context) {
	watcher, err := fsnotify.NewWatcher()
	if err == nil {
		c.mu.Lock()
		c.watcher = watcher
		c.mu.Unlock()
	} else {
		c.mu.Lock()
		c.degraded = true
		c.mu.Unlock()
	}
	go func() {
		if watcher == nil {
			<-ctx.Done() // Pending still provides bounded catalog/safety checks.
			return
		}
		defer watcher.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					c.invalidateAll()
					return
				}
				c.observeEvent(event)
			case _, ok := <-watcher.Errors:
				c.invalidateAll()
				if !ok {
					return
				}
			}
		}
	}()
}

func (c *ProjectContextChanges) invalidateAll() {
	c.mu.Lock()
	c.degraded = true
	for id := range c.sources {
		c.epochs[id]++
	}
	c.nextCatalog = time.Time{}
	c.mu.Unlock()
	if c.Wake != nil {
		c.Wake()
	}
}

func contextEventRelevant(root, name string) bool {
	if name == root || name == filepath.Join(root, "AGENTS.md") {
		return true
	}
	for _, directory := range []string{".project", ".loom"} {
		path := filepath.Join(root, directory)
		if name == path || strings.HasPrefix(name, path+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (c *ProjectContextChanges) observeEvent(event fsnotify.Event) {
	c.mu.Lock()
	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		for path := range c.watched {
			if path == event.Name || strings.HasPrefix(path, event.Name+string(filepath.Separator)) {
				if c.watcher != nil {
					_ = c.watcher.Remove(path)
				}
				delete(c.watched, path) // Re-created directories need a new descriptor.
			}
		}
	}
	changed := false
	for id, source := range c.sources {
		if !source.Archived && contextEventRelevant(source.Root, event.Name) {
			c.epochs[id]++
			if event.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
				c.addWatchesLocked(source)
			}
			changed = true
		}
	}
	c.mu.Unlock()
	if changed && c.Wake != nil {
		c.Wake()
	}
}

func contextDirectorySafe(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	for {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		parent := filepath.Dir(path)
		if parent == path {
			return true
		}
		path = parent
	}
}

func (c *ProjectContextChanges) addWatchesLocked(source ProjectContextChangeSource) {
	if c.watcher == nil || source.Archived || !contextDirectorySafe(source.Root) {
		if !source.Archived {
			c.degraded = true
		}
		return
	}
	add := func(path string) {
		if !contextDirectorySafe(path) {
			return
		}
		if c.watched[path] == nil {
			if c.watcher.Add(path) != nil {
				c.degraded = true
				return // Safety reads remain necessary even when Add fails.
			}
			c.watched[path] = map[string]bool{}
		}
		c.watched[path][source.ProjectID] = true
	}
	add(filepath.Dir(source.Root))
	add(source.Root)
	for _, directory := range []string{".project", ".loom"} {
		count := 0
		root := filepath.Join(source.Root, directory)
		if !contextDirectorySafe(root) {
			continue // The source-root watch observes future creation/repair.
		}
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry == nil {
				c.degraded = true
				return filepath.SkipDir
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil // Skip the link, not its later sibling directories.
			}
			if !entry.IsDir() {
				return nil
			}
			count++
			if count > 256 {
				c.degraded = true
				return filepath.SkipAll
			}
			add(path)
			return nil
		})
	}
}

func (c *ProjectContextChanges) refreshSources(ctx context.Context, now time.Time) error {
	c.mu.Lock()
	due := !now.Before(c.nextCatalog)
	c.mu.Unlock()
	if !due {
		return nil
	}
	sources := map[string]ProjectContextChangeSource{}
	after := ""
	for {
		page, err := c.Sources.ListProjectContextChangeSources(ctx, c.Owner, after, 20)
		if err != nil {
			return fmt.Errorf("project context change inventory unavailable")
		}
		if len(page) > 20 {
			return fmt.Errorf("project context change page exceeds bound")
		}
		for _, source := range page {
			if ids.Validate(ids.ProjectPrefix, source.ProjectID) != nil || source.ProjectID <= after || source.Generation == "" {
				return fmt.Errorf("invalid project context change identity")
			}
			after = source.ProjectID
			sources[source.ProjectID] = source
		}
		if len(page) < 20 {
			break
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for path, owners := range c.watched {
		for id := range owners {
			source, exists := sources[id]
			if !exists || source.Archived || (path != filepath.Dir(source.Root) && !contextEventRelevant(source.Root, path)) {
				delete(owners, id)
			}
		}
		if len(owners) == 0 {
			_ = c.watcher.Remove(path)
			delete(c.watched, path)
		}
	}
	for _, source := range sources {
		previous, exists := c.sources[source.ProjectID]
		if !exists || previous != source || !c.watched[source.Root][source.ProjectID] {
			c.addWatchesLocked(source) // Register before observing/acknowledging a generation.
		}
	}
	c.sources = sources
	c.nextCatalog = now.Add(time.Minute)
	return nil
}

func (c *ProjectContextChanges) WatchStatus() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.watcher == nil || c.degraded {
		return "fallback"
	}
	return "native"
}

func (c *ProjectContextChanges) Pending(ctx context.Context, observations map[string]ProjectContextObservation, now time.Time, force bool) ([]ProjectContextChange, *time.Time, error) {
	if err := c.refreshSources(ctx, now); err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	pending := []ProjectContextChange{}
	var next *time.Time
	for id, source := range c.sources {
		generation := fmt.Sprintf("%s:%d:%d", source.Generation, c.session, c.epochs[id])
		previous, exists := observations[id]
		due := previous.AttemptedAt.Add(ProjectContextSafetyInterval)
		reason := ""
		switch {
		case force:
			reason = "manual_refresh"
		case !exists || previous.Generation != generation:
			reason = "source_or_file_changed"
		case !source.Archived || previous.Status != "refreshed":
			if !now.Before(due) {
				reason = "safety_reconcile"
			} else if next == nil || due.Before(*next) {
				at := due
				next = &at
			}
		}
		if reason != "" {
			pending = append(pending, ProjectContextChange{ProjectID: id, Generation: generation, Reason: reason})
		}
	}
	for id, previous := range observations {
		if _, exists := c.sources[id]; exists {
			continue
		}
		generation := fmt.Sprintf("source_removed:%d", c.session)
		due := previous.AttemptedAt.Add(ProjectContextSafetyInterval)
		if previous.Generation != generation || force || (previous.Status == "unavailable" && !now.Before(due)) {
			pending = append(pending, ProjectContextChange{ProjectID: id, Generation: generation, Reason: "source_removed"})
		} else if previous.Status == "unavailable" && (next == nil || due.Before(*next)) {
			at := due
			next = &at
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].ProjectID < pending[j].ProjectID })
	return pending, next, nil
}
