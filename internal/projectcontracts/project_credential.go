package projectcontracts

type ProjectCredentialImportRequest struct {
	ProjectRef string      `json:"project_ref"`
	NodeRef    string      `json:"node_ref,omitempty"`
	Resource   ResourceKey `json:"resource"`
	SourcePath string      `json:"source_path"`
	Account    string      `json:"account"`
	Scopes     []string    `json:"scopes"`
}

type ProjectCredentialImportResult struct {
	ProjectID string      `json:"project_id"`
	Resource  ResourceKey `json:"resource"`
	Reference string      `json:"reference"`
	Revision  string      `json:"revision"`
}
