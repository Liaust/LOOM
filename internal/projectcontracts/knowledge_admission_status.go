package projectcontracts

import "time"

type KnowledgeAdmissionStatus struct {
	RootID            string     `json:"root_id"`
	RootKey           string     `json:"root_key"`
	State             string     `json:"state"`
	Reason            string     `json:"reason"`
	AdmittedObjects   int        `json:"admitted_objects"`
	PendingBindings   int        `json:"pending_bindings"`
	LastObjectRefresh *time.Time `json:"last_object_refresh,omitempty"`
}
