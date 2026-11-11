package workspacefiles

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watch is a request-lifetime, shallow notification subscription. Notifications
// are hints only: subsequent reads recheck the original source authority. No
// listing, hashing, database query or polling occurs while a subscription is idle.
func (s *Service) Watch(ctx context.Context, root string, paths []string, notify func([]string, bool) error) error {
	if len(paths) < 1 || len(paths) > 4096 {
		return ErrInvalidPath
	}
	s.mu.Lock()
	if s.watchCount >= 32 {
		s.mu.Unlock()
		return fmt.Errorf("file notification subscription limit reached")
	}
	s.watchCount++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.watchCount--; s.mu.Unlock() }()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	directories := map[string]map[string]string{}
	for _, relative := range paths {
		requested, e := normalizePath(relative)
		if e != nil {
			return e
		}
		current := requested
		var suffix []string
		var absolute string
		for {
			var entry Entry
			absolute, entry, e = s.SourcePath(ctx, root, current)
			if e == nil {
				if entry.Kind != "directory" {
					return ErrNotDirectory
				}
				break
			}
			// A missing child must not disable every other live folder. Watch
			// only its closest admitted ancestor; exact reads remain authoritative.
			if !errors.Is(e, fs.ErrNotExist) || current == "." {
				return e
			}
			suffix = append([]string{path.Base(current)}, suffix...)
			current = path.Dir(current)
		}
		if directories[absolute] == nil {
			if e = watcher.Add(absolute); e != nil {
				return e
			}
			directories[absolute] = map[string]string{}
		}
		target := filepath.Join(append([]string{absolute}, suffix...)...)
		directories[absolute][target] = requested
	}
	if err = notify(paths, true); err != nil {
		return err
	}
	dirty := map[string]bool{}
	rebind := false
	var debounce <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Op&fsnotify.Chmod == event.Op {
				continue
			}
			for _, watched := range []string{filepath.Dir(event.Name), event.Name} {
				for target, relative := range directories[watched] {
					if target == watched || event.Name == watched || target == event.Name || strings.HasPrefix(target, event.Name+string(filepath.Separator)) {
						dirty[relative] = true
						// An inode watch cannot follow a replacement directory, nor
						// advance from a missing child's nearest existing ancestor.
						// End after the hint so the subscriber can resolve custody again.
						if event.Name == watched && event.Op&(fsnotify.Rename|fsnotify.Remove) != 0 || target != watched && event.Op&(fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
							rebind = true
						}
					}
				}
			}
			if len(dirty) > 0 && debounce == nil {
				timer = time.NewTimer(50 * time.Millisecond)
				debounce = timer.C
			}
		case <-debounce:
			changed := make([]string, 0, len(dirty))
			for relative := range dirty {
				changed = append(changed, relative)
			}
			if err = notify(changed, false); err != nil {
				return err
			}
			if rebind {
				return nil
			}
			dirty = map[string]bool{}
			debounce = nil
		case err := <-watcher.Errors:
			if err != nil {
				return err
			}
			return nil
		}
	}
}
