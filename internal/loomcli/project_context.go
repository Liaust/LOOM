package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"
	pc "loom.local/loom/internal/projectcontracts"
)

// This is a bounded CLI projection, not a new service or declaration schema.
type projectContext struct {
	Lifecycle           string                  `json:"lifecycle,omitempty"`
	WorkState           string                  `json:"work_state,omitempty"`
	Development         *contextDevelopment     `json:"development,omitempty"`
	SchemaVersion       string                  `json:"schema_version"`
	Target              *pc.DeclarationTarget   `json:"target,omitempty"`
	Revision            string                  `json:"revision,omitempty"`
	Readiness           pc.DeclarationReadiness `json:"readiness"`
	ObservedAt          string                  `json:"read_at,omitempty"`
	Repositories        []contextRepository     `json:"repositories,omitempty"`
	Resources           []contextResource       `json:"resources,omitempty"`
	DetailCommand       string                  `json:"detail_command"`
	HistoricalOperation *contextOperation       `json:"historical_operation,omitempty"`
	Source              *contextSource          `json:"source,omitempty"`
	Sources             []pc.DeclarationSource  `json:"sources,omitempty"`
	SourceStatus        string                  `json:"source_status"`
	Causes              []contextCause          `json:"causes"`
	NextAction          declarationNextAction   `json:"next_action"`
	Omitted             map[string]int          `json:"omitted"`
}

type contextDevelopment struct {
	pc.DeclarationDevelopmentContext
	PurposeStatus string `json:"purpose_status"`
}

const projectPurposeScaffold = "Describe the project's intended outcome and who it serves as work begins."

type contextOperation struct {
	OperationID      string                       `json:"operation_id"`
	PlanID           string                       `json:"plan_id"`
	Revision         string                       `json:"revision"`
	State            pc.DeclarationOperationState `json:"state"`
	CompletedEffects []string                     `json:"completed_effects,omitempty"`
}
type contextRepository struct {
	Key  string `json:"key"`
	ID   string `json:"id,omitempty"`
	Path string `json:"path,omitempty"`
}
type contextFact struct {
	pc.DeclarationFact
	Coverage      string `json:"coverage"`
	RevisionBasis string `json:"revision_basis"`
	ReadAt        string `json:"read_at,omitempty"`
	EvidenceTime  string `json:"evidence_time"`
}
type contextResource struct {
	Key                 string                        `json:"key"`
	Kind                pc.DeclarationResourceKind    `json:"kind"`
	DesiredRevision     string                        `json:"desired_revision,omitempty"`
	Applied             contextFact                   `json:"applied"`
	Healthy             contextFact                   `json:"healthy"`
	Protected           contextFact                   `json:"protected"`
	Verified            contextFact                   `json:"verified"`
	Endpoint            *pc.ApplicationEndpointStatus `json:"publication,omitempty"`
	PublicationCoverage string                        `json:"publication_coverage,omitempty"`
}
type contextSource struct {
	OwnerNodeID string `json:"owner_node_id"`
	Ref         string `json:"ref"`
}
type contextCause struct {
	Code             pc.DeclarationErrorCode `json:"code"`
	CauseCode        string                  `json:"cause_code"`
	Retryable        bool                    `json:"retryable"`
	CompletedEffects []string                `json:"completed_effects"`
}
type declarationNextAction struct {
	Kind    string `json:"kind"`
	Reason  string `json:"reason"`
	Command string `json:"command"`
}

