package capabilities

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/requestctx"
)

type SeedResult struct {
	ProvidersCreated      int `json:"providers_created"`
	ClassesCreated        int `json:"classes_created"`
	EndpointsCreated      int `json:"endpoints_created"`
	VersionsCreated       int `json:"versions_created"`
	UsageDocumentsCreated int `json:"usage_documents_created"`
}

const (
	ProvenanceHealthReadCapability         = "main@provenance.health.read"
	ProvenanceFoundationReadCapability     = "main@provenance.foundation.read"
	ProvenanceRegisterCapability           = "main@provenance.candidate.register"
	ProvenanceLifecycleCapability          = "main@provenance.lifecycle.apply"
	WorkspaceArchivePlanCapability         = "main@workspace-archive.archive.plan"
	WorkspaceArchiveApplyCapability        = "main@workspace-archive.archive.apply"
	WorkspaceArchiveInspectCapability      = "main@workspace-archive.operation.inspect"
	WorkspaceArchiveRecoverCapability      = "main@workspace-archive.operation.recover"
	WorkspaceArchiveRestorePlanCapability  = "main@workspace-archive.restore.plan"
	WorkspaceArchiveRestoreApplyCapability = "main@workspace-archive.restore.apply"
)

func SeedMainNodeRegistry(ctx context.Context, req requestctx.Context, service Service) (SeedResult, error) {
	result := SeedResult{}

	system, created, err := ensureProvider(ctx, req, service, RegisterProviderInput{
		ProviderKey:           "system",
		CompactAddress:        "main@system",
		DisplayName:           "System",
		Description:           "Main-node system health and status provider.",
		ProviderType:          ProviderTypeSystem,
		NodeRef:               req.OriginNodeID,
		ScopeRef:              req.ScopeID,
		Status:                ProviderStatusActive,
		RuntimeProfileJSON:    json.RawMessage(`{"runtime":"loomd","execution":"local"}`),
		DocumentationRefsJSON: json.RawMessage(`[]`),
		Metadata:              json.RawMessage(`{"seed":"slice_7"}`),
	})
	if err != nil {
		return result, err
	}
	if created {
		result.ProvidersCreated++
	}
	if _, err := service.UpsertProviderHealth(ctx, req, system.ProviderID, ProviderHealthInput{
		HealthStatus:       HealthStatusOK,
		AvailabilityStatus: AvailabilityStatusAvailable,
		Message:            "seeded provider available",
		DetailsJSON:        json.RawMessage(`{"seed":"slice_7"}`),
	}); err != nil {
		return result, err
	}

	objectStore, created, err := ensureProvider(ctx, req, service, RegisterProviderInput{
		ProviderKey:           "object-store",
		CompactAddress:        "main@object-store",
		DisplayName:           "Object Store",
		Description:           "Main-node object and artifact provider.",
		ProviderType:          ProviderTypeObjectStore,
		NodeRef:               req.OriginNodeID,
		ScopeRef:              req.ScopeID,
		Status:                ProviderStatusActive,
		RuntimeProfileJSON:    json.RawMessage(`{"runtime":"loomd","execution":"local"}`),
		DocumentationRefsJSON: json.RawMessage(`[]`),
		Metadata:              json.RawMessage(`{"seed":"slice_7"}`),
	})
	if err != nil {
		return result, err
	}
	if created {
		result.ProvidersCreated++
	}
	if _, err := service.UpsertProviderHealth(ctx, req, objectStore.ProviderID, ProviderHealthInput{
		HealthStatus:       HealthStatusOK,
		AvailabilityStatus: AvailabilityStatusAvailable,
		Message:            "seeded provider available",
		DetailsJSON:        json.RawMessage(`{"seed":"slice_7"}`),
	}); err != nil {
		return result, err
	}

	scriptRunner, created, err := ensureProvider(ctx, req, service, RegisterProviderInput{
		ProviderKey:           "script-runner",
		CompactAddress:        "main@script-runner",
		DisplayName:           "Script Runner",
		Description:           "Main-node script, job, and log provider.",
		ProviderType:          ProviderTypeScriptRunner,
		NodeRef:               req.OriginNodeID,
		ScopeRef:              req.ScopeID,
		Status:                ProviderStatusActive,
		RuntimeProfileJSON:    json.RawMessage(`{"runtime":"loomd","execution":"local"}`),
		DocumentationRefsJSON: json.RawMessage(`[]`),
		Metadata:              json.RawMessage(`{"seed":"slice_7"}`),
	})
	if err != nil {
		return result, err
	}
	if created {
		result.ProvidersCreated++
	}
	if _, err := service.UpsertProviderHealth(ctx, req, scriptRunner.ProviderID, ProviderHealthInput{
		HealthStatus:       HealthStatusOK,
		AvailabilityStatus: AvailabilityStatusAvailable,
		Message:            "seeded provider available",
		DetailsJSON:        json.RawMessage(`{"seed":"slice_7"}`),
	}); err != nil {
		return result, err
	}

	admin, created, err := ensureProvider(ctx, req, service, RegisterProviderInput{
		ProviderKey:           "admin",
		CompactAddress:        "main@admin",
		DisplayName:           "Admin",
		Description:           "Main-node policy, approval, grant, and security audit provider.",
		ProviderType:          ProviderTypeSystem,
		NodeRef:               req.OriginNodeID,
		ScopeRef:              req.ScopeID,
		Status:                ProviderStatusActive,
		RuntimeProfileJSON:    json.RawMessage(`{"runtime":"loomd","execution":"local"}`),
		DocumentationRefsJSON: json.RawMessage(`[]`),
		Metadata:              json.RawMessage(`{"seed":"slice_8"}`),
	})
	if err != nil {
		return result, err
	}
	if created {
		result.ProvidersCreated++
	}
	if _, err := service.UpsertProviderHealth(ctx, req, admin.ProviderID, ProviderHealthInput{
		HealthStatus:       HealthStatusOK,
		AvailabilityStatus: AvailabilityStatusAvailable,
		Message:            "seeded provider available",
		DetailsJSON:        json.RawMessage(`{"seed":"slice_8"}`),
	}); err != nil {
		return result, err
	}

	provenanceProvider, created, err := ensureProvider(ctx, req, service, RegisterProviderInput{
		ProviderKey:           "provenance",
		CompactAddress:        "main@provenance",
		DisplayName:           "Provenance",
		Description:           "Authorized bounded provenance foundation provider.",
		ProviderType:          ProviderTypeSystem,
		NodeRef:               req.OriginNodeID,
		ScopeRef:              req.ScopeID,
		Status:                ProviderStatusActive,
		RuntimeProfileJSON:    json.RawMessage(`{"runtime":"loomd","execution":"local"}`),
		DocumentationRefsJSON: json.RawMessage(`[]`),
		Metadata:              json.RawMessage(`{"seed":"provenance_runtime_foundation_slice_4"}`),
	})
	if err != nil {
		return result, err
	}
	if created {
		result.ProvidersCreated++
	}
	if _, err := service.UpsertProviderHealth(ctx, req, provenanceProvider.ProviderID, ProviderHealthInput{
		HealthStatus:       HealthStatusOK,
		AvailabilityStatus: AvailabilityStatusAvailable,
		Message:            "seeded provider available",
		DetailsJSON:        json.RawMessage(`{"seed":"provenance_runtime_foundation_slice_4"}`),
	}); err != nil {
		return result, err
	}

	workspaceArchiveProvider, created, err := ensureProvider(ctx, req, service, RegisterProviderInput{
		ProviderKey:           "workspace-archive",
		CompactAddress:        "main@workspace-archive",
		DisplayName:           "Workspace Archive",
		Description:           "Typed workspace archive lifecycle operations without raw storage authority.",
		ProviderType:          ProviderTypeSystem,
		NodeRef:               req.OriginNodeID,
		ScopeRef:              req.ScopeID,
		Status:                ProviderStatusActive,
		RuntimeProfileJSON:    json.RawMessage(`{"runtime":"loomd","execution":"local"}`),
		DocumentationRefsJSON: json.RawMessage(`[]`),
		Metadata:              json.RawMessage(`{"seed":"workspace_archive_lifecycle_slice_3","raw_storage_authority":false}`),
	})
	if err != nil {
		return result, err
	}
	if created {
		result.ProvidersCreated++
	}
	if _, err := service.UpsertProviderHealth(ctx, req, workspaceArchiveProvider.ProviderID, ProviderHealthInput{
		HealthStatus: HealthStatusOK, AvailabilityStatus: AvailabilityStatusAvailable,
		Message:     "seeded provider available",
		DetailsJSON: json.RawMessage(`{"seed":"workspace_archive_lifecycle_slice_3"}`),
	}); err != nil {
		return result, err
	}

	classByName := map[string]CapabilityClass{}
	classInputs := []RegisterCapabilityClassInput{
		classInput("system", "health.read", "System Health Read", CapabilityFormQuery, RiskLevelLow),
		classInput("system", "status.read", "System Status Read", CapabilityFormQuery, RiskLevelLow),
		classInput("object", "object.inspect", "Object Inspect", CapabilityFormQuery, RiskLevelLow),
		classInput("object", "object.ingest", "Object Ingest", CapabilityFormCommand, RiskLevelMedium),
		classInput("object", "artifact.inspect", "Artifact Inspect", CapabilityFormQuery, RiskLevelLow),
		classInput("execution", "script.register", "Script Register", CapabilityFormCommand, RiskLevelMedium),
		classInput("execution", "script.run", "Script Run", CapabilityFormJob, RiskLevelHigh),
		classInput("execution", "job.inspect", "Job Inspect", CapabilityFormQuery, RiskLevelLow),
		classInput("execution", "job.logs.read", "Job Logs Read", CapabilityFormQuery, RiskLevelLow),
		classInput("admin", "policy.explain", "Policy Explain", CapabilityFormQuery, RiskLevelLow),
		classInput("admin", "policy.decision.read", "Policy Decision Read", CapabilityFormQuery, RiskLevelLow),
		classInput("admin", "approval.list", "Approval List", CapabilityFormQuery, RiskLevelLow),
		classInput("admin", "approval.read", "Approval Read", CapabilityFormQuery, RiskLevelLow),
		classInput("admin", "approval.decide", "Approval Decide", CapabilityFormCommand, RiskLevelHigh),
		classInput("admin", "grant.list", "Grant List", CapabilityFormQuery, RiskLevelLow),
		classInput("admin", "grant.read", "Grant Read", CapabilityFormQuery, RiskLevelLow),
		classInput("admin", "grant.revoke", "Grant Revoke", CapabilityFormCommand, RiskLevelHigh),
		classInput("admin", "security.audit.read", "Security Audit Read", CapabilityFormQuery, RiskLevelHigh),
	}
	classInputs = append(classInputs, ProvenanceCapabilityClassInputs()...)
	classInputs = append(classInputs, WorkspaceArchiveCapabilityClassInputs()...)
	classInputs = append(classInputs, ServiceCapabilityClassInputs()...)
	for _, input := range classInputs {
		class, created, err := ensureCapabilityClass(ctx, req, service, input)
		if err != nil {
			return result, err
		}
		if created {
			result.ClassesCreated++
		}
		classByName[input.Namespace+"."+input.Name] = class
	}

	endpointByAddress := map[string]CapabilityEndpoint{}
	endpointInputs := []RegisterCapabilityEndpointInput{
		endpointInput(system, classByName["system.health.read"], "health.read", CapabilityFormQuery, RiskLevelLow, 1),
		endpointInput(system, classByName["system.status.read"], "status.read", CapabilityFormQuery, RiskLevelLow, 1),
		endpointInput(objectStore, classByName["object.object.inspect"], "object.inspect", CapabilityFormQuery, RiskLevelLow, 1),
		endpointInput(objectStore, classByName["object.object.ingest"], "object.ingest", CapabilityFormCommand, RiskLevelMedium, 3),
		endpointInput(objectStore, classByName["object.artifact.inspect"], "artifact.inspect", CapabilityFormQuery, RiskLevelLow, 1),
		endpointInput(scriptRunner, classByName["execution.script.register"], "script.register", CapabilityFormCommand, RiskLevelMedium, 3),
		endpointInput(scriptRunner, classByName["execution.script.run"], "script.run", CapabilityFormJob, RiskLevelHigh, 4),
		endpointInput(scriptRunner, classByName["execution.job.inspect"], "job.inspect", CapabilityFormQuery, RiskLevelLow, 1),
		endpointInput(scriptRunner, classByName["execution.job.logs.read"], "job.logs.read", CapabilityFormQuery, RiskLevelLow, 2),
		endpointInput(admin, classByName["admin.policy.explain"], "policy.explain", CapabilityFormQuery, RiskLevelLow, 1),
		endpointInput(admin, classByName["admin.policy.decision.read"], "policy.decision.read", CapabilityFormQuery, RiskLevelLow, 2),
		endpointInput(admin, classByName["admin.approval.list"], "approval.list", CapabilityFormQuery, RiskLevelLow, 2),
		endpointInput(admin, classByName["admin.approval.read"], "approval.read", CapabilityFormQuery, RiskLevelLow, 2),
		endpointInput(admin, classByName["admin.approval.decide"], "approval.decide", CapabilityFormCommand, RiskLevelHigh, 5),
		endpointInput(admin, classByName["admin.grant.list"], "grant.list", CapabilityFormQuery, RiskLevelLow, 2),
		endpointInput(admin, classByName["admin.grant.read"], "grant.read", CapabilityFormQuery, RiskLevelLow, 2),
		endpointInput(admin, classByName["admin.grant.revoke"], "grant.revoke", CapabilityFormCommand, RiskLevelHigh, 5),
		endpointInput(admin, classByName["admin.security.audit.read"], "security.audit.read", CapabilityFormQuery, RiskLevelHigh, 4),
		endpointInput(provenanceProvider, classByName["provenance.health.read"], "health.read", CapabilityFormQuery, RiskLevelLow, 1),
		endpointInput(provenanceProvider, classByName["provenance.foundation.read"], "foundation.read", CapabilityFormQuery, RiskLevelLow, 2),
		endpointInput(provenanceProvider, classByName["provenance.candidate.register"], "candidate.register", CapabilityFormCommand, RiskLevelMedium, 3),
		endpointInput(provenanceProvider, classByName["provenance.lifecycle.apply"], "lifecycle.apply", CapabilityFormCommand, RiskLevelHigh, 4),
	}
	for _, spec := range WorkspaceArchiveCapabilitySpecs() {
		endpointInputs = append(endpointInputs, endpointInput(workspaceArchiveProvider, classByName["workspace_archive."+spec.Name], spec.Name, spec.Form, spec.Risk, spec.AuthorizationLevel))
	}
	for _, input := range endpointInputs {
		endpoint, created, err := ensureCapabilityEndpoint(ctx, req, service, input)
		if err != nil {
			return result, err
		}
		if created {
			result.EndpointsCreated++
		}
		_, versionCreated, err := ensureEndpointVersion(ctx, req, service, RegisterEndpointVersionInput{
			CapabilityEndpointRef:       endpoint.CapabilityEndpointID,
			VersionLabel:                "0.1.0",
			ManifestJSON:                json.RawMessage(`{"seed":"slice_7"}`),
			InputSchemaJSON:             endpoint.InputSchemaJSON,
			OutputSchemaJSON:            endpoint.OutputSchemaJSON,
			RiskLevel:                   endpoint.RiskLevel,
			ExecutionAuthorizationLevel: endpoint.ExecutionAuthorizationLevel,
			PolicyRequirementsJSON:      endpoint.PolicyRequirementsJSON,
			CredentialRequirementsJSON:  endpoint.CredentialRequirementsJSON,
			ApprovalRequirementsJSON:    endpoint.ApprovalRequirementsJSON,
			Status:                      EndpointVersionStatusActive,
			ApprovedByActorID:           req.ActorID,
			Metadata:                    json.RawMessage(`{"seed":"slice_7"}`),
		})
		if err != nil {
			return result, err
		}
		endpointByAddress[endpoint.CompactAddress] = endpoint
		if versionCreated {
			result.VersionsCreated++
		}
	}

	approvedAt := time.Now().UTC()
	for _, doc := range []struct {
		address string
		title   string
		body    string
	}{
		{
			address: "main@system.status.read",
			title:   "Use main@system.status.read",
			body:    statusReadUsageDocumentBody(),
		},
		{
			address: "main@object-store.object.ingest",
			title:   "Use main@object-store.object.ingest",
			body:    objectIngestUsageDocumentBody(),
		},
		{
			address: "main@script-runner.script.run",
			title:   "Use main@script-runner.script.run",
			body:    scriptRunUsageDocumentBody(),
		},
		{
			address: "main@admin.policy.explain",
			title:   "Use main@admin.policy.explain",
			body:    adminPolicyExplainUsageDocumentBody(),
		},
		{
			address: "main@admin.approval.decide",
			title:   "Use main@admin.approval.decide",
			body:    adminApprovalDecideUsageDocumentBody(),
		},
		{
			address: "main@admin.grant.revoke",
			title:   "Use main@admin.grant.revoke",
			body:    adminGrantRevokeUsageDocumentBody(),
		},
	} {
		endpoint, ok := endpointByAddress[doc.address]
		if !ok {
			return result, fmt.Errorf("seed endpoint missing for usage document: %s", doc.address)
		}
		_, created, err := ensureUsageDocument(ctx, req, service, usageDocumentInput(req, endpoint, doc.title, doc.body, approvedAt))
		if err != nil {
			return result, err
		}
		if created {
			result.UsageDocumentsCreated++
		}
	}
	for _, operation := range []string{"status", "start", "stop", "restart", "logs"} {
		class := classByName["service."+operation]
		body := "# service." + operation + "\n\nOperate only an already-provisioned unit selected by a reviewed node allowlist. Arbitrary commands, unit text, paths, packages, and secret values are not accepted.\n"
		_, created, err := ensureUsageDocument(ctx, req, service, usageDocumentClassInput(req, class, "Use service."+operation, body, approvedAt))
		if err != nil {
			return result, err
		}
		if created {
			result.UsageDocumentsCreated++
		}
	}

	return result, nil
}

