package loomdapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/notesworkspace"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/workspacefiles"
)

const workspacePathNotesSchema = "notes_workspace.path_operation.v1"

type workspacePathNotesPlan struct {
	Schema   string                                  `json:"schema"`
	Kind     string                                  `json:"kind"`
	Location *notesworkspace.TreeMove                `json:"location,omitempty"`
	Retained *notesworkspace.CollectionRetentionPlan `json:"retained,omitempty"`
}

type workspacePathNotesAdapter struct {
	db    *sql.DB
	files *workspacefiles.Service
	roots storagearchive.TrustedWorkspaceRoots
	node  string
}

type workspacePathNotesOwner struct {
	adapter *workspacePathNotesAdapter
	store   notesworkspace.SQLStore
	box     notesworkspace.Source
	active  bool
}

// Construction has no effects. The storage owner supplies configured roots and
// retains its project/custody locks; CommitNotes joins its final transaction.
func workspacePathNotesRuntime(db *sql.DB, files *workspacefiles.Service, roots storagearchive.TrustedWorkspaceRoots, nodeKey string) (
	withNotesLock func(context.Context, func(storagearchive.WorkspacePathNotesOwner) error) error,
	commitNotes func(context.Context, *sql.Tx, storagearchive.WorkspacePathPlan) error,
) {
	a := &workspacePathNotesAdapter{db: db, files: files, roots: roots, node: nodeKey}
	return a.withLock, a.commit
}

func (a *workspacePathNotesAdapter) configured() error {
	if a.db == nil || a.files == nil || a.node == "" || strings.TrimSpace(a.node) != a.node {
		return notesworkspace.ErrMembership
	}
	for _, root := range []string{a.roots.BoxRoot, a.roots.StorageRoot} {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) || strings.ContainsRune(root, 0) {
			return notesworkspace.ErrUnsafePath
		}
	}
	if withinWorkspacePath(a.roots.BoxRoot, a.roots.StorageRoot) || withinWorkspacePath(a.roots.StorageRoot, a.roots.BoxRoot) {
		return notesworkspace.ErrUnsafePath
	}
	return nil
}

func withinWorkspacePath(value, root string) bool {
	return value == root || strings.HasPrefix(value, root+string(filepath.Separator))
}