func addProjectContextCommand(parent *cobra.Command, opts *options) {
	var node string
	var verbose bool
	cmd := &cobra.Command{Use: "context <project-ref-or-path>", Short: "Read project development context and technical status", Long: "Read project context from its verified owner. Ordinary edits do not require this command. The default performs one status read including bounded .project context. --verbose additionally reads matching plan source pointers; it does not refresh projections or apply work.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		cid, client, err := declarationClient(cmd, opts)
		if err != nil {
			return err
		}
		projectRef, err := declarationReadProjectRef(args[0], node)
		if err != nil {
			return err
		}
		env, err := client.GetProjectDeclarationStatus(ctx, cid, projectRef, node)
		if err != nil {
			return renderDeclarationFailure(cmd, opts, cid, err, nil, args[0])
		}
		view := newProjectContext(env.Data)
		if (verbose || opts.verboseOutput) && env.Data.Lifecycle != "archived" {
			view.SourceStatus = "unavailable"
			target := env.Data.Target
			// Never replace a missing owner with caller-local state or a guessed node.
			if target.ProjectID != "" && target.OwnerNodeID != "" && env.Data.Revision != "" {
				plan, e := client.PlanProjectDeclaration(ctx, cid, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: target.ProjectID, NodeRef: target.OwnerNodeID, Effects: declarationEffects(false)})
				if e == nil {
					view.SourceStatus = "drift"
					if plan.Data.PlanID == env.Data.Revision && plan.Data.Basis.Target == target {
						view.SourceStatus = "matched"
						for i, source := range plan.Data.Basis.Sources {
							if i >= 8 {
								view.Omitted["sources"] += len(plan.Data.Basis.Sources) - i
								break
							}
							if !declarationDisplayText(source.Ref, source.SchemaVersion, source.Hash, source.Revision) {
								view.Omitted["sources"]++
								continue
							}
							view.Sources = append(view.Sources, source)
							if !view.fits() {
								view.Sources = view.Sources[:len(view.Sources)-1]
								view.Omitted["sources"]++
							}
						}
					}
				}
			}
		}
		return renderProjectContext(cmd, opts, view)
	}}
	cmd.Flags().StringVar(&node, "node", "", "explicit owner node; paths resolve on that node")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "read detailed source pointers only from a plan matching current context")
	parent.AddCommand(cmd)
}

// Control/format characters cannot create extra terminal lines, bidi text or
// executable advice. Values are omitted whole; even identifiers are never cut.
func declarationDisplayText(values ...string) bool {
	for _, s := range values {
		if len(s) > 4096 || !utf8.ValidString(s) || strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) }) {
			return false
		}
	}
	return true
}
func declarationDisplay(s string) string {
	if !declarationDisplayText(s) {
		return "[omitted unsafe or oversized value]"
	}
	return s
}

func conversationExcerptSafe(value string) bool {
	return len(value) <= 1200 && utf8.ValidString(value) && !strings.ContainsFunc(value, func(r rune) bool {
		return r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp))
	})
}
func (v projectContext) fits() bool {
	b, err := json.Marshal(v)
	// Reserve 1 KiB for the finite set of omission counters and the newline.
	return err == nil && len(b) <= 7168
}
func declarationFacts(r *pc.DeclarationReadiness) []*pc.DeclarationFact {
	return []*pc.DeclarationFact{&r.Desired, &r.Queued, &r.Applied, &r.Processing, &r.Healthy, &r.Protected, &r.Verified}
}

var declarationFactNames = []string{"Desired", "Queued", "Applied", "Processing", "Healthy", "Protected", "Verified"}

