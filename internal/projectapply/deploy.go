package projectapply

import (
	"context"
	"slices"

	pc "loom.local/loom/internal/projectcontracts"
)

type deploymentSelector interface {
	DeploymentSelection(context.Context, Principal, pc.ProjectDeploymentRequest) (pc.ResourceKey, string, error)
}

func (r *LocalResolver) DeploymentSelection(ctx context.Context, p Principal, request pc.ProjectDeploymentRequest) (pc.ResourceKey, string, error) {
	x, err := r.load(ctx, p, request.ProjectRef, request.NodeRef)
	if err != nil {
		return "", "", err
	}
	keys := []pc.ResourceKey{}
	for key, res := range x.Analysis.Loaded.Declaration.Resources {
		if res.Application != nil && (request.Resource == "" || key == request.Resource) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	if len(keys) != 1 {
		return "", "", fail(pc.DeclarationInvalid, "select_one_application_resource")
	}
	network := x.Analysis.Loaded.Declaration.Resources[keys[0]].Application.Network
	if network == "" {
		network = "loopback"
	}
	return keys[0], network, nil
}

func (s *Service) Deploy(ctx context.Context, p Principal, input pc.ProjectDeploymentRequest) (pc.ProjectDeploymentResult, error) {
	var out pc.ProjectDeploymentResult
	if err := s.config(!input.Check); err != nil {
		return out, err
	}
	if err := validPrincipal(p); err != nil {
		return out, err
	}
	selector, ok := s.resolver.(deploymentSelector)
	if !ok {
		return out, fail(pc.DeclarationUnsupported, "application_deployment_not_configured")
	}
	key, network, err := selector.DeploymentSelection(ctx, p, input)
	if err != nil {
		return out, err
	}
	request := pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: input.ProjectRef, NodeRef: input.NodeRef, Effects: []pc.DeclarationEffect{pc.ApplicationDeployEffect(key)}}
	x, err := s.resolve(ctx, p, request)
	if err != nil {
		return out, err
	}
	b := x.Plan.Basis
	out = pc.ProjectDeploymentResult{ProjectID: b.Target.ProjectID, NodeID: b.Target.OwnerNodeID, Resource: key, Network: network, State: "checked", Scope: "Loopback only; no public exposure."}
	for _, payload := range x.Payloads {
		var app applicationPayload
		if strictOwnerPayload(payload, &app) == nil && app.Source != nil && app.Source.SourceSnapshot.Program != nil {
			out.Builder = app.Source.SourceSnapshot.Program.Build.Kind
			out.Program = app.Source.SourceSnapshot.Program.Run.Binary
			if out.Program == "" {
				out.Program = app.Source.SourceSnapshot.Program.Run.PythonModule
			}
		}
	}
	if network == "outbound_https" {
		out.Scope = "Public-destination TCP/443, configured DNS, no private/control-plane access or public inbound ports. Not Google-only."
	}
	if input.Check {
		out.Plan = &x.Plan
		return out, nil
	}
	// Source/selection identity is stable across registration and application
	// receipts. A lost connection therefore finds the exact original request.
	raw, err := canonicalValue(struct {
		Target  pc.DeclarationTarget
		Sources []pc.DeclarationSource
		Effects []pc.DeclarationEffect
	}{b.Target, b.Sources, b.Effects})
	if err != nil {
		return out, err
	}
	replay := "project-deploy:" + hashBytes(raw)
	operation, err := findOperation(ctx, s.db, p, b.Target.ProjectID, replay)
	if err != nil {
		return out, err
	}
	apply := pc.DeclarationApplyRequest{SchemaVersion: request.SchemaVersion, ProjectRef: request.ProjectRef, NodeRef: request.NodeRef, Effects: request.Effects, PlanID: x.Plan.PlanID, IdempotencyKey: replay}
	if operation != "" {
		previous, err := loadOperation(ctx, s.db, operation)
		if err != nil {
			return out, err
		}
		if previous.Principal != p || previous.Request.IdempotencyKey != replay {
			return out, fail(pc.DeclarationUnauthorized, "deployment_operation_mismatch")
		}
		apply = previous.Request
		apply.OperationID = operation
	}
	result, err := s.Apply(ctx, p, apply)
	if result.OperationID != "" {
		out.Operation = &result
		out.State = string(result.State)
		if out.Builder != "" && s.ApplicationBuilds != nil {
			op, loadErr := loadOperation(ctx, s.db, result.OperationID)
			if loadErr == nil {
				for _, a := range op.Resolution.Plan.Basis.Actions {
					if a.ID == "apply_application:"+string(key) {
						job, e := s.ApplicationBuilds.Jobs.ApplicationBuild(ctx, hashBytes([]byte(op.ID+"\x00"+a.ID+"\x00"+a.InputHash)))
						if e == nil {
							out.BuildJobID = job.JobID
						}
					}
				}
			}
		}
	}
	if out.BuildJobID != "" && applicationBuildPending(err) {
		err = nil
	}
	return out, err
}
