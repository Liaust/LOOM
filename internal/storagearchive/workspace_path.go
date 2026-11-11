package storagearchive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"strings"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/workspaceactions"
)

const WorkspacePathSchema = "storage.workspace_path_plan.v1"

type WorkspacePathPlan struct {
	Schema          string                 `json:"schema"`
	Kind            string                 `json:"kind"`
	ProjectID       string                 `json:"project_id"`
	Move            RetainedRelocationPlan `json:"move"`
	ProjectSnapshot json.RawMessage        `json:"project_snapshot"`
	Dependencies    string                 `json:"dependencies"`
	Notes           json.RawMessage        `json:"notes"`
	Digest          string                 `json:"digest"`
}

type WorkspacePathService struct {
	Roots         TrustedWorkspaceRoots
	Projects      projects.Service
	Catalog       storagecatalog.Service
	Snapshot      func(context.Context, string, string) (json.RawMessage, error)
	Dependencies  func(context.Context, string, string, string, string) (string, error)
	WithNotesLock func(context.Context, func(WorkspacePathNotesOwner) error) error
	Commit        func(context.Context, *sql.Tx, WorkspacePathPlan) error
	ReadersReady  func(context.Context, WorkspacePathPlan) error
}

type WorkspacePathNotesOwner interface {
	Plan(context.Context, WorkspacePathPlan) (json.RawMessage, error)
}

func workspacePathDigest(p WorkspacePathPlan) string {
	p.Digest = ""
	p.Move.Digest = ""
	raw, _ := json.Marshal(p)
	// JSONB preserves values, not object key order. Seal the value rather than
	// RawMessage presentation so a saved review survives its database round trip.
	canonical, _ := json.Marshal(jsonValue(raw))
	return sha256Digest(canonical)
}

func (p WorkspacePathPlan) Summary(state string) map[string]any {
	return map[string]any{"id": p.Move.ID, "box_id": p.Move.BoxID, "kind": p.Kind, "project": p.ProjectID, "state": state, "plan_hash": p.Digest, "from_root": p.Move.Source.Root, "source": p.Move.Source.RelativePath, "to_root": p.Move.Destination.Root, "destination": p.Move.Destination.RelativePath, "entries": len(p.Move.Inventory.Entries), "bytes": p.Move.Inventory.TotalBytes, "qualification": "Reviewed same-filesystem move. Original inodes and history are retained; no indexing or downloads are started. Project lifecycle and runtime activation are unchanged."}
}

func (s WorkspacePathService) locks(ctx context.Context, projectID, id string, fn func(WorkspacePathNotesOwner) error) error {
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, projectID)
	if err != nil {
		return err
	}
	keys := projects.ProjectArchiveLockKeys(projectID, detail.Project.Project.Slug, id, nil, nil)
	release, err := s.Projects.AcquireProjectArchiveLocks(ctx, keys)
	if err != nil {
		return err
	}
	defer release()
	return s.WithNotesLock(ctx, fn)
}

func (s WorkspacePathService) validateRefs(p WorkspacePathPlan) error {
	if err := validateWorkspaceBoxBinding(p.Move.BoxID, s.Roots); err != nil {
		return err
	}
	if p.Schema != WorkspacePathSchema || (p.Kind != "relocate_project" && p.Kind != "retain_child") || p.Move.Source.Root != WorkspaceRootBox {
		return fmt.Errorf("unsupported workspace path operation")
	}
	for _, ref := range []WorkspacePathRef{p.Move.Source, p.Move.Destination} {
		want, e := pathOperationRef(s.Roots, ref.Root, ref.RelativePath)
		if e != nil || want != ref {
			return fmt.Errorf("trusted roots changed")
		}
	}
	if p.Kind == "relocate_project" {
		if p.Move.Destination.Root != WorkspaceRootBox || !strings.HasPrefix(p.Move.Source.RelativePath, "Projects/") || !strings.HasPrefix(p.Move.Destination.RelativePath, "Projects/") {
			return fmt.Errorf("registered projects relocate within Box Projects")
		}
	} else if p.Move.Destination.Root != WorkspaceRootStorage || !retainedDestination(p.Move.Destination.RelativePath) {
		return fmt.Errorf("choose a named Storage Projects, Library or Records destination")
	}
	if workspaceactions.Within(p.Move.Source.AbsolutePath, p.Move.Destination.AbsolutePath) || workspaceactions.Within(p.Move.Destination.AbsolutePath, p.Move.Source.AbsolutePath) {
		return fmt.Errorf("source and destination must not overlap")
	}
	return nil
}