func ProvenanceCapabilityClassInputs() []RegisterCapabilityClassInput {
	inputs := []RegisterCapabilityClassInput{
		classInput("provenance", "health.read", "Provenance Health Read", CapabilityFormQuery, RiskLevelLow),
		classInput("provenance", "foundation.read", "Provenance Foundation Read", CapabilityFormQuery, RiskLevelLow),
		classInput("provenance", "candidate.register", "Provenance Candidate Register", CapabilityFormCommand, RiskLevelMedium),
		classInput("provenance", "lifecycle.apply", "Provenance Lifecycle Apply", CapabilityFormCommand, RiskLevelHigh),
	}
	for index := range inputs {
		inputs[index].Metadata = json.RawMessage(`{"seed":"provenance_runtime_foundation_slice_4"}`)
	}
	return inputs
}

func WorkspaceArchiveCapabilityClassInputs() []RegisterCapabilityClassInput {
	inputs := []RegisterCapabilityClassInput{
		classInput("workspace_archive", "archive.plan", "Workspace Archive Plan", CapabilityFormQuery, RiskLevelLow),
		classInput("workspace_archive", "archive.apply", "Workspace Archive Apply", CapabilityFormCommand, RiskLevelHigh),
		classInput("workspace_archive", "operation.inspect", "Workspace Archive Inspect", CapabilityFormQuery, RiskLevelLow),
		classInput("workspace_archive", "operation.recover", "Workspace Archive Recover", CapabilityFormCommand, RiskLevelHigh),
		classInput("workspace_archive", "restore.plan", "Workspace Restore Plan", CapabilityFormQuery, RiskLevelLow),
		classInput("workspace_archive", "restore.apply", "Workspace Restore Apply", CapabilityFormCommand, RiskLevelHigh),
	}
	for index := range inputs {
		inputs[index].Metadata = json.RawMessage(`{"seed":"workspace_archive_lifecycle_slice_3","raw_storage_authority":false}`)
	}
	return inputs
}

