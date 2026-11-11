package projectcontracts

// Deployment is a compact front door to the existing declaration operation.
// It is not a new job kind, clock or persistence owner.
type ProjectDeploymentRequest struct {
	ProjectRef string      `json:"project_ref"`
	NodeRef    string      `json:"node_ref,omitempty"`
	Resource   ResourceKey `json:"resource,omitempty"`
	Check      bool        `json:"check,omitempty"`
}

type ProjectDeploymentResult struct {
	Builder    string             `json:"builder,omitempty"`
	Program    string             `json:"program,omitempty"`
	BuildJobID string             `json:"build_job_id,omitempty"`
	ProjectID  string             `json:"project_id"`
	NodeID     string             `json:"node_id"`
	Resource   ResourceKey        `json:"resource"`
	State      string             `json:"state"`
	Network    string             `json:"network"`
	Scope      string             `json:"network_scope"`
	Plan       *DeclarationPlan   `json:"plan,omitempty"`
	Operation  *DeclarationResult `json:"operation,omitempty"`
}
