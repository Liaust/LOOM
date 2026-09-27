package projectwatch

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	sr "loom.local/loom/internal/serviceregistry"
)

func ApplicationDataReference(metadata []byte) (*pc.KnowledgeApplicationData, error) {
	var m struct {
		Knowledge struct {
			ApplicationData *pc.KnowledgeApplicationData `json:"application_data"`
		} `json:"knowledge_source"`
	}
	if err := json.Unmarshal(metadata, &m); err != nil {
		return nil, err
	}
	return m.Knowledge.ApplicationData, nil
}

func ApplicationDataSafeKey(projectID, rootKey string) string {
	return "declaration_app_" + strings.ToLower(projectID) + "_" + rootKey
}

func BindApplicationData(ref pc.KnowledgeApplicationData, bindingRef, projectID, nodeID, location string, f sr.ApplicationPrerequisiteSnapshot) (*projects.DeclarationApplicationDataBinding, error) {
	data, ok := f.Data[string(ref.Data)]
	yes := func(v *bool) bool { return v != nil && *v }
	no := func(v *bool) bool { return v != nil && !*v }
	if !ok || f.SchemaVersion != sr.ApplicationPrerequisiteSchema || f.Owner != (sr.ApplicationOwner{ProjectID: projectID, NodeID: nodeID, Resource: string(ref.Application)}) || f.GrantMissing || f.LocationRevision != location || !f.Installation.Present || !yes(f.Installation.Committed) || !yes(f.Installation.Applied) || !no(f.Installation.Retired) || !no(f.Installation.Fenced) || data.BindingRef != bindingRef || data.Availability != "available" || data.Custody != "matches" || data.Identity == nil || !filepath.IsAbs(data.Path) || filepath.Clean(data.Path) != data.Path || data.Path == "/" {
		return nil, fmt.Errorf("application_data_allocation_required")
	}
	i := data.Identity
	return &projects.DeclarationApplicationDataBinding{Application: string(ref.Application), Data: string(ref.Data), Subpath: ref.Subpath, BindingRef: bindingRef, Path: data.Path, PoolIdentity: data.PoolIdentity, Device: i.Device, Inode: i.Inode, PoolInode: i.PoolInode, UID: i.UID, GID: i.GID, Mode: i.Mode, InstallationRevision: f.Installation.Revision, PolicyRevision: f.PolicyRevision, LocationRevision: f.LocationRevision}, nil
}

func ValidateApplicationDataBinding(root projects.DeclarationWatchRoot) error {
	ref, err := ApplicationDataReference(root.Metadata)
	b := root.ApplicationData
	if err != nil {
		return err
	}
	if ref == nil {
		if b != nil {
			return fmt.Errorf("unexpected application data binding")
		}
		return nil
	}
	if b == nil || b.Application != string(ref.Application) || b.Data != string(ref.Data) || b.Subpath != ref.Subpath || b.BindingRef == "" || !filepath.IsAbs(b.Path) || b.Path == "/" || filepath.Clean(b.Path) != b.Path || b.Inode == 0 || b.PoolInode == 0 || b.PoolIdentity == "" || b.InstallationRevision == "" || b.PolicyRevision == "" || b.LocationRevision == "" {
		return fmt.Errorf("application data binding required")
	}
	return nil
}
