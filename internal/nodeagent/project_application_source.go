package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/nodeagent/watchedroots"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	sr "loom.local/loom/internal/serviceregistry"
)

type applicationSourceMetadata struct {
	Source      string                                      `json:"source"`
	ProjectID   string                                      `json:"project_id"`
	ProjectRoot string                                      `json:"project_root"`
	NodeID      string                                      `json:"node_id"`
	Resource    string                                      `json:"resource"`
	Binding     *projects.DeclarationApplicationDataBinding `json:"application_data"`
}

func validateApplicationSource(ctx context.Context, config Config, state State, safe filesystemconnector.SafeRoot, cfg watchedroots.RootConfig) error {
	if !strings.HasPrefix(cfg.SafeRootKey, "declaration_app_") {
		return nil
	}
	var m applicationSourceMetadata
	if json.Unmarshal(safe.Metadata, &m) != nil {
		return fmt.Errorf("application_data_metadata_invalid")
	}
	if m.Source != "project.application_data" {
		return fmt.Errorf("application_data_metadata_invalid")
	}
	b := m.Binding
	if b == nil || m.NodeID != state.NodeID || safe.RootKey != projectwatch.ApplicationDataSafeKey(m.ProjectID, cfg.RootKey) || safe.AbsolutePath != b.Path {
		return fmt.Errorf("application_data_binding_changed")
	}
	analysis := pc.Analyze(m.ProjectRoot)
	if analysis.Loaded == nil || analysis.Loaded.Declaration == nil || !analysis.Report.OK || analysis.Loaded.Declaration.Project.ID != m.ProjectID || analysis.Loaded.Declaration.Project.Status == "archived" {
		return fmt.Errorf("application_data_project_unavailable")
	}
	d := analysis.Loaded.Declaration
	k := d.Resources[pc.ResourceKey(m.Resource)].Knowledge
	if k == nil || k.ApplicationData == nil {
		return fmt.Errorf("application_data_source_withdrawn")
	}
	ref := *k.ApplicationData
	if string(ref.Application) != b.Application || string(ref.Data) != b.Data || ref.Subpath != b.Subpath {
		return fmt.Errorf("application_data_source_changed")
	}
	app := d.Resources[ref.Application].Application
	if app == nil || app.Data[ref.Data].BindingRef != b.BindingRef {
		return fmt.Errorf("application_data_binding_changed")
	}
	facts, err := (sr.ApplicationHelperClient{SocketPath: config.ServiceManager.ApplicationSocketPath}).QueryPrerequisites(ctx, sr.ApplicationPrerequisiteQuery{SchemaVersion: sr.ApplicationPrerequisiteSchema, Owner: sr.ApplicationOwner{ProjectID: m.ProjectID, NodeID: m.NodeID, Resource: b.Application}, SourceData: b.Data})
	if err != nil {
		return fmt.Errorf("application_data_owner_unavailable")
	}
	current, err := projectwatch.BindApplicationData(ref, b.BindingRef, m.ProjectID, m.NodeID, b.LocationRevision, facts)
	if err != nil || !sameApplicationSourceCustody(current, b) {
		return fmt.Errorf("application_data_custody_changed")
	}
	return validateApplicationSourcePath(*b)
}

func sameApplicationSourceCustody(current, reviewed *projects.DeclarationApplicationDataBinding) bool {
	if current == nil || reviewed == nil {
		return false
	}
	// The current owner query has already checked active, committed custody.
	// A normal application reconcile may advance these receipts without moving
	// its data. Keep the reviewed receipts, but compare the actual allocation.
	c := *current
	c.InstallationRevision = reviewed.InstallationRevision
	c.PolicyRevision = reviewed.PolicyRevision
	return c == *reviewed
}

// Do not follow symlinks in the allocation or subfolder. Application write
// permission does not turn a link into a new Notes source.
func validateApplicationSourcePath(b projects.DeclarationApplicationDataBinding) error {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { unix.Close(fd) }()
	parts := strings.Split(strings.TrimPrefix(b.Path, "/"), "/")
	for _, part := range parts {
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("application_data_path_unavailable")
		}
		unix.Close(fd)
		fd = next
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || uint64(st.Dev) != b.Device || st.Ino != b.Inode {
		return fmt.Errorf("application_data_identity_changed")
	}
	if b.Subpath != "" {
		if filepath.IsAbs(b.Subpath) || filepath.Clean(b.Subpath) != b.Subpath || b.Subpath == "." || strings.Contains(b.Subpath, "\\") {
			return fmt.Errorf("application_data_subpath_invalid")
		}
		for _, part := range strings.Split(b.Subpath, "/") {
			if part == ".." || part == "" {
				return fmt.Errorf("application_data_subpath_invalid")
			}
			next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return fmt.Errorf("application_data_subpath_unavailable")
			}
			unix.Close(fd)
			fd = next
		}
	}
	return nil
}

func applicationSourceSafeRoot(group projects.DeclarationWatchGroup, item projects.DeclarationWatchRoot, cfg watchedroots.RootConfig) filesystemconnector.SafeRoot {
	var source struct {
		Resource string `json:"resource_key"`
	}
	_ = json.Unmarshal(item.KnowledgeSource, &source)
	m := applicationSourceMetadata{Source: "project.application_data", ProjectID: group.ProjectID, ProjectRoot: group.ProjectRoot, NodeID: group.NodeID, Resource: source.Resource, Binding: item.ApplicationData}
	// This root is private input to the declaration-owned watcher, not another
	// general filesystem capability. The watcher validates current enrollment
	// before scanning/uploading; direct filesystem dispatch must not bypass it.
	return filesystemconnector.SafeRoot{RootKey: cfg.SafeRootKey, DisplayName: item.LocalRootKey, AbsolutePath: item.ApplicationData.Path, MaxFileBytes: watchedRootRequiredSafeRootMaxFileBytes(cfg), Metadata: projectWatchJSON(m)}
}

func validateQueuedApplicationSource(ctx context.Context, store Store, config Config, state State, object LocalSyncObject) error {
	rootKey := metadataString(object.Metadata, "watched_root")
	if rootKey == "" {
		return nil
	}
	instance, cfg, err := loadWatchedRootInstance(store.DataDir, rootKey)
	for _, safe := range config.Filesystem.SafeRoots {
		if !strings.HasPrefix(safe.RootKey, "declaration_app_") || !strings.HasPrefix(object.ContentPath, safe.AbsolutePath+"/") {
			continue
		}
		if err != nil || !instance.Enabled {
			return fmt.Errorf("application_data_source_withdrawn")
		}
		if cfg.SafeRootKey != safe.RootKey {
			continue
		}
		return validateApplicationSource(ctx, config, state, safe, cfg)
	}
	if err == nil && strings.HasPrefix(cfg.SafeRootKey, "declaration_app_") {
		return fmt.Errorf("application_data_source_unavailable")
	}
	return nil
}
