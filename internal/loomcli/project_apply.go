package loomcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/localclient"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/response"
)

func addProjectDeclarationCommands(parent *cobra.Command, opts *options, plan, status *cobra.Command) {
	addProjectContextCommand(parent, opts)
	legacyPlan, legacyStatus := plan.RunE, status.RunE
	analyze := &cobra.Command{Use: "analyze <project-folder-or-ref>", Short: "Print the legacy read-only contract registration analysis", Args: cobra.ExactArgs(1), RunE: legacyPlan}
	analyze.Flags().AddFlagSet(plan.Flags())
	parent.AddCommand(analyze)
	parent.AddCommand(&cobra.Command{Use: "registration-status <project-ref>", Short: "Show the legacy registration and facet inventory", Args: cobra.ExactArgs(1), RunE: legacyStatus})
	var planNode, statusNode string
	var refresh, legacyPlanFlag, legacyStatusFlag, conversionPreview bool
	plan.Short = "Review declaration effects and resolved owner; local v0.3/v0.4 sources retain legacy analysis"
	plan.Flags().StringVar(&planNode, "node", "", "explicit owner node; paths resolve on that node")
	plan.Flags().BoolVar(&refresh, "refresh-projections", false, "include the explicit projection refresh effect")
	plan.Flags().BoolVar(&legacyPlanFlag, "legacy", false, "use the legacy contract analysis path")
	plan.RunE = func(cmd *cobra.Command, args []string) error {
		if legacyPlanFlag {
			if planNode != "" || refresh {
				return fmt.Errorf("--legacy cannot select declaration nodes or effects")
			}
			return legacyPlan(cmd, args)
		}
		backend, _ := cmd.Flags().GetBool("backend")
		projectRef := args[0]
		// Only a positively identified current legacy source selects the old reader.
		// Explicit node selection never opens a caller-local file or changes host.
		if planNode == "" && !refresh {
			var analysis pc.Analysis
			if backend {
				var err error
				analysis, _, err = analyzeProjectForCLI(cmd, opts, args[0], true)
				if err != nil {
					return err
				}
			} else if _, err := os.Stat(args[0]); err == nil {
				analysis = pc.Analyze(args[0])
			}
			if analysis.Loaded != nil && (analysis.Loaded.Contract.SchemaVersion == pc.ProjectSchemaV03 || analysis.Loaded.Contract.SchemaVersion == pc.ProjectSchemaV04) {
				return legacyPlan(cmd, args)
			}
			if !backend && analysis.Loaded != nil && analysis.Loaded.Declaration != nil {
				projectRef = analysis.Loaded.RootPath
			}
			// An old backend response without source identity is never a v0.5 plan.
			if backend && analysis.Loaded == nil {
				return legacyPlan(cmd, args)
			}
		}
		if !backend && planNode == "" && !filepath.IsAbs(projectRef) {
			if info, e := os.Stat(projectRef); e == nil && info.IsDir() {
				projectRef, e = filepath.Abs(projectRef)
				if e != nil {
					return e
				}
			}
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		cid, client, err := declarationClient(cmd, opts)
		if err != nil {
			return err
		}
		input := pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: projectRef, NodeRef: planNode, Effects: declarationEffects(refresh)}
		env, err := client.PlanProjectDeclaration(ctx, cid, input)
		if err != nil {
			return renderDeclarationFailure(cmd, opts, cid, err, nil)
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(env.Data)
		}
		target := env.Data.Basis.Target
		fmt.Fprintf(cmd.OutOrStdout(), "Declaration plan: %s\nProject: %s\nNode: %s\nRoot: %s\nActions: %d\n", declarationDisplay(env.Data.PlanID), declarationDisplay(target.ProjectID), declarationDisplay(target.OwnerNodeID), declarationDisplay(target.ProjectRoot), len(env.Data.Basis.Actions))
		for _, action := range env.Data.Basis.Actions {
			if action.PublicURL != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Public exposure (%s): %s\n", declarationDisplay(string(action.Resource)), declarationDisplay(action.PublicURL))
			}
		}
		renderDeclarationReadiness(cmd, env.Data.Readiness)
		next := pc.DeclarationApplyRequest{ProjectRef: input.ProjectRef, NodeRef: input.NodeRef, Effects: input.Effects, PlanID: env.Data.PlanID, IdempotencyKey: "declaration:" + env.Data.PlanID}
		action := declarationNextAction{Kind: "apply", Reason: "Apply the reviewed plan.", Command: declarationApplyCommand(next)}
		if len(env.Data.Errors) > 0 {
			renderDeclarationCauses(cmd, env.Data.Errors, nil)
			action = declarationResultNextAction(pc.DeclarationResult{Target: target, State: pc.DeclarationOperationFailed, Errors: env.Data.Errors}, &next)
		} else if !declarationRequestCustody(next) {
			action = declarationInspectAction(target, "", "Inspect the request; an exact apply command is unavailable.")
		}
		renderDeclarationNextAction(cmd, boundDeclarationAction(action, target, ""))
		return nil
	}
	status.Short = "Read declaration readiness without advancing an operation"
	status.Flags().StringVar(&statusNode, "node", "", "explicit owner node")
	status.Flags().BoolVar(&legacyStatusFlag, "legacy", false, "read the legacy registration inventory")
	status.Flags().BoolVar(&conversionPreview, "conversion-preview", false, "assess registered legacy conversion without publishing source or applying effects")
	status.RunE = func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("conversion-preview") && cmd.Flags().Changed("legacy") {
			return fmt.Errorf("--conversion-preview conflicts with --legacy")
		}
		if legacyStatusFlag {
			if statusNode != "" {
				return fmt.Errorf("--legacy cannot select a declaration node")
			}
			return legacyStatus(cmd, args)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		cid, client, err := declarationClient(cmd, opts)
		if err != nil {
			return err
		}
		if conversionPreview {
			env, err := client.GetProjectConversionAssessment(ctx, cid, args[0], statusNode)
			if err != nil {
				return renderDeclarationFailure(cmd, opts, cid, err, nil)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(env.Data)
			}
			a := env.Data
			fmt.Fprintf(cmd.OutOrStdout(), "Conversion assessment: %s\nProject: %s\nNode: %s\nBasis: %s\n", a.State, declarationDisplay(a.ProjectID), declarationDisplay(a.OwnerNode), a.SnapshotRevision)
			fmt.Fprintln(cmd.OutOrStdout(), "Observed reads only; source publication is pending.")
			for _, issue := range a.Issues {
				fmt.Fprintf(cmd.OutOrStdout(), "Issue: %s (%s)\n", declarationDisplay(issue.Code), declarationDisplay(issue.Family))
			}
			for _, refusal := range a.Preview.Issues {
				fmt.Fprintf(cmd.OutOrStdout(), "Refusal: %s\n", declarationDisplay(refusal.CauseCode))
			}
			if a.Preview.Candidate != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Hypothetical candidate: %s\n", a.Preview.Candidate.Digest)
			}
			renderDeclarationReadiness(cmd, a.Readiness)
			return nil
		}
		env, err := client.GetProjectDeclarationStatus(ctx, cid, args[0], statusNode)
		if err != nil {
			return renderDeclarationFailure(cmd, opts, cid, err, nil)
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(env.Data)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Declaration: %s\nNode: %s\nRoot: %s\nCurrent revision: %s\n", declarationDisplay(env.Data.Target.ProjectID), declarationDisplay(env.Data.Target.OwnerNodeID), declarationDisplay(env.Data.Target.ProjectRoot), declarationDisplay(env.Data.Revision))
		renderDeclarationReadiness(cmd, env.Data.Readiness)
		keys := make([]string, 0, len(env.Data.Resources))
		for key := range env.Data.Resources {
			keys = append(keys, string(key))
		}
		sort.Strings(keys)
		for _, key := range keys {
			if endpoint := env.Data.Resources[pc.ResourceKey(key)].Readiness.Endpoint; endpoint != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Application: %s\n", declarationDisplay(key))
				renderDeclarationEndpoint(cmd, endpoint)
			}
		}
		if env.Data.Operation != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Historical operation: %s (%s); revision %s\n", declarationDisplay(env.Data.Operation.OperationID), declarationDisplay(string(env.Data.Operation.State)), declarationDisplay(env.Data.Operation.Revision))
		}
		renderDeclarationCauses(cmd, env.Data.Errors, nil)
		renderDeclarationNextAction(cmd, declarationStatusNextAction(env.Data))
		return nil
	}
	var input pc.DeclarationApplyRequest
	var applyRefresh bool
	apply := &cobra.Command{Use: "apply <project-folder-or-ref>", Short: "Apply a reviewed declaration or resume its original operation", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		input.SchemaVersion = pc.DeclarationRequestSchemaV05
		input.ProjectRef = args[0]
		if input.NodeRef == "" && !filepath.IsAbs(input.ProjectRef) {
			if info, e := os.Stat(input.ProjectRef); e == nil && info.IsDir() {
				input.ProjectRef, e = filepath.Abs(input.ProjectRef)
				if e != nil {
					return e
				}
			}
		}
		input.Effects = declarationEffects(applyRefresh)
		ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
		defer cancel()
		cid, client, err := declarationClient(cmd, opts)
		if err != nil {
			return err
		}
		env, err := client.ApplyProjectDeclaration(ctx, cid, input)
		if err != nil {
			return renderDeclarationFailure(cmd, opts, cid, err, &input)
		}
		return renderDeclarationResult(cmd, opts, env.Data, &input)
	}}
	apply.Flags().StringVar(&input.NodeRef, "node", "", "explicit owner node; paths resolve on that node")
	apply.Flags().StringVar(&input.PlanID, "plan-id", "", "exact reviewed declaration plan ID")
	apply.Flags().StringVar(&input.IdempotencyKey, "idempotency-key", "", "stable original request key, including after a lost response")
	apply.Flags().StringVar(&input.OperationID, "resume", "", "resume this operation with its original selectors, effects, plan and key")
	apply.Flags().StringArrayVar(&input.ApprovalRefs, "approval", nil, "exact approval reference (repeatable)")
	apply.Flags().BoolVar(&applyRefresh, "refresh-projections", false, "include the reviewed projection refresh effect")
	_ = apply.MarkFlagRequired("plan-id")
	_ = apply.MarkFlagRequired("idempotency-key")
	parent.AddCommand(apply)
	parent.AddCommand(&cobra.Command{Use: "operation <operation-id>", Short: "Read a historical declaration operation without resuming it", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		cid, client, err := declarationClient(cmd, opts)
		if err != nil {
			return err
		}
		env, err := client.GetProjectDeclarationOperation(ctx, cid, args[0])
		if err != nil {
			return renderDeclarationFailure(cmd, opts, cid, err, nil)
		}
		return renderDeclarationResult(cmd, opts, env.Data, nil)
	}})
}
func declarationEffects(refresh bool) []pc.DeclarationEffect {
	effects := []pc.DeclarationEffect{pc.DeclarationReconcile}
	if refresh {
		effects = append(effects, pc.DeclarationProjections)
	}
	return effects
}
func declarationClient(cmd *cobra.Command, opts *options) (string, localclient.Client, error) {
	cid := correlation.Normalize(opts.correlationID)
	_, client, err := commandClient(opts)
	if err != nil {
		return cid, client, renderError(cmd, opts, cid, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
	}
	return cid, client, nil
}
func renderDeclarationFailure(cmd *cobra.Command, opts *options, cid string, err error, input *pc.DeclarationApplyRequest) error {
	var failure *localclient.DeclarationRequestError
	if errors.As(err, &failure) && failure.Result != nil && failure.Result.OperationID != "" {
		return renderOwnedCLIError(cmd, err, func(cmd *cobra.Command) error {
			result := *failure.Result
			if !opts.jsonOutput && failure.Detail.Code != "" {
				result.Errors = append(append([]pc.DeclarationError(nil), result.Errors...), failure.Detail)
			}
			if e := renderDeclarationResult(cmd, opts, result, input); e != nil {
				return e
			}
			if !opts.jsonOutput {
				renderDeclarationErrorMetadata(cmd, failure)
			}
			// Exactly one JSON result has already been emitted. Keep the typed transport
			// error for the exit status and callers; never print a second error envelope.
			return nil
		})
	}
	if failure != nil && failure.Detail.Code != "" {
		return renderOwnedCLIError(cmd, err, func(cmd *cobra.Command) error {
			if opts.jsonOutput {
				// Preserve the original standard envelope and server extension verbatim.
				if e := json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					response.ErrorEnvelope
					Detail pc.DeclarationError `json:"declaration_error"`
				}{failure.Envelope, failure.Detail}); e != nil {
					return e
				}
			} else {
				renderDeclarationErrorMetadata(cmd, failure)
				renderDeclarationCauses(cmd, []pc.DeclarationError{failure.Detail}, nil)
				renderDeclarationNextAction(cmd, declarationResultNextAction(pc.DeclarationResult{State: pc.DeclarationOperationFailed, Errors: []pc.DeclarationError{failure.Detail}}, input))
			}
			return nil
		})
	}
	var requestErr *localclient.RequestError
	if errors.As(err, &requestErr) {
		return renderError(cmd, opts, cid, err)
	}
	return renderError(cmd, opts, cid, loomerrors.Wrap("transport.unavailable", "runtime", "declaration", "Could not reach the declaration service. Retry with the original plan and idempotency key if the response was lost.", err))
}
func renderDeclarationErrorMetadata(cmd *cobra.Command, failure *localclient.DeclarationRequestError) {
	env := failure.Envelope
	fmt.Fprintf(cmd.ErrOrStderr(), "Error: %s: %s\n", declarationDisplay(env.Error.Code), declarationDisplay(env.Error.Summary))
	for _, field := range []struct{ label, value string }{
		{"Domain", env.Error.Domain}, {"Target", env.Error.Target},
		{"Idempotency key", env.Meta.IdempotencyKey}, {"Correlation", failure.CorrelationID("")},
	} {
		if field.value != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: %s\n", field.label, declarationDisplay(field.value))
		}
	}
}
func renderDeclarationResult(cmd *cobra.Command, opts *options, result pc.DeclarationResult, input *pc.DeclarationApplyRequest) error {
	if opts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Declaration operation: %s\nState: %s\n", declarationDisplay(result.OperationID), declarationDisplay(string(result.State)))
	renderDeclarationCauses(cmd, result.Errors, result.Actions)
	renderDeclarationReadiness(cmd, result.Readiness)
	renderDeclarationNextAction(cmd, declarationResultNextAction(result, input))
	return nil
}
func renderDeclarationReadiness(cmd *cobra.Command, r pc.DeclarationReadiness) {
	fmt.Fprintf(cmd.OutOrStdout(), "Desired: %s | Queued: %s | Applied: %s\nProcessing: %s | Healthy: %s | Protected: %s | Verified: %s\n", declarationDisplay(string(r.Desired.State)), declarationDisplay(string(r.Queued.State)), declarationDisplay(string(r.Applied.State)), declarationDisplay(string(r.Processing.State)), declarationDisplay(string(r.Healthy.State)), declarationDisplay(string(r.Protected.State)), declarationDisplay(string(r.Verified.State)))
}

