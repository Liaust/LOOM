package storagearchive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/workspaceactions"
)

const RetainedRelocationSchema = "storage.retained_relocation.v1"
const retainedInventorySchema = "storage.retained_inventory.v1"

func validateRetainedInventory(inventory NoFollowInventory) error {
	return validateNoFollowInventory(inventory, inventory.SchemaVersion == retainedInventorySchema)
}

func retainedParentWritable(fd int) error {
	if err := unix.Faccessat(fd, ".", unix.W_OK|unix.X_OK, unix.AT_EACCESS); err != nil {
		return fmt.Errorf("retained move parent is not writable by its service owner; prepare its exact migration access before applying: %w", err)
	}
	return nil
}

type RetainedRelocationPlan struct {
	BoxID                string                          `json:"box_id,omitempty"`
	Schema               string                          `json:"schema"`
	ID                   string                          `json:"id"`
	CustodyID            string                          `json:"custody_id"`
	Original             string                          `json:"original"`
	Source               WorkspacePathRef                `json:"source"`
	Destination          WorkspacePathRef                `json:"destination"`
	OwnerDigest          string                          `json:"owner_digest"`
	Inventory            NoFollowInventory               `json:"inventory"`
	SourceAncestors      []WorkspaceAncestorBinding      `json:"source_ancestors"`
	DestinationAncestors []WorkspaceAncestorBinding      `json:"destination_ancestors"`
	Catalog              storagecatalog.RebindPathsInput `json:"catalog"`
	Actor                string                          `json:"actor"`
	Reason               string                          `json:"reason"`
	At                   time.Time                       `json:"at"`
	Digest               string                          `json:"digest"`
}

// Proof comes from the existing custody owner, not from a caller's pathname or
// copied project descriptor. It also rechecks unfinished lifecycle operations.
type RetainedCustodyProof struct{ ID, Original, Digest string }
type RetainedRelocationService struct {
	Roots   TrustedWorkspaceRoots
	Catalog storagecatalog.Service
	Prove   func(context.Context, WorkspacePathRef, string, NoFollowInventory) (RetainedCustodyProof, error)
	// ReadersReady is enabled by composition only after backup/restore readers
	// support this custody kind. Planning cannot itself authorise an unsafe move.
	ReadersReady    func(context.Context, RetainedRelocationPlan) error
	operatorPrepare bool
}

var errOperatorPrepared = errors.New("operator filesystem intent prepared")

func (s RetainedRelocationService) PlanOperator(ctx context.Context, from, to, actor, reason string) (RetainedRelocationPlan, error) {
	s.operatorPrepare = true
	return s.Plan(ctx, from, to, actor, reason)
}

func (s RetainedRelocationService) PrepareOperator(ctx context.Context, id, digest string) (storagecatalog.RetainedRelocation, error) {
	s.operatorPrepare = true
	r, err := s.Apply(ctx, id, digest)
	if errors.Is(err, errOperatorPrepared) {
		err = nil
	}
	return r, err
}

func ExecuteRetainedOperator(ctx context.Context, roots TrustedWorkspaceRoots, p RetainedRelocationPlan, digest string, confirmPrepared func(context.Context) error) error {
	if err := validateWorkspaceBoxBinding(p.BoxID, roots); err != nil {
		return err
	}
	allowed := strings.HasPrefix(p.CustodyID, "legacy-import:") || strings.HasPrefix(p.CustodyID, "lane:") && sha256DigestPattern.MatchString("sha256:"+strings.TrimPrefix(p.CustodyID, "lane:")) || strings.HasPrefix(p.CustodyID, "workspace:") && workspaceOperationID.MatchString(strings.TrimPrefix(p.CustodyID, "workspace:")) && validWorkspaceRetainedDestination(roots, p)
	if os.Geteuid() != 0 || confirmPrepared == nil || p.Schema != RetainedRelocationSchema || p.Digest != digest || relocationDigest(p) != digest || !allowed || validateRetainedInventory(p.Inventory) != nil || !retainedDestination(p.Destination.RelativePath) {
		return fmt.Errorf("root operator requires the exact reviewed supported retained plan")
	}
	for _, ref := range []WorkspacePathRef{p.Source, p.Destination} {
		want, err := retainedRef(roots, ref.RelativePath)
		if err != nil || want != ref {
			return fmt.Errorf("operator plan roots changed")
		}
	}
	release, err := lockRetainedExecutor(roots)
	if err != nil {
		return err
	}
	defer release()
	if err = confirmPrepared(ctx); err != nil {
		return fmt.Errorf("operator intent is no longer prepared: %w", err)
	}
	_, err = moveRetained(ctx, roots, p)
	return err
}