func newProjectContext(status pc.DeclarationStatus) projectContext {
	v := projectContext{SchemaVersion: "loom.project.context.v1", Readiness: status.Readiness, SourceStatus: "not_requested", Causes: []contextCause{}, Omitted: map[string]int{}}
	if pc.ValidProjectWorkState(status.WorkState) {
		v.WorkState = status.WorkState
	} else if status.WorkState != "" {
		v.Omitted["work_state"]++
	}
	v.NextAction = declarationStatusNextAction(status)
	v.DetailCommand = declarationInspectAction(status.Target, "", "").Command
	v.ObservedAt = contextReadAt(status.ObservedAt)
	if status.ObservedAt != "" && v.ObservedAt == "" {
		v.Omitted["read_time"]++
	}
	original := status.Readiness
	v.Readiness.Endpoint = nil
	for _, fact := range declarationFacts(&v.Readiness) {
		fact.Revision, fact.EvidenceRef = "", ""
		fact.State = contextFactState(fact.State, v.Omitted)
	}
	if status.Lifecycle == "archived" {
		v.Lifecycle = "archived"
		v.SourceStatus = "historical_unavailable"
		candidate := "loom project archive inspect " + quoteDeclarationArg(status.Target.ProjectID) + " --json"
		if declarationSelector(status.Target.ProjectID) && declarationSafeCommand(candidate) {
			v.DetailCommand = candidate
		}
	}
	// Navigation precedes optional prose, resource detail and operation history.
	if declarationDisplayText(status.Target.ProjectID, status.Target.OwnerNodeID, status.Target.ProjectRoot, status.Target.LocationRevision) {
		target := status.Target
		v.Target = &target
		if !v.fits() {
			v.Target = nil
		}
	}
	if v.Target == nil {
		v.Omitted["target"]++
	}
	if declarationDisplayText(status.Revision) {
		v.Revision = status.Revision
		if !v.fits() {
			v.Revision = ""
		}
	}
	if v.Revision == "" && status.Revision != "" {
		v.Omitted["revision"]++
	}
	keys := make([]string, 0, len(status.Resources))
	for key := range status.Resources {
		keys = append(keys, string(key))
	}
	sort.Strings(keys)
	for _, key := range keys {
		resource := status.Resources[pc.ResourceKey(key)]
		if resource.Binding.Kind != pc.DeclarationRepository {
			continue
		}
		if len(v.Repositories) >= 4 || !declarationDisplayText(key, resource.Binding.RepositoryID, resource.Path) {
			v.Omitted["repositories"]++
			continue
		}
		v.Repositories = append(v.Repositories, contextRepository{Key: key, ID: resource.Binding.RepositoryID, Path: resource.Path})
		if !v.fits() {
			v.Repositories = v.Repositories[:len(v.Repositories)-1]
			v.Omitted["repositories"]++
		}
	}
	if d := status.Development; d != nil {
		if declarationDisplayText(d.Posture, d.SourceDigest, d.Purpose, d.CurrentFocus, d.Progress, d.Blockers, d.NextAction, d.SnapshotRef) {
			v.Development = &contextDevelopment{DeclarationDevelopmentContext: pc.DeclarationDevelopmentContext{Posture: d.Posture}, PurposeStatus: "unset"}
			for _, field := range []string{"purpose", "current_focus", "progress", "blockers", "next_action"} {
				if slices.Contains(d.TruncatedFields, field) {
					v.Development.TruncatedFields = append(v.Development.TruncatedFields, field)
				}
			}
		}
		if v.Development == nil || !v.fits() {
			v.Development = nil
			v.Omitted["development"]++
		}
		if v.Development != nil {
			if index := d.Conversations; index != nil && index.Path == ".project/CONVERSATIONS.md" && index.Hash != "" && declarationDisplayText(index.Hash) {
				copy := *index
				if !conversationExcerptSafe(copy.Excerpt) {
					copy.Excerpt, copy.Truncated = "", true
					v.Omitted["conversation_excerpt"]++
				}
				v.Development.Conversations = &copy
				if !v.fits() {
					copy.Excerpt, copy.Truncated = "", true
					v.Omitted["conversation_excerpt"]++
				}
				if !v.fits() {
					v.Development.Conversations = nil
					v.Omitted["conversations"]++
				}
			}
		}
	}
	// Reserve current source context before repeated optional resource evidence.
	if d := status.Development; d != nil && v.Development != nil {
		purpose := d.Purpose
		if strings.TrimSpace(purpose) == projectPurposeScaffold {
			purpose = ""
			v.Development.PurposeStatus = "unset_scaffold"
		} else if purpose != "" {
			v.Development.PurposeStatus = "source_declared"
		}
		for _, field := range []struct {
			name  string
			dst   *string
			value string
			limit int
		}{
			{"", &v.Development.SourceDigest, d.SourceDigest, 0},
			{"current_focus", &v.Development.CurrentFocus, d.CurrentFocus, 512},
			{"progress", &v.Development.Progress, d.Progress, 768},
			{"blockers", &v.Development.Blockers, d.Blockers, 384},
			{"next_action", &v.Development.NextAction, d.NextAction, 512},
			{"purpose", &v.Development.Purpose, purpose, 384},
			{"", &v.Development.SnapshotRef, d.SnapshotRef, 0},
		} {
			if !declarationDisplayText(field.value) {
				v.Omitted["development_detail"]++
				continue
			}
			value, truncated := projectContextExcerpt(field.value, field.limit)
			if truncated {
				v.Omitted["development_excerpt"]++
				if !slices.Contains(v.Development.TruncatedFields, field.name) {
					v.Development.TruncatedFields = append(v.Development.TruncatedFields, field.name)
				}
			}
			*field.dst = value
			if !v.fits() {
				*field.dst = ""
				v.Omitted["development_detail"]++
			}
		}
	}
	for _, key := range keys {
		resource := status.Resources[pc.ResourceKey(key)]
		if len(v.Resources) >= 4 || !declarationDisplayText(key, string(resource.Binding.Kind)) {
			v.Omitted["resources"]++
			continue
		}
		summary := projectResourceContext(key, resource, status, v.Omitted)
		v.Resources = append(v.Resources, summary)
		if !v.fits() {
			v.Resources = v.Resources[:len(v.Resources)-1]
			v.Omitted["resources"]++
		}
	}
	if d := status.Development; d != nil && v.Development != nil {
		if a := d.Archive; a != nil {
			if declarationDisplayText(a.OperationID, a.ManifestDigest, a.ArchivedAt) && len(a.MissingDocuments) <= 32 && declarationDisplayText(a.MissingDocuments...) {
				copy := *a
				copy.ReadOnlyRoot = ""
				v.Development.Archive = &copy
			}
			if v.Development.Archive == nil || !v.fits() {
				v.Development.Archive = nil
				v.Omitted["archive"]++
			} else if status.Lifecycle == "archived" {
				v.SourceStatus = "verified_historical"
			}
			if v.Development.Archive != nil && a.ReadOnlyRoot != "" {
				if declarationDisplayText(a.ReadOnlyRoot) && path.IsAbs(a.ReadOnlyRoot) && path.Clean(a.ReadOnlyRoot) == a.ReadOnlyRoot {
					v.Development.Archive.ReadOnlyRoot = a.ReadOnlyRoot
				}
				if v.Development.Archive.ReadOnlyRoot == "" || !v.fits() {
					v.Development.Archive.ReadOnlyRoot = ""
					v.Omitted["archive_root"]++
				}
			}
		}
	}
	if status.Lifecycle != "archived" && v.Target != nil && path.IsAbs(v.Target.ProjectRoot) && v.Target.OwnerNodeID != "" {
		v.Source = &contextSource{OwnerNodeID: v.Target.OwnerNodeID, Ref: path.Join(v.Target.ProjectRoot, pc.CanonicalRootContractPath)}
		if !v.fits() {
			v.Source = nil
		}
	}
	if v.Source == nil && status.Lifecycle != "archived" {
		v.Omitted["source"]++
	}
	for i, fact := range declarationFacts(&v.Readiness) {
		old := declarationFacts(&original)[i]
		for j, value := range []string{old.Revision, old.EvidenceRef} {
			if value == "" {
				continue
			}
			if !declarationDisplayText(value) {
				v.Omitted["readiness_detail"]++
				continue
			}
			field := &fact.Revision
			if j == 1 {
				field = &fact.EvidenceRef
			}
			*field = value
			if !v.fits() {
				*field = ""
				v.Omitted["readiness_detail"]++
			}
		}
	}
	failures := append([]pc.DeclarationError(nil), status.Errors...)
	for _, key := range keys {
		failures = append(failures, status.Resources[pc.ResourceKey(key)].Errors...)
	}
	for i, failure := range failures {
		if i >= 3 {
			v.Omitted["causes"] += len(failures) - i
			break
		}
		if !declarationDisplayText(string(failure.Code), failure.CauseCode) {
			v.Omitted["causes"]++
			continue
		}
		cause := contextCause{Code: failure.Code, CauseCode: failure.CauseCode, Retryable: failure.Retryable, CompletedEffects: []string{}}
		v.Causes = append(v.Causes, cause)
		if !v.fits() {
			v.Causes = v.Causes[:len(v.Causes)-1]
			v.Omitted["causes"]++
			continue
		}
		index := len(v.Causes) - 1
		for j, effect := range failure.CompletedEffects {
			if j >= 4 {
				v.Omitted["completed_effects"] += len(failure.CompletedEffects) - j
				break
			}
			if !declarationDisplayText(effect) {
				v.Omitted["completed_effects"]++
				continue
			}
			v.Causes[index].CompletedEffects = append(v.Causes[index].CompletedEffects, effect)
			if !v.fits() {
				v.Causes[index].CompletedEffects = v.Causes[index].CompletedEffects[:len(v.Causes[index].CompletedEffects)-1]
				v.Omitted["completed_effects"]++
			}
		}
	}
	if op := status.Operation; op != nil {
		if declarationDisplayText(op.OperationID, op.PlanID, op.Revision, string(op.State)) {
			v.HistoricalOperation = &contextOperation{OperationID: op.OperationID, PlanID: op.PlanID, Revision: op.Revision, State: op.State}
			if !v.fits() {
				v.HistoricalOperation = nil
			}
		}
		if v.HistoricalOperation == nil {
			v.Omitted["historical_operation"]++
		} else {
			for _, effect := range declarationCompletedEffects(*op) {
				if len(v.HistoricalOperation.CompletedEffects) >= 4 || !declarationDisplayText(effect) {
					v.Omitted["operation_effects"]++
					continue
				}
				v.HistoricalOperation.CompletedEffects = append(v.HistoricalOperation.CompletedEffects, effect)
				if !v.fits() {
					v.HistoricalOperation.CompletedEffects = v.HistoricalOperation.CompletedEffects[:len(v.HistoricalOperation.CompletedEffects)-1]
					v.Omitted["operation_effects"]++
				}
			}
		}
	}
	return v
}