type WorkspaceArchiveCapabilitySpec struct {
	Address            string
	Name               string
	Form               string
	Risk               string
	AuthorizationLevel int
}

func WorkspaceArchiveCapabilitySpecs() []WorkspaceArchiveCapabilitySpec {
	return []WorkspaceArchiveCapabilitySpec{
		{WorkspaceArchivePlanCapability, "archive.plan", CapabilityFormQuery, RiskLevelLow, 2},
		{WorkspaceArchiveApplyCapability, "archive.apply", CapabilityFormCommand, RiskLevelHigh, 4},
		{WorkspaceArchiveInspectCapability, "operation.inspect", CapabilityFormQuery, RiskLevelLow, 2},
		{WorkspaceArchiveRecoverCapability, "operation.recover", CapabilityFormCommand, RiskLevelHigh, 4},
		{WorkspaceArchiveRestorePlanCapability, "restore.plan", CapabilityFormQuery, RiskLevelLow, 2},
		{WorkspaceArchiveRestoreApplyCapability, "restore.apply", CapabilityFormCommand, RiskLevelHigh, 4},
	}
}

func ServiceCapabilityClassInputs() []RegisterCapabilityClassInput {
	inputs := []RegisterCapabilityClassInput{}
	for _, item := range []struct{ name, display, form, risk string }{
		{"status", "Service Status", CapabilityFormQuery, RiskLevelLow},
		{"start", "Service Start", CapabilityFormCommand, RiskLevelHigh},
		{"stop", "Service Stop", CapabilityFormCommand, RiskLevelHigh},
		{"restart", "Service Restart", CapabilityFormCommand, RiskLevelHigh},
		{"logs", "Service Logs", CapabilityFormQuery, RiskLevelLow},
	} {
		input := classInput("service", item.name, item.display, item.form, item.risk)
		input.Metadata = json.RawMessage(`{"seed":"service_registry_slice_3"}`)
		inputs = append(inputs, input)
	}
	return inputs
}

