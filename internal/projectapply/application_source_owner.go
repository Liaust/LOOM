package projectapply

import (
	"context"
	"encoding/json"
	"strings"

	"loom.local/loom/internal/capabilities"
	pc "loom.local/loom/internal/projectcontracts"
)

func validateSourceAction(action pc.DeclarationAction, p applicationPayload) error {
	if p.Source == nil || p.Browse != nil || p.Preparation != nil || p.Request.Manifest != nil || p.Request.Owner.ProjectID != "" || action.ID != "apply_application:"+string(action.Resource) || action.Kind != pc.DeclarationApplyApplication || action.Owner != pc.DeclarationOwnerApplication || action.Resource != p.Source.Resource || action.TargetRef != p.Source.Target.ProjectID+"/"+string(action.Resource) || action.PublicURL != "" {
		return fail(pc.DeclarationInvalid, "application_source_action_invalid")
	}
	address, err := capabilities.ParseAddress(p.Endpoint)
	if err != nil || p.Endpoint != capabilities.NodeSystemProviderAddress(strings.TrimPrefix(address.ScopePath, "workspace/"))+".project.application.apply" {
		return fail(pc.DeclarationInvalid, "application_source_endpoint_invalid")
	}
	_, err = validateSourceApplicationPayload(*p.Source)
	return err
}

func (o ApplicationOwner) materializeSource(ctx context.Context, a pc.DeclarationAction, p applicationPayload, build bool) (json.RawMessage, error) {
	if err := validateSourceAction(a, p); err != nil {
		return nil, err
	}
	if o.Source == nil {
		return nil, fail(pc.DeclarationTargetUnavailable, "application_source_not_configured")
	}
	resolved, err := o.Source.Resolve(ctx, *p.Source, build)
	if err != nil {
		return nil, err
	}
	resolved.Endpoint = p.Endpoint
	return canonicalValue(resolved)
}

func (r *LocalResolver) appendSourceApplication(ctx context.Context, x localDeclaration, key pc.ResourceKey, declaration pc.ApplicationDeclaration, basis *pc.DeclarationPlanBasis, payloads map[string]json.RawMessage) error {
	if !x.Authority.CanWrite || x.Authority.Level < 5 {
		return fail(pc.DeclarationUnauthorized, "application_execution_authority_required")
	}
	if r.SourceApplications == nil {
		return fail(pc.DeclarationTargetUnavailable, "application_source_not_configured")
	}
	source, err := r.SourceApplications.Snapshot(ctx, x.Target, key, declaration)
	if err != nil {
		return err
	}
	endpoint := capabilities.NodeSystemProviderAddress(x.Input.Project.OwnerNode) + ".project.application.apply"
	payload, err := canonicalValue(applicationPayload{Endpoint: endpoint, Source: &source})
	if err != nil {
		return err
	}
	auth := basis.Actions[0].Authorization
	auth.Level = 5
	action := pc.DeclarationAction{ID: "apply_application:" + string(key), Kind: pc.DeclarationApplyApplication, Owner: pc.DeclarationOwnerApplication, Resource: key, TargetRef: x.Target.ProjectID + "/" + string(key), InputHash: hashBytes(payload), DependsOn: []string{basis.Actions[len(basis.Actions)-1].ID}, Authorization: auth}
	basis.Actions = append(basis.Actions, action)
	payloads[action.ID] = payload
	basis.Sources = append(basis.Sources, sourceApplicationEvidence(key, source.ExpectedSourceDigest))
	if source.SourceSnapshot.ProducerDigest != "" {
		basis.Sources = append(basis.Sources, sourceApplicationProducerEvidence(key, source.SourceSnapshot.ProducerDigest))
	}
	return nil
}

func sourceApplicationEvidence(key pc.ResourceKey, digest string) pc.DeclarationSource {
	return pc.DeclarationSource{Ref: "application-source:" + string(key), SchemaVersion: "application.source.v1", Hash: digest, Revision: digest}
}

func sourceApplicationProducerEvidence(key pc.ResourceKey, digest string) pc.DeclarationSource {
	return pc.DeclarationSource{Ref: "application-producer:" + string(key), SchemaVersion: "application.producer.v1", Hash: digest, Revision: digest}
}

func (r *LocalResolver) currentApplicationSources(ctx context.Context, x localDeclaration, reviewed []pc.DeclarationSource, current *Prerequisites) error {
	for _, evidence := range reviewed {
		name, source := strings.CutPrefix(evidence.Ref, "application-source:")
		producer := false
		if !source {
			name, producer = strings.CutPrefix(evidence.Ref, "application-producer:")
		}
		if !source && !producer {
			continue
		}
		key := pc.ResourceKey(name)
		resource, exists := x.Analysis.Loaded.Declaration.Resources[key]
		if !exists || resource.Application == nil || resource.Application.Source == nil || r.SourceApplications == nil || nilInterface(r.SourceApplications.Builder) {
			return fail(pc.DeclarationPlanStale, "application_source_changed")
		}
		input, err := sourceApplicationInput(x.Target, key, *resource.Application)
		if err != nil {
			return err
		}
		snapshot, err := r.SourceApplications.Builder.Snapshot(ctx, input)
		if err != nil {
			return sourceApplicationFailure(ctx, err)
		}
		if producer {
			current.Sources = append(current.Sources, sourceApplicationProducerEvidence(key, snapshot.ProducerDigest))
		} else {
			current.Sources = append(current.Sources, sourceApplicationEvidence(key, snapshot.SourceDigest))
		}
	}
	return nil
}
