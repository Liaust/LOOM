package workspacefiles

import (
	"context"
	"crypto/rand"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"time"
)

type SearchInput struct {
	Root, Path, Query, Cursor string
	Limit                     int
}
type SearchPage struct {
	Entries    []Entry `json:"entries"`
	NextCursor string  `json:"next_cursor,omitempty"`
	HasMore    bool    `json:"has_more"`
	Coverage   string  `json:"coverage"`
	Examined   int     `json:"examined"`
	Skipped    int     `json:"skipped"`
}
type searchDirectory struct{ path, cursor string }
type searchLease struct {
	mu                sync.Mutex
	input             SearchInput
	queue             []searchDirectory
	pending           []Entry
	token             string
	lastToken         string
	lastPage          SearchPage
	used              time.Time
	examined, skipped int
	truncated         bool
}

// Search is an explicit, bounded filesystem observation, independent of Notes
// enrollment. It never reads content or creates catalog/index records. Each
// continuation must be requested; idle searches perform no work.
func (s *Service) Search(ctx context.Context, in SearchInput) (SearchPage, error) {
	in.Query = strings.TrimSpace(in.Query)
	if len(in.Query) < 2 || len(in.Query) > 160 || strings.ContainsAny(in.Query, "\x00\r\n") {
		return SearchPage{}, ErrInvalidPath
	}
	var err error
	in.Path, err = normalizePath(in.Path)
	if err != nil {
		return SearchPage{}, err
	}
	if in.Limit == 0 {
		in.Limit = 100
	}
	if in.Limit < 1 || in.Limit > MaxLimit {
		return SearchPage{}, ErrInvalidPath
	}
	if _, err = s.root(in.Root); err != nil {
		return SearchPage{}, err
	}
	s.searchMu.Lock()
	if s.searches == nil {
		s.searches = map[string]*searchLease{}
	}
	for k, l := range s.searches {
		if s.now().Sub(l.used) > 2*time.Minute {
			delete(s.searches, k)
		}
	}
	var lease *searchLease
	if in.Cursor != "" {
		lease = s.searches[in.Cursor]
	} else {
		if len(s.searches) >= 32 {
			s.searchMu.Unlock()
			return SearchPage{}, ErrInvalidCursor
		}
		lease = &searchLease{input: in, queue: []searchDirectory{{path: in.Path}}, used: s.now()}
	}
	if lease == nil {
		s.searchMu.Unlock()
		return SearchPage{}, ErrInvalidCursor
	}
	s.searchMu.Unlock()
	lease.mu.Lock()
	defer lease.mu.Unlock()
	failed := true
	defer func() {
		if failed {
			s.searchMu.Lock()
			for key, value := range s.searches {
				if value == lease {
					delete(s.searches, key)
				}
			}
			s.searchMu.Unlock()
		}
	}()
	if lease.input.Root != in.Root || lease.input.Path != in.Path || lease.input.Query != in.Query || lease.input.Limit != in.Limit {
		return SearchPage{}, ErrInvalidCursor
	}
	if in.Cursor != "" && in.Cursor == lease.lastToken {
		failed = false
		return cloneSearchPage(lease.lastPage), nil
	}
	if in.Cursor != "" && in.Cursor != lease.token {
		return SearchPage{}, ErrInvalidCursor
	}
	page := SearchPage{Entries: []Entry{}, Coverage: "partial"}
	started := time.Now()
	examined := 0
	for (len(lease.queue) > 0 || len(lease.pending) > 0) && len(page.Entries) < in.Limit && examined < 4096 && time.Since(started) < 750*time.Millisecond {
		if err = ctx.Err(); err != nil {
			return SearchPage{}, err
		}
		if len(lease.pending) == 0 {
			dir := lease.queue[0]
			listing, e := s.List(ctx, ListInput{Root: in.Root, Path: dir.path, Cursor: dir.cursor, Limit: 200})
			if e != nil {
				if errors.Is(e, fs.ErrPermission) || errors.Is(e, fs.ErrNotExist) {
					lease.skipped++
					lease.queue = lease.queue[1:]
					continue
				}
				return SearchPage{}, e
			}
			lease.pending = listing.Entries
			if listing.HasMore {
				lease.queue[0].cursor = listing.NextCursor
			} else {
				lease.queue = lease.queue[1:]
			}
			if len(lease.pending) == 0 {
				examined++
				continue
			}
		}
		child := lease.pending[0]
		lease.pending = lease.pending[1:]
		examined++
		lease.examined++
		if child.Kind == "directory" {
			if child.Name == ".git" {
				continue
			}
			if len(lease.queue) >= 4096 || strings.Count(child.RelativePath, "/") >= 64 {
				lease.truncated = true
				continue
			}
			lease.queue = append(lease.queue, searchDirectory{path: child.RelativePath})
		} else if child.Kind == "file" && strings.Contains(strings.ToLower(child.Name), strings.ToLower(in.Query)) {
			// Reopen through the no-follow source owner, never trust a stale lstat path.
			current, e := s.Stat(ctx, in.Root, child.RelativePath)
			if e != nil || current.Kind != "file" {
				lease.skipped++
				continue
			}
			page.Entries = append(page.Entries, current)
		}
		if lease.examined >= 250000 {
			lease.truncated = true
			lease.queue = nil
			lease.pending = nil
			break
		}
	}
	page.Examined = lease.examined
	page.Skipped = lease.skipped
	page.HasMore = len(lease.queue) > 0 || len(lease.pending) > 0
	if !page.HasMore {
		page.Coverage = "complete"
		if lease.truncated || lease.skipped > 0 {
			page.Coverage = "limited"
		}
	}
	if page.HasMore {
		page.NextCursor = rand.Text()
	}
	s.searchMu.Lock()
	if lease.lastToken != "" {
		delete(s.searches, lease.lastToken)
	}
	lease.lastToken = in.Cursor
	lease.token = page.NextCursor
	lease.used = s.now()
	lease.lastPage = cloneSearchPage(page)
	if in.Cursor != "" {
		s.searches[in.Cursor] = lease
	}
	if page.NextCursor != "" {
		s.searches[page.NextCursor] = lease
	}
	s.searchMu.Unlock()
	failed = false
	return page, nil
}
func cloneSearchPage(p SearchPage) SearchPage { p.Entries = append([]Entry{}, p.Entries...); return p }