func classInput(namespace, name, displayName, form, risk string) RegisterCapabilityClassInput {
	return RegisterCapabilityClassInput{
		Namespace:                     namespace,
		Name:                          name,
		DisplayName:                   displayName,
		Description:                   fmt.Sprintf("Seeded capability class for %s.%s.", namespace, name),
		Form:                          form,
		InputSchemaJSON:               json.RawMessage(`{"type":"object"}`),
		OutputSchemaJSON:              json.RawMessage(`{"type":"object"}`),
		DefaultRiskLevel:              risk,
		DefaultPolicyRequirementsJSON: json.RawMessage(`{}`),
		Status:                        CapabilityClassStatusActive,
		Metadata:                      json.RawMessage(`{"seed":"slice_7"}`),
	}
}

func endpointInput(provider Provider, class CapabilityClass, name, form, risk string, level int) RegisterCapabilityEndpointInput {
	return RegisterCapabilityEndpointInput{
		ProviderRef:                 provider.ProviderID,
		CapabilityClassRef:          class.CapabilityClassID,
		EndpointName:                name,
		CompactAddress:              provider.CompactAddress + "." + name,
		Form:                        form,
		InputSchemaJSON:             json.RawMessage(`{"type":"object"}`),
		OutputSchemaJSON:            json.RawMessage(`{"type":"object"}`),
		RiskLevel:                   risk,
		ExecutionAuthorizationLevel: level,
		SideEffectsJSON:             json.RawMessage(`{}`),
		PolicyRequirementsJSON:      json.RawMessage(`{}`),
		CredentialRequirementsJSON:  json.RawMessage(`{}`),
		ApprovalRequirementsJSON:    json.RawMessage(`{}`),
		JobBehaviorJSON:             json.RawMessage(`{}`),
		SessionBehaviorJSON:         json.RawMessage(`{}`),
		StreamBehaviorJSON:          json.RawMessage(`{}`),
		LeaseBehaviorJSON:           json.RawMessage(`{}`),
		Status:                      EndpointStatusActive,
		Metadata:                    json.RawMessage(`{"seed":"slice_7"}`),
	}
}

