package workspacefiles

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/ids"
	sr "loom.local/loom/internal/serviceregistry"
)

type ApplicationFiles interface {
	ReadBrowse(context.Context, sr.ApplicationBrowseRead) (sr.ApplicationBrowseResult, error)
	OpenBrowse(context.Context, sr.ApplicationBrowseRead) (*os.File, sr.ApplicationBrowseRoot, error)
}

var ErrApplicationBoxOwnership = errors.New("application.browse.box_ownership_unavailable")

type applicationProjectScope struct {
	restricted bool
	boxes      map[string]string
}

func (s *Service) applicationReadScope(ctx context.Context, q sr.ApplicationBrowseRead) (sr.ApplicationBrowseRead, applicationProjectScope, error) {
	selection, scoped := boxregistry.FromContext(ctx)
	scope := applicationProjectScope{restricted: scoped}
	if !scoped {
		return q, scope, nil
	}
	if s.ApplicationProjectBoxes == nil {
		return q, scope, ErrApplicationBoxOwnership
	}
	if q.Ref != "" {
		projectID, err := sr.ApplicationBrowseProjectID(q.Ref)
		if err != nil {
			return q, scope, err
		}
		if q.ProjectID != "" && q.ProjectID != projectID {
			return q, scope, fs.ErrPermission
		}
		q.ProjectID = projectID
	}
	var requested []string
	if q.ProjectID != "" {
		if ids.Validate(ids.ProjectPrefix, q.ProjectID) != nil {
			return q, scope, ErrInvalidPath
		}
		requested = []string{q.ProjectID}
	}
	current, err := s.ApplicationProjectBoxes(ctx, requested)
	if err != nil {
		return q, scope, err
	}
	scope.boxes = map[string]string{}
	for projectID, boxID := range current {
		if q.ProjectID != "" && projectID != q.ProjectID {
			continue
		}
		if boxID == "" || !boxregistry.IsBoxRef("box:"+boxID) {
			continue // Unknown historical ownership is not implicitly Personal.
		}
		if ids.Validate(ids.ProjectPrefix, projectID) != nil {
			return q, scope, ErrApplicationBoxOwnership
		}
		if selection.AllBoxes || selection.Contains(boxID) {
			scope.boxes[projectID] = boxID
		}
	}
	if q.ProjectID != "" && scope.boxes[q.ProjectID] == "" {
		if current[q.ProjectID] == "" || !boxregistry.IsBoxRef("box:"+current[q.ProjectID]) {
			return q, scope, ErrApplicationBoxOwnership
		}
		return q, scope, fs.ErrPermission
	}
	if len(scope.boxes) > sr.ApplicationBrowseMaxProjectIDs {
		return q, scope, ErrApplicationBoxOwnership
	}
	q.RestrictProjects, q.ProjectIDs = true, []string{}
	for projectID := range scope.boxes {
		q.ProjectIDs = append(q.ProjectIDs, projectID)
	}
	sort.Strings(q.ProjectIDs)
	return q, scope, nil
}

func (scope applicationProjectScope) root(v sr.ApplicationBrowseRoot) (Root, error) {
	root := rootFromApplication(v)
	if scope.restricted {
		root.BoxID = scope.boxes[v.ProjectID]
		if root.BoxID == "" {
			return Root{}, ErrApplicationBoxOwnership
		}
	}
	return root, nil
}

type applicationViewContextKey struct{}

func WithApplicationView(ctx context.Context, revision string) context.Context {
	return context.WithValue(ctx, applicationViewContextKey{}, revision)
}
func expectedApplicationView(ctx context.Context) string {
	value, _ := ctx.Value(applicationViewContextKey{}).(string)
	return value
}

type ApplicationRootsPage struct {
	Roots      []Root `json:"roots"`
	NextCursor int    `json:"next_cursor,omitempty"`
}
type ArchiveListing struct {
	Archive Entry           `json:"archive"`
	Members []ArchiveMember `json:"members"`
}