func retainedRef(roots TrustedWorkspaceRoots, relative string) (WorkspacePathRef, error) {
	if !fs.ValidPath(relative) || relative == "." || strings.ContainsAny(relative, "\\\x00") || len(relative) > 4096 {
		return WorkspacePathRef{}, fmt.Errorf("exact retained relative path required")
	}
	return WorkspacePathRef{Root: WorkspaceRootStorage, RelativePath: relative, AbsolutePath: filepath.Join(roots.StorageRoot, filepath.FromSlash(relative))}, nil
}
func retainedDestination(relative string) bool {
	parts := strings.Split(relative, "/")
	for _, part := range parts {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return len(parts) >= 2 && (parts[0] == "Projects" || parts[0] == "Library" || parts[0] == "Records")
}
func withinRetained(p, root string) bool { return p == root || strings.HasPrefix(p, root+"/") }
func relocationDigest(p RetainedRelocationPlan) string {
	p.Digest = ""
	raw, _ := json.Marshal(p)
	return sha256Digest(raw)
}

func (s RetainedRelocationService) Plan(ctx context.Context, from, to, actor, reason string) (p RetainedRelocationPlan, err error) {
	p.BoxID = s.Roots.BoxID
	if s.Catalog.DB == nil || s.Prove == nil || !validWorkspaceActorID(actor) || strings.TrimSpace(reason) == "" || len(reason) > maxWorkspaceArchiveReasonBytes {
		return p, fmt.Errorf("trusted custody owner, actor and bounded reason required")
	}
	p.Source, err = retainedRef(s.Roots, from)
	if err != nil {
		return p, err
	}
	p.Destination, err = retainedRef(s.Roots, to)
	if err != nil {
		return p, err
	}
	if !retainedDestination(to) || withinRetained(from, to) || withinRetained(to, from) {
		return p, fmt.Errorf("choose a separate named Projects, Library or Records destination")
	}
	if err = workspaceactions.GuardOrdinaryMove(ctx, s.Catalog.DB, p.Destination.AbsolutePath, ""); err != nil {
		return p, err
	}
	source, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(from), nil)
	if err != nil {
		return p, err
	}
	defer source.Close()
	destination, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(to), nil)
	if err != nil {
		return p, err
	}
	defer destination.Close()
	if !s.operatorPrepare {
		for _, parent := range []*heldDirectoryChain{source, destination} {
			if err = retainedParentWritable(parent.ParentFD()); err != nil {
				return p, err
			}
		}
		if err = retainedDirectoryWritable(source.ParentFD(), path.Base(from)); err != nil {
			return p, err
		}
	}
	if present, e := namePresentNoFollow(destination.ParentFD(), path.Base(to)); e != nil || present {
		return p, fmt.Errorf("destination must be absent: %v", e)
	}
	if err = verifyWorkspaceRenameMounts(source.ParentFD(), path.Base(from), destination.ParentFD()); err != nil {
		return p, err
	}
	p.Inventory, err = inventoryRetained(ctx, s.Roots, p.Source)
	if err != nil {
		return p, err
	}
	if len(p.Inventory.Entries) > 250000 {
		return p, fmt.Errorf("choose a narrower retained collection")
	}
	proof, err := s.Prove(ctx, p.Source, "", p.Inventory)
	if err != nil {
		return p, err
	}
	if proof.ID == "" || !filepath.IsAbs(proof.Original) || !sha256DigestPattern.MatchString(proof.Digest) {
		return p, fmt.Errorf("custody proof incomplete")
	}
	p.Schema, p.ID, p.CustodyID, p.Original, p.OwnerDigest = RetainedRelocationSchema, newWorkspaceID("retained_relocation"), proof.ID, proof.Original, proof.Digest
	p.Actor, p.Reason, p.At = actor, reason, time.Now().UTC()
	if strings.HasPrefix(p.CustodyID, "workspace:") && !validWorkspaceRetainedDestination(s.Roots, p) {
		return p, fmt.Errorf("registered archive destination must be its exact named Projects path")
	}
	p.SourceAncestors, p.DestinationAncestors = source.evidence, destination.evidence
	p.Catalog, err = s.catalogPlan(ctx, p.Source, p.Destination)
	if err != nil {
		return p, err
	}
	if err = source.verifyNamed(); err != nil {
		return p, err
	}
	if err = destination.verifyNamed(); err != nil {
		return p, err
	}
	p.Digest = relocationDigest(p)
	raw, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	err = s.Catalog.SaveRetainedRelocation(ctx, storagecatalog.RetainedRelocation{ID: p.ID, CustodyID: p.CustodyID, Original: p.Original, Source: p.Source.AbsolutePath, Destination: p.Destination.AbsolutePath, Digest: p.Digest, Plan: raw})
	return
}