func (s WorkspacePathService) Plan(ctx context.Context, kind, projectID, from, to, actor, reason string) (p WorkspacePathPlan, err error) {
	if s.Snapshot == nil || s.Dependencies == nil || s.WithNotesLock == nil || s.Commit == nil || s.ReadersReady == nil || !validWorkspaceActorID(actor) || !workspaceProjectIDPattern.MatchString(projectID) || strings.TrimSpace(reason) == "" || len(reason) > maxWorkspaceArchiveReasonBytes {
		return p, fmt.Errorf("workspace location owners unavailable")
	}
	p.Schema, p.Kind, p.ProjectID = WorkspacePathSchema, kind, projectID
	p.Move = RetainedRelocationPlan{BoxID: s.Roots.BoxID, Schema: RetainedRelocationSchema, ID: "workspace_path_operation_" + strings.TrimPrefix(ids.NewJobID(), "job_"), CustodyID: "project-location:" + projectID, Actor: actor, Reason: reason, At: time.Now().UTC()}
	p.Move.Source, err = pathOperationRef(s.Roots, WorkspaceRootBox, from)
	if err != nil {
		return p, err
	}
	destRoot := WorkspaceRootBox
	if kind == "retain_child" {
		destRoot = WorkspaceRootStorage
		p.Move.CustodyID = "project-child:" + p.Move.ID
	}
	p.Move.Destination, err = pathOperationRef(s.Roots, destRoot, to)
	if err != nil {
		return p, err
	}
	if err = s.validateRefs(p); err != nil {
		return p, err
	}
	err = s.locks(ctx, projectID, p.Move.ID, func(notes WorkspacePathNotesOwner) error {
		if err := s.ReadersReady(ctx, p); err != nil {
			return err
		}
		detail, e := s.Projects.GetProjectRegistrationStatus(ctx, projectID)
		if e != nil {
			return e
		}
		if detail.Registration == nil || detail.Project.Project.Status != "active" {
			return fmt.Errorf("active registered project required")
		}
		if e = projects.EnsureProjectMutable(detail.Project.Project, "filesystem", p.Move.ID); e != nil {
			return e
		}
		root := detail.Registration.ProjectRoot
		if kind == "relocate_project" && root != p.Move.Source.AbsolutePath || kind == "retain_child" && (root == p.Move.Source.AbsolutePath || !workspaceactions.Within(p.Move.Source.AbsolutePath, root)) {
			return fmt.Errorf("select the exact project root or a strict project child")
		}
		related, e := workspaceactions.RelatedProjects(ctx, s.Catalog.DB, p.Move.Source.AbsolutePath, p.Move.Destination.AbsolutePath)
		if e != nil {
			return e
		}
		for _, item := range related {
			if item.ID != projectID {
				return fmt.Errorf("path intersects registered project %s", item.Slug)
			}
		}
		p.Dependencies, e = s.Dependencies(ctx, projectID, kind, p.Move.Source.AbsolutePath, root)
		if e != nil {
			return e
		}
		p.ProjectSnapshot, e = s.Snapshot(ctx, projectID, p.Move.ID)
		if e != nil {
			return e
		}
		for index, ref := range []WorkspacePathRef{p.Move.Source, p.Move.Destination} {
			parent, e := openHeldDirectoryChain(pathOperationRoot(s.Roots, ref.Root), path.Dir(ref.RelativePath), nil)
			if e != nil {
				return e
			}
			if e = retainedParentWritable(parent.ParentFD()); e != nil {
				parent.Close()
				return e
			}
			if index == 0 {
				p.Move.SourceAncestors = parent.evidence
			} else {
				p.Move.DestinationAncestors = parent.evidence
				present, e := namePresentNoFollow(parent.ParentFD(), path.Base(ref.RelativePath))
				if e != nil || present {
					parent.Close()
					return fmt.Errorf("destination must be absent")
				}
			}
			parent.Close()
		}
		p.Move.Inventory, e = inventoryRetainedSchema(ctx, s.Roots, p.Move.Source, NoFollowInventorySchemaVersion)
		if e != nil {
			return e
		}
		if len(p.Move.Inventory.Entries) > 250000 {
			return fmt.Errorf("choose a narrower directory")
		}
		p.Move.Original = p.Move.Source.AbsolutePath
		if kind == "relocate_project" {
			var original string
			e = s.Catalog.DB.QueryRowContext(ctx, `SELECT original_prefix FROM storage.custody_locations WHERE custody_id=$1 AND current_prefix=$2`, p.Move.CustodyID, p.Move.Source.AbsolutePath).Scan(&original)
			if e == nil {
				p.Move.Original = original
			} else if e != sql.ErrNoRows {
				return e
			}
		}
		p.Move.Catalog, e = (RetainedRelocationService{Catalog: s.Catalog}).catalogPlan(ctx, p.Move.Source, p.Move.Destination)
		if e != nil {
			return e
		}
		p.Notes, e = notes.Plan(ctx, p)
		if e != nil {
			return e
		}
		p.Digest = workspacePathDigest(p)
		p.Move.Digest = p.Digest
		raw, _ := json.Marshal(p)
		return s.Catalog.SaveRetainedRelocation(ctx, storagecatalog.RetainedRelocation{ID: p.Move.ID, CustodyID: p.Move.CustodyID, Original: p.Move.Original, Source: p.Move.Source.AbsolutePath, Destination: p.Move.Destination.AbsolutePath, Digest: p.Digest, Plan: raw})
	})
	return p, err
}

