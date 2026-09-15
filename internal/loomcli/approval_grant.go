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
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/policy"
)

func newApprovalsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approvals",
		Short: "List LOOM approval requests",
	}
	filter := policy.ApprovalFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List approval requests",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListApprovals(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list approvals.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, approval := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), approval.ApprovalID)
				}
				return nil
			}
			renderApprovalList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of approvals to return")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by approval status")
	listCmd.Flags().StringVar(&filter.ActorRef, "actor", "", "filter by requesting actor ID or key")
	listCmd.Flags().StringVar(&filter.ApprovingActorRef, "approving-actor", "", "filter by approving actor ID or key")
	listCmd.Flags().StringVar(&filter.TargetNodeRef, "target-node", "", "filter by target node ID or key")
	listCmd.Flags().StringVar(&filter.CapabilityEndpointRef, "capability", "", "filter by capability endpoint ID or compact address")
	cmd.AddCommand(listCmd)
	return cmd
}

func newApprovalCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approval",
		Short: "Inspect and decide LOOM approvals",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <approval-ref>",
		Short: "Inspect an approval request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetApproval(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect approval.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
				return nil
			}
			renderApproval(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	decision := struct {
		approve bool
		deny    bool
		input   policy.ApprovalDecisionInput
		ttl     time.Duration
	}{}
	decideCmd := &cobra.Command{
		Use:   "decide <approval-ref>",
		Short: "Approve or deny an approval request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if decision.approve == decision.deny {
				return renderError(cmd, opts, correlationID, loomerrors.New("approval.decision_required", "policy", "decision", "Choose exactly one of --approve or --deny."))
			}
			if decision.approve {
				decision.input.Decision = policy.ApprovalDecisionApprove
			} else {
				decision.input.Decision = policy.ApprovalDecisionDeny
			}
			if decision.ttl > 0 {
				decision.input.GrantTTLSeconds = int(decision.ttl.Seconds())
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.DecideApproval(ctx, correlationID, args[0], decision.input)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not decide approval.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Approval.Status)
				return nil
			}
			renderApprovalDecisionResult(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	decideCmd.Flags().BoolVar(&decision.approve, "approve", false, "approve the request and issue a bounded grant")
	decideCmd.Flags().BoolVar(&decision.deny, "deny", false, "deny the request without issuing a grant")
	decideCmd.Flags().StringVar(&decision.input.DecidingActorRef, "actor", "", "actor ID or key making the decision")
	decideCmd.Flags().StringVar(&decision.input.DecisionReason, "reason", "", "decision reason")
	decideCmd.Flags().DurationVar(&decision.ttl, "grant-ttl", 0, "grant TTL for approved requests, for example 15m")
	cmd.AddCommand(decideCmd)

	return cmd
}

func newGrantsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "grants",
		Short: "List LOOM authorization grants",
	}
	filter := policy.GrantFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List authorization grants",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListGrants(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list grants.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, grant := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), grant.GrantID)
				}
				return nil
			}
			renderGrantList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of grants to return")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by grant status")
	listCmd.Flags().StringVar(&filter.GrantType, "type", "", "filter by grant type")
	listCmd.Flags().StringVar(&filter.GrantedToActorRef, "actor", "", "filter by granted-to actor ID or key")
	listCmd.Flags().StringVar(&filter.GrantedByActorRef, "granted-by", "", "filter by granting actor ID or key")
	listCmd.Flags().StringVar(&filter.ApprovalRef, "approval", "", "filter by approval ID or key")
	listCmd.Flags().StringVar(&filter.TargetNodeRef, "target-node", "", "filter by target node ID or key")
	listCmd.Flags().StringVar(&filter.CapabilityEndpointRef, "capability", "", "filter by capability endpoint ID or compact address")
	cmd.AddCommand(listCmd)
	return cmd
}

func newGrantCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "grant",
		Short: "Inspect and revoke LOOM authorization grants",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <grant-ref>",
		Short: "Inspect an authorization grant",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetGrant(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect grant.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
				return nil
			}
			renderGrant(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	input := policy.GrantRevokeInput{}
	revokeCmd := &cobra.Command{
		Use:   "revoke <grant-ref>",
		Short: "Revoke an active authorization grant",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.RevokeGrant(ctx, correlationID, args[0], input)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not revoke grant.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
				return nil
			}
			renderGrant(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	revokeCmd.Flags().StringVar(&input.RevokedByActorRef, "actor", "", "actor ID or key revoking the grant")
	revokeCmd.Flags().StringVar(&input.Reason, "reason", "", "revocation reason")
	cmd.AddCommand(revokeCmd)

	return cmd
}

func newSecurityCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "security",
		Short: "Inspect LOOM security audit records",
	}
	filter := events.ListFilter{Limit: 50}
	auditCmd := &cobra.Command{
		Use:   "audit",
		Short: "List policy, approval, and grant audit events",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListEvents(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list security audit events.", err))
			}
			if filter.EventType == "" {
				envelope.Data = filterSecurityAuditEvents(envelope.Data)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, event := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), event.EventID)
				}
				return nil
			}
			renderEventList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	auditCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of events to return")
	auditCmd.Flags().StringVar(&filter.EventType, "type", "", "filter by exact event type")
	auditCmd.Flags().StringVar(&filter.ActorRef, "actor", "", "filter by actor ID")
	auditCmd.Flags().StringVar(&filter.NodeRef, "node", "", "filter by origin node ID")
	auditCmd.Flags().StringVar(&filter.TargetKind, "target-kind", "", "filter by target kind")
	auditCmd.Flags().StringVar(&filter.TargetID, "target-id", "", "filter by target ID")
	cmd.AddCommand(auditCmd)
	return cmd
}