func renderDeclarationEndpoint(cmd *cobra.Command, e *pc.ApplicationEndpointStatus) {
	fmt.Fprintf(cmd.OutOrStdout(), "Public URL: %s\nDNS: %s | TLS: %s | Route: %s | HTTP: %d\n", declarationDisplay(e.URL), declarationDisplay(e.DNS), declarationDisplay(e.TLS), declarationDisplay(e.Routing), e.HTTPStatus)
}
func quoteDeclarationArg(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func declarationApplyCommand(input pc.DeclarationApplyRequest) string {
	next := "loom project apply " + quoteDeclarationArg(input.ProjectRef)
	if input.NodeRef != "" {
		next += " --node " + quoteDeclarationArg(input.NodeRef)
	}
	next += " --plan-id " + quoteDeclarationArg(input.PlanID) + " --idempotency-key " + quoteDeclarationArg(input.IdempotencyKey)
	for _, effect := range input.Effects {
		if effect == pc.DeclarationProjections {
			next += " --refresh-projections"
		}
	}
	for _, ref := range input.ApprovalRefs {
		next += " --approval " + quoteDeclarationArg(ref)
	}
	if input.OperationID != "" {
		next += " --resume " + quoteDeclarationArg(input.OperationID)
	}
	return next
}

func declarationSelector(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && declarationDisplayText(s)
}
func declarationSafeCommand(s string) bool { return len(s) <= 1024 && declarationDisplayText(s) }
func declarationInspectAction(target pc.DeclarationTarget, operation, reason string) declarationNextAction {
	command := "loom project context --help"
	if declarationSelector(operation) {
		candidate := "loom project operation " + quoteDeclarationArg(operation) + " --json"
		if declarationSafeCommand(candidate) {
			command = candidate
		}
	}
	if command == "loom project context --help" && declarationSelector(target.ProjectID) && declarationSelector(target.OwnerNodeID) {
		candidate := "loom project status " + quoteDeclarationArg(target.ProjectID) + " --node " + quoteDeclarationArg(target.OwnerNodeID) + " --json"
		if declarationSafeCommand(candidate) {
			command = candidate
		}
	}
	return declarationNextAction{Kind: "inspect", Reason: reason, Command: command}
}
func boundDeclarationAction(action declarationNextAction, target pc.DeclarationTarget, operation string) declarationNextAction {
	if !declarationSafeCommand(action.Command) {
		return declarationInspectAction(target, operation, "Inspect the operation/request; the exact command is unsafe or exceeds the display budget.")
	}
	return action
}
func declarationRequestCustody(input pc.DeclarationApplyRequest) bool {
	if !declarationSelector(input.ProjectRef) || input.NodeRef != "" && !declarationSelector(input.NodeRef) || input.PlanID == "" || input.IdempotencyKey == "" || !declarationDisplayText(input.PlanID, input.IdempotencyKey, input.OperationID) || !declarationDisplayText(input.ApprovalRefs...) {
		return false
	}
	// The CLI can express exactly these effects; never silently lose a future one.
	if len(input.Effects) == 0 || len(input.Effects) > 2 || input.Effects[0] != pc.DeclarationReconcile {
		return false
	}
	return len(input.Effects) == 1 || input.Effects[1] == pc.DeclarationProjections
}
func declarationReplanAction(input pc.DeclarationApplyRequest, target pc.DeclarationTarget, operation string) declarationNextAction {
	if input.NodeRef == "" {
		input.NodeRef = target.OwnerNodeID
	}
	if !declarationSelector(input.ProjectRef) || !declarationSelector(input.NodeRef) {
		return declarationInspectAction(target, operation, "Inspect source identity and owner before replanning.")
	}
	command := "loom project plan " + quoteDeclarationArg(input.ProjectRef)
	if input.NodeRef != "" {
		command += " --node " + quoteDeclarationArg(input.NodeRef)
	}
	for _, effect := range input.Effects {
		if effect == pc.DeclarationProjections {
			command += " --refresh-projections"
		}
	}
	return boundDeclarationAction(declarationNextAction{Kind: "replan", Reason: "Resolve source/identity drift, then review a new plan on the same owner.", Command: command}, target, operation)
}
func declarationResultNextAction(result pc.DeclarationResult, input *pc.DeclarationApplyRequest) declarationNextAction {
	inspect := func(reason string) declarationNextAction {
		return declarationInspectAction(result.Target, result.OperationID, reason)
	}
	if result.State == pc.DeclarationOperationSucceeded || result.State == pc.DeclarationOperationSuperseded {
		return declarationInspectAction(result.Target, "", "Inspect current status; operation history does not establish current readiness.")
	}
	failures := append([]pc.DeclarationError(nil), result.Errors...)
	for _, action := range result.Actions {
		if action.Error != nil {
			failures = append(failures, *action.Error)
		}
	}
	stale, retryable := false, len(failures) > 0
	for _, e := range failures {
		switch e.Code {
		case pc.DeclarationUnauthorized:
			return inspect("Resolve the permission prerequisite, then inspect the operation.")
		case pc.DeclarationApprovalRequired:
			return inspect("Obtain the required approval, then inspect the operation.")
		case pc.DeclarationUnsupported:
			return inspect("Resolve the unsupported prerequisite, then inspect the operation.")
		case pc.DeclarationPlanStale, pc.DeclarationIdentityConflict:
			stale = true
		}
		pending := e.Code == pc.DeclarationOwnerFailed && (e.CauseCode == "owner_pending" || e.CauseCode == "observation_uncertain")
		if !e.Retryable && !pending {
			retryable = false
		}
	}
	if stale && input != nil {
		return declarationReplanAction(*input, result.Target, result.OperationID)
	}
	if input == nil {
		return inspect("Inspect current status and prerequisites; the original request is not available here.")
	}
	if !declarationRequestCustody(*input) {
		return inspect("Inspect the request; exact selectors, effects, plan, key and approvals are required.")
	}
	pending := len(failures) == 0 && (result.State == pc.DeclarationOperationQueued || result.State == pc.DeclarationOperationRunning || result.State == pc.DeclarationOperationPartial)
	if !stale && (pending || retryable) && declarationSelector(result.OperationID) {
		next := *input
		next.OperationID = result.OperationID
		return boundDeclarationAction(declarationNextAction{Kind: "resume", Reason: "Continue with the original request.", Command: declarationApplyCommand(next)}, result.Target, result.OperationID)
	}
	return inspect("Inspect the cause and resolve its prerequisite before further work.")
}
func declarationStatusNextAction(status pc.DeclarationStatus) declarationNextAction {
	if len(status.Errors) > 0 {
		// A verified status target permits same-owner replanning, but carries no
		// original apply request, key or approval custody and can never resume.
		input := pc.DeclarationApplyRequest{ProjectRef: status.Target.ProjectID, NodeRef: status.Target.OwnerNodeID}
		return declarationResultNextAction(pc.DeclarationResult{Target: status.Target, State: pc.DeclarationOperationFailed, Errors: status.Errors}, &input)
	}
	if status.Operation != nil {
		return declarationInspectAction(status.Target, status.Operation.OperationID, "Inspect the historical operation; current readiness remains separate.")
	}
	return declarationReplanAction(pc.DeclarationApplyRequest{ProjectRef: status.Target.ProjectID, NodeRef: status.Target.OwnerNodeID}, status.Target, "")
}
func renderDeclarationNextAction(cmd *cobra.Command, action declarationNextAction) {
	fmt.Fprintln(cmd.OutOrStdout(), "Next: "+action.Reason+" "+action.Command)
}
func renderDeclarationCauses(cmd *cobra.Command, failures []pc.DeclarationError, actions []pc.DeclarationActionResult) {
	// Full machine-readable results remain untouched. Human collections are
	// bounded while retaining available completed-effect references after failure.
	seen := map[string]bool{}
	remaining, omitted := 12, 0
	line := func(label, value string) {
		key := label + value
		if seen[key] {
			return
		}
		seen[key] = true
		if remaining == 0 || len(value) > 512 || !declarationDisplayText(value) {
			omitted++
			return
		}
		fmt.Fprintln(cmd.OutOrStdout(), label+value)
		remaining--
	}
	cause := func(e pc.DeclarationError) {
		for _, issue := range e.Preflight {
			line("Requirement: ", fmt.Sprintf("%s.%s [%s] %s: %s", issue.Resource, issue.Field, issue.State, issue.Code, issue.Message))
		}
		if e.Code != "" || e.CauseCode != "" {
			line("Cause: ", fmt.Sprintf("%s (%s); retryable=%t", e.CauseCode, e.Code, e.Retryable))
		}
		for _, effect := range e.CompletedEffects {
			line("Completed effect: ", effect)
		}
	}
	for _, e := range failures {
		cause(e)
	}
	for _, action := range actions {
		if action.EffectRef != "" {
			line("Completed effect: ", action.EffectRef)
		}
		if action.Error != nil {
			cause(*action.Error)
		}
	}
	if omitted > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Omitted: %d cause/effect fields; inspect full JSON.\n", omitted)
	}
}