func IsApplicationRoot(ref string) bool { return strings.HasPrefix(ref, "appdata_") }
func applicationRead(ref, relative, revision string) sr.ApplicationBrowseRead {
	return sr.ApplicationBrowseRead{Operation: "open", Ref: ref, Path: relative, Revision: revision}
}
func rootFromApplication(v sr.ApplicationBrowseRoot) Root {
	label := v.Label
	if v.ProjectName != "" {
		label = v.ProjectName + " / " + label
	}
	return Root{Ref: v.Ref, Label: label, Node: v.Node, SourcePath: v.SourcePath, ReadOnly: true, Available: v.Available, Verification: "allocation_custody", SourceKind: "application_data", ProjectID: v.ProjectID, Application: v.Application, DataKey: v.DataKey, Format: v.Format, ViewRevision: v.Revision, Reason: v.Reason}
}
func (s *Service) ApplicationRoots(ctx context.Context, project string, cursor, limit int) (ApplicationRootsPage, error) {
	out := ApplicationRootsPage{Roots: []Root{}}
	if s.Applications == nil {
		return out, nil
	}
	q, scope, e := s.applicationReadScope(ctx, sr.ApplicationBrowseRead{Operation: "catalog", ProjectID: project, Cursor: cursor, Limit: limit})
	if e != nil {
		return out, e
	}
	result, e := s.Applications.ReadBrowse(ctx, q)
	if e != nil {
		return out, e
	}
	for _, v := range result.Roots {
		if q.ProjectID != "" && v.ProjectID != q.ProjectID {
			return ApplicationRootsPage{}, ErrRootChanged
		}
		root, err := scope.root(v)
		if err != nil {
			return ApplicationRootsPage{}, err // Never post-filter a foreign page.
		}
		out.Roots = append(out.Roots, root)
	}
	out.NextCursor = result.NextCursor
	return out, nil
}

// ApplicationRoot resolves dynamic application roots through current project
// membership, independently of their physical service-owned allocation paths.
func (s *Service) ApplicationRoot(ctx context.Context, ref string) (Root, error) {
	return s.applicationRoot(ctx, ref)
}

func (s *Service) applicationRoot(ctx context.Context, ref string) (Root, error) {
	if s.Applications == nil {
		return Root{}, ErrUnknownRoot
	}
	q, scope, e := s.applicationReadScope(ctx, sr.ApplicationBrowseRead{Operation: "resolve", Ref: ref})
	if e != nil {
		return Root{}, e
	}
	x, e := s.Applications.ReadBrowse(ctx, q)
	if e != nil {
		return Root{}, e
	}
	if x.Root == nil || x.Root.Ref != ref || q.ProjectID != "" && x.Root.ProjectID != q.ProjectID {
		return Root{}, ErrUnknownRoot
	}
	return scope.root(*x.Root)
}

func (s *Service) openApplicationRoot(ctx context.Context, q sr.ApplicationBrowseRead) (*os.File, Root, error) {
	if s.Applications == nil {
		return nil, Root{}, ErrUnknownRoot
	}
	q, scope, err := s.applicationReadScope(ctx, q)
	if err != nil {
		return nil, Root{}, err
	}
	file, v, err := s.Applications.OpenBrowse(ctx, q)
	if err != nil {
		if file != nil {
			file.Close()
		}
		return nil, Root{}, err
	}
	root, err := scope.root(v)
	if err != nil || file == nil || v.Ref != q.Ref || q.ProjectID != "" && v.ProjectID != q.ProjectID || !v.Available {
		if file != nil {
			file.Close()
		}
		if err != nil {
			return nil, Root{}, err
		}
		return nil, Root{}, ErrUnknownRoot
	}
	if q.Revision != "" && q.Revision != root.ViewRevision {
		file.Close()
		return nil, Root{}, ErrRootChanged
	}
	return file, root, nil
}
func (s *Service) openApplication(ctx context.Context, ref, relative string, regular bool) (*os.File, Entry, error) {
	relative, e := normalizePath(relative)
	if e != nil {
		return nil, Entry{}, e
	}
	if mandatoryExcluded(relative) {
		return nil, Entry{}, fs.ErrPermission
	}
	if s.Applications == nil {
		return nil, Entry{}, ErrUnknownRoot
	}
	f, root, e := s.openApplicationRoot(ctx, applicationRead(ref, relative, expectedApplicationView(ctx)))
	if e != nil {
		return nil, Entry{}, e
	}
	info, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, Entry{}, e
	}
	if regular && !info.Mode().IsRegular() {
		f.Close()
		return nil, Entry{}, ErrNotRegular
	}
	v := entry(relative, info)
	v.Revision = applicationRevision(root.ViewRevision, v.Size, v.ModifiedAt)
	return f, v, nil
}
func applicationRevision(view string, size int64, modified time.Time) string {
	return `W/"` + strings.TrimPrefix(view, "sha256:") + "-" + strconv.FormatInt(size, 16) + "-" + strconv.FormatInt(modified.UnixNano(), 16) + `"`
}