func projectContextExcerpt(value string, limit int) (string, bool) {
	raw, _ := json.Marshal(value)
	if limit <= 0 || len(raw) <= limit {
		return value, false
	}
	runes := []rune(value)
	low, high := 0, len(runes)
	for low < high {
		mid := (low + high + 1) / 2
		raw, _ = json.Marshal(string(runes[:mid]) + "... [excerpt]")
		if len(raw) <= limit {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return string(runes[:low]) + "... [excerpt]", true
}

func contextFactState(state pc.DeclarationFactState, omitted map[string]int) pc.DeclarationFactState {
	switch state {
	case pc.DeclarationUnknown, pc.DeclarationNotApplicable, pc.DeclarationPending, pc.DeclarationSatisfied, pc.DeclarationFailed:
		return state
	default:
		if state != "" {
			omitted["readiness_state"]++
		}
		return pc.DeclarationUnknown
	}
}

func contextResourceFact(fact pc.DeclarationFact, status pc.DeclarationStatus, omitted map[string]int) contextFact {
	fact.State = contextFactState(fact.State, omitted)
	if !declarationDisplayText(fact.Revision, fact.EvidenceRef) {
		fact = pc.DeclarationFact{State: pc.DeclarationUnknown}
		omitted["resource_detail"]++
	}
	out := contextFact{DeclarationFact: fact, Coverage: "resource_only", RevisionBasis: "owner_revision_not_declaration", EvidenceTime: "not_reported"}
	out.ReadAt = contextReadAt(status.ObservedAt)
	if fact.Revision == "" {
		out.RevisionBasis = "not_reported"
	} else if status.Revision != "" && fact.Revision == status.Revision {
		out.RevisionBasis = "current_declaration"
	}
	if fact.State == pc.DeclarationUnknown {
		out.Coverage = "unknown"
	} else if fact.State == pc.DeclarationNotApplicable {
		out.Coverage = "not_applicable"
	} else if fact.State == pc.DeclarationFailed {
		out.Coverage = "blocked_resource_only"
	}
	return out
}

func contextReadAt(value string) string {
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return ""
	}
	return value
}

func renderProjectContext(cmd *cobra.Command, opts *options, v projectContext) error {
	if opts.jsonOutput {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if len(data)+1 > 8192 {
			return fmt.Errorf("project context exceeded its JSON budget")
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return err
	}
	omitted := make(map[string]int, len(v.Omitted))
	for key, count := range v.Omitted {
		omitted[key] = count
	}
	v.Omitted = omitted
	// Navigation comes first; reserve compact current readiness before optional
	// prose/history. Each resource remains qualified, not project-wide green.
	mandatory := []string{
		fmt.Sprintf("Desired: %s | Queued: %s | Applied: %s", v.Readiness.Desired.State, v.Readiness.Queued.State, v.Readiness.Applied.State),
		fmt.Sprintf("Processing: %s | Healthy: %s | Protected: %s | Verified: %s", v.Readiness.Processing.State, v.Readiness.Healthy.State, v.Readiness.Protected.State, v.Readiness.Verified.State),
	}
	if v.WorkState != "" {
		mandatory = append(mandatory, "Work state: "+v.WorkState+" (independent of runtime status)")
	}
	if v.SourceStatus != "not_requested" {
		mandatory = append(mandatory, "Source detail: "+v.SourceStatus)
	}
	if len(v.Resources) > 0 {
		mandatory = append(mandatory, projectResourceEvidenceLine(v.ObservedAt))
	}
	lines := []string{}
	if v.Target != nil {
		label := "Root: "
		if v.Lifecycle == "archived" {
			label = "Root (historical registration): "
		}
		lines = append(lines, label+v.Target.ProjectRoot, "Project: "+v.Target.ProjectID+"; Node: "+v.Target.OwnerNodeID)
	}
	if d := v.Development; d != nil && d.Archive != nil && d.Archive.ReadOnlyRoot != "" {
		lines = append(lines, "Retained root (read-only; verified custody): "+d.Archive.ReadOnlyRoot)
	}
	for _, repository := range v.Repositories {
		lines = append(lines, "Repository: "+repository.Key+"; path "+repository.Path+"; id "+repository.ID)
	}
	lines = append(lines, mandatory...)
	if d := v.Development; d != nil {
		development := "Development: " + d.Posture + " (independent of deployment)"
		if d.PurposeStatus == "unset_scaffold" {
			development += "; purpose unset (unchanged OVERVIEW scaffold)"
		}
		if len(d.TruncatedFields) > 0 {
			development += "; excerpts: " + strings.Join(d.TruncatedFields, ", ") + " (read .project sources for full text)"
		}
		lines = append(lines, development)
		for _, field := range []struct{ name, value string }{{"Focus", d.CurrentFocus}, {"Progress", d.Progress}} {
			if field.value != "" {
				lines = append(lines, field.name+": "+field.value)
			}
		}
		if index := d.Conversations; index != nil {
			lines = append(lines, fmt.Sprintf("Conversation index: %s; hash=%s; excerpt truncated=%t", index.Path, index.Hash, index.Truncated))
			if index.Excerpt != "" {
				lines = append(lines, strings.Join(strings.Fields(index.Excerpt), " "))
			}
		}
	}
	if v.Revision != "" {
		lines = append(lines, "Current revision: "+v.Revision)
	}
	for _, resource := range v.Resources {
		lines = append(lines, projectResourceSummaryLines(resource)...)
	}
	for _, cause := range v.Causes {
		lines = append(lines, fmt.Sprintf("Cause: %s (%s); retryable=%t", cause.CauseCode, cause.Code, cause.Retryable))
		for _, effect := range cause.CompletedEffects {
			lines = append(lines, "Completed effect: "+effect)
		}
	}
	if d := v.Development; d != nil {
		if d.Archive != nil {
			lines = append(lines, "Archive: "+d.Archive.OperationID+"; manifest "+d.Archive.ManifestDigest)
		}
		purpose := d.Purpose
		for _, field := range []struct{ name, value string }{{"Blockers", d.Blockers}, {"Next project action", d.NextAction}, {"Purpose", purpose}} {
			if field.value != "" {
				lines = append(lines, field.name+": "+field.value)
			}
		}
	}
	if op := v.HistoricalOperation; op != nil {
		lines = append(lines, fmt.Sprintf("Historical operation: %s (%s); revision %s; plan %s", op.OperationID, op.State, op.Revision, op.PlanID))
		for _, effect := range op.CompletedEffects {
			lines = append(lines, "Completed effect: "+effect)
		}
	}
	if v.Source != nil {
		lines = append(lines, "Source: "+v.Source.OwnerNodeID+":"+v.Source.Ref)
	}
	for _, source := range v.Sources {
		lines = append(lines, "Source detail: "+source.Ref+"; hash "+source.Hash+"; revision "+source.Revision)
	}
	// Reserve the final two lines for an exact detail command and next action.
	next := strings.TrimSpace("Next: " + v.NextAction.Reason + " " + v.NextAction.Command)
	omissionLine := func() string {
		keys := make([]string, 0, len(v.Omitted))
		for key := range v.Omitted {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := []string{}
		for _, key := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", key, v.Omitted[key]))
		}
		return "Omitted: " + strings.Join(parts, ", ") + "; detail: " + v.DetailCommand
	}
	// Allow growth of counts and the three human-only omission keys.
	omissionBudget := len(omissionLine()) + 128 + 1
	kept := []string{}
	bytes := len(next) + 1
	keptLines := 0
	reservedBytes := 0
	for _, line := range mandatory {
		reservedBytes += len(line) + 1
	}
	reservedLines := len(mandatory)
	for _, line := range lines {
		required := false
		for _, essential := range mandatory {
			if line == essential {
				required = true
				break
			}
		}
		if required {
			reservedBytes -= len(line) + 1
			reservedLines--
		}
		lineCount := strings.Count(line, "\n") + 1
		if keptLines+lineCount+reservedLines > 14 || bytes+len(line)+1+reservedBytes+omissionBudget > 4096 {
			v.Omitted["human_lines"]++
			if strings.HasPrefix(line, "Resource: ") {
				v.Omitted["human_resources"]++
			} else if strings.HasPrefix(line, "Repository: ") {
				v.Omitted["human_repositories"]++
			} else if strings.HasPrefix(line, "Retained root ") {
				v.Omitted["archive_root"]++
			}
			continue
		}
		kept = append(kept, line)
		keptLines += lineCount
		bytes += len(line) + 1
	}
	if len(v.Omitted) > 0 {
		kept = append(kept, omissionLine())
	}
	kept = append(kept, next)
	_, err := fmt.Fprintln(cmd.OutOrStdout(), strings.Join(kept, "\n"))
	return err
}
