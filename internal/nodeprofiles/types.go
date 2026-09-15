package nodeprofiles

const (
	AuthorityMainNodeDefault           = "main_node_default"
	AuthorityOwnedWorkspaceDefault     = "owned_workspace_default"
	AuthorityPrimaryWorkspaceDefault   = "primary_workspace_default"
	AuthoritySecondaryWorkspaceDefault = "secondary_workspace_default"
	AuthorityHardwareCapabilityDefault = "hardware_capability_default"
	AuthorityComputeRunnerDefault      = "compute_runner_default"
	AuthorityStorageEdgeDefault        = "storage_edge_default"
	AuthorityAutomationEdgeDefault     = "automation_edge_default"
	AuthorityGuestRestrictedDefault    = "guest_restricted_default"

	RuntimeMainNodeDefault       = "main_node_default"
	RuntimeOwnedWorkspaceDefault = "owned_workspace_default"
	RuntimeMainFull              = "main_full"
	RuntimeWorkspaceFull         = "workspace_full"
	RuntimeWorkspaceLight        = "workspace_light"
	RuntimeHardwareAgent         = "hardware_agent"
	RuntimeComputeRunner         = "compute_runner"
	RuntimeStorageEdge           = "storage_edge"
	RuntimeGuestRestricted       = "guest_restricted"
	RuntimeGuestRestrictedLegacy = "guest_restricted_default"
)

const (
	SeverityWarning = "warning"
)

type ResolveInput struct {
	NodeKind     string
	NodeRole     string
	RuntimeClass string
}

type Assignment struct {
	AuthorityProfileKey string       `json:"authority_profile_key"`
	RuntimeProfileKey   string       `json:"runtime_profile_key"`
	Warnings            []Diagnostic `json:"warnings,omitempty"`
}

type Diagnostic struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Field    string `json:"field,omitempty"`
}