func inventoryRetained(ctx context.Context, roots TrustedWorkspaceRoots, ref WorkspacePathRef) (NoFollowInventory, error) {
	return inventoryRetainedSchema(ctx, roots, ref, retainedInventorySchema)
}

func inventoryRetainedSchema(ctx context.Context, roots TrustedWorkspaceRoots, ref WorkspacePathRef, schema string) (NoFollowInventory, error) {
	if schema != retainedInventorySchema && schema != NoFollowInventorySchemaVersion {
		return NoFollowInventory{}, fmt.Errorf("unsupported retained inventory schema")
	}
	root, info, err := openMappedDirectoryNoFollow(roots, ref)
	if err != nil {
		return NoFollowInventory{}, err
	}
	defer root.Close()
	return inventoryOpenRootMode(ctx, root, info, schema == retainedInventorySchema)
}

func (s RetainedRelocationService) Apply(ctx context.Context, id, digest string) (storagecatalog.RetainedRelocation, error) {
	if s.Prove == nil {
		return storagecatalog.RetainedRelocation{}, fmt.Errorf("trusted retained custody owner unavailable")
	}
	r, p, err := s.savedPlan(ctx, id, digest)
	if err != nil {
		return r, err
	}
	if r.State == "complete" {
		return r, nil
	}
	if r.State == "cancelled" {
		return r, fmt.Errorf("cancelled relocation cannot be applied; review a new plan")
	}
	if s.ReadersReady == nil {
		return r, fmt.Errorf("retained relocation is held until the custody readers and protection selection are ready")
	}
	if err = s.ReadersReady(ctx, p); err != nil {
		return r, err
	}
	_, sourceErr := os.Lstat(p.Source.AbsolutePath)
	if !s.operatorPrepare && sourceErr == nil {
		for _, relative := range []string{p.Source.RelativePath, p.Destination.RelativePath} {
			parent, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(relative), nil)
			if err != nil {
				return r, err
			}
			err = retainedParentWritable(parent.ParentFD())
			parent.Close()
			if err != nil {
				return r, err
			}
		}
		parent, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(p.Source.RelativePath), nil)
		if err != nil {
			return r, err
		}
		err = retainedDirectoryWritable(parent.ParentFD(), path.Base(p.Source.RelativePath))
		parent.Close()
		if err != nil {
			return r, err
		}
	} else if sourceErr != nil && !os.IsNotExist(sourceErr) {
		return r, sourceErr
	}
	release, err := lockRetainedExecutor(s.Roots)
	if err != nil {
		return r, err
	}
	defer release()
	return s.Catalog.ApplyRetainedRelocation(ctx, id, digest, func(record storagecatalog.RetainedRelocation) (storagecatalog.RebindPathsInput, json.RawMessage, error) {
		if err := workspaceactions.GuardRetainedDestination(ctx, s.Catalog.DB, p.Destination.AbsolutePath, p.CustodyID); err != nil {
			return storagecatalog.RebindPathsInput{}, nil, err
		}
		proof, err := s.Prove(ctx, p.Source, p.CustodyID, p.Inventory)
		if err != nil || proof.ID != p.CustodyID || proof.Original != p.Original || proof.Digest != p.OwnerDigest {
			return storagecatalog.RebindPathsInput{}, nil, fmt.Errorf("custody owner changed: %v", err)
		}
		catalog, err := s.catalogPlan(ctx, p.Source, p.Destination)
		if err != nil {
			return catalog, nil, err
		}
		if !reflect.DeepEqual(catalog, p.Catalog) {
			return catalog, nil, fmt.Errorf("catalog references changed since review")
		}
		if s.operatorPrepare {
			return catalog, nil, errOperatorPrepared
		}
		evidence, err := moveRetained(ctx, s.Roots, p)
		return catalog, evidence, err
	})
}

