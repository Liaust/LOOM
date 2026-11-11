package serviceregistry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	pc "loom.local/loom/internal/projectcontracts"
)

type ApplicationBrowseBinding struct {
	BindingRef string                   `json:"binding_ref"`
	View       pc.ApplicationDataBrowse `json:"view"`
}
type ApplicationBrowseRequest struct {
	Owner                        ApplicationOwner                    `json:"owner"`
	ProjectName                  string                              `json:"project_name"`
	Operation                    string                              `json:"operation"`
	OperationToken               string                              `json:"operation_token"`
	ExpectedInstallationRevision string                              `json:"expected_installation_revision"`
	RuntimeDesired               string                              `json:"runtime_desired,omitempty"`
	ExpectedRevision             string                              `json:"expected_revision"`
	Views                        map[string]ApplicationBrowseBinding `json:"views"`
}

func (q ApplicationBrowseRequest) Digest() string { return applicationSHA(q) }

type ApplicationBrowseReceipt struct {
	Owner          ApplicationOwner `json:"owner"`
	OperationToken string           `json:"operation_token"`
	InputDigest    string           `json:"input_digest"`
	Revision       string           `json:"revision"`
	State          string           `json:"state"`
	Matches        bool             `json:"matches"`
}
type ApplicationBrowseRecord struct {
	Owner       ApplicationOwner                    `json:"owner"`
	ProjectName string                              `json:"project_name"`
	InputDigest string                              `json:"input_digest"`
	Revision    string                              `json:"revision"`
	Views       map[string]ApplicationBrowseBinding `json:"views"`
	Data        map[string]ApplicationDataIdentity  `json:"data"`
	ViewInodes  map[string]ApplicationBrowseInode   `json:"view_inodes"`
}
type ApplicationBrowseInode struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}
type ApplicationBrowseRoot struct {
	Ref         string `json:"ref"`
	Label       string `json:"label"`
	Node        string `json:"node"`
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	Application string `json:"application"`
	DataKey     string `json:"data_key"`
	SourcePath  string `json:"source_path"`
	Revision    string `json:"revision"`
	Format      string `json:"format"`
	ReadOnly    bool   `json:"read_only"`
	Available   bool   `json:"available"`
	Reason      string `json:"reason,omitempty"`
}
type ApplicationBrowseRead struct {
	// The trusted daemon supplies current project membership. This is a read
	// selection, not a replacement grant or a persisted per-file permission.
	RestrictProjects bool     `json:"restrict_projects,omitempty"`
	ProjectIDs       []string `json:"project_ids,omitempty"`
	Operation        string   `json:"operation"`
	Ref              string   `json:"ref,omitempty"`
	Path             string   `json:"path,omitempty"`
	Revision         string   `json:"revision,omitempty"`
	Names            []string `json:"names,omitempty"`
	ProjectID        string   `json:"project_id,omitempty"`
	Cursor           int      `json:"cursor,omitempty"`
	Limit            int      `json:"limit,omitempty"`
}

const ApplicationBrowseMaxProjectIDs = 4096

// ApplicationBrowseProjectID extracts a canonical reference's project identity;
// callers must still resolve its current membership through the projects owner.
func ApplicationBrowseProjectID(ref string) (string, error) {
	x, err := parseApplicationBrowseRef(ref)
	return x.Owner.ProjectID, err
}

func (q ApplicationBrowseRead) permitsProject(id string) bool {
	if q.ProjectID != "" && q.ProjectID != id {
		return false
	}
	if !q.RestrictProjects {
		return true
	}
	for _, allowed := range q.ProjectIDs {
		if allowed == id {
			return true
		}
	}
	return false
}

func (q ApplicationBrowseRead) validateProjectSelection() error {
	if len(q.ProjectIDs) > ApplicationBrowseMaxProjectIDs || !q.RestrictProjects && len(q.ProjectIDs) > 0 || q.ProjectID != "" && !applicationProjectPattern.MatchString(q.ProjectID) {
		return applicationError("browse.project_selection_invalid")
	}
	seen := map[string]bool{}
	for _, id := range q.ProjectIDs {
		if !applicationProjectPattern.MatchString(id) || seen[id] {
			return applicationError("browse.project_selection_invalid")
		}
		seen[id] = true
	}
	if q.RestrictProjects && q.ProjectID != "" && !q.permitsProject(q.ProjectID) {
		return applicationError("browse.project_outside_selection")
	}
	return nil
}

