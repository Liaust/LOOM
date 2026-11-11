package hermesschedules

import (
	"context"
	"errors"
	"regexp"
)

// ProjectSpec contains only declaration-owned fields. Hermes owns all timing,
// execution, inference configuration and delivery (local only in this adapter).
type ProjectSpec struct {
	Codex    *CodexProjectSpec `json:"codex,omitempty"`
	Profile  string            `json:"profile,omitempty"`
	Schedule string            `json:"schedule"`
	Prompt   string            `json:"prompt"`
	Skills   []string          `json:"skills"`
	Status   string            `json:"status"`
	Workdir  string            `json:"workdir"`
}

type CodexProjectSpec struct {
	Model          string `json:"model" yaml:"model"`
	TimeoutSeconds int    `json:"timeout_seconds" yaml:"timeout_seconds"`
	Sandbox        string `json:"sandbox" yaml:"sandbox"`
}

var codexModel = regexp.MustCompile(`^gpt-[a-z0-9.-]{1,100}$`)

func ValidCodexProjectSpec(s CodexProjectSpec) bool {
	return codexModel.MatchString(s.Model) && s.TimeoutSeconds >= 1 && s.TimeoutSeconds <= 1800 && (s.Sandbox == "read-only" || s.Sandbox == "workspace-write" || s.Sandbox == "danger-full-access")
}

type AgentSpec struct {
	Instructions string `json:"instructions"`
	Model        string `json:"model"`
	Provider     string `json:"provider"`
	Workdir      string `json:"workdir"`
}

type ProjectJob struct {
	Box               *BoxOwnership `json:"box,omitempty"`
	Kind              string        `json:"kind,omitempty"`
	CommittedRevision string        `json:"committed_revision"`
	ID                string        `json:"id"`
	ProjectID         string        `json:"project_id"`
	Resource          string        `json:"resource"`
	Profile           string        `json:"profile"`
	Revision          string        `json:"revision"`
	DesiredHash       string        `json:"desired_hash"`
	Paused            bool          `json:"paused"`
	Retired           bool          `json:"retired"`
	Drift             bool          `json:"drift"`
	Token             string        `json:"token"`
	InputHash         string        `json:"input_hash"`
	BeforeRevision    string        `json:"before_revision"`
	BeforeID          string        `json:"before_id"`
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
	Agent            *AgentSpec   `json:"agent,omitempty"`
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