func moveRetained(ctx context.Context, roots TrustedWorkspaceRoots, p RetainedRelocationPlan) (json.RawMessage, error) {
	if err := validateWorkspaceBoxBinding(p.BoxID, roots); err != nil {
		return nil, err
	}
	performed := false
	source, err := openHeldDirectoryChain(pathOperationRoot(roots, p.Source.Root), path.Dir(p.Source.RelativePath), p.SourceAncestors)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	destination, err := openHeldDirectoryChain(pathOperationRoot(roots, p.Destination.Root), path.Dir(p.Destination.RelativePath), p.DestinationAncestors)
	if err != nil {
		return nil, err
	}
	defer destination.Close()
	present, matches, err := inspectNamedDirectory(source.ParentFD(), path.Base(p.Source.RelativePath), p.Inventory.RootIdentity)
	if err != nil {
		return nil, err
	}
	destPresent, destMatches, err := inspectNamedDirectory(destination.ParentFD(), path.Base(p.Destination.RelativePath), p.Inventory.RootIdentity)
	if err != nil {
		return nil, err
	}
	if present && matches && !destPresent {
		inventory, err := inventoryRetainedSchema(ctx, roots, p.Source, p.Inventory.SchemaVersion)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(inventory, p.Inventory) {
			return nil, fmt.Errorf("source changed since review")
		}
		if err = source.verifyNamed(); err != nil {
			return nil, err
		}
		if err = destination.verifyNamed(); err != nil {
			return nil, err
		}
		if err = verifyWorkspaceRenameMounts(source.ParentFD(), path.Base(p.Source.RelativePath), destination.ParentFD()); err != nil {
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if err = renameNoReplaceAt(source.ParentFD(), path.Base(p.Source.RelativePath), destination.ParentFD(), path.Base(p.Destination.RelativePath)); err != nil {
			return nil, fmt.Errorf("retained no-replace rename failed; legacy directory ownership may require the local --operator owner: %w", err)
		}
		performed = true
		if err = unix.Fsync(source.ParentFD()); err != nil {
			return nil, err
		}
		if err = unix.Fsync(destination.ParentFD()); err != nil {
			return nil, err
		}
	} else if present || !destPresent || !destMatches {
		return nil, fmt.Errorf("relocation source/destination is ambiguous; no overwrite or copy fallback")
	}
	observed, err := inventoryRetainedSchema(ctx, roots, p.Destination, p.Inventory.SchemaVersion)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(observed, p.Inventory) {
		return nil, fmt.Errorf("moved inventory does not match the reviewed payload")
	}
	if err = source.verifyNamed(); err != nil {
		return nil, err
	}
	if err = destination.verifyNamed(); err != nil {
		return nil, err
	}
	if present, err = namePresentNoFollow(source.ParentFD(), path.Base(p.Source.RelativePath)); err != nil || present {
		return nil, fmt.Errorf("old source was recreated; retain both locations for review")
	}
	sourceParent, err := identityForFD(source.ParentFD())
	if err != nil {
		return nil, err
	}
	destinationParent, err := identityForFD(destination.ParentFD())
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Digest                          string `json:"inventory_digest"`
		VerificationUID                 int    `json:"verification_uid"`
		MovePerformed                   bool   `json:"move_performed"`
		SourceParent, DestinationParent PathIdentity
		Source, Destination             string
	}{observed.Digest, os.Geteuid(), performed, sourceParent, destinationParent, p.Source.AbsolutePath, p.Destination.AbsolutePath})
}

