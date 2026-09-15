package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/routing"
)

const defaultCapabilityCallWaitTimeoutSeconds = 120

func addCapabilityCallCommand(cmd *cobra.Command, opts *options) {
	input := routing.CapabilityCallInput{}
	var inputText string
	var inputFile string
	var idempotencyKey string
	var wait bool
	var timeoutSeconds int
	callCmd := &cobra.Command{
		Use:   "call <target>",
		Short: "Call a LOOM capability through the router",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			timeout := time.Duration(timeoutSeconds) * time.Second
			if timeout <= 0 {
				timeout = 15 * time.Second
			}
			contextTimeout := 20 * time.Second
			if wait && timeout+5*time.Second > contextTimeout {
				contextTimeout = timeout + 5*time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), contextTimeout)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			callInput := input
			callInput.Target = args[0]
			rawInput, err := readCapabilityInput(inputText, inputFile)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("capability_call.input_invalid", "routing", "input", "Capability call input is invalid.", err))
			}
			callInput.Input = rawInput
			client, _ = withEffectIdempotency(client, idempotencyKey, "capability.call."+args[0])
			envelope, err := client.CallCapability(ctx, correlationID, callInput)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not call capability.", err))
			}
			if wait {
				waited, err := waitForCapabilityCall(ctx, client, correlationID, envelope.Data, timeout)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("capability_call.wait_timeout", "routing", envelope.Data.CapabilityCall.CapabilityCallID, "Capability call did not reach a terminal state before the wait timeout.", err))
				}
				envelope.Data = waited
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.CapabilityCall.CapabilityCallID)
				return nil
			}
			renderCapabilityCallOutcome(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	callCmd.Flags().StringVar(&inputText, "input", "", "capability input JSON object")
	callCmd.Flags().StringVar(&inputFile, "input-file", "", "path to capability input JSON object")
	callCmd.Flags().StringVar(&input.ActorRef, "actor", "", "actor ID or key")
	callCmd.Flags().StringVar(&input.OriginNodeRef, "origin-node", "", "origin node ID or key")
	callCmd.Flags().StringVar(&input.ScopeRef, "scope", "", "scope ID, key, or slug")
	callCmd.Flags().BoolVar(&input.RequestApproval, "request-approval", false, "create or reuse an approval request when approval is required")
	callCmd.Flags().StringVar(&input.ApprovalReason, "approval-reason", "", "approval reason to store if approval is requested")
	callCmd.Flags().BoolVar(&input.DryRun, "dry-run", false, "plan and authorize without dispatching the provider adapter")
	callCmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "retry key for this capability call")
	callCmd.Flags().BoolVar(&wait, "wait", false, "wait for the capability call to complete or fail")
	callCmd.Flags().IntVar(&timeoutSeconds, "timeout-seconds", defaultCapabilityCallWaitTimeoutSeconds, "maximum seconds to wait when --wait is set")
	cmd.AddCommand(callCmd)
}

func waitForCapabilityCall(ctx context.Context, client localclient.Client, correlationID string, initial routing.CapabilityCallOutcome, timeout time.Duration) (routing.CapabilityCallOutcome, error) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	out := initial
	if capabilityCallTerminal(out.Status) {
		return out, nil
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if time.Now().After(deadline) {
			return out, fmt.Errorf("last status was %s", out.Status)
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-ticker.C:
			callEnvelope, err := client.GetCapabilityCall(ctx, correlationID, out.CapabilityCall.CapabilityCallID)
			if err != nil {
				return out, err
			}
			out = outcomeWithCall(out, callEnvelope.Data)
			if routeEnvelope, err := client.GetRoute(ctx, correlationID, out.Route.RouteID); err == nil {
				out.Route = routeEnvelope.Data
			}
			if capabilityCallTerminal(out.Status) {
				return out, nil
			}
		}
	}
}

func capabilityCallTerminal(status string) bool {
	switch status {
	case routing.CapabilityCallStatusCompleted, routing.CapabilityCallStatusFailed, routing.CapabilityCallStatusCancelled:
		return true
	default:
		return false
	}
}