func usageDocumentInput(req requestctx.Context, endpoint CapabilityEndpoint, title, body string, approvedAt time.Time) RegisterUsageDocumentInput {
	body = strings.TrimSpace(body) + "\n"
	return RegisterUsageDocumentInput{
		TargetKind:           UsageTargetKindCapabilityEndpoint,
		TargetID:             endpoint.CapabilityEndpointID,
		TargetAddress:        endpoint.CompactAddress,
		Title:                title,
		VersionLabel:         "0.1.0",
		BodyFormat:           UsageDocumentFormatMarkdown,
		Body:                 body,
		SectionMapJSON:       usageDocumentSectionMap(body),
		VisibilityPolicyJSON: json.RawMessage(`{}`),
		ReviewStatus:         UsageReviewStatusApproved,
		ContentHash:          contentHashForBody(body),
		SourceKind:           UsageSourceKindBootstrap,
		SourceRef:            "slice_7_seed",
		ApprovedByActorID:    req.ActorID,
		ApprovedAt:           &approvedAt,
		Metadata:             json.RawMessage(`{"seed":"slice_7","document_kind":"usage"}`),
	}
}

func usageDocumentClassInput(req requestctx.Context, class CapabilityClass, title, body string, approvedAt time.Time) RegisterUsageDocumentInput {
	body = strings.TrimSpace(body) + "\n"
	address := class.Namespace + "." + class.Name
	return RegisterUsageDocumentInput{TargetKind: UsageTargetKindCapabilityClass, TargetID: class.CapabilityClassID, TargetAddress: address, Title: title, VersionLabel: "0.1.0", BodyFormat: UsageDocumentFormatMarkdown, Body: body, SectionMapJSON: usageDocumentSectionMap(body), VisibilityPolicyJSON: json.RawMessage(`{}`), ReviewStatus: UsageReviewStatusApproved, ContentHash: contentHashForBody(body), SourceKind: UsageSourceKindBootstrap, SourceRef: "service_registry_slice_3", ApprovedByActorID: req.ActorID, ApprovedAt: &approvedAt, Metadata: json.RawMessage(`{"seed":"service_registry_slice_3","document_kind":"usage"}`)}
}

