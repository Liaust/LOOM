package serviceregistry

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const ApplicationProjectStateStorageClass = "project_state"

// Generated once by the trusted provision planner after checking the registered
// project location against node Box policy. These observations are frozen grant
// inputs, not authority supplied by an application or a status-time rescan.
type ApplicationProjectStatePolicy struct {
	ProjectRoot              string                         `json:"project_root"`
	RelativePath             string                         `json:"relative_path"`
	Owner                    ApplicationOwner               `json:"owner"`
	ProjectUID               uint32                         `json:"project_uid"` // Existing state owner, not necessarily root owner.
	ProjectGID               uint32                         `json:"project_gid"` // State-directory group; existing files may differ.
	RootIdentity             ApplicationStatePathIdentity   `json:"root_identity"`
	DirectoryIdentity        ApplicationStatePathIdentity   `json:"directory_identity"`
	Files                    []ApplicationStateAdoptionFile `json:"files,omitempty"`
	NormalizePrivateDefaults bool                           `json:"normalize_private_defaults,omitempty"`
}

func applicationProjectStateOpen(path string) (*os.File, unix.Stat_t, error) {
	var st unix.Stat_t
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, st, applicationError("adoption.path_unavailable")
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return nil, st, applicationError("adoption.path_unavailable")
		}
		fd = next
	}
	if unix.Fstat(fd, &st) != nil {
		unix.Close(fd)
		return nil, st, applicationError("adoption.path_unavailable")
	}
	return os.NewFile(uintptr(fd), path), st, nil
}

// ObserveApplicationProjectState does not create files, read payload bytes,
// inspect credentials or invoke ACL tools. It observes at most 64 direct regular
// children within at most 128 direct entries, rejecting links and metadata/source
// paths. Owned private direct
// subdirectories are opaque: neither their ACLs nor contents are read or granted.
// Mutable parent custody/ACL approval belongs to the host, not this observer.
func ObserveApplicationProjectState(root, relative string, owner ApplicationOwner) (ApplicationProjectStatePolicy, error) {
	p := ApplicationProjectStatePolicy{ProjectRoot: root, RelativePath: relative, Owner: owner}
	if !applicationAbsolutePath(root) || !applicationAdoptionRelative(relative) || !owner.valid() || len(strings.Split(root+"/"+relative, "/")) > 64 {
		return p, applicationError("adoption.request_invalid")
	}
	rootFile, rootStat, err := applicationProjectStateOpen(root)
	if err != nil {
		return p, err
	}
	defer rootFile.Close()
	p.RootIdentity = (applicationAdoptionHandle{stat: rootStat}).identity()
	statePath := filepath.Join(root, relative)
	state, st, err := applicationProjectStateOpen(statePath)
	if err != nil {
		return p, err
	}
	defer state.Close()
	p.ProjectUID, p.ProjectGID = st.Uid, st.Gid
	p.DirectoryIdentity = (applicationAdoptionHandle{stat: st}).identity()
	if st.Uid == 0 || st.Mode&07002 != 0 || st.Dev != rootStat.Dev {
		return p, applicationError("adoption.path_custody")
	}
	entries, err := state.ReadDir(129)
	if err != nil && err != io.EOF {
		return p, applicationError("adoption.file_unavailable")
	}
	if len(entries) > 128 {
		return p, applicationError("adoption.entry_limit")
	}
	for _, entry := range entries {
		if !applicationAdoptionRelative(entry.Name()) {
			return p, applicationError("adoption.file_invalid")
		}
		fd, e := unix.Openat(int(state.Fd()), entry.Name(), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if e != nil {
			return p, applicationError("adoption.file_unavailable")
		}
		e = unix.Fstat(fd, &st)
		unix.Close(fd)
		if e != nil || st.Mode&07002 != 0 || st.Uid != p.ProjectUID || st.Dev != rootStat.Dev {
			return p, applicationError("adoption.file_custody")
		}
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			if st.Mode&0077 != 0 {
				return p, applicationError("adoption.opaque_directory_custody")
			}
			continue
		}
		if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
			return p, applicationError("adoption.file_custody")
		}
		p.Files = append(p.Files, ApplicationStateAdoptionFile{Name: entry.Name(), Expected: (applicationAdoptionHandle{stat: st}).identity()})
		if len(p.Files) > 64 {
			return p, applicationError("adoption.file_limit")
		}
	}
	sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Name < p.Files[j].Name })
	// A second descriptor walk fences root/state replacement during discovery.
	for path, want := range map[string]ApplicationStatePathIdentity{root: p.RootIdentity, statePath: p.DirectoryIdentity} {
		f, now, e := applicationProjectStateOpen(path)
		if e != nil {
			return p, e
		}
		f.Close()
		if (applicationAdoptionHandle{stat: now}).identity() != want {
			return p, applicationError("adoption.path_changed")
		}
	}
	for _, item := range p.Files {
		fd, e := unix.Openat(int(state.Fd()), item.Name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if e != nil {
			return p, applicationError("adoption.file_unavailable")
		}
		e = unix.Fstat(fd, &st)
		unix.Close(fd)
		if e != nil || (applicationAdoptionHandle{stat: st}).identity() != item.Expected || st.Nlink != 1 {
			return p, applicationError("adoption.identity_changed")
		}
	}
	return p, nil
}