func outcomeWithCall(out routing.CapabilityCallOutcome, call routing.CapabilityCall) routing.CapabilityCallOutcome {
	out.CapabilityCall = call
	out.PolicyDecisionID = ptrOrEmpty(call.PolicyDecisionID)
	out.ApprovalID = ptrOrEmpty(call.ApprovalID)
	out.GrantID = ptrOrEmpty(call.GrantID)
	out.JobID = ptrOrEmpty(call.JobID)
	out.Result = jsonObjectOrDefault(call.ResultJSON)
	out.ResultRefs = jsonObjectOrDefault(call.ResultRefsJSON)
	out.Status = call.Status
	out.ErrorCode = ptrOrEmpty(call.ErrorCode)
	out.ErrorMessage = ptrOrEmpty(call.ErrorMessage)
	return out
}

func jsonObjectOrDefault(raw json.RawMessage) json.RawMessage {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`)
	}
	return raw
}

func ptrOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func newRoutesCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "routes",
		Short: "List LOOM capability routes",
	}
	filter := routing.RouteFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List capability routes",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListRoutes(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list routes.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, route := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), route.RouteID)
				}
				return nil
			}
			renderRouteList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of routes to return")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by route status")
	listCmd.Flags().StringVar(&filter.ActorRef, "actor", "", "filter by actor ID or key")
	listCmd.Flags().StringVar(&filter.OriginNodeRef, "origin-node", "", "filter by origin node ID or key")
	listCmd.Flags().StringVar(&filter.TargetNodeRef, "target-node", "", "filter by target node ID or key")
	listCmd.Flags().StringVar(&filter.ProviderRef, "provider", "", "filter by provider ID, key, or compact address")
	listCmd.Flags().StringVar(&filter.CapabilityEndpointRef, "capability", "", "filter by capability endpoint ID or compact address")
	listCmd.Flags().StringVar(&filter.CorrelationID, "correlation", "", "filter by correlation ID")
	cmd.AddCommand(listCmd)
	return cmd
}

func newRouteCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "route",
		Short: "Inspect LOOM capability routes",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <route-ref>",
		Short: "Inspect a capability route",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetRoute(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect route.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderRoute(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func newCapabilityCallsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "capability-calls",
		Short: "List LOOM capability calls",
	}
	filter := routing.CapabilityCallFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List capability calls",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListCapabilityCalls(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list capability calls.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, call := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), call.CapabilityCallID)
				}
				return nil
			}
			renderCapabilityCallList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of capability calls to return")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by capability call status")
	listCmd.Flags().StringVar(&filter.ActorRef, "actor", "", "filter by actor ID or key")
	listCmd.Flags().StringVar(&filter.OriginNodeRef, "origin-node", "", "filter by origin node ID or key")
	listCmd.Flags().StringVar(&filter.TargetNodeRef, "target-node", "", "filter by target node ID or key")
	listCmd.Flags().StringVar(&filter.ProviderRef, "provider", "", "filter by provider ID, key, or compact address")
	listCmd.Flags().StringVar(&filter.CapabilityEndpointRef, "capability", "", "filter by capability endpoint ID or compact address")
	listCmd.Flags().StringVar(&filter.CorrelationID, "correlation", "", "filter by correlation ID")
	listCmd.Flags().StringVar(&filter.IdempotencyKey, "idempotency-key", "", "filter by idempotency key")
	cmd.AddCommand(listCmd)
	return cmd
}

func newCapabilityCallCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "capability-call",
		Short: "Inspect LOOM capability calls",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <capability-call-ref>",
		Short: "Inspect a capability call",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetCapabilityCall(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect capability call.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderCapabilityCall(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func readCapabilityInput(inputText, inputFile string) (json.RawMessage, error) {
	inputText = strings.TrimSpace(inputText)
	inputFile = strings.TrimSpace(inputFile)
	if inputText != "" && inputFile != "" {
		return nil, fmt.Errorf("choose only one of --input or --input-file")
	}
	if inputFile != "" {
		raw, err := os.ReadFile(inputFile)
		if err != nil {
			return nil, err
		}
		inputText = string(raw)
	}
	if inputText == "" {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(inputText), &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, fmt.Errorf("input must be a JSON object")
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func renderCapabilityCallOutcome(cmd *cobra.Command, outcome routing.CapabilityCallOutcome) {
	fmt.Fprintf(cmd.OutOrStdout(), "Capability call: %s\n", outcome.CapabilityCall.CapabilityCallID)
	fmt.Fprintf(cmd.OutOrStdout(), "Route: %s\n", outcome.Route.RouteID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", outcome.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Operation: %s\n", outcome.CapabilityCall.Operation)
	fmt.Fprintf(cmd.OutOrStdout(), "Policy decision: %s\n", dashIfEmpty(outcome.PolicyDecisionID))
	fmt.Fprintf(cmd.OutOrStdout(), "Approval: %s\n", dashIfEmpty(outcome.ApprovalID))
	fmt.Fprintf(cmd.OutOrStdout(), "Grant: %s\n", dashIfEmpty(outcome.GrantID))
	if outcome.DispatchMessageID != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Dispatch message: %s\n", outcome.DispatchMessageID)
	}
	if outcome.ErrorCode != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Error: %s %s\n", outcome.ErrorCode, outcome.ErrorMessage)
	}
	if len(outcome.Result) > 0 && string(outcome.Result) != "{}" {
		fmt.Fprintf(cmd.OutOrStdout(), "Result: %s\n", compactText(string(outcome.Result), 240))
	}
}

func renderRouteList(cmd *cobra.Command, routes []routing.Route) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tSTATUS\tKIND\tMODE\tACTOR\tPROVIDER\tCAPABILITY\tCREATED")
	for _, route := range routes {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			route.RouteID,
			route.Status,
			route.RouteKind,
			route.ExecutionMode,
			route.ActorID,
			route.ProviderID,
			route.CapabilityEndpointID,
			route.CreatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderRoute(cmd *cobra.Command, route routing.Route) {
	fmt.Fprintf(cmd.OutOrStdout(), "Route: %s\n", route.RouteID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", route.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Kind: %s\n", route.RouteKind)
	fmt.Fprintf(cmd.OutOrStdout(), "Mode: %s\n", route.ExecutionMode)
	fmt.Fprintf(cmd.OutOrStdout(), "Actor: %s\n", route.ActorID)
	fmt.Fprintf(cmd.OutOrStdout(), "Origin node: %s\n", route.OriginNodeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Target node: %s\n", route.TargetNodeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Provider: %s\n", route.ProviderID)
	fmt.Fprintf(cmd.OutOrStdout(), "Capability endpoint: %s\n", route.CapabilityEndpointID)
	fmt.Fprintf(cmd.OutOrStdout(), "Policy decision: %s\n", ptrOrDash(route.PolicyDecisionID))
	fmt.Fprintf(cmd.OutOrStdout(), "Approval: %s\n", ptrOrDash(route.ApprovalID))
	fmt.Fprintf(cmd.OutOrStdout(), "Grant: %s\n", ptrOrDash(route.GrantID))
	fmt.Fprintf(cmd.OutOrStdout(), "Job: %s\n", ptrOrDash(route.JobID))
	fmt.Fprintf(cmd.OutOrStdout(), "Created: %s\n", route.CreatedAt.UTC().Format(time.RFC3339))
}

func renderCapabilityCallList(cmd *cobra.Command, calls []routing.CapabilityCall) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tSTATUS\tOPERATION\tROUTE\tACTOR\tCREATED")
	for _, call := range calls {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			call.CapabilityCallID,
			call.Status,
			call.Operation,
			call.RouteID,
			call.ActorID,
			call.CreatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderCapabilityCall(cmd *cobra.Command, call routing.CapabilityCall) {
	fmt.Fprintf(cmd.OutOrStdout(), "Capability call: %s\n", call.CapabilityCallID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", call.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Operation: %s\n", call.Operation)
	fmt.Fprintf(cmd.OutOrStdout(), "Route: %s\n", call.RouteID)
	fmt.Fprintf(cmd.OutOrStdout(), "Actor: %s\n", call.ActorID)
	fmt.Fprintf(cmd.OutOrStdout(), "Origin node: %s\n", call.OriginNodeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Target node: %s\n", call.TargetNodeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Provider: %s\n", call.ProviderID)
	fmt.Fprintf(cmd.OutOrStdout(), "Capability endpoint: %s\n", call.CapabilityEndpointID)
	fmt.Fprintf(cmd.OutOrStdout(), "Policy decision: %s\n", ptrOrDash(call.PolicyDecisionID))
	fmt.Fprintf(cmd.OutOrStdout(), "Approval: %s\n", ptrOrDash(call.ApprovalID))
	fmt.Fprintf(cmd.OutOrStdout(), "Grant: %s\n", ptrOrDash(call.GrantID))
	fmt.Fprintf(cmd.OutOrStdout(), "Job: %s\n", ptrOrDash(call.JobID))
	fmt.Fprintf(cmd.OutOrStdout(), "Created: %s\n", call.CreatedAt.UTC().Format(time.RFC3339))
}