type ApplicationBrowseEntry struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Kind       string    `json:"kind"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
}
type ApplicationBrowseResult struct {
	Roots      []ApplicationBrowseRoot  `json:"roots,omitempty"`
	Root       *ApplicationBrowseRoot   `json:"root,omitempty"`
	Entries    []ApplicationBrowseEntry `json:"entries,omitempty"`
	NextCursor int                      `json:"next_cursor,omitempty"`
}
type applicationBrowseRef struct {
	Owner ApplicationOwner `json:"owner"`
	Key   string           `json:"key"`
}

func applicationBrowseRootRef(owner ApplicationOwner, key string) string {
	b, _ := json.Marshal(applicationBrowseRef{owner, key})
	return "appdata_" + base64.RawURLEncoding.EncodeToString(b)
}
func parseApplicationBrowseRef(ref string) (applicationBrowseRef, error) {
	var x applicationBrowseRef
	if len(ref) > 768 || !strings.HasPrefix(ref, "appdata_") {
		return x, applicationError("browse.ref_invalid")
	}
	b, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ref, "appdata_"))
	if e != nil || applicationDecodeJSON(b, &x) != nil || !x.Owner.valid() || !applicationKeyPattern.MatchString(x.Key) || applicationBrowseRootRef(x.Owner, x.Key) != ref {
		return x, applicationError("browse.ref_invalid")
	}
	return x, nil
}

func (r ApplicationRuntime) BrowseControl(ctx context.Context, peer uint32, q ApplicationBrowseRequest) (ApplicationBrowseReceipt, error) {
	out := ApplicationBrowseReceipt{Owner: q.Owner, OperationToken: q.OperationToken, InputDigest: q.Digest(), State: "observed"}
	if !q.Owner.valid() || (q.Operation != "apply" && q.Operation != "inspect") || !applicationRevisionPattern.MatchString(q.OperationToken) || len(q.Views) > 32 || len(q.ProjectName) > 160 || strings.ContainsAny(q.ProjectName, "\x00\r\n") {
		return out, applicationError("browse.request_invalid")
	}
	p, e := r.policy()
	if e != nil {
		return out, e
	}
	if peer != p.InstallerUID {
		return out, applicationError("peer.denied")
	}
	unlock, e := r.Store.lock(ctx, q.Owner.Instance())
	if e != nil {
		return out, e
	}
	defer unlock()
	p, e = r.policy()
	if e != nil {
		return out, e
	}
	if peer != p.InstallerUID {
		return out, applicationError("peer.denied")
	}
	g, e := r.resolveGrant(p, q.Owner)
	if e != nil {
		return out, e
	}
	var installed ApplicationInstallation
	if r.Store.read("installation-"+q.Owner.Instance(), &installed) != nil || installed.Owner != q.Owner || !installed.Applied || installed.Fenced || installed.Retired {
		return out, applicationError("browse.installation_unavailable")
	}
	var old ApplicationBrowseRecord
	err := r.Store.read("browse-"+q.Owner.Instance(), &old)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return out, err
	}
	out.Revision = old.Revision
	out.Matches = applicationSHA(old.Views) == applicationSHA(q.Views) && old.ProjectName == q.ProjectName
	if q.Operation == "inspect" {
		return out, nil
	}
	journal := "browse-operation-" + q.Owner.Instance() + "-" + applicationSHA(q.OperationToken)[7:]
	var previous ApplicationBrowseReceipt
	if e = r.Store.read(journal, &previous); e == nil {
		if previous.InputDigest != out.InputDigest || previous.Owner != q.Owner {
			return out, applicationError("operation.token_conflict")
		}
		if old.Revision != previous.Revision {
			return out, applicationError("browse.revision_conflict")
		}
		return previous, nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return out, e
	}
	if old.InputDigest == out.InputDigest {
		out.State, out.Revision, out.Matches = "succeeded", old.Revision, true
		return out, r.Store.write(journal, out)
	}
	runtimeMatches := q.RuntimeDesired != "" && installed.Request != nil && installed.Request.RuntimeDesiredDigest() == q.RuntimeDesired
	if (!runtimeMatches && (q.RuntimeDesired != "" || installed.Revision != q.ExpectedInstallationRevision)) || old.Revision != q.ExpectedRevision {
		return out, applicationError("browse.revision_conflict")
	}
	record := ApplicationBrowseRecord{Owner: q.Owner, ProjectName: q.ProjectName, InputDigest: out.InputDigest, Views: q.Views, Data: map[string]ApplicationDataIdentity{}, ViewInodes: map[string]ApplicationBrowseInode{}}
	for key, binding := range q.Views {
		policy, ok := g.Data[key]
		identity, exists := installed.Data[key]
		if !ok || !exists || !applicationKeyPattern.MatchString(key) || binding.BindingRef == "" || binding.BindingRef != policy.BindingRef || !binding.View.Valid() {
			return out, applicationError("browse.binding_invalid")
		}
		actual, e := applicationDataIdentityOwner(policy.Path, policy.Pool, identity.UID, identity.GID)
		if e != nil || !applicationSameDataIdentity(identity, actual) {
			return out, applicationError("browse.custody_changed")
		}
		f, e := applicationBrowseAllocationOpen(policy.Path, actual, binding.View.Subpath)
		if e != nil {
			return out, e
		}
		st, e := f.Stat()
		var inode unix.Stat_t
		statErr := unix.Fstat(int(f.Fd()), &inode)
		f.Close()
		if e != nil || statErr != nil || !st.IsDir() {
			return out, applicationError("browse.subpath_invalid")
		}
		record.Data[key] = identity
		record.ViewInodes[key] = ApplicationBrowseInode{uint64(inode.Dev), inode.Ino}
	}
	record.Revision = applicationSHA(record)
	if e = r.Store.write("browse-"+q.Owner.Instance(), record); e != nil {
		return out, e
	}
	out.State, out.Revision, out.Matches = "succeeded", record.Revision, true
	return out, r.Store.write(journal, out)
}

// Descriptor walking is confined to the accepted allocation and rejects links,
// including links in host ancestors. No modes, owners or service state change.
func applicationBrowseOpen(root, relative string) (*os.File, error) {
	if !applicationAbsolutePath(root) || relative != "" && relative != "." && (!fs.ValidPath(relative) || strings.ContainsAny(relative, "\\\x00")) {
		return nil, applicationError("browse.path_invalid")
	}
	parts := strings.Split(strings.TrimPrefix(root, "/"), "/")
	if relative != "" && relative != "." {
		parts = append(parts, strings.Split(relative, "/")...)
	}
	fd, e := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(fd, part, flags, 0)
		unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), path.Join(root, relative))
	info, e := f.Stat()
	if e != nil || (!info.Mode().IsRegular() && !info.IsDir()) {
		f.Close()
		return nil, applicationError("browse.file_kind_denied")
	}
	return f, nil
}

func (r ApplicationRuntime) browseRoot(ref string) (ApplicationBrowseRoot, *os.File, error) {
	x, e := parseApplicationBrowseRef(ref)
	if e != nil {
		return ApplicationBrowseRoot{}, nil, e
	}
	var record ApplicationBrowseRecord
	if e = r.Store.read("browse-"+x.Owner.Instance(), &record); e != nil {
		return ApplicationBrowseRoot{}, nil, applicationError("browse.private")
	}
	v, ok := record.Views[x.Key]
	if !ok || record.Owner != x.Owner || !v.View.Valid() {
		return ApplicationBrowseRoot{}, nil, applicationError("browse.private")
	}
	root := ApplicationBrowseRoot{Ref: ref, Label: v.View.Label, Node: x.Owner.NodeID, ProjectID: x.Owner.ProjectID, ProjectName: record.ProjectName, Application: x.Owner.Resource, DataKey: x.Key, Revision: record.Revision, Format: v.View.Format, ReadOnly: true}
	if root.Label == "" {
		root.Label = x.Key
	}
	if root.Format == "" {
		root.Format = "files"
	}
	p, e := r.policy()
	if e != nil {
		return root, nil, e
	}
	g, e := r.resolveGrant(p, x.Owner)
	if e != nil {
		return root, nil, e
	}
	policy, ok := g.Data[x.Key]
	id, exists := record.Data[x.Key]
	if !ok || !exists || v.BindingRef != policy.BindingRef {
		return root, nil, applicationError("browse.binding_changed")
	}
	idNow, e := applicationDataIdentityOwner(policy.Path, policy.Pool, id.UID, id.GID)
	if e != nil || !applicationSameDataIdentity(id, idNow) {
		return root, nil, applicationError("browse.custody_changed")
	}
	var installed ApplicationInstallation
	if r.Store.read("installation-"+x.Owner.Instance(), &installed) != nil || installed.Owner != x.Owner || !applicationSameDataIdentity(installed.Data[x.Key], id) {
		return root, nil, applicationError("browse.installation_unavailable")
	}
	root.SourcePath = filepath.Join(policy.Path, filepath.FromSlash(v.View.Subpath))
	f, e := applicationBrowseAllocationOpen(policy.Path, idNow, v.View.Subpath)
	if e != nil {
		return root, nil, e
	}
	var st unix.Stat_t
	if e = unix.Fstat(int(f.Fd()), &st); e != nil || !fModeDirectory(uint32(st.Mode)) || record.ViewInodes[x.Key].Inode != st.Ino || uint64(st.Dev) != idNow.Device {
		f.Close()
		return root, nil, applicationError("browse.custody_changed")
	}
	root.Available = true
	return root, f, nil
}
func fModeDirectory(mode uint32) bool { return mode&unix.S_IFMT == unix.S_IFDIR }

func applicationBrowseAllocationOpen(root string, identity ApplicationDataIdentity, relative string) (*os.File, error) {
	anchor, e := applicationBrowseOpen(root, "")
	if e != nil {
		return nil, e
	}
	defer anchor.Close()
	var st unix.Stat_t
	if unix.Fstat(int(anchor.Fd()), &st) != nil || st.Ino != identity.Inode || uint64(st.Dev) != identity.Device {
		return nil, applicationError("browse.custody_changed")
	}
	file, e := applicationBrowseOpenAt(anchor, relative)
	if e != nil {
		return nil, e
	}
	if unix.Fstat(int(file.Fd()), &st) != nil || uint64(st.Dev) != identity.Device {
		file.Close()
		return nil, applicationError("browse.custody_changed")
	}
	return file, nil
}

func (r ApplicationRuntime) BrowseRead(ctx context.Context, peer uint32, q ApplicationBrowseRead) (ApplicationBrowseResult, *os.File, error) {
	out := ApplicationBrowseResult{}
	p, e := r.policy()
	if e != nil {
		return out, nil, e
	}
	if peer != p.InstallerUID {
		return out, nil, applicationError("peer.denied")
	}
	if e = ctx.Err(); e != nil {
		return out, nil, e
	}
	if e = q.validateProjectSelection(); e != nil {
		return out, nil, e
	}
	if q.Operation == "catalog" {
		if q.Limit == 0 {
			q.Limit = 100
		}
		if q.Limit < 1 || q.Limit > 500 || q.Cursor < 0 {
			return out, nil, applicationError("browse.page_invalid")
		}
		if q.RestrictProjects && len(q.ProjectIDs) == 0 {
			out.Roots = []ApplicationBrowseRoot{}
			return out, nil, nil
		}
		dir, e := os.Open(r.Store.Root)
		if e != nil {
			return out, nil, e
		}
		defer dir.Close()
		names, e := dir.Readdirnames(8193)
		if e != nil && !errors.Is(e, io.EOF) {
			return out, nil, e
		}
		if len(names) > 8192 {
			return out, nil, applicationError("browse.catalog_limit")
		}
		sort.Strings(names)
		if q.RestrictProjects {
			// Scope owner records before applying cursor/limit. Foreign projects
			// cannot consume a page, expose cursor gaps or trigger custody opens.
			selected := []string{}
			for _, name := range names {
				if e = ctx.Err(); e != nil {
					return out, nil, e
				}
				if !strings.HasPrefix(name, "browse-la-") || !strings.HasSuffix(name, ".json") {
					continue
				}
				var record ApplicationBrowseRecord
				if e = r.Store.read(strings.TrimSuffix(name, ".json"), &record); e != nil {
					return out, nil, e
				}
				if q.permitsProject(record.Owner.ProjectID) {
					selected = append(selected, name)
				}
			}
			names = selected
		}
		out.Roots = []ApplicationBrowseRoot{}
		for i := q.Cursor; i < len(names); i++ {
			if e = ctx.Err(); e != nil {
				return out, nil, e
			}
			name := names[i]
			if !strings.HasPrefix(name, "browse-la-") || !strings.HasSuffix(name, ".json") {
				continue
			}
			var record ApplicationBrowseRecord
			if e = r.Store.read(strings.TrimSuffix(name, ".json"), &record); e != nil {
				return out, nil, e
			}
			if !q.permitsProject(record.Owner.ProjectID) {
				continue
			}
			keys := make([]string, 0, len(record.Views))
			for key := range record.Views {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			// An owner's small view set is returned together; never split a record.
			if len(out.Roots) > 0 && len(out.Roots)+len(keys) > q.Limit {
				out.NextCursor = i
				return out, nil, nil
			}
			for _, key := range keys {
				root, f, e := r.browseRoot(applicationBrowseRootRef(record.Owner, key))
				if f != nil {
					f.Close()
				}
				if e != nil {
					root.Available = false
					root.Reason = applicationSafeError(e)
				}
				if root.Ref != "" {
					out.Roots = append(out.Roots, root)
				}
			}
		}
		return out, nil, nil
	}
	projectID, e := ApplicationBrowseProjectID(q.Ref)
	if e != nil {
		return out, nil, e
	}
	if !q.permitsProject(projectID) {
		return out, nil, applicationError("browse.project_outside_selection")
	}
	root, anchor, e := r.browseRoot(q.Ref)
	if e != nil {
		return out, nil, e
	}
	defer anchor.Close()
	out.Root = &root
	if q.Revision != "" && q.Revision != root.Revision {
		return out, nil, applicationError("browse.view_changed")
	}
	if q.Operation == "resolve" {
		return out, nil, nil
	}
	if q.Operation != "open" && q.Operation != "children" {
		return out, nil, applicationError("browse.operation_invalid")
	}
	// Open relative to the held approved subdirectory, never re-resolve a path
	// after authority was checked. Every component rejects symlinks.
	file, e := applicationBrowseOpenAt(anchor, q.Path)
	if e != nil {
		return out, nil, e
	}
	if q.Operation == "open" {
		return out, file, nil
	}
	defer file.Close()
	if len(q.Names) > 500 {
		return out, nil, applicationError("browse.page_invalid")
	}
	out.Entries = []ApplicationBrowseEntry{}
	for _, name := range q.Names {
		if e = ctx.Err(); e != nil {
			return out, nil, e
		}
		if name == "." || !fs.ValidPath(name) || strings.ContainsAny(name, "/\\\x00") {
			return out, nil, applicationError("browse.path_invalid")
		}
		child, err := applicationBrowseOpenAt(file, name)
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) || err != nil && err.Error() == "application.browse.file_kind_denied" {
			continue
		}
		if err != nil {
			return out, nil, err
		}
		info, err := child.Stat()
		child.Close()
		if err != nil {
			return out, nil, err
		}
		kind := "file"
		if info.IsDir() {
			kind = "directory"
		}
		out.Entries = append(out.Entries, ApplicationBrowseEntry{Name: name, Path: path.Join(q.Path, name), Kind: kind, Size: info.Size(), ModifiedAt: info.ModTime().UTC()})
	}
	return out, nil, nil
}

func applicationBrowseOpenAt(anchor *os.File, relative string) (*os.File, error) {
	if relative == "" {
		relative = "."
	}
	if !fs.ValidPath(relative) || strings.ContainsAny(relative, "\\\x00") {
		return nil, applicationError("browse.path_invalid")
	}
	fd, e := unix.Openat(int(anchor.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	if relative != "." {
		for i, part := range strings.Split(relative, "/") {
			flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK | unix.O_NOFOLLOW
			if i < len(strings.Split(relative, "/"))-1 {
				flags |= unix.O_DIRECTORY
			}
			next, err := unix.Openat(fd, part, flags, 0)
			unix.Close(fd)
			if err != nil {
				return nil, err
			}
			fd = next
		}
	}
	f := os.NewFile(uintptr(fd), relative)
	info, e := f.Stat()
	if e != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
		f.Close()
		return nil, applicationError("browse.file_kind_denied")
	}
	return f, nil
}

// RuntimeDesiredDigest excludes operation receipts and presentation metadata.
func (q ApplicationRuntimeRequest) RuntimeDesiredDigest() string {
	q.Operation = "apply"
	q.OperationToken = ""
	q.ExpectedInstallationRevision = ""
	return applicationSHA(q)
}