type ApplicationStateAdoptionMode string

const (
	ApplicationStateAdopt      ApplicationStateAdoptionMode = "adopt"
	ApplicationStateInitialize ApplicationStateAdoptionMode = "initialize"
)

type ApplicationStatePathIdentity struct {
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	UID       uint32 `json:"uid"`
	GID       uint32 `json:"gid"`
	Directory bool   `json:"directory"`
}

type ApplicationStateAdoptionFile struct {
	Name     string                       `json:"name"`
	Expected ApplicationStatePathIdentity `json:"expected"`
}

// ProjectRoot must come from the exact registered project, not request input.
// Expected is required for adoption and forbidden for initialization. Files is
// an explicit allowlist of existing direct children (e.g. SQLite DB/WAL/SHM),
// never a recursive scan. ApplicationGroups must include all runtime groups.
type ApplicationStateAdoptionRequest struct {
	ProjectRoot       string                         `json:"project_root"`
	Directory         string                         `json:"directory"`
	ProjectUID        uint32                         `json:"project_uid"`
	ProjectGID        uint32                         `json:"project_gid"`
	ApplicationUID    uint32                         `json:"application_uid"`
	ApplicationGroups []uint32                       `json:"application_groups"`
	Mode              ApplicationStateAdoptionMode   `json:"mode"`
	Expected          *ApplicationStatePathIdentity  `json:"expected,omitempty"`
	Files             []ApplicationStateAdoptionFile `json:"files,omitempty"`
	RootIdentity      ApplicationStatePathIdentity   `json:"root_identity"`
	// Host-derived exact paths only. These are not caller-supplied grants.
	AllowedParentOwners map[string]uint32 `json:"allowed_parent_owners,omitempty"`
	AllowedParentGroups map[string]uint32 `json:"allowed_parent_groups,omitempty"`
	// Explicit node-policy transition: preserve current effective access while
	// replacing only managed state defaults with private owner/app defaults.
	NormalizePrivateDefaults bool `json:"normalize_private_defaults,omitempty"`
}

type ApplicationStateACLChange struct {
	Path     string                       `json:"path"`
	Role     string                       `json:"role"`
	Identity ApplicationStatePathIdentity `json:"identity"`
	Before   string                       `json:"before"`
	After    string                       `json:"after"`
}

// This is a private effect-journal payload, not a public browse grant. Identity
// composes the existing allocated-data identity: Mount/PoolInode refer to the
// registered project root. Pool custody validators deliberately remain intact.
type ApplicationStateAdoptionRecord struct {
	StorageClass string                          `json:"storage_class"`
	Request      ApplicationStateAdoptionRequest `json:"request"`
	Identity     ApplicationDataIdentity         `json:"identity"`
	Created      bool                            `json:"created"`
	Changes      []ApplicationStateACLChange     `json:"changes"`
}

type ApplicationStateACLTools struct {
	Getfacl string
	Setfacl string
}

