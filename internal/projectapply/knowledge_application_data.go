package projectapply

import (
	"context"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	sr "loom.local/loom/internal/serviceregistry"
)

func (r *LocalResolver) bindKnowledgeAllocations(ctx context.Context, x localDeclaration, group *projects.DeclarationWatchGroup) error {
	for i := range group.Roots {
		root := &group.Roots[i]
		ref, err := projectwatch.ApplicationDataReference(root.Metadata)
		if err != nil {
			return fail(pc.DeclarationInvalid, "knowledge_source_invalid")
		}
		if ref == nil {
			continue
		}
		if nilInterface(r.Applications) {
			return fail(pc.DeclarationTargetUnavailable, "application_data_owner_unavailable")
		}
		owner := sr.ApplicationOwner{ProjectID: x.Target.ProjectID, NodeID: x.Target.OwnerNodeID, Resource: string(ref.Application)}
		facts, err := r.Applications.QueryPrerequisites(ctx, sr.ApplicationPrerequisiteQuery{SchemaVersion: sr.ApplicationPrerequisiteSchema, Owner: owner, SourceData: string(ref.Data)})
		if err != nil {
			return applicationFailure(err)
		}
		app := x.Analysis.Loaded.Declaration.Resources[ref.Application].Application
		if app == nil {
			return fail(pc.DeclarationInvalid, "application_data_source_missing")
		}
		root.ApplicationData, err = projectwatch.BindApplicationData(*ref, app.Data[ref.Data].BindingRef, owner.ProjectID, owner.NodeID, x.Target.LocationRevision, facts)
		if err != nil {
			return fail(pc.DeclarationTargetUnavailable, "application_data_allocation_required")
		}
	}
	return nil
}
