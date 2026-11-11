// Package workspacefiles reads actual configured roots without an index, catalog,
// enrollment, or another filesystem projection.
package workspacefiles

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/nodeagent/watchedroots"
)

var (
	ErrInvalidPath   = errors.New("files: invalid relative path")
	ErrUnknownRoot   = errors.New("files: unknown root")
	ErrInvalidCursor = errors.New("files: cursor expired or directory changed; restart listing")
	ErrNotDirectory  = errors.New("files: not a directory")
	ErrNotRegular    = errors.New("files: not a regular file")
	ErrRootChanged   = errors.New("files: configured root identity changed")
)

const (
	DefaultLimit       = 100
	MaxLimit           = 500
	maxExaminedPerPage = 4096
)

type Root struct {
	BoxID        string `json:"box_id,omitempty"`
	BoxKey       string `json:"box_key,omitempty"`
	Writable     bool   `json:"writable"`
	SourceKind   string `json:"source_kind,omitempty"`
	ProjectID    string `json:"project_id,omitempty"`
	Application  string `json:"application,omitempty"`
	DataKey      string `json:"data_key,omitempty"`
	Format       string `json:"format,omitempty"`
	ViewRevision string `json:"view_revision,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Ref          string `json:"ref"`
	Label        string `json:"label"`
	Node         string `json:"node"`
	SourcePath   string `json:"source_path"`
	ReadOnly     bool   `json:"read_only"`
	Verification string `json:"verification"`
	Available    bool   `json:"available"`
}

func (r Root) IsWritableBox() bool {
	return !r.ReadOnly && (r.Ref == "box" || r.SourceKind == "box" && strings.HasPrefix(r.Ref, "box:"))
}

type Entry struct {
	Name         string    `json:"name"`
	RelativePath string    `json:"relative_path"`
	Kind         string    `json:"kind"`
	Size         int64     `json:"size"`
	ModifiedAt   time.Time `json:"modified_at"`
	Revision     string    `json:"revision"`
}

type ListInput struct {
	Root        string
	Path        string
	Cursor      string
	Limit       int
	ShowIgnored bool
}

type Page struct {
	Root       string  `json:"root"`
	Path       string  `json:"path"`
	Entries    []Entry `json:"entries"`
	NextCursor string  `json:"next_cursor,omitempty"`
	HasMore    bool    `json:"has_more"`
	Order      string  `json:"order"`
}

// Service holds only bounded, short-lived directory handles for pagination.
// Root configuration remains with the existing daemon owner, not a new registry.
type Service struct {
	Applications ApplicationFiles
	Roots        []Root
	LeaseTTL     time.Duration
	MaxLeases    int
	Now          func() time.Time
	mu           sync.Mutex
	leases       map[string]*directoryLease
	rootMu       sync.Mutex
	bindings     map[string]rootBinding
	watchCount   int
	searchMu     sync.Mutex
	searches     map[string]*searchLease

	// Nil IDs requests the complete current selected-project inventory; non-nil
	// IDs requests exact projects. The projects owner supplies current home IDs;
	// unknown/unavailable Boxes remain absent/empty. Never truncate the inventory.
	ApplicationProjectBoxes func(context.Context, []string) (map[string]string, error)
}

type rootBinding struct {
	source, canonical string
	info              fs.FileInfo
}

type directoryLease struct {
	root        Root
	rootHandle  *os.Root
	directory   *os.File
	info        fs.FileInfo
	relative    string
	limit       int
	showIgnored bool
	pending     []os.DirEntry
	eof         bool
	sequence    int
	lastInput   int
	lastPage    Page
	used        time.Time
	owner       *Service

	applicationSelection *boxregistry.ResolvedSelection
}

type cursor struct {
	Lease string `json:"lease"`
	Page  int    `json:"page"`
}

func (s *Service) ListRoots(ctx context.Context) ([]Root, error) {
	result := make([]Root, 0, len(s.Roots))
	for _, root := range s.Roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if root.Ref == "" || root.SourcePath == "" {
			continue
		}
		handle, err := s.openRoot(root)
		root.Available = err == nil
		if handle != nil {
			_ = handle.Close()
		}
		result = append(result, root)
	}
	return result, nil
}

func (s *Service) Stat(ctx context.Context, ref, relative string) (Entry, error) {
	if IsApplicationRoot(ref) {
		f, metadata, err := s.openApplication(ctx, ref, relative, false)
		if f != nil {
			f.Close()
		}
		return metadata, err
	}
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	root, err := s.root(ref)
	if err != nil {
		return Entry{}, err
	}
	relative, err = normalizePath(relative)
	if err != nil {
		return Entry{}, err
	}
	if mandatoryExcluded(relative) {
		return Entry{}, fs.ErrPermission
	}
	handle, err := s.openRoot(root)
	if err != nil {
		return Entry{}, err
	}
	defer handle.Close()
	file, err := openNonblocking(handle, relative)
	if err != nil {
		return Entry{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Entry{}, err
	}
	return entry(relative, info), nil
}

// Open returns a confined regular-file descriptor. It never retrieves an older
// retained copy when the requested current source is missing.
func (s *Service) Open(ctx context.Context, ref, relative string) (*os.File, Entry, error) {
	if IsApplicationRoot(ref) {
		return s.openApplication(ctx, ref, relative, true)
	}
	if err := ctx.Err(); err != nil {
		return nil, Entry{}, err
	}
	root, err := s.root(ref)
	if err != nil {
		return nil, Entry{}, err
	}
	relative, err = normalizePath(relative)
	if err != nil {
		return nil, Entry{}, err
	}
	if mandatoryExcluded(relative) {
		return nil, Entry{}, fs.ErrPermission
	}
	handle, err := s.openRoot(root)
	if err != nil {
		return nil, Entry{}, err
	}
	defer handle.Close()
	file, err := openNonblocking(handle, relative)
	if err != nil {
		return nil, Entry{}, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, Entry{}, err
		}
		return nil, Entry{}, ErrNotRegular
	}
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return nil, Entry{}, err
	}
	return file, entry(relative, info), nil
}

func (s *Service) List(ctx context.Context, input ListInput) (Page, error) {
	if IsApplicationRoot(input.Root) {
		return s.listApplication(ctx, input)
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	root, err := s.root(input.Root)
	if err != nil {
		return Page{}, err
	}
	input.Path, err = normalizePath(input.Path)
	if err != nil {
		return Page{}, err
	}
	if mandatoryExcluded(input.Path) || (!input.ShowIgnored && rebuildable(input.Path)) {
		return Page{}, fs.ErrPermission
	}
	if input.Limit == 0 {
		input.Limit = DefaultLimit
	}
	if input.Limit < 1 || input.Limit > MaxLimit {
		return Page{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidPath, MaxLimit)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.expire(now)
	var lease *directoryLease
	var selected cursor
	if input.Cursor != "" {
		if len(input.Cursor) > 256 {
			return Page{}, ErrInvalidCursor
		}
		decoded, err := base64.RawURLEncoding.DecodeString(input.Cursor)
		if err != nil || json.Unmarshal(decoded, &selected) != nil || selected.Page < 1 {
			return Page{}, ErrInvalidCursor
		}
		lease = s.leases[selected.Lease]
		if lease == nil || lease.root != root || lease.relative != input.Path || lease.limit != input.Limit || lease.showIgnored != input.ShowIgnored {
			return Page{}, ErrInvalidCursor
		}
		if !lease.current() {
			s.remove(selected.Lease)
			return Page{}, ErrInvalidCursor
		}
		lease.used = now
		if selected.Page == lease.lastInput {
			return clonePage(lease.lastPage), nil
		}
		if selected.Page != lease.sequence {
			return Page{}, ErrInvalidCursor
		}
	} else {
		lease, err = s.newDirectoryLease(root, input, now)
		if err != nil {
			return Page{}, err
		}
		selected.Lease = rand.Text()
		defer func() {
			if s.leases[selected.Lease] != lease {
				lease.close()
			}
		}()
	}
	page := Page{Root: root.Ref, Path: input.Path, Entries: []Entry{}, Order: "filesystem"}
	for examined := 0; examined < maxExaminedPerPage && len(page.Entries) < input.Limit; examined++ {
		if err := ctx.Err(); err != nil {
			s.remove(selected.Lease)
			return Page{}, err
		}
		if len(lease.pending) == 0 && !lease.eof {
			lease.pending, err = lease.directory.ReadDir(input.Limit + 1)
			if err != nil && !errors.Is(err, io.EOF) {
				s.remove(selected.Lease)
				return Page{}, err
			}
			lease.eof = errors.Is(err, io.EOF)
		}
		if len(lease.pending) == 0 {
			break
		}
		child := lease.pending[0]
		lease.pending = lease.pending[1:]
		if !utf8.ValidString(child.Name()) {
			s.remove(selected.Lease)
			return Page{}, fmt.Errorf("%w: unsupported filename encoding", ErrInvalidPath)
		}
		relative := path.Join(input.Path, child.Name())
		if mandatoryExcluded(relative) || (!input.ShowIgnored && rebuildable(relative)) {
			continue
		}
		info, infoErr := lease.rootHandle.Lstat(relative)
		if errors.Is(infoErr, fs.ErrNotExist) {
			continue
		}
		if infoErr != nil {
			s.remove(selected.Lease)
			return Page{}, infoErr
		}
		page.Entries = append(page.Entries, entry(relative, info))
	}
	if len(lease.pending) == 0 && !lease.eof {
		lease.pending, err = lease.directory.ReadDir(1)
		if err != nil && !errors.Is(err, io.EOF) {
			s.remove(selected.Lease)
			return Page{}, err
		}
		lease.eof = errors.Is(err, io.EOF)
	}
	if !lease.current() {
		s.remove(selected.Lease)
		return Page{}, ErrInvalidCursor
	}
	page.HasMore = len(lease.pending) > 0 || !lease.eof
	lease.sequence++
	if page.HasMore {
		if input.Cursor == "" {
			s.evict()
			if s.leases == nil {
				s.leases = map[string]*directoryLease{}
			}
			s.leases[selected.Lease] = lease
		}
		raw, _ := json.Marshal(cursor{Lease: selected.Lease, Page: lease.sequence})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	} else {
		lease.close()
	}
	lease.lastInput, lease.lastPage = selected.Page, clonePage(page)
	return page, nil
}

func (s *Service) newDirectoryLease(root Root, input ListInput, now time.Time) (*directoryLease, error) {
	handle, err := s.openRoot(root)
	if err != nil {
		return nil, err
	}
	directory, err := openNonblocking(handle, input.Path)
	if err != nil {
		_ = handle.Close()
		return nil, err
	}
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		_ = directory.Close()
		_ = handle.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrNotDirectory
	}
	return &directoryLease{owner: s, root: root, rootHandle: handle, directory: directory, info: info, relative: input.Path, limit: input.Limit, showIgnored: input.ShowIgnored, lastInput: -1, used: now}, nil
}

func (l *directoryLease) current() bool {
	if l.root.SourceKind == "application_data" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if l.applicationSelection != nil {
			ctx = boxregistry.WithSelection(ctx, *l.applicationSelection)
		}
		f, root, e := l.owner.openApplicationRoot(ctx, applicationRead(l.root.Ref, l.relative, l.root.ViewRevision))
		if e != nil {
			return false
		}
		defer f.Close()
		info, e := f.Stat()
		return e == nil && root == l.root && os.SameFile(info, l.info) && info.ModTime().Equal(l.info.ModTime()) && info.Size() == l.info.Size()
	}
	root, err := l.owner.openRoot(l.root)
	if err != nil {
		return false
	}
	defer root.Close()
	directory, err := openNonblocking(root, l.relative)
	if err != nil {
		return false
	}
	defer directory.Close()
	info, err := directory.Stat()
	return err == nil && os.SameFile(info, l.info) && info.ModTime().Equal(l.info.ModTime()) && info.Size() == l.info.Size()
}

// Bind each configured root once. A legitimate platform prefix such as macOS's
// /tmp alias is resolved at binding, not reinterpreted as new authority per read.
func (s *Service) openRoot(root Root) (*os.Root, error) {
	s.rootMu.Lock()
	defer s.rootMu.Unlock()
	if !filepath.IsAbs(root.SourcePath) || filepath.Clean(root.SourcePath) == "/" {
		return nil, ErrRootChanged
	}
	sourceInfo, err := os.Lstat(root.SourcePath)
	if err != nil {
		return nil, err
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 {
		return nil, ErrRootChanged
	}
	canonical, err := filepath.EvalSymlinks(root.SourcePath)
	if err != nil {
		return nil, err
	}
	binding, bound := s.bindings[root.Ref]
	if bound && (binding.source != root.SourcePath || binding.canonical != canonical) {
		return nil, ErrRootChanged
	}
	// A no-follow descriptor proves which inode OpenRoot must subsequently bind.
	filesystem, err := os.OpenRoot("/")
	if err != nil {
		return nil, err
	}
	defer filesystem.Close()
	proof, err := openNonblocking(filesystem, strings.TrimPrefix(canonical, "/"))
	if err != nil {
		return nil, err
	}
	defer proof.Close()
	info, err := proof.Stat()
	if err != nil {
		return nil, err
	}
	if bound && !os.SameFile(info, binding.info) {
		return nil, ErrRootChanged
	}
	handle, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, err
	}
	openedInfo, err := handle.Stat(".")
	if err != nil || !os.SameFile(info, openedInfo) {
		_ = handle.Close()
		return nil, ErrRootChanged
	}
	if !bound {
		if s.bindings == nil {
			s.bindings = map[string]rootBinding{}
		}
		s.bindings[root.Ref] = rootBinding{source: root.SourcePath, canonical: canonical, info: info}
	}
	return handle, nil
}

func (s *Service) root(ref string) (Root, error) {
	for _, root := range s.Roots {
		if root.Ref == ref && root.SourcePath != "" {
			return root, nil
		}
	}
	return Root{}, ErrUnknownRoot
}

// SourcePath resolves only an already permitted exact entry, never an index hit.
func (s *Service) SourcePath(ctx context.Context, ref, relative string) (string, Entry, error) {
	metadata, err := s.Stat(ctx, ref, relative)
	if err != nil {
		return "", Entry{}, err
	}
	root, err := s.root(ref)
	if IsApplicationRoot(ref) {
		root, err = s.applicationRoot(ctx, ref)
	}
	if err != nil {
		return "", Entry{}, err
	}
	return filepath.Join(root.SourcePath, filepath.FromSlash(metadata.RelativePath)), metadata, nil
}

func normalizePath(value string) (string, error) {
	if value == "" {
		return ".", nil
	}
	if len(value) > 4096 || strings.ContainsAny(value, "\\\x00") || !utf8.ValidString(value) || !fs.ValidPath(value) {
		return "", ErrInvalidPath
	}
	return value, nil
}

func entry(relative string, info fs.FileInfo) Entry {
	kind := "special"
	switch {
	case info.IsDir():
		kind = "directory"
	case info.Mode().IsRegular():
		kind = "file"
	case info.Mode()&os.ModeSymlink != 0:
		kind = "symlink"
	}
	return Entry{Name: path.Base(relative), RelativePath: relative, Kind: kind, Size: info.Size(), ModifiedAt: info.ModTime().UTC(), Revision: `W/"` + strconv.FormatInt(info.Size(), 16) + "-" + strconv.FormatInt(info.ModTime().UnixNano(), 16) + `"`}
}

func clonePage(page Page) Page { page.Entries = append([]Entry{}, page.Entries...); return page }
func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
func (s *Service) expire(now time.Time) {
	ttl := s.LeaseTTL
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	for id, lease := range s.leases {
		if now.Sub(lease.used) >= ttl {
			s.remove(id)
		}
	}
}
func (s *Service) evict() {
	maximum := s.MaxLeases
	if maximum <= 0 {
		maximum = 32
	}
	for len(s.leases) >= maximum {
		var oldest string
		var used time.Time
		for id, lease := range s.leases {
			if oldest == "" || lease.used.Before(used) {
				oldest, used = id, lease.used
			}
		}
		s.remove(oldest)
	}
}
func (s *Service) remove(id string) {
	if lease := s.leases[id]; lease != nil {
		lease.close()
		delete(s.leases, id)
	}
}
func (l *directoryLease) close() {
	if l.directory != nil {
		_ = l.directory.Close()
		l.directory = nil
	}
	if l.rootHandle != nil {
		_ = l.rootHandle.Close()
		l.rootHandle = nil
	}
}
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.leases {
		s.remove(id)
	}
	return nil
}

func mandatoryExcluded(relative string) bool {
	return watchedroots.IsNotesRecoveryPath(relative) || filepolicy.EvaluateBuiltIn(filepolicy.ProfileExact, relative).RuleCategory == filepolicy.RuleCategoryMandatorySafety
}
func rebuildable(relative string) bool {
	return filepolicy.EvaluateBuiltIn(filepolicy.ProfileManaged, relative).RuleCategory == filepolicy.RuleCategoryReconstructible
}