// The root helper injects fixed, custody-verified executables and a direct exec
// runner with bounded output and LANG=C/LC_ALL=C, no POSIXLY_CORRECT, no shell.
// Targets are /proc/<this-process>/fd/<held-fd>, not mutable source pathnames.
// The runner must run locally, as the helper, while those descriptors are held.
type ApplicationStateACLCommandRunner func(context.Context, string, ...string) ([]byte, error)

// The caller owns cross-process project/application locking and quiesces ALL
// competing writers (including the service and named actions) until completion.
// Fence verifies that held authority; it must remain effective on error too.
type ApplicationStateAdoptionFence func(context.Context) error

// Persist must durably save each intent BEFORE a filesystem/ACL effect in the
// root helper's trusted journal. On any error the returned record still belongs
// there. A crash during initialization can leave an unclaimed empty directory;
// it must be inspected, never automatically deleted or claimed on retry.
type ApplicationStateAdoptionJournal func(ApplicationStateAdoptionRecord) error

type ApplicationStateAdopter struct {
	tools ApplicationStateACLTools
	run   ApplicationStateACLCommandRunner
	fence ApplicationStateAdoptionFence
	mu    sync.Mutex
}

func NewApplicationStateAdopter(tools ApplicationStateACLTools, run ApplicationStateACLCommandRunner, fence ApplicationStateAdoptionFence) (*ApplicationStateAdopter, error) {
	if !applicationAbsolutePath(tools.Getfacl) || filepath.Base(tools.Getfacl) != "getfacl" || !applicationAbsolutePath(tools.Setfacl) || filepath.Base(tools.Setfacl) != "setfacl" || run == nil || fence == nil {
		return nil, applicationError("adoption.backend_invalid")
	}
	return &ApplicationStateAdopter{tools: tools, run: run, fence: fence}, nil
}

func applicationAdoptionRelative(path string) bool {
	if !fs.ValidPath(path) || path == "." || len(path) > 1024 || strings.ContainsAny(path, "\\\x00\r\n\t") || len(strings.Split(path, "/")) > 16 {
		return false
	}
	for _, p := range strings.Split(path, "/") {
		switch strings.ToLower(p) {
		case ".project", ".loom", ".git", ".github", ".gitlab", ".gitignore", ".gitattributes", ".gitmodules", ".hg", ".svn",
			"src", "source", "sources", "code", "repo", "repos", "repository", "repositories", ".repo", ".repos", "worktrees", ".worktrees",
			"tools", ".tools", ".codex", ".agents", ".claude", ".cursor", ".idea", ".vscode", "node_modules", "venv", ".venv", ".direnv", ".devenv", ".nix":
			return false
		}
	}
	return true
}

func applicationAdoptionValidate(q ApplicationStateAdoptionRequest) error {
	if !applicationAbsolutePath(q.ProjectRoot) || q.ProjectRoot == "/" || len(q.ProjectRoot) > 4096 || strings.ContainsAny(q.ProjectRoot, "\r\n") || !applicationAdoptionRelative(q.Directory) || q.ProjectUID == 0 || q.ApplicationUID == 0 || q.ProjectUID == q.ApplicationUID || len(q.Files) > 64 || len(q.ApplicationGroups) > 64 {
		return applicationError("adoption.request_invalid")
	}
	if q.RootIdentity.Inode == 0 || !q.RootIdentity.Directory || len(q.AllowedParentOwners) > 64 || len(q.AllowedParentGroups) > 64 {
		return applicationError("adoption.root_identity_required")
	}
	for path := range q.AllowedParentOwners {
		if !applicationAbsolutePath(path) || !(path == q.ProjectRoot || strings.HasPrefix(q.ProjectRoot+"/"+q.Directory, path+"/")) {
			return applicationError("adoption.parent_policy_invalid")
		}
	}
	for path := range q.AllowedParentGroups {
		if !applicationAbsolutePath(path) || !(path == q.ProjectRoot || strings.HasPrefix(q.ProjectRoot+"/"+q.Directory, path+"/")) {
			return applicationError("adoption.parent_policy_invalid")
		}
	}
	switch q.Mode {
	case ApplicationStateAdopt:
		if q.Expected == nil || q.Expected.Inode == 0 || !q.Expected.Directory || q.Expected.UID != q.ProjectUID || q.Expected.GID != q.ProjectGID {
			return applicationError("adoption.expected_identity_required")
		}
	case ApplicationStateInitialize:
		if q.Expected != nil || len(q.Files) != 0 {
			return applicationError("adoption.initialize_conflict")
		}
	default:
		return applicationError("adoption.mode_required")
	}
	seen := map[string]bool{}
	for _, f := range q.Files {
		if !applicationAdoptionRelative(f.Name) || strings.Contains(f.Name, "/") || seen[f.Name] || f.Expected.Inode == 0 || f.Expected.Directory || f.Expected.UID != q.ProjectUID {
			return applicationError("adoption.file_invalid")
		}
		seen[f.Name] = true
	}
	return nil
}

