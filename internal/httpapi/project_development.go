package httpapi

import (
	"context"
	"net/http"
	"strings"

	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/response"
)

type ProjectCreationConnector interface {
	ConnectCreatedProject(context.Context, projectapply.Principal, string, string, string) (pc.DeclarationResult, error)
}

func (s Server) attachProjectDevelopment(ctx context.Context, status *pc.DeclarationStatus) {
	observer, ok := s.services.ProjectRepos.Provenance.(interface {
		ObserveProjectContextForProvenance(context.Context, string) (projectstate.ProvenanceProjectSource, error)
	})
	if !ok || status.Target.ProjectID == "" {
		return
	}
	source, err := observer.ObserveProjectContextForProvenance(ctx, status.Target.ProjectID)
	if err != nil {
		status.Development = &pc.DeclarationDevelopmentContext{Posture: "unavailable"}
		return
	}
	d := source.Projection.Development
	if d.Posture == "" {
		return
	}
	compact := func(s string) string {
		s = strings.Join(strings.Fields(s), " ")
		r := []rune(s)
		if len(r) > 200 {
			s = string(r[:200]) + "..."
		}
		return s
	}
	status.Development = &pc.DeclarationDevelopmentContext{Posture: string(d.Posture), SourceDigest: d.SourceDigest, Purpose: compact(d.Purpose), CurrentFocus: compact(d.CurrentFocus), Progress: compact(d.Progress), Blockers: compact(d.Blockers), NextAction: compact(d.NextAction), SnapshotRef: "/v1/provenance/projects/" + status.Target.ProjectID}
}

func (s Server) connectCreatedProject(w http.ResponseWriter, r *http.Request, result *pc.ScaffoldResult) bool {
	p, err := s.declarationPrincipal(r)
	connector, configured := s.services.ProjectDeclaration.(ProjectCreationConnector)
	if err != nil || !configured {
		s.writeProjectCreatePending(w, r, *result, "project.context_pending", "Project source exists; identity registration is unavailable. Retry the same create command after restoring the backend connection.")
		return false
	}
	_, ctx := requestMeta(r)
	registration, err := connector.ConnectCreatedProject(ctx, p, result.ProjectID, result.ProjectRoot, result.OwnerNode)
	if registration.OperationID != "" {
		result.Registration = &registration
	}
	if err != nil || registration.State != pc.DeclarationOperationSucceeded {
		s.writeProjectCreatePending(w, r, *result, "project.context_pending", "Project source exists; identity registration is pending. Retry the same create command to resume without replacing files or identity.")
		return false
	}
	result.ContextState = "registered_refresh_pending"
	return true
}

func (s Server) writeProjectCreatePending(w http.ResponseWriter, r *http.Request, result pc.ScaffoldResult, code, summary string) {
	cid, _ := requestMeta(r)
	result.OK = false
	result.ContextState = "context_pending"
	response.WriteJSON(w, http.StatusServiceUnavailable, struct {
		response.ErrorEnvelope
		Data pc.ScaffoldResult `json:"data"`
	}{response.ErrorEnvelope{Error: response.ErrorBody{Code: code, Summary: summary, Domain: "projects", Target: result.ProjectID, CorrelationID: cid}, Meta: response.NewMeta(cid)}, result})
}
