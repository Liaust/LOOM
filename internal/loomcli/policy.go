package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/policy"
)

func newPolicyCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Explain and inspect LOOM policy decisions",
	}

	explainInput := policy.DecisionInput{}
	explainCmd := &cobra.Command{
		Use:   "explain <operation>",
		Short: "Explain whether an operation is allowed, denied, or needs approval",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			explainInput.Operation = args[0]
			envelope, err := client.ExplainPolicy(ctx, correlationID, explainInput)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not explain policy decision.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Decision.Decision)
				return nil
			}
			renderPolicyExplanation(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	explainCmd.Flags().StringVar(&explainInput.ActorRef, "actor", "", "actor ID or key to evaluate")
	explainCmd.Flags().StringVar(&explainInput.OriginNodeRef, "origin-node", "", "origin node ID or key to evaluate")
	explainCmd.Flags().StringVar(&explainInput.ScopeRef, "scope", "", "scope ID, key, or slug to evaluate")
	explainCmd.Flags().BoolVar(&explainInput.CreateApprovalRequest, "request-approval", false, "create or reuse a pending approval request when approval is required")
	explainCmd.Flags().StringVar(&explainInput.ApprovalReason, "approval-reason", "", "reason to store on a created approval request")
	explainCmd.Flags().IntVar(&explainInput.ApprovalTTLSeconds, "approval-ttl-seconds", 0, "approval request TTL in seconds")
	cmd.AddCommand(explainCmd)

	decisionsCmd := &cobra.Command{
		Use:   "decisions",
		Short: "List policy decisions",
	}
	decisionFilter := policy.DecisionFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List persisted policy decisions",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListPolicyDecisions(ctx, correlationID, decisionFilter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list policy decisions.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, decision := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), decision.PolicyDecisionID)
				}
				return nil
			}
			renderPolicyDecisionList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&decisionFilter.Limit, "limit", 50, "maximum number of decisions to return")
	listCmd.Flags().StringVar(&decisionFilter.ActorRef, "actor", "", "filter by actor ID or key")
	listCmd.Flags().StringVar(&decisionFilter.OriginNodeRef, "origin-node", "", "filter by origin node ID or key")
	listCmd.Flags().StringVar(&decisionFilter.TargetNodeRef, "target-node", "", "filter by target node ID or key")
	listCmd.Flags().StringVar(&decisionFilter.Operation, "operation", "", "filter by exact operation string")
	listCmd.Flags().StringVar(&decisionFilter.Decision, "decision", "", "filter by allow, deny, or approval_required")
	listCmd.Flags().StringVar(&decisionFilter.CapabilityEndpointRef, "capability", "", "filter by capability endpoint ID or compact address")
	listCmd.Flags().StringVar(&decisionFilter.ApprovalRef, "approval", "", "filter by approval ID or key")
	listCmd.Flags().StringVar(&decisionFilter.GrantRef, "grant", "", "filter by grant ID or key")
	decisionsCmd.AddCommand(listCmd)
	cmd.AddCommand(decisionsCmd)

	decisionCmd := &cobra.Command{
		Use:   "decision",
		Short: "Inspect one policy decision",
	}
	decisionCmd.AddCommand(&cobra.Command{
		Use:   "inspect <decision-ref>",
		Short: "Inspect a policy decision by ID or key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetPolicyDecision(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect policy decision.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Decision)
				return nil
			}
			renderPolicyDecision(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	cmd.AddCommand(decisionCmd)

	return cmd
}

func renderPolicyExplanation(cmd *cobra.Command, explanation policy.PolicyExplanation) {
	renderPolicyDecision(cmd, explanation.Decision)
	if explanation.Target.CapabilityAddress != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Capability: %s\n", explanation.Target.CapabilityAddress)
	}
	if explanation.Target.ProviderAddress != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Provider: %s\n", explanation.Target.ProviderAddress)
	}
	if explanation.Target.TargetNodeKey != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Target node: %s (%s)\n", explanation.Target.TargetNodeKey, explanation.Target.TargetNodeID)
	}
	if explanation.Approval != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Approval: %s (%s, expires %s)\n", explanation.Approval.ApprovalID, explanation.Approval.Status, explanation.Approval.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if explanation.Grant != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Grant: %s (%s)\n", explanation.Grant.GrantID, explanation.Grant.Status)
	}
}

func renderPolicyDecisionList(cmd *cobra.Command, decisions []policy.Decision) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tDECISION\tREASON\tOPERATION\tACTOR\tTARGET\tAUTH\tCREATED")
	for _, decision := range decisions {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s/%s\t%s\n",
			decision.PolicyDecisionID,
			decision.Decision,
			decision.ReasonCode,
			decision.Operation,
			ptrOrDash(decision.ActorID),
			ptrOrDash(decision.TargetNodeID),
			intPtrOrDash(decision.ActorAuthorizationLevel),
			intPtrOrDash(decision.ExecutionAuthorizationLevel),
			decision.CreatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderPolicyDecision(cmd *cobra.Command, decision policy.Decision) {
	fmt.Fprintf(cmd.OutOrStdout(), "Decision: %s\n", decision.Decision)
	fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\n", decision.PolicyDecisionID)
	fmt.Fprintf(cmd.OutOrStdout(), "Reason: %s\n", decision.ReasonCode)
	if strings.TrimSpace(decision.SafeExplanation) != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Explanation: %s\n", decision.SafeExplanation)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Operation: %s\n", decision.Operation)
	fmt.Fprintf(cmd.OutOrStdout(), "Actor: %s\n", ptrOrDash(decision.ActorID))
	fmt.Fprintf(cmd.OutOrStdout(), "Origin node: %s\n", ptrOrDash(decision.OriginNodeID))
	fmt.Fprintf(cmd.OutOrStdout(), "Target node: %s\n", ptrOrDash(decision.TargetNodeID))
	fmt.Fprintf(cmd.OutOrStdout(), "Scope: %s\n", ptrOrDash(decision.ScopeID))
	fmt.Fprintf(cmd.OutOrStdout(), "Capability endpoint: %s\n", ptrOrDash(decision.CapabilityEndpointID))
	fmt.Fprintf(cmd.OutOrStdout(), "Provider: %s\n", ptrOrDash(decision.ProviderID))
	fmt.Fprintf(cmd.OutOrStdout(), "Risk: %s\n", ptrOrDash(decision.RiskLevel))
	fmt.Fprintf(cmd.OutOrStdout(), "Authorization: actor=%s required=%s\n", intPtrOrDash(decision.ActorAuthorizationLevel), intPtrOrDash(decision.ExecutionAuthorizationLevel))
	fmt.Fprintf(cmd.OutOrStdout(), "Approval: %s\n", ptrOrDash(decision.ApprovalID))
	fmt.Fprintf(cmd.OutOrStdout(), "Grant: %s\n", ptrOrDash(decision.GrantID))
	fmt.Fprintf(cmd.OutOrStdout(), "Context hash: %s\n", decision.ContextHash)
	fmt.Fprintf(cmd.OutOrStdout(), "Created: %s\n", decision.CreatedAt.UTC().Format(time.RFC3339))
}

func intPtrOrDash(value *int) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *value)
}