type applicationAdoptionHandle struct {
	file *os.File
	path string
	stat unix.Stat_t
}

func (h applicationAdoptionHandle) identity() ApplicationStatePathIdentity {
	return ApplicationStatePathIdentity{Device: uint64(h.stat.Dev), Inode: h.stat.Ino, UID: h.stat.Uid, GID: h.stat.Gid, Directory: h.stat.Mode&unix.S_IFMT == unix.S_IFDIR}
}

func (h applicationAdoptionHandle) target() string {
	return fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), h.file.Fd())
}

func applicationAdoptionClose(handles []applicationAdoptionHandle) {
	for _, h := range handles {
		h.file.Close()
	}
}

// Walk from / with no-follow openat; retain every ancestor for identity fences.
// Parent custody uses exact host-approved owners/groups, including editable
// project parents. State remains private. Sticky system parents may be traversed
// but never modified. Cross-device project state is refused.
func applicationAdoptionOpen(q ApplicationStateAdoptionRequest, missing bool) (handles []applicationAdoptionHandle, err error) {
	defer func() {
		if err != nil {
			applicationAdoptionClose(handles)
			handles = nil
		}
	}()
	path := filepath.Join(q.ProjectRoot, q.Directory)
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, applicationError("adoption.path_unavailable")
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil {
		unix.Close(fd)
		return nil, applicationError("adoption.path_unavailable")
	}
	handles = append(handles, applicationAdoptionHandle{os.NewFile(uintptr(fd), "/"), "/", st})
	rootDevice := uint64(0)
	current := ""
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		current += "/" + part
		if len(handles) >= 64 {
			return handles, applicationError("adoption.path_limit")
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if e != nil {
			if missing && current == path && e == unix.ENOENT {
				return handles, nil
			}
			return handles, applicationError("adoption.path_unavailable")
		}
		fd = next
		f := os.NewFile(uintptr(fd), current)
		if unix.Fstat(fd, &st) != nil {
			f.Close()
			return handles, applicationError("adoption.path_unavailable")
		}
		handles = append(handles, applicationAdoptionHandle{f, current, st})
		inProject := current == q.ProjectRoot || strings.HasPrefix(current, q.ProjectRoot+"/")
		sticky := !inProject && st.Uid == 0 && st.Mode&unix.S_ISVTX != 0
		state := current == path
		ownerOK := st.Uid == 0 || st.Uid == q.ProjectUID
		if owner, ok := q.AllowedParentOwners[current]; !state && ok {
			ownerOK = st.Uid == owner
		}
		group, groupApproved := q.AllowedParentGroups[current]
		if !state && groupApproved && st.Gid != group {
			return handles, applicationError("adoption.parent_group_changed")
		}
		trustedSetgid := !state && groupApproved && st.Gid == group && st.Mode&07000 == unix.S_ISGID
		// Group mode bits may be an ACL mask. Shared editable parents are
		// allowed only by exact host owner/group policy, never for state.
		if !ownerOK || (st.Mode&0002 != 0 && !sticky) || (st.Mode&07000 != 0 && !sticky && !trustedSetgid) || (state && (st.Uid != q.ProjectUID || st.Gid != q.ProjectGID)) {
			return handles, applicationError("adoption.path_custody")
		}
		if current == q.ProjectRoot {
			if handles[len(handles)-1].identity() != q.RootIdentity {
				return handles, applicationError("adoption.root_identity_changed")
			}
			rootDevice = uint64(st.Dev)
		} else if inProject {
			if uint64(st.Dev) != rootDevice {
				return handles, applicationError("adoption.filesystem_mismatch")
			}
			for _, marker := range []string{".loom", ".git"} {
				var markerStat unix.Stat_t
				e := unix.Fstatat(fd, marker, &markerStat, unix.AT_SYMLINK_NOFOLLOW)
				if e != unix.ENOENT {
					return handles, applicationError("adoption.nested_project")
				}
			}
		}
	}
	if missing {
		return handles, applicationError("adoption.initialize_exists")
	}
	dir := handles[len(handles)-1]
	if q.Expected == nil || dir.identity() != *q.Expected {
		return handles, applicationError("adoption.identity_changed")
	}
	for _, item := range q.Files {
		fd, e := unix.Openat(int(dir.file.Fd()), item.Name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if e != nil {
			return handles, applicationError("adoption.file_unavailable")
		}
		f := os.NewFile(uintptr(fd), filepath.Join(path, item.Name))
		if unix.Fstat(fd, &st) != nil {
			f.Close()
			return handles, applicationError("adoption.file_unavailable")
		}
		h := applicationAdoptionHandle{f, f.Name(), st}
		handles = append(handles, h)
		if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07002 != 0 || st.Nlink != 1 || uint64(st.Dev) != rootDevice || h.identity() != item.Expected {
			return handles, applicationError("adoption.file_custody")
		}
	}
	return handles, nil
}

func (a *ApplicationStateAdopter) readACL(ctx context.Context, h applicationAdoptionHandle) (string, error) {
	out, err := a.run(ctx, a.tools.Getfacl, "--omit-header", "--numeric", "--no-effective", "--absolute-names", "--", h.target())
	if err != nil {
		return "", applicationError("adoption.acl_read_failed")
	}
	acl, err := applicationAdoptionParseACL(string(out), h.identity().Directory)
	if err != nil {
		return "", err
	}
	var st unix.Stat_t
	if unix.Fstat(int(h.file.Fd()), &st) != nil || acl.mode() != uint32(st.Mode)&0777 {
		return "", applicationError("adoption.acl_mode_conflict")
	}
	return acl.text(), nil
}

func applicationAdoptionACLCustody(raw string, h applicationAdoptionHandle, q ApplicationStateAdoptionRequest) error {
	acl, err := applicationAdoptionParseACL(raw, h.identity().Directory)
	if err != nil {
		return err
	}
	if h.stat.Uid == 0 && h.stat.Mode&unix.S_ISVTX != 0 {
		return nil
	}
	parent := h.identity().Directory && h.path != filepath.Join(q.ProjectRoot, q.Directory)
	parentGroup, sharedParent := q.AllowedParentGroups[h.path]
	sharedParent = parent && sharedParent && h.stat.Gid == parentGroup
	for _, prefix := range []string{"", "default:"} {
		if prefix == "default:" && !parent && h.identity().Directory && q.NormalizePrivateDefaults {
			// This explicit transition tightens future defaults; access ACLs
			// are still checked for currently effective foreign writers.
			continue
		}
		mask := uint8(7)
		if m, ok := acl[prefix+"mask:"]; ok {
			mask = m
		}
		for k, p := range acl {
			if strings.HasPrefix(k, "default:") != (prefix != "") {
				continue
			}
			tag := strings.TrimPrefix(k, prefix)
			groupWriter := strings.HasPrefix(tag, "group:") && p&mask&2 != 0
			// A host-approved shared parent may already expose the registered
			// project's group through a named ACL (Box's owning group can
			// differ). Preserve those editors, never an unknown writer group.
			trustedGroup := sharedParent && (tag == "group:" || tag == fmt.Sprintf("group:%d", parentGroup) || tag == fmt.Sprintf("group:%d", q.ProjectGID))
			if tag == "other:" && p&2 != 0 || groupWriter && !trustedGroup {
				return applicationError("adoption.acl_foreign_writer")
			}
			// Exact host-approved editable parents retain their existing named
			// project editors. They do not gain any application read/write.
			if strings.HasPrefix(tag, "user:") && tag != "user:" && tag != fmt.Sprintf("user:%d", q.ProjectUID) && tag != fmt.Sprintf("user:%d", q.RootIdentity.UID) && tag != fmt.Sprintf("user:%d", q.ApplicationUID) && p&mask&2 != 0 && !sharedParent {
				return applicationError("adoption.acl_foreign_writer")
			}
		}
	}
	return nil
}

func (a *ApplicationStateAdopter) setACL(ctx context.Context, h applicationAdoptionHandle, acl string) error {
	parsed, err := applicationAdoptionParseACL(acl, h.identity().Directory)
	if err != nil || parsed.text() != acl {
		return applicationError("adoption.acl_invalid")
	}
	args := []string{"--no-mask"}
	if h.identity().Directory {
		args = append(args, "--remove-default")
	}
	args = append(args, "--set="+strings.ReplaceAll(strings.TrimSuffix(acl, "\n"), "\n", ","), "--", h.target())
	out, err := a.run(ctx, a.tools.Setfacl, args...)
	if err != nil || len(out) > 16384 {
		return applicationError("adoption.acl_write_failed")
	}
	return nil
}

func applicationAdoptionIdentity(handles []applicationAdoptionHandle, q ApplicationStateAdoptionRequest) (ApplicationDataIdentity, error) {
	var root, dir *applicationAdoptionHandle
	for i := range handles {
		h := &handles[i]
		if h.path == q.ProjectRoot {
			root = h
		}
		if h.path == filepath.Join(q.ProjectRoot, q.Directory) {
			dir = h
		}
	}
	var fs unix.Statfs_t
	if root == nil || dir == nil || unix.Fstatfs(int(dir.file.Fd()), &fs) != nil {
		return ApplicationDataIdentity{}, applicationError("adoption.filesystem_unavailable")
	}
	mount, _ := applicationMountID(dir.path)
	return ApplicationDataIdentity{Filesystem: fmt.Sprintf("%x:%v", fs.Type, fs.Fsid), PoolInode: root.stat.Ino, MountID: mount, Path: dir.path, Device: uint64(dir.stat.Dev), Inode: dir.stat.Ino, UID: dir.stat.Uid, GID: dir.stat.Gid, Mount: root.path}, nil
}

func applicationAdoptionSamePaths(handles []applicationAdoptionHandle, changes []ApplicationStateACLChange) bool {
	if len(handles) != len(changes) {
		return false
	}
	for i, h := range handles {
		if h.path != changes[i].Path || h.identity() != changes[i].Identity {
			return false
		}
	}
	return true
}

func applicationAdoptionClone(r ApplicationStateAdoptionRecord) ApplicationStateAdoptionRecord {
	r.Request.Files = append([]ApplicationStateAdoptionFile(nil), r.Request.Files...)
	r.Request.ApplicationGroups = append([]uint32(nil), r.Request.ApplicationGroups...)
	r.Request.AllowedParentOwners = maps.Clone(r.Request.AllowedParentOwners)
	r.Request.AllowedParentGroups = maps.Clone(r.Request.AllowedParentGroups)
	if r.Request.Expected != nil {
		id := *r.Request.Expected
		r.Request.Expected = &id
	}
	r.Changes = append([]ApplicationStateACLChange(nil), r.Changes...)
	return r
}

// Adopt changes ACLs only, never file bytes, names or existing ownership. It
// returns the full before/after journal even on partial failure. Initialization
// creates only the final empty directory explicitly; it NEVER creates a DB.
func (a *ApplicationStateAdopter) Adopt(ctx context.Context, q ApplicationStateAdoptionRequest, persist ApplicationStateAdoptionJournal) (record ApplicationStateAdoptionRecord, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err = applicationAdoptionValidate(q); err != nil {
		return record, err
	}
	if persist == nil {
		return record, applicationError("adoption.journal_required")
	}
	q = applicationAdoptionClone(ApplicationStateAdoptionRecord{Request: q}).Request
	save := persist
	persist = func(r ApplicationStateAdoptionRecord) error { return save(applicationAdoptionClone(r)) }
	if err = a.fence(ctx); err != nil {
		return record, applicationError("adoption.writer_not_quiesced")
	}
	record = ApplicationStateAdoptionRecord{StorageClass: ApplicationProjectStateStorageClass, Request: q}
	if q.Mode == ApplicationStateInitialize {
		if err = a.initialize(ctx, &record, persist); err != nil {
			return record, err
		}
		q = record.Request
	}
	handles, err := applicationAdoptionOpen(q, false)
	if err != nil {
		return record, err
	}
	defer applicationAdoptionClose(handles)
	if record.Identity, err = applicationAdoptionIdentity(handles, q); err != nil {
		return record, err
	}
	for _, h := range handles {
		role := "parent"
		if h.path == record.Identity.Path {
			role = "state"
		} else if !h.identity().Directory {
			role = "file"
		}
		before, e := a.readACL(ctx, h)
		if e != nil {
			return record, e
		}
		if e = applicationAdoptionACLCustody(before, h, q); e != nil {
			return record, e
		}
		after, e := applicationAdoptionDesiredACL(before, role, h.identity(), q)
		if e != nil {
			return record, e
		}
		if role == "parent" && h.stat.Mode&unix.S_ISVTX != 0 && before != after {
			return record, applicationError("adoption.shared_parent_traversal")
		}
		record.Changes = append(record.Changes, ApplicationStateACLChange{h.path, role, h.identity(), before, after})
	}
	if err = persist(record); err != nil {
		return record, err
	}
	for i, h := range handles {
		c := record.Changes[i]
		if c.Before == c.After {
			continue
		}
		if err = a.fence(ctx); err != nil {
			return record, applicationError("adoption.writer_not_quiesced")
		}
		if err = a.checkRecord(ctx, record, false); err != nil {
			return record, err
		}
		current, e := a.readACL(ctx, h)
		if e != nil || current != c.Before {
			return record, applicationError("adoption.acl_changed")
		}
		if err = a.setACL(ctx, h, c.After); err != nil {
			return record, err
		}
		current, e = a.readACL(ctx, h)
		if e != nil || current != c.After {
			return record, applicationError("adoption.acl_verification_failed")
		}
	}
	if err = a.checkRecord(ctx, record, true); err != nil {
		return record, err
	}
	return record, nil
}

func (a *ApplicationStateAdopter) initialize(ctx context.Context, record *ApplicationStateAdoptionRecord, persist ApplicationStateAdoptionJournal) error {
	q := record.Request
	handles, err := applicationAdoptionOpen(q, true)
	if err != nil {
		return err
	}
	defer applicationAdoptionClose(handles)
	parent := handles[len(handles)-1]
	if err = persist(*record); err != nil {
		return err
	}
	if err = a.fence(ctx); err != nil {
		return applicationError("adoption.writer_not_quiesced")
	}
	fresh, err := applicationAdoptionOpen(q, true)
	if err != nil {
		return err
	}
	defer applicationAdoptionClose(fresh)
	if len(fresh) != len(handles) {
		return applicationError("adoption.path_changed")
	}
	for i := range fresh {
		if fresh[i].identity() != handles[i].identity() {
			return applicationError("adoption.path_changed")
		}
	}
	name := filepath.Base(q.Directory)
	if unix.Mkdirat(int(parent.file.Fd()), name, 0700) != nil {
		return applicationError("adoption.initialize_failed")
	}
	record.Created = true
	fd, err := unix.Openat(int(parent.file.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return applicationError("adoption.initialize_failed")
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil {
		return applicationError("adoption.initialize_failed")
	}
	// Only the directory just created by explicit initialize may change owner.
	if st.Uid != q.ProjectUID || st.Gid != q.ProjectGID {
		if unix.Fchown(fd, int(q.ProjectUID), int(q.ProjectGID)) != nil || unix.Fstat(fd, &st) != nil {
			return applicationError("adoption.initialize_owner_failed")
		}
	}
	h := applicationAdoptionHandle{stat: st}
	id := h.identity()
	record.Request.Mode, record.Request.Expected = ApplicationStateAdopt, &id
	if unix.Fsync(fd) != nil || unix.Fsync(int(parent.file.Fd())) != nil {
		return applicationError("adoption.initialize_sync_failed")
	}
	return persist(*record)
}

// Check is the start/restart fence: it reopens the entire no-symlink chain,
// validates the composed data identity and exact observed ACLs, without effects.
func (a *ApplicationStateAdopter) Check(ctx context.Context, record ApplicationStateAdoptionRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.checkRecord(ctx, record, true)
}

func (a *ApplicationStateAdopter) checkRecord(ctx context.Context, r ApplicationStateAdoptionRecord, after bool) error {
	if r.StorageClass != ApplicationProjectStateStorageClass || r.Request.Mode != ApplicationStateAdopt || applicationAdoptionValidate(r.Request) != nil {
		return applicationError("adoption.record_invalid")
	}
	handles, err := applicationAdoptionOpen(r.Request, false)
	if err != nil {
		return err
	}
	defer applicationAdoptionClose(handles)
	id, err := applicationAdoptionIdentity(handles, r.Request)
	if err != nil || !applicationSameDataIdentity(id, r.Identity) || !applicationAdoptionSamePaths(handles, r.Changes) {
		return applicationError("adoption.record_identity_changed")
	}
	for i, h := range handles {
		c := r.Changes[i]
		role := "parent"
		if h.path == id.Path {
			role = "state"
		} else if !h.identity().Directory {
			role = "file"
		}
		wanted, e := applicationAdoptionDesiredACL(c.Before, role, h.identity(), r.Request)
		if e != nil || wanted != c.After || c.Role != role {
			return applicationError("adoption.record_invalid")
		}
		current, e := a.readACL(ctx, h)
		if e != nil || (current != c.After && (after || current != c.Before)) {
			return applicationError("adoption.acl_changed")
		}
		if e = applicationAdoptionACLCustody(current, h, r.Request); e != nil {
			return e
		}
	}
	// Reopen after the ACL observations too: a runner/read must not hide a
	// replacement behind the original held descriptors at a start fence.
	fresh, err := applicationAdoptionOpen(r.Request, false)
	if err != nil {
		return err
	}
	defer applicationAdoptionClose(fresh)
	if !applicationAdoptionSamePaths(fresh, r.Changes) {
		return applicationError("adoption.path_changed")
	}
	return nil
}

// Rollback is conservative whole-snapshot CAS, not --remove-all or chown. It
// accepts already-restored entries for retry, but refuses any unrelated drift
// before changing anything. New directories and all data are always retained.
func (a *ApplicationStateAdopter) Rollback(ctx context.Context, record ApplicationStateAdoptionRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.fence(ctx); err != nil {
		return applicationError("adoption.writer_not_quiesced")
	}
	if err := a.checkRecord(ctx, record, false); err != nil {
		return err
	}
	handles, err := applicationAdoptionOpen(record.Request, false)
	if err != nil {
		return err
	}
	defer applicationAdoptionClose(handles)
	for i := len(handles) - 1; i >= 0; i-- {
		c, h := record.Changes[i], handles[i]
		if c.Before == c.After {
			continue
		}
		if err = a.fence(ctx); err != nil {
			return applicationError("adoption.writer_not_quiesced")
		}
		if err = a.checkRecord(ctx, record, false); err != nil {
			return err
		}
		current, e := a.readACL(ctx, h)
		if e != nil {
			return e
		}
		if current == c.Before {
			continue
		}
		if current != c.After {
			return applicationError("adoption.acl_changed")
		}
		if err = a.setACL(ctx, h, c.Before); err != nil {
			return err
		}
		current, e = a.readACL(ctx, h)
		if e != nil || current != c.Before {
			return applicationError("adoption.acl_verification_failed")
		}
	}
	return a.checkRecord(ctx, record, false)
}