func (s WorkspacePathService) Load(ctx context.Context, id, digest string) (p WorkspacePathPlan, r storagecatalog.RetainedRelocation, err error) {
	return s.load(ctx, id, digest, false)
}

func (s WorkspacePathService) LoadCancellation(ctx context.Context, id, digest string) (WorkspacePathPlan, storagecatalog.RetainedRelocation, error) {
	return s.load(ctx, id, digest, true)
}

func (s WorkspacePathService) load(ctx context.Context, id, digest string, cancelling bool) (p WorkspacePathPlan, r storagecatalog.RetainedRelocation, err error) {
	r, err = s.Catalog.RetainedRelocation(ctx, id)
	if err != nil {
		return p, r, err
	}
	if json.Unmarshal(r.Plan, &p) != nil || p.Schema != WorkspacePathSchema || p.Move.ID != id || !strings.HasPrefix(id, "workspace_path_operation_") || !validWorkspaceActorID(p.Move.Actor) || validateRetainedInventory(p.Move.Inventory) != nil || p.Move.Inventory.SchemaVersion != NoFollowInventorySchemaVersion || p.Move.Digest != p.Digest || p.Move.CustodyID != r.CustodyID || p.Move.Original != r.Original || p.Digest != r.Digest || digest != "" && p.Digest != digest || p.Move.Source.AbsolutePath != r.Source || p.Move.Destination.AbsolutePath != r.Destination {
		return p, r, fmt.Errorf("saved operation does not match its exact reviewed plan")
	}
	// The first unsubmitted reviews used presentation-dependent JSON hashes.
	// They can never apply. Explicit cancellation still proves the exact source
	// inode and absent destination, without changing files or inventing a seal.
	if workspacePathDigest(p) != p.Digest && (!cancelling || (r.State != "planned" && r.State != "cancelled") || digest == "") {
		return p, r, fmt.Errorf("saved review encoding changed; cancel the unsubmitted review and review again")
	}
	err = s.validateRefs(p)
	return
}

func (s WorkspacePathService) Apply(ctx context.Context, id, digest string) (p WorkspacePathPlan, r storagecatalog.RetainedRelocation, err error) {
	p, r, err = s.Load(ctx, id, digest)
	if err != nil || r.State == "complete" {
		return
	}
	if r.State == "cancelled" {
		err = fmt.Errorf("cancelled operation cannot be applied")
		return
	}
	err = s.locks(ctx, p.ProjectID, id, func(notesOwner WorkspacePathNotesOwner) error {
		release, e := lockRetainedExecutor(s.Roots)
		if e != nil {
			return e
		}
		defer release()
		if e = s.ReadersReady(ctx, p); e != nil {
			return e
		}
		r, e = s.Catalog.ApplyWorkspacePathRelocation(ctx, id, digest, func(current storagecatalog.RetainedRelocation) (storagecatalog.RebindPathsInput, json.RawMessage, error) {
			observed, e := s.Snapshot(ctx, p.ProjectID, id)
			if e != nil || !reflect.DeepEqual(jsonValue(observed), jsonValue(p.ProjectSnapshot)) {
				return p.Move.Catalog, nil, fmt.Errorf("project registration changed since review: %v", e)
			}
			physicalRoot := p.Move.Source.AbsolutePath
			if p.Kind == "retain_child" {
				detail, e := s.Projects.GetProjectRegistrationStatus(ctx, p.ProjectID)
				if e != nil {
					return p.Move.Catalog, nil, e
				}
				physicalRoot = detail.Registration.ProjectRoot
			}
			// Recovery may read the frozen contract at the exact moved inode.
			if p.Kind == "relocate_project" {
				parent, e := openHeldDirectoryChain(s.Roots.BoxRoot, path.Dir(p.Move.Source.RelativePath), p.Move.SourceAncestors)
				if e != nil {
					return p.Move.Catalog, nil, e
				}
				present, e := namePresentNoFollow(parent.ParentFD(), path.Base(p.Move.Source.RelativePath))
				parent.Close()
				if e != nil {
					return p.Move.Catalog, nil, e
				}
				if !present {
					physicalRoot = p.Move.Destination.AbsolutePath
				}
			}
			deps, e := s.Dependencies(ctx, p.ProjectID, p.Kind, p.Move.Source.AbsolutePath, physicalRoot)
			if e != nil || deps != p.Dependencies {
				return p.Move.Catalog, nil, fmt.Errorf("project dependencies changed since review: %v", e)
			}
			notes, e := notesOwner.Plan(ctx, p)
			if e != nil || !reflect.DeepEqual(jsonValue(notes), jsonValue(p.Notes)) {
				return p.Move.Catalog, nil, fmt.Errorf("Notes bindings changed since review: %v", e)
			}
			catalog, e := (RetainedRelocationService{Catalog: s.Catalog}).catalogPlan(ctx, p.Move.Source, p.Move.Destination)
			if e != nil || !reflect.DeepEqual(catalog, p.Move.Catalog) {
				return p.Move.Catalog, nil, fmt.Errorf("catalog changed since review: %v", e)
			}
			evidence, e := moveRetained(ctx, s.Roots, p.Move)
			return p.Move.Catalog, evidence, e
		}, func(ctx context.Context, tx *sql.Tx) error { return s.markPending(ctx, tx, p) }, func(ctx context.Context, tx *sql.Tx) error {
			if e := s.Commit(ctx, tx, p); e != nil {
				return e
			}
			_, e := tx.ExecContext(ctx, `UPDATE projects.projects SET metadata=metadata-'workspace_filesystem_operation' WHERE project_id=$1 AND metadata#>>'{workspace_filesystem_operation,id}'=$2`, p.ProjectID, id)
			return e
		})
		return e
	})
	return
}

