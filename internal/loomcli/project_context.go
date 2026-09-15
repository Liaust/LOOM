package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
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
	Development         *pc.DeclarationDevelopmentContext `json:"development,omitempty"`
	SchemaVersion       string                            `json:"schema_version"`
	Target              *pc.DeclarationTarget             `json:"target,omitempty"`
	Revision            string                            `json:"revision,omitempty"`
	Readiness           pc.DeclarationReadiness           `json:"readiness"`
	HistoricalOperation *contextOperation                 `json:"historical_operation,omitempty"`
	Source              *contextSource                    `json:"source,omitempty"`
	Sources             []pc.DeclarationSource            `json:"sources,omitempty"`
	SourceStatus        string                            `json:"source_status"`
	Causes              []contextCause                    `json:"causes"`
	NextAction          declarationNextAction             `json:"next_action"`
	Omitted             map[string]int                    `json:"omitted"`
}
type contextOperation struct {
	OperationID string                       `json:"operation_id"`
	PlanID      string                       `json:"plan_id"`
	Revision    string                       `json:"revision"`
	State       pc.DeclarationOperationState `json:"state"`
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
		env, err := client.GetProjectDeclarationStatus(ctx, cid, args[0], node)
		if err != nil {
			return renderDeclarationFailure(cmd, opts, cid, err, nil)
		}
		view := newProjectContext(env.Data)
		if verbose || opts.verboseOutput {
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
	if d := status.Development; d != nil {
		if declarationDisplayText(d.Posture, d.SourceDigest, d.Purpose, d.CurrentFocus, d.Progress, d.Blockers, d.NextAction, d.SnapshotRef) {
			copy := *d
			v.Development = &copy
		}
		if v.Development == nil || !v.fits() {
			v.Development = nil
			v.Omitted["development"]++
		}
	}
	// Operation history contributes neither current readiness nor current causes.
	v.NextAction = declarationStatusNextAction(status)
	original := status.Readiness
	for _, fact := range declarationFacts(&v.Readiness) {
		fact.Revision, fact.EvidenceRef = "", ""
		switch fact.State {
		case pc.DeclarationUnknown, pc.DeclarationNotApplicable, pc.DeclarationPending, pc.DeclarationSatisfied, pc.DeclarationFailed:
		default:
			if fact.State != "" {
				v.Omitted["readiness_state"]++
			}
			fact.State = pc.DeclarationUnknown
		}
	}
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
	if v.Target != nil && path.IsAbs(v.Target.ProjectRoot) && v.Target.OwnerNodeID != "" {
		v.Source = &contextSource{OwnerNodeID: v.Target.OwnerNodeID, Ref: path.Join(v.Target.ProjectRoot, pc.CanonicalRootContractPath)}
		if !v.fits() {
			v.Source = nil
		}
	}
	if v.Source == nil {
		v.Omitted["source"]++
	}
	if op := status.Operation; op != nil {
		if declarationDisplayText(op.OperationID, op.PlanID, op.Revision, string(op.State)) {
			v.HistoricalOperation = &contextOperation{op.OperationID, op.PlanID, op.Revision, op.State}
			if !v.fits() {
				v.HistoricalOperation = nil
			}
		}
		if v.HistoricalOperation == nil {
			v.Omitted["historical_operation"]++
		}
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
	for i, failure := range status.Errors {
		if i >= 3 {
			v.Omitted["causes"] += len(status.Errors) - i
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
	if len(status.Resources) > 0 {
		v.Omitted["resources"] = len(status.Resources)
	}
	return v
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
	lines := []string{}
	if d := v.Development; d != nil {
		lines = append(lines, "Development: "+d.Posture)
		for _, field := range []struct{ name, value string }{{"Purpose", d.Purpose}, {"Focus", d.CurrentFocus}, {"Progress", d.Progress}, {"Blockers", d.Blockers}, {"Next project action", d.NextAction}} {
			if field.value != "" {
				lines = append(lines, field.name+": "+field.value)
			}
		}
	}
	omitted := make(map[string]int, len(v.Omitted))
	for key, count := range v.Omitted {
		omitted[key] = count
	}
	v.Omitted = omitted
	// Reserve every current readiness dimension and verbose drift information
	// before optional long identity/source lines can consume the human budget.
	mandatory := []string{}
	for i, fact := range declarationFacts(&v.Readiness) {
		mandatory = append(mandatory, declarationFactNames[i]+": "+string(fact.State))
	}
	if v.SourceStatus != "not_requested" {
		mandatory = append(mandatory, "Source detail: "+v.SourceStatus)
	}
	if v.Target != nil {
		lines = append(lines, "Project: "+v.Target.ProjectID, "Node: "+v.Target.OwnerNodeID, "Root: "+v.Target.ProjectRoot)
	}
	if v.Revision != "" {
		lines = append(lines, "Current revision: "+v.Revision)
	}
	lines = append(lines, mandatory...)
	if op := v.HistoricalOperation; op != nil {
		lines = append(lines, fmt.Sprintf("Historical operation: %s (%s); revision %s; plan %s", op.OperationID, op.State, op.Revision, op.PlanID))
	}
	if v.Source != nil {
		lines = append(lines, "Source: "+v.Source.OwnerNodeID+":"+v.Source.Ref)
	}
	for _, cause := range v.Causes {
		lines = append(lines, fmt.Sprintf("Cause: %s (%s); retryable=%t", cause.CauseCode, cause.Code, cause.Retryable))
		if len(cause.CompletedEffects) > 0 {
			v.Omitted["human_effects"] += len(cause.CompletedEffects)
		}
	}
	for _, source := range v.Sources {
		lines = append(lines, "Source detail: "+source.Ref+"; hash "+source.Hash+"; revision "+source.Revision)
	}
	// Reserve the final two lines for omissions and one whole next action.
	next := "Next: " + v.NextAction.Reason + " " + v.NextAction.Command
	kept := []string{}
	bytes := len(next) + 1
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
		if len(kept)+reservedLines >= 14 || bytes+len(line)+1+reservedBytes > 3584 {
			v.Omitted["human_lines"]++
			continue
		}
		kept = append(kept, line)
		bytes += len(line) + 1
	}
	if len(v.Omitted) > 0 {
		keys := make([]string, 0, len(v.Omitted))
		for k := range v.Omitted {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := []string{}
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", k, v.Omitted[k]))
		}
		kept = append(kept, "Omitted: "+strings.Join(parts, ", ")+"; inspect full status/operation JSON.")
	}
	kept = append(kept, next)
	_, err := fmt.Fprintln(cmd.OutOrStdout(), strings.Join(kept, "\n"))
	return err
}