func filterSecurityAuditEvents(input []events.Event) []events.Event {
	out := make([]events.Event, 0, len(input))
	for _, event := range input {
		switch {
		case strings.HasPrefix(event.EventType, "policy."):
			out = append(out, event)
		case strings.HasPrefix(event.EventType, "approval."):
			out = append(out, event)
		case strings.HasPrefix(event.EventType, "grant."):
			out = append(out, event)
		}
	}
	return out
}

func renderApprovalList(cmd *cobra.Command, approvals []policy.Approval) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "APPROVAL ID\tSTATUS\tREQUESTED BY\tCAPABILITY\tAUTH\tEXPIRES")
	for _, approval := range approvals {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%d\t%s\n",
			approval.ApprovalID,
			approval.Status,
			approval.RequestedByActorID,
			ptrOrDash(approval.CapabilityEndpointID),
			approval.ExecutionAuthorizationLevel,
			approval.ExpiresAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderApproval(cmd *cobra.Command, approval policy.Approval) {
	fmt.Fprintf(cmd.OutOrStdout(), "Approval: %s\n", approval.ApprovalID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", approval.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Operation: %s\n", approval.Operation)
	fmt.Fprintf(cmd.OutOrStdout(), "Requested by actor: %s\n", approval.RequestedByActorID)
	fmt.Fprintf(cmd.OutOrStdout(), "Approving actor: %s\n", ptrOrDash(approval.ApprovingActorID))
	fmt.Fprintf(cmd.OutOrStdout(), "Origin node: %s\n", ptrOrDash(approval.OriginNodeID))
	fmt.Fprintf(cmd.OutOrStdout(), "Target node: %s\n", ptrOrDash(approval.TargetNodeID))
	fmt.Fprintf(cmd.OutOrStdout(), "Scope: %s\n", ptrOrDash(approval.ScopeID))
	fmt.Fprintf(cmd.OutOrStdout(), "Capability endpoint: %s\n", ptrOrDash(approval.CapabilityEndpointID))
	fmt.Fprintf(cmd.OutOrStdout(), "Provider: %s\n", ptrOrDash(approval.ProviderID))
	fmt.Fprintf(cmd.OutOrStdout(), "Risk: %s\n", approval.RiskLevel)
	fmt.Fprintf(cmd.OutOrStdout(), "Authorization required: %d\n", approval.ExecutionAuthorizationLevel)
	fmt.Fprintf(cmd.OutOrStdout(), "Request reason: %s\n", dashIfEmpty(approval.RequestReason))
	fmt.Fprintf(cmd.OutOrStdout(), "Action: %s\n", approval.ActionSummary)
	fmt.Fprintf(cmd.OutOrStdout(), "Decision reason: %s\n", ptrOrDash(approval.DecisionReason))
	fmt.Fprintf(cmd.OutOrStdout(), "Resulting grant: %s\n", ptrOrDash(approval.ResultingGrantID))
	fmt.Fprintf(cmd.OutOrStdout(), "Created: %s\n", approval.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Expires: %s\n", approval.ExpiresAt.UTC().Format(time.RFC3339))
}

func renderApprovalDecisionResult(cmd *cobra.Command, result policy.ApprovalDecisionResult) {
	renderApproval(cmd, result.Approval)
	if result.Grant != nil {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		renderGrant(cmd, *result.Grant)
	}
}

func renderGrantList(cmd *cobra.Command, grants []policy.Grant) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "GRANT ID\tSTATUS\tTYPE\tTO ACTOR\tMAX AUTH\tEXPIRES")
	for _, grant := range grants {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			grant.GrantID,
			grant.Status,
			grant.GrantType,
			grant.GrantedToActorID,
			intPtrOrDash(grant.MaxAuthorizationLevel),
			grant.ExpiresAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderGrant(cmd *cobra.Command, grant policy.Grant) {
	fmt.Fprintf(cmd.OutOrStdout(), "Grant: %s\n", grant.GrantID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", grant.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Type: %s\n", grant.GrantType)
	fmt.Fprintf(cmd.OutOrStdout(), "Granted by actor: %s\n", grant.GrantedByActorID)
	fmt.Fprintf(cmd.OutOrStdout(), "Granted to actor: %s\n", grant.GrantedToActorID)
	fmt.Fprintf(cmd.OutOrStdout(), "Approval: %s\n", ptrOrDash(grant.ApprovalID))
	fmt.Fprintf(cmd.OutOrStdout(), "Bypass confirmation: %t\n", grant.BypassConfirmation)
	fmt.Fprintf(cmd.OutOrStdout(), "Max risk: %s\n", ptrOrDash(grant.MaxRiskLevel))
	fmt.Fprintf(cmd.OutOrStdout(), "Max authorization: %s\n", intPtrOrDash(grant.MaxAuthorizationLevel))
	fmt.Fprintf(cmd.OutOrStdout(), "Uses: %d/%s\n", grant.UsesCount, intPtrOrDash(grant.MaxUses))
	fmt.Fprintf(cmd.OutOrStdout(), "Audit level: %s\n", grant.AuditLevel)
	fmt.Fprintf(cmd.OutOrStdout(), "Scope constraints: %s\n", string(grant.ScopeConstraints))
	fmt.Fprintf(cmd.OutOrStdout(), "Node constraints: %s\n", string(grant.NodeConstraints))
	fmt.Fprintf(cmd.OutOrStdout(), "Capability constraints: %s\n", string(grant.CapabilityConstraints))
	fmt.Fprintf(cmd.OutOrStdout(), "Created: %s\n", grant.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Expires: %s\n", grant.ExpiresAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Revoked by actor: %s\n", ptrOrDash(grant.RevokedByActorID))
	fmt.Fprintf(cmd.OutOrStdout(), "Revoked reason: %s\n", ptrOrDash(grant.RevokedReason))
}