func jsonValue(raw json.RawMessage) any {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&value)
	return value
}

func (s WorkspacePathService) markPending(ctx context.Context, tx *sql.Tx, p WorkspacePathPlan) error {
	raw, _ := json.Marshal(map[string]string{"id": p.Move.ID, "kind": p.Kind, "source": p.Move.Source.AbsolutePath, "destination": p.Move.Destination.AbsolutePath})
	result, err := tx.ExecContext(ctx, `UPDATE projects.projects SET metadata=jsonb_set(metadata,'{workspace_filesystem_operation}',$2::jsonb) WHERE project_id=$1 AND (NOT metadata ? 'workspace_filesystem_operation' OR metadata#>>'{workspace_filesystem_operation,id}'=$3)`, p.ProjectID, raw, p.Move.ID)
	if err != nil {
		return err
	}
	n, e := result.RowsAffected()
	if e != nil || n != 1 {
		return fmt.Errorf("another project filesystem operation is pending")
	}
	return nil
}

func (s WorkspacePathService) Cancel(ctx context.Context, id, digest string) (p WorkspacePathPlan, r storagecatalog.RetainedRelocation, err error) {
	p, r, err = s.load(ctx, id, digest, true)
	if err != nil || r.State == "cancelled" {
		return
	}
	err = s.locks(ctx, p.ProjectID, id, func(WorkspacePathNotesOwner) error {
		release, e := lockRetainedExecutor(s.Roots)
		if e != nil {
			return e
		}
		defer release()
		r, e = s.Catalog.CancelWorkspacePathRelocation(ctx, id, digest, func(storagecatalog.RetainedRelocation) (json.RawMessage, error) {
			return proveRetainedUnmoved(ctx, s.Roots, p.Move)
		}, func(ctx context.Context, tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, `UPDATE projects.projects SET metadata=metadata-'workspace_filesystem_operation' WHERE project_id=$1 AND metadata#>>'{workspace_filesystem_operation,id}'=$2`, p.ProjectID, id)
			return e
		})
		return e
	})
	return
}

func pathOperationRoot(roots TrustedWorkspaceRoots, kind WorkspaceRoot) string {
	switch kind {
	case WorkspaceRootBox:
		return roots.BoxRoot
	case WorkspaceRootStorage:
		return roots.StorageRoot
	default:
		return "/invalid-loom-workspace-root"
	}
}

func pathOperationRef(roots TrustedWorkspaceRoots, kind WorkspaceRoot, relative string) (WorkspacePathRef, error) {
	ref, err := retainedRef(roots, relative)
	if err != nil {
		return ref, err
	}
	if kind != WorkspaceRootBox && kind != WorkspaceRootStorage {
		return ref, fmt.Errorf("unsupported path root")
	}
	ref.Root = kind
	ref.AbsolutePath = pathOperationRoot(roots, kind) + "/" + relative
	return ref, validateWorkspaceRefForRoots(ref, roots)
}
