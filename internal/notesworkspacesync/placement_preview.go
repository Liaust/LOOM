package notesworkspacesync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"loom.local/loom/internal/knowledge"
	pc "loom.local/loom/internal/projectcontracts"
)

// PreviewProjectPlacement uses the runtime's retained enrollment and resolver.
// It publishes nothing and reads no document bodies or native client settings.
func PreviewProjectPlacement(ctx context.Context, db *sql.DB, cfg RuntimeConfig, nodeKey, projectID, projectName, projectRoot, workState string, declaration pc.ProjectDeclaration) ([]pc.DeclarationNotesPlacement, error) {
	resources := declaration.Resources
	var ids []pc.ResourceKey
	for key, resource := range resources {
		if resource.Knowledge != nil {
			ids = append(ids, key)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	previous, err := readEnrollment(enrollmentPath(cfg))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	runtime := Runtime{Config: cfg, DB: db, Knowledge: knowledge.NewService(db), NodeKey: nodeKey}
	candidates, err := runtime.enrollmentCandidates(ctx)
	if err != nil {
		return nil, err
	}
	// Identity survives an ACK/config handover even while admission is held.
	// This is preview metadata only; runtime source access remains fenced.
	retainedRoots, err := runtime.Knowledge.Store().ListSourceRoots(ctx, knowledge.SourceRootFilter{ProjectID: projectID, NodeKey: nodeKey, IncludeInactive: true})
	if err != nil {
		return nil, err
	}
	var previews []pc.DeclarationNotesPlacement
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, resource := range ids {
		k := *resources[resource].Knowledge
		policy, err := pc.DeclarationKnowledgePolicy(declaration, resource)
		if err != nil {
			return nil, err
		}
		relative := pc.KnowledgeLogicalPath(resource, k)
		root, err := declarationDisplayRoot(declaration, projectID, relative, projectName)
		if err != nil {
			return nil, err
		}
		candidate := enrollmentCandidate{ID: "preview_" + string(resource), ProjectID: projectID, Root: root, RelativePath: relative}
		found := false
		for index, current := range candidates {
			if current.ProjectID == projectID && current.RelativePath == relative {
				candidate = current
				candidates = append(candidates[:index:index], candidates[index+1:]...)
				found = true
				break
			}
		}
		if !found {
			for _, current := range retainedRoots {
				if current.RootKind == knowledge.RootKindProjectMaterial && current.RootRelativePath == relative {
					candidate.ID, found = current.NotesSourceRootID, true
					break
				}
			}
		}
		candidate.Root = root
		candidate.WorkState = workState
		candidate.ExplicitRoot = ""
		indexInclude, indexExclude := k.Include, k.Exclude
		if declaration.SchemaVersion == pc.ProjectSchemaV06 && k.Exclude == nil {
			indexExclude = pc.DefaultDeviceFileExcludes()
		}
		if len(indexInclude) == 0 {
			indexInclude = []string{"**/*"}
		}
		p := pc.DeclarationNotesPlacement{Resource: resource, SourcePath: filepath.Join(projectRoot, relative), IndexInclude: indexInclude, IndexExclude: append([]string{}, indexExclude...), WorkspaceInclude: indexInclude, WorkspaceExclude: append([]string{}, indexExclude...), Change: "enroll"}
		if k.ApplicationData != nil {
			p.SourcePath = relative // The actual allocation remains application-owner resolved.
		}
		if found {
			p.SourceCollection = candidate.ID
		}
		if policy != nil && policy.Workspace != nil {
			candidate.ExplicitRoot = policy.Workspace.Path
			p.WorkspaceInclude, p.WorkspaceExclude = policy.Workspace.Include, append([]string{}, policy.Workspace.Exclude...)
		}
		for _, old := range previous.Collections {
			if old.SourceCollection == candidate.ID {
				p.CurrentRoot, p.SourceStatus = old.Root, old.Status
			}
		}
		candidates = append(candidates, candidate)
		previews = append(previews, p)
	}
	resolved, err := mergeEnrollmentSelections(cfg, previous, candidates)
	if err != nil {
		return nil, err
	}
	for index := range previews {
		id := previews[index].SourceCollection
		if id == "" {
			id = "preview_" + string(previews[index].Resource)
		}
		for _, selected := range resolved {
			if selected.SourceCollection == id {
				previews[index].DesiredRoot = selected.Root
			}
		}
		if previews[index].DesiredRoot == previews[index].CurrentRoot {
			previews[index].Change = "unchanged"
		} else if previews[index].CurrentRoot != "" {
			previews[index].Change = "layout"
		}
	}
	// Intent is bound by normal declaration source hashes; these roots describe
	// observed delivery posture. Current() revalidates collisions before apply.
	return previews, nil
}

func declarationDisplayRoot(declaration pc.ProjectDeclaration, projectID, relative, name string) (string, error) {
	metadata, _ := json.Marshal(map[string]any{"registration_metadata": map[string]any{"knowledge_source": map[string]any{"schema_version": declaration.SchemaVersion}}})
	return enrollmentDisplayRoot(knowledge.SourceRoot{ProjectID: &projectID, RootRelativePath: relative, Metadata: metadata}, name)
}
