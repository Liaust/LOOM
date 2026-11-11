package notesworkspacesync

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"time"
)

type layoutDependencyState struct {
	keys        []string
	fingerprint string
	dirty       bool
	force       bool
}

type layoutDependencyChange struct {
	revision      uint64
	key           string
	prefix, force bool
}

type layoutPhaseState struct {
	scope         Scope
	native        Native
	epoch         string
	adoptExisting bool
	incomplete    bool
	retryAt       time.Time
	repairAt      time.Time
	dependencies  map[string]string
	holdReasons   map[string]int
}

func (s layoutPhaseState) next() time.Time {
	if !s.retryAt.IsZero() && s.retryAt.Before(s.repairAt) {
		return s.retryAt
	}
	return s.repairAt
}

// A disposable reverse index of retained dependencies. SQL records remain the
// authority; startup/reconnect and persisted retry deadlines repair missed hints.
type layoutDependencyCache struct {
	mu            sync.Mutex
	revision      uint64
	entries       map[string]layoutDependencyState
	reverse       map[string]map[string]bool
	changes       []layoutDependencyChange
	layouts       map[string]layoutPhaseState
	resetRevision uint64
}

// Keep only scheduling evidence, not the potentially large durable cursor.
func (c *layoutDependencyCache) beginLayout(scope Scope, native Native, epoch string, adopt bool, now time.Time) (bool, layoutPhaseState, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := layoutCursorID(scope)
	state, ok := c.layouts[id]
	comparable := native != nil && reflect.TypeOf(native).Comparable()
	due := !ok || state.scope != scope || !comparable || state.native != native || state.epoch != epoch || state.adoptExisting != adopt ||
		state.incomplete || !now.Before(state.repairAt) || !state.retryAt.IsZero() && !now.Before(state.retryAt)
	if !due {
		for key, expected := range state.dependencies {
			e, exists := c.entries[key]
			if !exists || e.dirty || e.force || e.fingerprint != expected {
				due = true
				break
			}
		}
	}
	if due {
		// Errors must leave an unknown phase, not a previously clean snapshot.
		delete(c.layouts, id)
	}
	return due, state, c.revision
}

func (c *layoutDependencyCache) finishLayout(scope Scope, native Native, epoch string, adopt bool, cursor layoutCursor, now time.Time, version uint64) {
	state := layoutPhaseState{scope: scope, native: native, epoch: epoch, adoptExisting: adopt,
		incomplete: !cursor.Complete || cursor.Retry && cursor.RetryAt.IsZero(), retryAt: cursor.RetryAt,
		repairAt: now.Add(layoutEvidenceRetry), dependencies: map[string]string{}, holdReasons: map[string]int{}}
	for _, hold := range cursor.Holds {
		state.dependencies[layoutDependencyKey(scope, hold.MappingID)] = hold.Dependency
		state.holdReasons[hold.Reason]++
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resetRevision > version {
		return // Reconnect during the phase still requires a new baseline.
	}
	if c.layouts == nil {
		c.layouts = map[string]layoutPhaseState{}
	}
	c.layouts[layoutCursorID(scope)] = state
}

func (c *layoutDependencyCache) version() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revision
}

func (c *layoutDependencyCache) keys(key string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.entries[key].keys...)
}

func (c *layoutDependencyCache) needs(key, expected string, now, retry time.Time) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	return !ok || e.dirty || e.force || e.fingerprint != expected || !now.Before(retry), e.force
}

func (c *layoutDependencyCache) record(key string, keys []string, fingerprint string, version uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]layoutDependencyState{}
		c.reverse = map[string]map[string]bool{}
	}
	old := c.entries[key]
	for _, dep := range old.keys {
		delete(c.reverse[dep], key)
		if len(c.reverse[dep]) == 0 {
			delete(c.reverse, dep)
		}
	}
	unique := map[string]bool{}
	for _, dep := range keys {
		if unique[dep] {
			continue
		}
		unique[dep] = true
		if c.reverse[dep] == nil {
			c.reverse[dep] = map[string]bool{}
		}
		c.reverse[dep][key] = true
	}
	dirty, force := false, old.force
	if len(c.changes) > 0 && version < c.changes[0].revision-1 {
		dirty = true
	}
	for _, change := range c.changes {
		if change.revision <= version {
			continue
		}
		for _, dep := range keys {
			if change.key == "" || dependencyKeyMatches(change.key, dep, change.prefix) {
				dirty, force = true, force || change.force
				break
			}
		}
	}
	c.entries[key] = layoutDependencyState{keys: keys, fingerprint: fingerprint, dirty: dirty, force: force}
}

