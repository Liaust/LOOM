package projectcontracts

type ProjectActionRequest struct {
	ProjectRef    string      `json:"project_ref"`
	NodeRef       string      `json:"node_ref,omitempty"`
	Resource      ResourceKey `json:"resource"`
	Action        string      `json:"action"`
	Run           string      `json:"run,omitempty"`
	Check         bool        `json:"check,omitempty"`
	ReleaseFailed bool        `json:"release_failed,omitempty"`
}

type ProjectActionResult struct {
	ProjectID      string      `json:"project_id"`
	Resource       ResourceKey `json:"resource"`
	Action         string      `json:"action"`
	Run            string      `json:"run"`
	State          string      `json:"state"`
	CallRef        string      `json:"call_ref,omitempty"`
	ErrorCode      string      `json:"error_code,omitempty"`
	ServiceResumed bool        `json:"service_resumed"`
	Finalized      bool        `json:"finalized"`
}
