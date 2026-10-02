package hermesschedules

import (
	"context"
	"errors"
)

// ProjectSpec contains only declaration-owned fields. Hermes owns all timing,
// execution, inference configuration and delivery (local only in this adapter).
type ProjectSpec struct {
	Schedule string   `json:"schedule"`
	Prompt   string   `json:"prompt"`
	Skills   []string `json:"skills"`
	Status   string   `json:"status"`
	Workdir  string   `json:"workdir"`
}

type ProjectJob struct {
	CommittedRevision string `json:"committed_revision"`
	ID                string `json:"id"`
	ProjectID         string `json:"project_id"`
	Resource          string `json:"resource"`
	Profile           string `json:"profile"`
	Revision          string `json:"revision"`
	DesiredHash       string `json:"desired_hash"`
	Paused            bool   `json:"paused"`
	Retired           bool   `json:"retired"`
	Drift             bool   `json:"drift"`
	Token             string `json:"token"`
	InputHash         string `json:"input_hash"`
	BeforeRevision    string `json:"before_revision"`
	BeforeID          string `json:"before_id"`
}

type ProjectRequest struct {
	Operation        string       `json:"operation"`
	Source           Source       `json:"source"`
	ProjectID        string       `json:"project_id"`
	Resource         string       `json:"resource,omitempty"`
	ExpectedRevision string       `json:"expected_revision,omitempty"`
	ExpectedID       string       `json:"expected_id,omitempty"`
	Token            string       `json:"token,omitempty"`
	InputHash        string       `json:"input_hash,omitempty"`
	Spec             *ProjectSpec `json:"spec,omitempty"`
}

type ProjectResponse struct {
	Source Source       `json:"source"`
	Jobs   []ProjectJob `json:"jobs"`
	Error  string       `json:"error,omitempty"`
}

var ErrProjectConflict = errors.New("Hermes project schedule changed or identity conflicted")
var ErrTerminalRequiresResume = errors.New("Hermes terminal schedule requires explicit native resume before timing changes")

// ProjectNative is an explicit profile-owner boundary, not the read-only S1
// inventory. Implementations must fence revisions and mutations atomically.
type ProjectNative interface {
	Call(context.Context, ProjectRequest) (ProjectResponse, error)
}