func dependencyKeyMatches(dep, key string, prefix bool) bool {
	child := dep + "/"
	if strings.HasSuffix(dep, ":") {
		child = dep
	}
	return key == dep || prefix && strings.HasPrefix(key, child)
}

func (c *layoutDependencyCache) changed(dep string, prefix, force bool) {
	c.revision++
	// A bounded race journal covers keys first discovered during an SQL read.
	// Unrelated concurrent notifications must not dirty that freshly read graph.
	if len(c.changes) == 512 {
		copy(c.changes, c.changes[1:])
		c.changes = c.changes[:511]
	}
	c.changes = append(c.changes, layoutDependencyChange{c.revision, dep, prefix, force})
}

func (c *layoutDependencyCache) invalidate(dep string, prefix, force bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.changed(dep, prefix, force)
	changed := false
	mark := func(ids map[string]bool) {
		for id := range ids {
			e := c.entries[id]
			changed = changed || !e.dirty || force && !e.force
			e.dirty, e.force = true, e.force || force
			c.entries[id] = e
		}
	}
	if !prefix {
		mark(c.reverse[dep])
	} else {
		for key, ids := range c.reverse {
			if dependencyKeyMatches(dep, key, true) {
				mark(ids)
			}
		}
	}
	return changed
}

func (c *layoutDependencyCache) reset() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.changed("", true, false)
	c.resetRevision = c.revision
	changed := len(c.layouts) > 0
	c.layouts = nil
	for key, e := range c.entries {
		changed = changed || !e.dirty
		e.dirty = true
		c.entries[key] = e
	}
	return changed
}

func (c *layoutDependencyCache) settled(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key]
	if !e.dirty {
		e.force = false
		c.entries[key] = e
	}
}

func (c *layoutDependencyCache) forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forgetLocked(key)
}

func (c *layoutDependencyCache) forgetLocked(key string) {
	for _, dep := range c.entries[key].keys {
		delete(c.reverse[dep], key)
		if len(c.reverse[dep]) == 0 {
			delete(c.reverse, dep)
		}
	}
	delete(c.entries, key)
}

func (c *layoutDependencyCache) retainScopes(scopes []Scope) {
	current := map[string]Scope{}
	for _, scope := range scopes {
		current[layoutCursorID(scope)] = scope
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		owner, _, _ := strings.Cut(key, ":")
		if _, ok := current[owner]; !ok {
			c.forgetLocked(key)
		}
	}
	for id, state := range c.layouts {
		if scope, ok := current[id]; !ok || scope != state.scope {
			delete(c.layouts, id)
		}
	}
}

func (s *Service) dependencyCache() *layoutDependencyCache {
	if s.layoutDependencies == nil {
		s.layoutDependencies = &layoutDependencyCache{}
	}
	return s.layoutDependencies
}

func layoutDependencyKey(scope Scope, id string) string { return layoutCursorID(scope) + ":" + id }
func layoutPathKey(collection, relative string) string  { return "path:" + collection + ":" + relative }

type referenceRetryHint struct{ Collection, Path string }

func (c *layoutDependencyCache) referenceHints() []referenceRetryHint {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := map[referenceRetryHint]bool{}
	var out []referenceRetryHint
	for id, entry := range c.entries {
		_, name, _ := strings.Cut(id, ":")
		if !strings.HasPrefix(name, "reference_") || !entry.dirty && !entry.force {
			continue
		}
		for _, key := range entry.keys {
			if !strings.HasPrefix(key, "path:") {
				continue
			}
			collection, path, ok := strings.Cut(strings.TrimPrefix(key, "path:"), ":")
			hint := referenceRetryHint{collection, path}
			if ok && referencePath(path) && !seen[hint] {
				seen[hint] = true
				out = append(out, hint)
			}
		}
	}
	return out
}

// DependencyChanged consumes only payload-free commit notifications. An unknown
// record or another replica cannot turn all held graphs into runnable work.
func (r *Runtime) DependencyChanged(raw []byte) {
	changed := false
	if len(raw) == 0 {
		changed = r.layoutDependencies.reset()
	} else {
		var hint struct{ Replica, Kind, ID string }
		if json.Unmarshal(raw, &hint) != nil || hint.ID == "" || hint.Replica != "" && hint.Replica != r.Config.Replica {
			return
		}
		switch hint.Kind {
		case "source":
			changed = r.layoutDependencies.invalidate("source:"+hint.ID, false, false)
		case "file", "source_file", "binding", "export":
			changed = r.layoutDependencies.invalidate("record:"+hint.Kind+":"+hint.ID, false, false)
		}
	}
	if changed {
		r.dependencyRevision.Add(1)
		if r.Wake != nil {
			r.Wake()
		}
	}
}