func retainedDirectoryWritable(parent int, name string) error {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err = retainedParentWritable(fd); err != nil {
		return fmt.Errorf("moving a directory across parents also requires write access to the directory itself; use the explicit local --operator migration owner for retained foreign ownership: %w", err)
	}
	return nil
}

func moveRetainedValue(value, from, to string) (string, bool) {
	if withinRetained(value, from) {
		return to + strings.TrimPrefix(value, from), true
	}
	return value, false
}
func (s RetainedRelocationService) catalogPlan(ctx context.Context, from, to WorkspacePathRef) (storagecatalog.RebindPathsInput, error) {
	entries, err := s.Catalog.ListWorkspaceArchiveCatalogEntries(ctx, from.AbsolutePath, from.RelativePath)
	if err != nil {
		return storagecatalog.RebindPathsInput{}, err
	}
	result := storagecatalog.RebindPathsInput{}
	for _, detail := range entries {
		e := detail.Entry
		next, changed := moveRetainedValue(e.CurrentViewPath, from.AbsolutePath, to.AbsolutePath)
		if !changed {
			next, changed = moveRetainedValue(e.CurrentViewPath, from.RelativePath, to.RelativePath)
		}
		if changed {
			result.Entries = append(result.Entries, storagecatalog.EntryPathRebind{StorageEntryID: e.StorageEntryID, ExpectedOriginalSourcePath: e.OriginalSourcePath, NewOriginalSourcePath: e.OriginalSourcePath, ExpectedCurrentViewPath: e.CurrentViewPath, NewCurrentViewPath: next})
		}
		for _, ref := range detail.PhysicalRefs {
			old, ok := storagecatalog.PhysicalRefLocalPath(ref)
			if !ok {
				continue
			}
			next, changed := moveRetainedValue(old, from.AbsolutePath, to.AbsolutePath)
			if !changed {
				continue
			}
			if strings.HasPrefix(ref.URI, "file://") {
				next = (&url.URL{Scheme: "file", Path: next}).String()
			}
			result.PhysicalRefs = append(result.PhysicalRefs, storagecatalog.PhysicalRefPathRebind{StoragePhysicalRefID: ref.StoragePhysicalRefID, StorageEntryID: e.StorageEntryID, ExpectedURI: ref.URI, NewURI: next})
		}
	}
	sort.Slice(result.Entries, func(i, j int) bool { return result.Entries[i].StorageEntryID < result.Entries[j].StorageEntryID })
	sort.Slice(result.PhysicalRefs, func(i, j int) bool {
		return result.PhysicalRefs[i].StoragePhysicalRefID < result.PhysicalRefs[j].StoragePhysicalRefID
	})
	return result, nil
}