func usageDocumentSectionMap(body string) json.RawMessage {
	sections := []map[string]any{}
	for index, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		title := strings.TrimSpace(strings.TrimPrefix(line, "## "))
		if title == "" {
			continue
		}
		sections = append(sections, map[string]any{
			"key":   usageDocumentSectionKey(title),
			"title": title,
			"line":  index + 1,
		})
	}
	raw, err := json.Marshal(map[string]any{"sections": sections})
	if err != nil {
		return json.RawMessage(`{"sections":[]}`)
	}
	return raw
}

func usageDocumentSectionKey(title string) string {
	var builder strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(title) {
		isWord := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isWord {
			builder.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore && builder.Len() > 0 {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(builder.String(), "_")
}

func statusReadUsageDocumentBody() string {
	return `
## Purpose
Use main@system.status.read when an actor, agent, smoke test, dashboard, or workflow needs a compact status summary for the main node. It answers whether loomd, PostgreSQL, migrations, bootstrap, search queues, job counts, and recent event counts look healthy enough to continue work.

## Direct Usage
Call this capability before starting a maintenance workflow, before running a larger sync or indexing operation, or when a user asks for the current operational state of LOOM. It is read-only, low risk, and level 1 because it returns system metadata without changing node state.

## Workflow Usage
Health, deployment, and regression workflows should call this after main@system.health.read. If the returned status is degraded or unhealthy, the workflow should inspect logs and recent events before requesting higher-risk capabilities such as script execution, object ingestion, or network changes.
`
}

func objectIngestUsageDocumentBody() string {
	return `
## Purpose
Use main@object-store.object.ingest when a local file needs to enter the LOOM object store as a managed object with a stable record, version, content hash, and searchable text indexing pipeline. It is the main entry point for notes, markdown files, logs, project artifacts, and module files that should become LOOM objects.

## Direct Usage
Pass a readable file path plus optional project, scope, name, object type, source path, and metadata. The capability copies the file into the object store, creates object and version records, and queues text indexing when the object is not in a private backup-only area.

## Workflow Usage
Project setup, script artifact capture, note import, and module backup workflows should call this when the file should be tracked and searchable. Private-folder backup workflows should not use this capability because private files are copied to backup storage without stable object IDs, hashes, metadata indexing, text indexing, or embeddings.
`
}

func scriptRunUsageDocumentBody() string {
	return `
## Purpose
Use main@script-runner.script.run when an approved script package should execute once on the provider node and produce logs, structured outputs, artifacts, and job lifecycle events. Scripts are trusted owner-reviewed executables with a controlled working directory, not portable sandboxed functions.

## Direct Usage
Pass a registered script reference and the required script inputs, commonly a source object or project reference. The capability creates a job, runs the active script version on the provider machine, captures stdout, stderr, runner logs, outputs, artifacts, and emits job and script-run events.

## Workflow Usage
Workflows should call this for single-node actions such as processing an object, generating an artifact, running a scraper, checking a node-local condition, or invoking a local application wrapper. If a workflow needs work on another machine, it should request that machine's registered capability through main-node routing instead of moving the script elsewhere.
`
}

func adminPolicyExplainUsageDocumentBody() string {
	return `
## Purpose
Use main@admin.policy.explain when an actor, agent, CLI command, or workflow needs to understand whether a capability request would be allowed, denied, or require approval before attempting execution. It is the canonical policy preflight for capability URLs.

## Direct Usage
Pass an operation in the form capability:<compact-address> plus optional actor, origin node, scope, approval request flag, and approval reason. The result includes the persisted policy decision, target metadata, actor authorization context, and a pending approval reference when approval was requested.

## Workflow Usage
Agents should call this before exposing high-risk actions to a model as executable tools. Maintenance workflows should call it before script execution, object ingestion, grant inspection, or security operations so that approval-required paths are explicit and auditable.
`
}

func adminApprovalDecideUsageDocumentBody() string {
	return `
## Purpose
Use main@admin.approval.decide when an owner or level-sufficient actor needs to approve or deny a pending approval request. Approval creates a bounded grant; denial records the decision without granting execution.

## Direct Usage
Pass an approval reference, decision approve or deny, deciding actor, reason, and optional grant TTL. The deciding actor must be active on the approval target node with authorization equal to or higher than the requested capability level.

## Workflow Usage
Owner review flows, local offline approval flows, and agent elevation flows should call this after inspecting the approval summary. The response should not execute the original capability; it only changes authorization state and emits approval and grant audit events.
`
}

func adminGrantRevokeUsageDocumentBody() string {
	return `
## Purpose
Use main@admin.grant.revoke when an owner or level-sufficient actor needs to remove an active authorization grant before its expiry. This is the manual control for ending temporary elevations and approval-issued grants.

## Direct Usage
Pass a grant reference, revoking actor, and reason. The grant must be active, and the revoking actor must be authorized at the owner level on the constrained target node when the grant has a target-node constraint.

## Workflow Usage
Security workflows should revoke grants after a failed inspection, after a suspected compromise, or when a smoke test needs to reset authorization state. Revocation emits a security-visible audit event and future policy checks stop using the grant.
`
}

func ensureProvider(ctx context.Context, req requestctx.Context, service Service, input RegisterProviderInput) (Provider, bool, error) {
	if providerID, err := service.ResolveProviderRef(ctx, input.CompactAddress); err == nil {
		provider, err := getProvider(ctx, service.DB, providerID)
		return provider, false, err
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Provider{}, false, err
	}
	provider, err := service.RegisterProvider(ctx, req, input)
	return provider, err == nil, err
}

func ensureCapabilityClass(ctx context.Context, req requestctx.Context, service Service, input RegisterCapabilityClassInput) (CapabilityClass, bool, error) {
	ref := input.Namespace + "." + input.Name
	if classID, err := resolveCapabilityClassRef(ctx, service.DB, ref); err == nil {
		class, err := getCapabilityClass(ctx, service.DB, classID)
		return class, false, err
	} else if !errors.Is(err, sql.ErrNoRows) {
		return CapabilityClass{}, false, err
	}
	class, err := service.RegisterCapabilityClass(ctx, req, input)
	return class, err == nil, err
}

func ensureCapabilityEndpoint(ctx context.Context, req requestctx.Context, service Service, input RegisterCapabilityEndpointInput) (CapabilityEndpoint, bool, error) {
	if endpointID, err := service.ResolveEndpointRef(ctx, input.CompactAddress); err == nil {
		endpoint, err := getCapabilityEndpoint(ctx, service.DB, endpointID)
		return endpoint, false, err
	} else if !errors.Is(err, sql.ErrNoRows) {
		return CapabilityEndpoint{}, false, err
	}
	endpoint, err := service.RegisterCapabilityEndpoint(ctx, req, input)
	return endpoint, err == nil, err
}

func ensureEndpointVersion(ctx context.Context, req requestctx.Context, service Service, input RegisterEndpointVersionInput) (EndpointVersion, bool, error) {
	endpointID, err := service.ResolveEndpointRef(ctx, input.CapabilityEndpointRef)
	if err != nil {
		return EndpointVersion{}, false, err
	}
	var versionID string
	err = service.DB.QueryRowContext(ctx, `
		SELECT capability_endpoint_version_id
		FROM capabilities.endpoint_versions
		WHERE capability_endpoint_id = $1 AND version_label = $2
	`, endpointID, input.VersionLabel).Scan(&versionID)
	if err == nil {
		version, err := getEndpointVersion(ctx, service.DB, versionID)
		return version, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return EndpointVersion{}, false, err
	}
	version, err := service.RegisterEndpointVersion(ctx, req, input)
	return version, err == nil, err
}

func ensureUsageDocument(ctx context.Context, req requestctx.Context, service Service, input RegisterUsageDocumentInput) (UsageDocument, bool, error) {
	input.VersionLabel = defaultString(input.VersionLabel, "0.1.0")
	if strings.TrimSpace(input.ContentHash) == "" {
		input.ContentHash = contentHashForBody(input.Body)
	}
	var docID string
	err := service.DB.QueryRowContext(ctx, `
		SELECT capability_usage_document_id
		FROM capabilities.usage_documents
		WHERE target_kind = $1
		  AND target_id = $2
		  AND version_label = $3
		  AND content_hash = $4
	`, input.TargetKind, input.TargetID, input.VersionLabel, input.ContentHash).Scan(&docID)
	if err == nil {
		doc, err := getUsageDocument(ctx, service.DB, docID)
		return doc, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return UsageDocument{}, false, err
	}
	doc, err := service.RegisterUsageDocument(ctx, req, input)
	return doc, err == nil, err
}
