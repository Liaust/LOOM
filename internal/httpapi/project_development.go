package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

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
	if status.Lifecycle == "archived" && d.Archive == nil {
		status.Development = &pc.DeclarationDevelopmentContext{Posture: "unavailable"}
		return
	}
	if d.Posture == "" {
		return
	}
	development := &pc.DeclarationDevelopmentContext{Posture: string(d.Posture), SourceDigest: d.SourceDigest, SnapshotRef: "/v1/provenance/projects/" + status.Target.ProjectID}
	compact := func(field, s string) string {
		s = strings.Join(strings.Fields(s), " ")
		r := []rune(s)
		if len(r) > 200 {
			s = string(r[:200]) + "..."
			development.TruncatedFields = append(development.TruncatedFields, field)
		}
		return s
	}
	development.Purpose = compact("purpose", d.Purpose)
	development.CurrentFocus = compact("current_focus", d.CurrentFocus)
	development.Progress = compact("progress", d.Progress)
	development.Blockers = compact("blockers", d.Blockers)
	development.NextAction = compact("next_action", d.NextAction)
	status.Development = development
	for _, doc := range d.Documents {
		if doc.Path != ".project/CONVERSATIONS.md" || doc.Posture != "present" || doc.Hash == "" {
			continue
		}
		index := &pc.DeclarationConversationIndex{Path: doc.Path, Hash: doc.Hash, Excerpt: doc.Excerpt, Truncated: doc.Truncated}
		if len(index.Excerpt) > 1200 {
			end := 1200
			for !utf8.ValidString(index.Excerpt[:end]) {
				end--
			}
			index.Excerpt, index.Truncated = index.Excerpt[:end], true
		}
		status.Development.Conversations = index
		break
	}
	if d.Archive != nil {
		status.Development.Archive = &pc.DeclarationArchivedContext{ReadOnlyRoot: d.Archive.ReadOnlyRoot, OperationID: d.Archive.OperationID, ManifestDigest: d.Archive.ManifestDigest, ArchivedAt: d.Archive.ArchivedAt.UTC().Format(time.RFC3339Nano), MissingDocuments: append([]string(nil), d.MissingDocuments...)}
	}
}

func (s Server) connectCreatedProject(w http.ResponseWriter, r *http.Request, result *pc.ScaffoldResult) bool {
	p, err := s.declarationPrincipal(r)
	connector, configured := s.services.ProjectDeclaration.(ProjectCreationConnector)
	if err != nil || !configured {
		s.writeProjectCreatePending(w, r, *result, "project.context_pending", "Project source exists; identity registration is unavailable. Retry the same create command after restoring the backend connection.", nil)
		return false
	}
	_, ctx := requestMeta(r)
	registration, err := connector.ConnectCreatedProject(ctx, p, result.ProjectID, result.ProjectRoot, result.OwnerNode)
	if registration.OperationID != "" {
		result.Registration = &registration
	}
	if err != nil || registration.State != pc.DeclarationOperationSucceeded {
		s.writeProjectCreatePending(w, r, *result, "project.context_pending", "Project source exists; identity registration is pending. Retry the same create command to resume without replacing files or identity.", err)
		return false
	}
	result.ContextState = "registered_refresh_pending"
	return true
}

func (s Server) writeProjectCreatePending(w http.ResponseWriter, r *http.Request, result pc.ScaffoldResult, code, summary string, cause error) {
	cid, _ := requestMeta(r)
	hint := ""
	var failure *projectapply.Failure
	if errors.As(cause, &failure) && declarationCause.MatchString(failure.Cause) {
		hint = string(failure.Code) + ": " + failure.Cause
	}
	result.OK = false
	result.ContextState = "context_pending"
	response.WriteJSON(w, http.StatusServiceUnavailable, struct {
		response.ErrorEnvelope
		Data pc.ScaffoldResult `json:"data"`
	}{response.ErrorEnvelope{Error: response.ErrorBody{Code: code, Summary: summary, Hint: hint, Domain: "projects", Target: result.ProjectID, CorrelationID: cid}, Meta: response.NewMeta(cid)}, result})
}