func (a *workspacePathNotesAdapter) boxSource(ctx context.Context) (notesworkspace.Source, error) {
	source, err := a.files.MutationSource(ctx, a.roots.BoxRoot, "")
	if err != nil {
		return source, err
	}
	var st unix.Stat_t
	nodeIdentity := a.node
	if a.roots.BoxID != "" && !a.roots.Primary {
		nodeIdentity += ":box:" + a.roots.BoxID
	}
	if source.Path != a.roots.BoxRoot || !strings.HasPrefix(source.CollectionID, "filesystem_box_") || unix.Lstat(a.roots.BoxRoot, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || source.Generation != notesworkspace.FilesystemGeneration(source.CollectionID, nodeIdentity, source.Path, uint64(st.Dev), st.Ino) {
		return notesworkspace.Source{}, notesworkspace.ErrMembership
	}
	return source, nil
}

func (a *workspacePathNotesAdapter) retainedScope() (notesworkspace.SourceScope, error) {
	var st unix.Stat_t
	if err := unix.Lstat(a.roots.StorageRoot, &st); err != nil {
		return notesworkspace.SourceScope{}, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return notesworkspace.SourceScope{}, notesworkspace.ErrUnsafePath
	}
	// Stable across moves and inode replacement; generation drift is refused by
	// EnsureRetainedScope rather than silently creating another custody identity.
	id := fmt.Sprintf("filesystem_retained_%x", sha256.Sum256([]byte(a.node+"\x00"+a.roots.StorageRoot)))
	return notesworkspace.SourceScope{ID: id, Path: a.roots.StorageRoot, NodeKey: a.node,
		RootIdentity: notesworkspace.FilesystemGeneration(id, a.node, a.roots.StorageRoot, uint64(st.Dev), st.Ino)}, nil
}

func (a *workspacePathNotesAdapter) withLock(ctx context.Context, fn func(storagearchive.WorkspacePathNotesOwner) error) error {
	if err := a.configured(); err != nil {
		return err
	}
	if fn == nil {
		return notesworkspace.ErrMembership
	}
	store := notesworkspace.SQLStore{DB: a.db, ExclusiveSource: true}
	return store.WithLock(ctx, "workspace-path", func(raw notesworkspace.Store) error {
		locked, ok := raw.(notesworkspace.SQLStore)
		if !ok {
			return notesworkspace.ErrMembership
		}
		box, err := a.boxSource(ctx)
		if err != nil {
			return err
		}
		if err = locked.EnsureBoxScope(ctx, notesworkspace.SourceScope{ID: box.CollectionID, Path: box.Path, NodeKey: a.node, RootIdentity: box.Generation}); err != nil {
			return err
		}
		owner := &workspacePathNotesOwner{adapter: a, store: locked, box: box, active: true}
		defer func() { owner.active = false }()
		return fn(owner)
	})
}

func (a *workspacePathNotesAdapter) validatePlan(p storagearchive.WorkspacePathPlan) error {
	if p.Schema != storagearchive.WorkspacePathSchema || p.ProjectID == "" || p.Move.Schema != storagearchive.RetainedRelocationSchema || p.Move.ID == "" || len(p.Move.ID) > 160 || strings.TrimSpace(p.Move.ID) != p.Move.ID || strings.ContainsAny(p.Move.ID, "\x00\r\n") || p.Move.Source.Root != storagearchive.WorkspaceRootBox {
		return notesworkspace.ErrIntentMismatch
	}
	switch p.Kind {
	case "relocate_project":
		if p.Move.Destination.Root != storagearchive.WorkspaceRootBox {
			return notesworkspace.ErrIntentMismatch
		}
	case "retain_child":
		if p.Move.Destination.Root != storagearchive.WorkspaceRootStorage {
			return notesworkspace.ErrIntentMismatch
		}
	default:
		return notesworkspace.ErrIntentMismatch
	}
	for _, ref := range []storagearchive.WorkspacePathRef{p.Move.Source, p.Move.Destination} {
		root := a.roots.BoxRoot
		if ref.Root == storagearchive.WorkspaceRootStorage {
			root = a.roots.StorageRoot
		}
		if !fs.ValidPath(ref.RelativePath) || ref.RelativePath == "." || len(ref.RelativePath) > 4096 || strings.ContainsAny(ref.RelativePath, "\\\x00") || ref.AbsolutePath != filepath.Join(root, filepath.FromSlash(ref.RelativePath)) {
			return notesworkspace.ErrUnsafePath
		}
	}
	if withinWorkspacePath(p.Move.Source.AbsolutePath, p.Move.Destination.AbsolutePath) || withinWorkspacePath(p.Move.Destination.AbsolutePath, p.Move.Source.AbsolutePath) {
		return notesworkspace.ErrUnsafePath
	}
	return nil
}

func (o *workspacePathNotesOwner) Plan(ctx context.Context, p storagearchive.WorkspacePathPlan) (json.RawMessage, error) {
	if !o.active {
		return nil, notesworkspace.ErrMembership
	}
	if err := o.adapter.validatePlan(p); err != nil {
		return nil, err
	}
	box, err := o.adapter.boxSource(ctx)
	if err != nil {
		return nil, err
	}
	if box != o.box {
		return nil, notesworkspace.ErrMembership
	}
	plan := workspacePathNotesPlan{Schema: workspacePathNotesSchema, Kind: p.Kind}
	if p.Kind == "relocate_project" {
		location, err := o.store.PlanLocationBindings(ctx, box, p.Move.Source.RelativePath, p.Move.Destination.RelativePath, p.Move.ID)
		if err != nil {
			return nil, err
		}
		plan.Location = &location
	} else {
		scope, err := o.adapter.retainedScope()
		if err != nil {
			return nil, err
		}
		if err = o.store.EnsureRetainedScope(ctx, scope); err != nil {
			return nil, err
		}
		retained, err := o.store.PlanCollectionRetention(ctx, p.Move.Source.AbsolutePath, p.Move.Destination.AbsolutePath, p.Move.ID)
		if err != nil {
			return nil, err
		}
		if retained.Scope != scope {
			return nil, notesworkspace.ErrMembership
		}
		plan.Retained = &retained
	}
	return json.Marshal(plan)
}

func decodeWorkspacePathNotes(raw json.RawMessage) (p workspacePathNotesPlan, err error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&p); err != nil {
		return p, err
	}
	if err = d.Decode(&struct{}{}); err != io.EOF {
		return p, notesworkspace.ErrIntentMismatch
	}
	if p.Schema != workspacePathNotesSchema || p.Kind == "relocate_project" && (p.Location == nil || p.Retained != nil) || p.Kind == "retain_child" && (p.Retained == nil || p.Location != nil) || p.Kind != "relocate_project" && p.Kind != "retain_child" {
		return p, notesworkspace.ErrIntentMismatch
	}
	return p, nil
}

func (a *workspacePathNotesAdapter) commit(ctx context.Context, tx *sql.Tx, p storagearchive.WorkspacePathPlan) error {
	if err := a.configured(); err != nil {
		return err
	}
	if tx == nil {
		return notesworkspace.ErrIntentMismatch
	}
	if err := a.validatePlan(p); err != nil {
		return err
	}
	notes, err := decodeWorkspacePathNotes(p.Notes)
	if err != nil {
		return err
	}
	if notes.Kind != p.Kind {
		return notesworkspace.ErrIntentMismatch
	}
	if notes.Location != nil {
		box, err := a.boxSource(ctx)
		if err != nil {
			return err
		}
		v := notes.Location
		if v.ID != p.Move.ID || v.ScopeID != box.CollectionID || v.SourcePath != box.Path || v.Generation != box.Generation || v.From != p.Move.Source.RelativePath || v.To != p.Move.Destination.RelativePath || v.State != "preview" {
			return notesworkspace.ErrIntentMismatch
		}
		for _, binding := range v.Bindings {
			if binding.Retire {
				return notesworkspace.ErrIntentMismatch
			}
		}
		return notesworkspace.CommitLocationBindingsTx(ctx, tx, *v)
	}
	scope, err := a.retainedScope()
	if err != nil {
		return err
	}
	v := notes.Retained
	if v.ID != p.Move.ID || v.From != p.Move.Source.AbsolutePath || v.To != p.Move.Destination.AbsolutePath || v.Scope != scope {
		return notesworkspace.ErrIntentMismatch
	}
	return (notesworkspace.SQLStore{DB: a.db, ExclusiveSource: true}).CommitCollectionRetentionTx(ctx, tx, *v)
}