func (s *Service) listApplication(ctx context.Context, in ListInput) (Page, error) {
	var e error
	in.Path, e = normalizePath(in.Path)
	if e != nil {
		return Page{}, e
	}
	if mandatoryExcluded(in.Path) {
		return Page{}, fs.ErrPermission
	}
	if in.Limit == 0 {
		in.Limit = DefaultLimit
	}
	if in.Limit < 1 || in.Limit > MaxLimit {
		return Page{}, ErrInvalidPath
	}
	root, e := s.applicationRoot(ctx, in.Root)
	if e != nil {
		return Page{}, e
	}
	if expected := expectedApplicationView(ctx); expected != "" && expected != root.ViewRevision {
		return Page{}, ErrRootChanged
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.expire(now)
	var lease *directoryLease
	selected := cursor{}
	if in.Cursor != "" {
		if len(in.Cursor) > 256 {
			return Page{}, ErrInvalidCursor
		}
		raw, e := base64.RawURLEncoding.DecodeString(in.Cursor)
		if e != nil || json.Unmarshal(raw, &selected) != nil {
			return Page{}, ErrInvalidCursor
		}
		lease = s.leases[selected.Lease]
		if lease == nil || lease.root != root || lease.relative != in.Path || lease.limit != in.Limit || lease.showIgnored != in.ShowIgnored || !lease.current() {
			s.remove(selected.Lease)
			return Page{}, ErrInvalidCursor
		}
		if selected.Page == lease.lastInput {
			return clonePage(lease.lastPage), nil
		}
		if selected.Page != lease.sequence {
			return Page{}, ErrInvalidCursor
		}
	} else {
		file, openedRoot, err := s.openApplicationRoot(ctx, applicationRead(in.Root, in.Path, root.ViewRevision))
		if err != nil {
			return Page{}, err
		}
		if openedRoot != root {
			file.Close()
			return Page{}, ErrRootChanged
		}
		info, err := file.Stat()
		if err != nil || !info.IsDir() {
			file.Close()
			return Page{}, ErrNotDirectory
		}
		lease = &directoryLease{root: root, directory: file, info: info, relative: in.Path, limit: in.Limit, showIgnored: in.ShowIgnored, used: now, owner: s}
		if selection, scoped := boxregistry.FromContext(ctx); scoped {
			lease.applicationSelection = &selection
		}
		selected.Lease = rand.Text()
		defer func() {
			if s.leases[selected.Lease] != lease {
				lease.close()
			}
		}()
	}
	page := Page{Root: in.Root, Path: in.Path, Entries: []Entry{}, Order: "filesystem"}
	// The daemon holds the admitted directory descriptor for O(n) pagination;
	// one bounded helper batch supplies no-follow child metadata per page.
	names := []string{}
	for examined := 0; examined < maxExaminedPerPage && len(names) < in.Limit; examined++ {
		if e = ctx.Err(); e != nil {
			s.remove(selected.Lease)
			return Page{}, e
		}
		if len(lease.pending) == 0 && !lease.eof {
			lease.pending, e = lease.directory.ReadDir(in.Limit + 1)
			if e != nil && !errors.Is(e, io.EOF) {
				return Page{}, e
			}
			lease.eof = errors.Is(e, io.EOF)
		}
		if len(lease.pending) == 0 {
			break
		}
		child := lease.pending[0]
		lease.pending = lease.pending[1:]
		rel := child.Name()
		if in.Path != "." {
			rel = in.Path + "/" + rel
		}
		if mandatoryExcluded(rel) || !in.ShowIgnored && rebuildable(rel) || child.Type()&os.ModeSymlink != 0 {
			continue
		}
		names = append(names, child.Name())
	}
	q, scope, e := s.applicationReadScope(ctx, sr.ApplicationBrowseRead{Operation: "children", Ref: in.Root, Path: in.Path, Revision: root.ViewRevision, Names: names})
	if e != nil {
		s.remove(selected.Lease)
		return Page{}, e
	}
	children, e := s.Applications.ReadBrowse(ctx, q)
	if e != nil {
		s.remove(selected.Lease)
		return Page{}, e
	}
	if scope.restricted {
		if children.Root == nil {
			s.remove(selected.Lease)
			return Page{}, ErrRootChanged
		}
		current, err := scope.root(*children.Root)
		if err != nil || current != root {
			s.remove(selected.Lease)
			return Page{}, ErrRootChanged
		}
	}
	for _, v := range children.Entries {
		page.Entries = append(page.Entries, Entry{Name: v.Name, RelativePath: v.Path, Kind: v.Kind, Size: v.Size, ModifiedAt: v.ModifiedAt, Revision: applicationRevision(root.ViewRevision, v.Size, v.ModifiedAt)})
	}
	if !lease.current() {
		s.remove(selected.Lease)
		return Page{}, ErrInvalidCursor
	}
	page.HasMore = len(lease.pending) > 0 || !lease.eof
	lease.sequence++
	lease.used = now
	if page.HasMore {
		if in.Cursor == "" {
			s.evict()
			if s.leases == nil {
				s.leases = map[string]*directoryLease{}
			}
			s.leases[selected.Lease] = lease
		}
		raw, _ := json.Marshal(cursor{Lease: selected.Lease, Page: lease.sequence})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	} else {
		s.remove(selected.Lease)
		lease.close()
	}
	lease.lastInput, lease.lastPage = selected.Page, clonePage(page)
	return page, nil
}
