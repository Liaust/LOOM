package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/artifacts"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/identity"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/scopes"
	"loom.local/loom/internal/scripts"
	"loom.local/loom/internal/search"
	"loom.local/loom/internal/workers"
)

func newActorCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "actor",
		Short: "Inspect LOOM actors",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <actor-ref>",
		Short: "Inspect an actor by ID or key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetActor(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect actor.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderActor(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func newNodeCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Inspect and enroll LOOM nodes",
	}
	listOpts := struct {
		limit int
	}{limit: 50}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List nodes",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListNodes(ctx, correlationID, listOpts.limit)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list nodes.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, node := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), node.NodeID)
				}
				return nil
			}
			renderNodeList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&listOpts.limit, "limit", 50, "maximum number of nodes to return")
	cmd.AddCommand(listCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <node-ref>",
		Short: "Inspect a node by ID or key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetNode(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect node.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderNode(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "health <node-ref>",
		Short: "Inspect node heartbeat and communication health",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetNodeHealth(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect node health.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderNodeHealth(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	credentialOpts := struct {
		revokeExisting bool
		reason         string
		idempotencyKey string
	}{}
	credentialCmd := &cobra.Command{
		Use:   "credential",
		Short: "Manage node credentials",
	}
	issueCredentialCmd := &cobra.Command{
		Use:   "issue <node-ref>",
		Short: "Issue a credential for an existing node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, credentialOpts.idempotencyKey, "node.credential.issue."+args[0])
			envelope, err := client.IssueNodeCredential(ctx, correlationID, args[0], nodes.IssueNodeCredentialInput{
				NodeRef:        args[0],
				RevokeExisting: credentialOpts.revokeExisting,
				Reason:         credentialOpts.reason,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not issue node credential.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.CredentialToken)
				return nil
			}
			renderNodeCredentialIssue(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	issueCredentialCmd.Flags().BoolVar(&credentialOpts.revokeExisting, "revoke-existing", false, "revoke existing active credentials for the node before issuing the new credential")
	issueCredentialCmd.Flags().StringVar(&credentialOpts.reason, "reason", "", "reason to store with credential audit events")
	issueCredentialCmd.Flags().StringVar(&credentialOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	credentialCmd.AddCommand(issueCredentialCmd)
	cmd.AddCommand(credentialCmd)

	tokenOpts := struct {
		ttlSeconds     int
		idempotencyKey string
	}{ttlSeconds: 1800}
	tokenCmd := &cobra.Command{
		Use:   "enrollment-token",
		Short: "Manage one-time node enrollment tokens",
	}
	createTokenCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a one-time node enrollment token",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, tokenOpts.idempotencyKey, "node.enrollment_token.create")
			envelope, err := client.CreateEnrollmentToken(ctx, correlationID, nodes.CreateEnrollmentTokenInput{TTLSeconds: tokenOpts.ttlSeconds})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not create enrollment token.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.TokenValue)
				return nil
			}
			renderEnrollmentTokenResult(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	createTokenCmd.Flags().IntVar(&tokenOpts.ttlSeconds, "ttl-seconds", 1800, "token lifetime in seconds")
	createTokenCmd.Flags().StringVar(&tokenOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	tokenCmd.AddCommand(createTokenCmd)
	cmd.AddCommand(tokenCmd)

	requestOpts := struct {
		limit           int
		status          string
		enrollmentToken string
		displayName     string
		nodeKind        string
		nodeRole        string
		runtimeClass    string
		idempotencyKey  string
		denialReason    string
	}{limit: 50, nodeKind: "workspace", nodeRole: "workspace", runtimeClass: "database_capable"}
	requestCmd := &cobra.Command{
		Use:   "enrollment-request",
		Short: "Create, inspect, approve, and deny node enrollment requests",
	}
	requestListCmd := &cobra.Command{
		Use:   "list",
		Short: "List node enrollment requests",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListEnrollmentRequests(ctx, correlationID, nodes.ListEnrollmentRequestsFilter{
				Limit:  requestOpts.limit,
				Status: requestOpts.status,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list enrollment requests.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderEnrollmentRequestList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	requestListCmd.Flags().IntVar(&requestOpts.limit, "limit", 50, "maximum number of enrollment requests to return")
	requestListCmd.Flags().StringVar(&requestOpts.status, "status", "", "filter by enrollment request status")
	requestCmd.AddCommand(requestListCmd)

	requestCreateCmd := &cobra.Command{
		Use:   "create <node-key>",
		Short: "Create a pending node enrollment request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, requestOpts.idempotencyKey, "node.enrollment_request.create."+args[0])
			displayName := requestOpts.displayName
			if strings.TrimSpace(displayName) == "" {
				displayName = args[0]
			}
			envelope, err := client.CreateEnrollmentRequest(ctx, correlationID, nodes.CreateEnrollmentRequestInput{
				EnrollmentToken:       requestOpts.enrollmentToken,
				RequestedNodeKey:      args[0],
				RequestedDisplayName:  displayName,
				RequestedNodeKind:     requestOpts.nodeKind,
				RequestedNodeRole:     requestOpts.nodeRole,
				RequestedRuntimeClass: requestOpts.runtimeClass,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not create enrollment request.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.NodeEnrollmentRequestID)
				return nil
			}
			renderEnrollmentRequest(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	requestCreateCmd.Flags().StringVar(&requestOpts.enrollmentToken, "token", "", "one-time enrollment token value")
	requestCreateCmd.Flags().StringVar(&requestOpts.displayName, "display-name", "", "display name for the requested node")
	requestCreateCmd.Flags().StringVar(&requestOpts.nodeKind, "kind", "workspace", "requested node kind")
	requestCreateCmd.Flags().StringVar(&requestOpts.nodeRole, "role", "workspace", "requested node role")
	requestCreateCmd.Flags().StringVar(&requestOpts.runtimeClass, "runtime-class", "database_capable", "requested runtime class")
	requestCreateCmd.Flags().StringVar(&requestOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	_ = requestCreateCmd.MarkFlagRequired("token")
	requestCmd.AddCommand(requestCreateCmd)

	requestCmd.AddCommand(&cobra.Command{
		Use:   "inspect <request-ref>",
		Short: "Inspect a node enrollment request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetEnrollmentRequest(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect enrollment request.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderEnrollmentRequest(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	approveCmd := &cobra.Command{
		Use:   "approve <request-ref>",
		Short: "Approve a pending node enrollment request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, requestOpts.idempotencyKey, "node.enrollment_request.approve."+args[0])
			envelope, err := client.ApproveEnrollment(ctx, correlationID, args[0], nodes.ApproveEnrollmentInput{EnrollmentRequestRef: args[0]})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not approve enrollment request.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.CredentialToken)
				return nil
			}
			renderEnrollmentApproval(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	approveCmd.Flags().StringVar(&requestOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	requestCmd.AddCommand(approveCmd)

	denyCmd := &cobra.Command{
		Use:   "deny <request-ref>",
		Short: "Deny a pending node enrollment request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, requestOpts.idempotencyKey, "node.enrollment_request.deny."+args[0])
			envelope, err := client.DenyEnrollment(ctx, correlationID, args[0], nodes.DenyEnrollmentInput{
				EnrollmentRequestRef: args[0],
				Reason:               requestOpts.denialReason,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not deny enrollment request.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderEnrollmentRequest(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	denyCmd.Flags().StringVar(&requestOpts.denialReason, "reason", "", "reason to store with the denial")
	denyCmd.Flags().StringVar(&requestOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	requestCmd.AddCommand(denyCmd)

	cmd.AddCommand(requestCmd)
	return cmd
}

func newScopeCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scope",
		Short: "Inspect and create LOOM scopes",
	}

	listOpts := struct {
		limit int
	}{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List scopes",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListScopes(ctx, correlationID, listOpts.limit)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list scopes.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderScopeList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&listOpts.limit, "limit", 50, "maximum number of scopes to return")
	cmd.AddCommand(listCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <scope-ref>",
		Short: "Inspect a scope by ID, key, or slug",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetScope(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect scope.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderScope(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	createOpts := struct {
		scopeType      string
		slug           string
		displayName    string
		ifNotExists    bool
		idempotencyKey string
	}{scopeType: "project"}
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a bare project scope",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, createOpts.idempotencyKey, "scope.create")

			envelope, err := client.CreateScope(ctx, correlationID, scopes.CreateInput{
				ScopeType:   createOpts.scopeType,
				Slug:        createOpts.slug,
				DisplayName: createOpts.displayName,
				IfNotExists: createOpts.ifNotExists,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not create scope.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			result := envelope.Data
			action := "existing"
			if result.Created {
				action = "created"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Scope %s: %s\n", action, result.Scope.ScopeKey)
			renderScope(cmd, result.Scope)
			if result.EventID != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Event: %s\n", result.EventID)
			}
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	createCmd.Flags().StringVar(&createOpts.scopeType, "type", "project", "scope type to create")
	createCmd.Flags().StringVar(&createOpts.slug, "slug", "", "scope slug")
	createCmd.Flags().StringVar(&createOpts.displayName, "name", "", "display name")
	createCmd.Flags().BoolVar(&createOpts.ifNotExists, "if-not-exists", false, "return existing scope instead of failing")
	createCmd.Flags().StringVar(&createOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	_ = createCmd.MarkFlagRequired("slug")
	cmd.AddCommand(createCmd)

	return cmd
}

func newEventsCommand(opts *options) *cobra.Command {
	listOpts := struct {
		events.ListFilter
		ObjectRef  string
		ProjectRef string
	}{}
	cmd := &cobra.Command{
		Use:   "events",
		Short: "List LOOM events",
	}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List durable events",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			filter := listOpts.ListFilter
			if listOpts.ObjectRef != "" {
				filter.TargetKind = "object"
				filter.TargetID = listOpts.ObjectRef
			}
			if listOpts.ProjectRef != "" {
				project, err := client.GetProject(ctx, correlationID, listOpts.ProjectRef)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not resolve project for event filter.", err))
				}
				filter.ScopeRef = project.Data.Project.ProjectScopeID
			}

			envelope, err := client.ListEvents(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list events.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderEventList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&listOpts.Limit, "limit", 50, "maximum number of events to return")
	listCmd.Flags().StringVar(&listOpts.EventType, "type", "", "filter by event type")
	listCmd.Flags().StringVar(&listOpts.ActorRef, "actor", "", "filter by actor ID or key")
	listCmd.Flags().StringVar(&listOpts.NodeRef, "node", "", "filter by node ID or key")
	listCmd.Flags().StringVar(&listOpts.ScopeRef, "scope", "", "filter by scope ID, key, or slug")
	listCmd.Flags().StringVar(&listOpts.CorrelationID, "correlation", "", "filter by correlation ID")
	listCmd.Flags().StringVar(&listOpts.JobRef, "job", "", "filter by job ID")
	listCmd.Flags().StringVar(&listOpts.TargetKind, "target-kind", "", "filter by target kind")
	listCmd.Flags().StringVar(&listOpts.TargetID, "target-id", "", "filter by target ID")
	listCmd.Flags().StringVar(&listOpts.ObjectRef, "object", "", "filter by object ID")
	listCmd.Flags().StringVar(&listOpts.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	cmd.AddCommand(listCmd)
	return cmd
}

func newProjectCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "project",
		Aliases: []string{"projects"},
		Short:   "Create and inspect LOOM projects",
	}

	createOpts := struct {
		slug           string
		description    string
		homeNodeRef    string
		ifNotExists    bool
		idempotencyKey string
	}{}
	createCmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a project and its backing project scope",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, createOpts.idempotencyKey, "project.create")

			envelope, err := client.CreateProject(ctx, correlationID, projects.CreateInput{
				Name:        args[0],
				Slug:        createOpts.slug,
				Description: createOpts.description,
				HomeNodeRef: createOpts.homeNodeRef,
				IfNotExists: createOpts.ifNotExists,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not create project.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			action := "existing"
			if envelope.Data.Created {
				action = "created"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Project %s: %s\n", action, envelope.Data.Project.Project.Slug)
			renderProjectDetail(cmd, envelope.Data.Project)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	createCmd.Flags().StringVar(&createOpts.slug, "slug", "", "project slug")
	createCmd.Flags().StringVar(&createOpts.description, "description", "", "project description")
	createCmd.Flags().StringVar(&createOpts.homeNodeRef, "home-node", "", "home node ID or key")
	createCmd.Flags().BoolVar(&createOpts.ifNotExists, "if-not-exists", false, "return existing project instead of failing")
	createCmd.Flags().StringVar(&createOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	cmd.AddCommand(createCmd)

	listOpts := struct {
		limit int
	}{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListProjects(ctx, correlationID, listOpts.limit)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list projects.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProjectList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&listOpts.limit, "limit", 50, "maximum number of projects to return")
	cmd.AddCommand(listCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <project-ref>",
		Short: "Inspect a project by ID, slug, or project scope key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetProject(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect project.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProjectDetail(cmd, envelope.Data)
			if analysis, analysisErr := client.AnalyzeProjectContractBackend(ctx, correlationID, backendAnalysisInputForRef(args[0])); analysisErr == nil && analysis.Data.Analysis.Loaded != nil {
				renderProjectAnalysisLayout(cmd, analysis.Data.Analysis, args[0], true)
			} else if registration, registrationErr := client.GetProjectRegistrationStatus(ctx, correlationID, args[0]); registrationErr == nil && registration.Data.Registration != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Contract: %s\n", registration.Data.Registration.ContractPath)
				if layout := projectLayoutFromResolvedPath(registration.Data.Registration.ProjectRoot, registration.Data.Registration.ContractPath); layout != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "Layout: %s\n", layout)
				}
			}
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	addProjectContractCommands(cmd, opts)
	addProjectRepositoryCommands(cmd, opts)

	return cmd
}

func newObjectCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "object",
		Aliases: []string{"objects"},
		Short:   "Ingest and inspect LOOM objects",
	}

	ingestOpts := struct {
		projectRef       string
		scopeRef         string
		name             string
		relationshipType string
		idempotencyKey   string
	}{relationshipType: "primary"}
	ingestCmd := &cobra.Command{
		Use:   "ingest <path>",
		Short: "Ingest a local file into the LOOM object store",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, ingestOpts.idempotencyKey, "object.ingest")

			envelope, err := client.IngestObject(ctx, correlationID, objects.IngestFileInput{
				Path:             args[0],
				ProjectRef:       ingestOpts.projectRef,
				ScopeRef:         ingestOpts.scopeRef,
				Name:             ingestOpts.name,
				RelationshipType: ingestOpts.relationshipType,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not ingest object.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderObjectIngest(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	ingestCmd.Flags().StringVar(&ingestOpts.projectRef, "project", "", "project ID, slug, or scope key to link")
	ingestCmd.Flags().StringVar(&ingestOpts.scopeRef, "scope", "", "scope ID, key, or slug to link")
	ingestCmd.Flags().StringVar(&ingestOpts.name, "name", "", "logical object name")
	ingestCmd.Flags().StringVar(&ingestOpts.relationshipType, "relationship", "primary", "object-scope relationship type")
	ingestCmd.Flags().StringVar(&ingestOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	cmd.AddCommand(ingestCmd)

	listOpts := objects.ListFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List objects",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListObjects(ctx, correlationID, listOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list objects.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderObjectList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&listOpts.Limit, "limit", 50, "maximum number of objects to return")
	listCmd.Flags().StringVar(&listOpts.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	listCmd.Flags().StringVar(&listOpts.ScopeRef, "scope", "", "filter by scope ID, key, or slug")
	listCmd.Flags().StringVar(&listOpts.ObjectType, "type", "", "filter by object type")
	cmd.AddCommand(listCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <object-ref>",
		Short: "Inspect an object by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetObject(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect object.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderObjectDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "versions <object-ref>",
		Short: "List object versions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListObjectVersions(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list object versions.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderObjectVersions(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	return cmd
}

func newSearchCommand(opts *options) *cobra.Command {
	searchOpts := search.SearchInput{}
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search indexed LOOM text",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			input := searchOpts
			input.Query = args[0]
			envelope, err := client.Search(ctx, correlationID, input)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not run search.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderSearchResultSet(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&searchOpts.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	cmd.Flags().StringVar(&searchOpts.ScopeRef, "scope", "", "filter by scope ID, key, or slug")
	cmd.Flags().StringVar(&searchOpts.ObjectType, "type", "", "filter by object type")
	cmd.Flags().IntVar(&searchOpts.Limit, "limit", 10, "maximum number of results to return")
	return cmd
}

func newIndexCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Inspect and rebuild LOOM indexes",
	}

	statusOpts := search.StatusFilter{}
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "List text extraction and search index status",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListIndexStatus(ctx, correlationID, statusOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list index status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderIndexStatusList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	statusCmd.Flags().IntVar(&statusOpts.Limit, "limit", 50, "maximum number of status records to return")
	statusCmd.Flags().StringVar(&statusOpts.ObjectRef, "object", "", "filter by object ID, slug, or name")
	statusCmd.Flags().StringVar(&statusOpts.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	statusCmd.Flags().StringVar(&statusOpts.ScopeRef, "scope", "", "filter by scope ID, key, or slug")
	statusCmd.Flags().StringVar(&statusOpts.IndexType, "index-type", "", "filter by index type")
	statusCmd.Flags().StringVar(&statusOpts.Status, "status", "", "filter by status")
	statusCmd.Flags().BoolVar(&statusOpts.FailedOnly, "failed", false, "show only failed status records")
	cmd.AddCommand(statusCmd)

	rebuildOpts := struct {
		search.RebuildInput
		idempotencyKey string
	}{}
	rebuildCmd := &cobra.Command{
		Use:   "rebuild",
		Short: "Rebuild an object's text extraction and search index",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, rebuildOpts.idempotencyKey, "index.rebuild")

			envelope, err := client.RebuildIndex(ctx, correlationID, rebuildOpts.RebuildInput)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not rebuild index.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderIndexResult(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	rebuildCmd.Flags().StringVar(&rebuildOpts.ObjectRef, "object", "", "object ID, slug, or name to rebuild")
	rebuildCmd.Flags().BoolVar(&rebuildOpts.Force, "force", false, "force rebuild even if the index looks current")
	rebuildCmd.Flags().StringVar(&rebuildOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	_ = rebuildCmd.MarkFlagRequired("object")
	cmd.AddCommand(rebuildCmd)

	return cmd
}

func newEventCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "event",
		Short: "Inspect LOOM events",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <event-id>",
		Short: "Inspect an event by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetEvent(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect event.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderEvent(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func newScriptCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "script",
		Short: "Register, inspect, and run LOOM scripts",
	}

	registerOpts := struct {
		projectRef     string
		scopeRef       string
		activate       bool
		idempotencyKey string
	}{}
	registerCmd := &cobra.Command{
		Use:   "register <manifest-path>",
		Short: "Register a script package manifest",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, registerOpts.idempotencyKey, "script.register")

			envelope, err := client.RegisterScript(ctx, correlationID, scripts.RegisterInput{
				ManifestPath: args[0],
				ProjectRef:   registerOpts.projectRef,
				ScopeRef:     registerOpts.scopeRef,
				Activate:     registerOpts.activate,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not register script.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderScriptRegister(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	registerCmd.Flags().StringVar(&registerOpts.projectRef, "project", "", "project ID, slug, or scope key that owns the script")
	registerCmd.Flags().StringVar(&registerOpts.scopeRef, "scope", "", "scope ID, key, or slug that owns the script")
	registerCmd.Flags().BoolVar(&registerOpts.activate, "activate", false, "activate the registered version immediately")
	registerCmd.Flags().StringVar(&registerOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	cmd.AddCommand(registerCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <script-ref>",
		Short: "Inspect a script by ID or slug",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetScript(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect script.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderScriptDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	runOpts := struct {
		objectRef        string
		projectRef       string
		scopeRef         string
		idempotencyKey   string
		wait             bool
		noWait           bool
		waitUntilStarted bool
		timeout          string
	}{}
	runCmd := &cobra.Command{
		Use:   "run <script-ref>",
		Short: "Run a registered script capability",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			mode, waitTimeoutSeconds, err := scriptRunModeAndTimeout(runOpts.wait, runOpts.noWait, runOpts.waitUntilStarted, runOpts.timeout)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("request.invalid", "scripts", "execution_mode", "Script run mode is invalid.", err))
			}
			ctxTimeout := 30 * time.Second
			if mode != jobs.ScriptRunEnqueueOnly {
				ctxTimeout = time.Duration(waitTimeoutSeconds+10) * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), ctxTimeout)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, runOpts.idempotencyKey, "script.run")

			envelope, err := client.RunScript(ctx, correlationID, args[0], jobs.CreateScriptRunInput{
				ObjectRef:       runOpts.objectRef,
				ProjectRef:      runOpts.projectRef,
				ScopeRef:        runOpts.scopeRef,
				ExecutionMode:   mode,
				WaitTimeoutSecs: waitTimeoutSeconds,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not run script.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderScriptRun(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	runCmd.Flags().StringVar(&runOpts.objectRef, "object", "", "object ID to pass as script input")
	runCmd.Flags().StringVar(&runOpts.projectRef, "project", "", "project ID, slug, or scope key for run scope")
	runCmd.Flags().StringVar(&runOpts.scopeRef, "scope", "", "scope ID, key, or slug for run scope")
	runCmd.Flags().BoolVar(&runOpts.wait, "wait", false, "wait for job completion")
	runCmd.Flags().BoolVar(&runOpts.noWait, "no-wait", false, "return after the job is durably queued")
	runCmd.Flags().BoolVar(&runOpts.waitUntilStarted, "wait-until-started", false, "wait until a runner starts the job")
	runCmd.Flags().StringVar(&runOpts.timeout, "timeout", "120s", "maximum time to wait for --wait or --wait-until-started")
	runCmd.Flags().StringVar(&runOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	cmd.AddCommand(runCmd)

	return cmd
}

func scriptRunModeAndTimeout(wait, noWait, waitUntilStarted bool, timeoutValue string) (string, int, error) {
	selected := 0
	for _, value := range []bool{wait, noWait, waitUntilStarted} {
		if value {
			selected++
		}
	}
	if selected > 1 {
		return "", 0, fmt.Errorf("provide only one of --wait, --no-wait, or --wait-until-started")
	}
	mode := jobs.ScriptRunWaitForCompletion
	if noWait {
		mode = jobs.ScriptRunEnqueueOnly
	}
	if waitUntilStarted {
		mode = jobs.ScriptRunWaitUntilStarted
	}
	if wait {
		mode = jobs.ScriptRunWaitForCompletion
	}
	if mode == jobs.ScriptRunEnqueueOnly {
		return mode, 0, nil
	}
	timeoutValue = strings.TrimSpace(timeoutValue)
	if timeoutValue == "" {
		timeoutValue = "120s"
	}
	duration, err := time.ParseDuration(timeoutValue)
	if err != nil {
		return "", 0, fmt.Errorf("timeout must be a Go duration like 120s or 2m: %w", err)
	}
	if duration <= 0 {
		return "", 0, fmt.Errorf("timeout must be positive")
	}
	seconds := int(duration.Round(time.Second) / time.Second)
	if seconds <= 0 {
		seconds = 1
	}
	return mode, seconds, nil
}

func newScriptsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scripts",
		Short: "List LOOM scripts",
	}
	listOpts := scripts.ListFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List registered scripts",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListScripts(ctx, correlationID, listOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list scripts.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderScriptList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&listOpts.Limit, "limit", 50, "maximum number of scripts to return")
	listCmd.Flags().StringVar(&listOpts.Status, "status", "", "filter by script status")
	listCmd.Flags().StringVar(&listOpts.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	listCmd.Flags().StringVar(&listOpts.ScopeRef, "scope", "", "filter by scope ID, key, or slug")
	cmd.AddCommand(listCmd)
	return cmd
}

func addJobListFlags(cmd *cobra.Command, filter *jobs.ListFilter) {
	cmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of jobs to return")
	cmd.Flags().StringVar(&filter.JobType, "type", "", "filter by job type")
	cmd.Flags().StringVar(&filter.ScriptRef, "script", "", "filter by script ID or slug")
	cmd.Flags().StringVar(&filter.WorkflowRef, "workflow", "", "filter by workflow ID or slug")
	cmd.Flags().StringVar(&filter.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	cmd.Flags().StringVar(&filter.ScopeRef, "scope", "", "filter by scope ID, key, or slug")
}

func addJobFailureAttentionFlags(cmd *cobra.Command, filter *jobs.ListFilter) {
	cmd.Flags().StringVar(&filter.AttentionStatus, "attention-status", "", "failure attention status to list: active, acknowledged, archived, or all")
	cmd.Flags().BoolVar(&filter.IncludeAcknowledged, "include-acknowledged", false, "include acknowledged failed-job attention")
	cmd.Flags().BoolVar(&filter.IncludeArchived, "include-archived", false, "include archived failed-job attention")
}

func newJobCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "job",
		Short: "Inspect LOOM jobs",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <job-id>",
		Short: "Inspect a job and its attempts, outputs, logs, and artifacts",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetJob(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect job.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderJobDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	logOpts := struct {
		stream string
		tail   int
	}{stream: "stdout"}
	logCmd := &cobra.Command{
		Use:   "logs <job-id>",
		Short: "Show captured job logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetJobLogs(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not read job logs.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderJobLogs(cmd, envelope.Data, logOpts.stream, logOpts.tail)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	logCmd.Flags().StringVar(&logOpts.stream, "stream", "stdout", "log stream to print: stdout, stderr, runner, or all")
	logCmd.Flags().IntVar(&logOpts.tail, "tail", 0, "only print the last N lines from each selected stream")
	cmd.AddCommand(logCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "outputs <job-id>",
		Short: "List structured outputs recorded for a job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetJobOutputs(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not read job outputs.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderJobOutputs(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	cancelOpts := struct {
		idempotencyKey string
	}{}
	cancelCmd := &cobra.Command{
		Use:   "cancel <job-id>",
		Short: "Cancel a queued job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, cancelOpts.idempotencyKey, "job.cancel."+args[0])
			envelope, err := client.CancelJob(ctx, correlationID, jobs.CancelJobInput{JobRef: args[0]})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not cancel job.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderJobAction(cmd, "Cancelled", envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cancelCmd.Flags().StringVar(&cancelOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	cmd.AddCommand(cancelCmd)

	retryOpts := struct {
		force          bool
		idempotencyKey string
	}{}
	retryCmd := &cobra.Command{
		Use:   "retry <job-id>",
		Short: "Requeue a failed or timed-out job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, retryOpts.idempotencyKey, "job.retry."+args[0])
			envelope, err := client.RetryJob(ctx, correlationID, jobs.RetryJobInput{JobRef: args[0], Force: retryOpts.force})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not retry job.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderJobAction(cmd, "Requeued", envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	retryCmd.Flags().BoolVar(&retryOpts.force, "force", false, "requeue even when the job has exhausted attempts")
	retryCmd.Flags().StringVar(&retryOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	cmd.AddCommand(retryCmd)

	ackOpts := struct {
		note           string
		idempotencyKey string
	}{}
	ackCmd := &cobra.Command{
		Use:   "acknowledge <job-id>",
		Short: "Acknowledge failed-job attention without changing job status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, ackOpts.idempotencyKey, "job.attention.acknowledge."+args[0])
			envelope, err := client.AcknowledgeJobAttention(ctx, correlationID, jobs.JobAttentionInput{JobRef: args[0], Note: ackOpts.note})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not acknowledge job attention.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderJobAction(cmd, "Acknowledged attention for", envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	ackCmd.Flags().StringVar(&ackOpts.note, "note", "", "operator note to store with the attention acknowledgement")
	ackCmd.Flags().StringVar(&ackOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	cmd.AddCommand(ackCmd)

	archiveOpts := struct {
		note           string
		idempotencyKey string
	}{}
	archiveCmd := &cobra.Command{
		Use:   "archive <job-id>",
		Short: "Archive failed-job attention without deleting the job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, archiveOpts.idempotencyKey, "job.attention.archive."+args[0])
			envelope, err := client.ArchiveJobAttention(ctx, correlationID, jobs.JobAttentionInput{JobRef: args[0], Note: archiveOpts.note})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not archive job attention.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderJobAction(cmd, "Archived attention for", envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	archiveCmd.Flags().StringVar(&archiveOpts.note, "note", "", "operator note to store with the attention archive")
	archiveCmd.Flags().StringVar(&archiveOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	cmd.AddCommand(archiveCmd)

	eventOpts := events.ListFilter{Limit: 50}
	eventCmd := &cobra.Command{
		Use:   "events <job-id>",
		Short: "List durable events associated with a job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			filter := eventOpts
			filter.JobRef = args[0]
			envelope, err := client.ListEvents(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list job events.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderEventList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	eventCmd.Flags().IntVar(&eventOpts.Limit, "limit", 50, "maximum number of job events to return")
	cmd.AddCommand(eventCmd)

	return cmd
}

func newJobsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "List LOOM jobs",
	}
	listOpts := jobs.ListFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List jobs",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListJobs(ctx, correlationID, listOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list jobs.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderJobList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&listOpts.Limit, "limit", 50, "maximum number of jobs to return")
	listCmd.Flags().StringVar(&listOpts.Status, "status", "", "filter by job status")
	listCmd.Flags().StringVar(&listOpts.JobType, "type", "", "filter by job type")
	listCmd.Flags().StringVar(&listOpts.ScriptRef, "script", "", "filter by script ID or slug")
	listCmd.Flags().StringVar(&listOpts.WorkflowRef, "workflow", "", "filter by workflow ID or slug")
	listCmd.Flags().StringVar(&listOpts.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	listCmd.Flags().StringVar(&listOpts.ScopeRef, "scope", "", "filter by scope ID, key, or slug")
	cmd.AddCommand(listCmd)

	queueOpts := jobs.ListFilter{}
	queueCmd := &cobra.Command{
		Use:   "queue",
		Short: "List queued jobs",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListQueuedJobs(ctx, correlationID, queueOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list queued jobs.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderJobList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	addJobListFlags(queueCmd, &queueOpts)
	cmd.AddCommand(queueCmd)

	failureOpts := jobs.ListFilter{}
	failuresCmd := &cobra.Command{
		Use:   "failures",
		Short: "List failed or timed-out jobs",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListFailedJobs(ctx, correlationID, failureOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list failed jobs.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderJobList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	addJobListFlags(failuresCmd, &failureOpts)
	addJobFailureAttentionFlags(failuresCmd, &failureOpts)
	cmd.AddCommand(failuresCmd)

	runOpts := struct {
		once           bool
		reason         string
		idempotencyKey string
		timeoutSeconds int
	}{timeoutSeconds: 3600}
	runCmd := &cobra.Command{
		Use:   "run next",
		Short: "Run the job runner once to claim and execute the next queued job",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 || args[0] != "next" {
				return fmt.Errorf("expected exactly: loom jobs run next --once")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !runOpts.once {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("jobs.run_mode_required", "jobs", "main.job_runner", "Pass --once to run the job runner once."))
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			client, _ := withEffectIdempotency(commandCtx.Client, runOpts.idempotencyKey, "jobs.run.next")
			timeout := runOpts.timeoutSeconds
			if timeout <= 0 {
				timeout = 3600
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
			defer cancel()

			envelope, err := client.RunNextJob(ctx, commandCtx.CorrelationID, workers.RunOnceInput{
				Reason: runOpts.reason,
			})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not run next queued job.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Run.WorkerRunID)
				return nil
			}
			renderWorkerRunOnce(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	runCmd.Flags().BoolVar(&runOpts.once, "once", false, "run the job runner once")
	runCmd.Flags().StringVar(&runOpts.reason, "reason", "", "reason recorded on the worker run")
	runCmd.Flags().StringVar(&runOpts.idempotencyKey, "idempotency-key", "", "explicit idempotency key for the run request")
	runCmd.Flags().IntVar(&runOpts.timeoutSeconds, "timeout", 3600, "maximum seconds to wait for this worker run")
	cmd.AddCommand(runCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Summarize job queue and runner state",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.JobStatus(ctx, correlationID)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not summarize jobs.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderJobQueueSummary(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func newRunnerCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runner",
		Short: "Inspect LOOM job runners",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <runner-ref>",
		Short: "Inspect a runner by ID or key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetRunner(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect runner.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderRunnerDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func newRunnersCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runners",
		Short: "List LOOM job runners",
	}
	listOpts := jobs.RunnerFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List runners",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListRunners(ctx, correlationID, listOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list runners.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderRunnerList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&listOpts.Limit, "limit", 50, "maximum number of runners to return")
	listCmd.Flags().StringVar(&listOpts.Status, "status", "", "filter by runner status")
	listCmd.Flags().StringVar(&listOpts.NodeID, "node", "", "filter by node ID")
	cmd.AddCommand(listCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Summarize runner and job queue state",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.RunnerStatus(ctx, correlationID)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not summarize runners.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderJobQueueSummary(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func newArtifactCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifact",
		Short: "Inspect LOOM artifacts",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <artifact-id>",
		Short: "Inspect an artifact by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetArtifact(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect artifact.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderArtifactDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func newArtifactsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifacts",
		Short: "List LOOM artifacts",
	}
	listOpts := artifacts.ListFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List artifacts",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListArtifacts(ctx, correlationID, listOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list artifacts.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderArtifactList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&listOpts.Limit, "limit", 50, "maximum number of artifacts to return")
	listCmd.Flags().StringVar(&listOpts.JobRef, "job", "", "filter by job ID")
	listCmd.Flags().StringVar(&listOpts.ScopeRef, "scope", "", "filter by scope ID, key, or slug")
	listCmd.Flags().StringVar(&listOpts.ObjectRef, "object", "", "filter by artifact object ID")
	cmd.AddCommand(listCmd)
	return cmd
}

func commandClient(opts *options) (config.Config, localclient.Client, error) {
	cfg, err := resolveCLIConfig(opts)
	if err != nil {
		return config.Config{}, localclient.Client{}, err
	}
	client, err := resolveCLIClient(cfg, opts)
	if err != nil {
		return config.Config{}, localclient.Client{}, err
	}
	return cfg, client, nil
}

func renderResponseMeta(cmd *cobra.Command, opts *options, meta response.Meta) {
	if opts != nil && opts.verboseOutput {
		if meta.Source != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Source: %s\n", meta.Source)
		}
		if meta.Freshness != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Freshness: %s\n", meta.Freshness)
		}
		if meta.IdempotencyKey != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Idempotency key: %s\n", meta.IdempotencyKey)
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Correlation: %s\n", meta.CorrelationID)
}

func renderActor(cmd *cobra.Command, actor identity.Actor) {
	fmt.Fprintf(cmd.OutOrStdout(), "Actor: %s\n", actor.ActorKey)
	fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\n", actor.ActorID)
	fmt.Fprintf(cmd.OutOrStdout(), "Kind: %s\n", actor.ActorKind)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", actor.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Home node: %s\n", ptrOrDash(actor.HomeNodeID))
	fmt.Fprintf(cmd.OutOrStdout(), "Default scope: %s\n", ptrOrDash(actor.DefaultScopeID))
}

func renderNode(cmd *cobra.Command, node nodes.Node) {
	fmt.Fprintf(cmd.OutOrStdout(), "Node: %s\n", node.NodeKey)
	fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\n", node.NodeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Role: %s\n", node.NodeRole)
	fmt.Fprintf(cmd.OutOrStdout(), "Kind: %s\n", node.NodeKind)
	fmt.Fprintf(cmd.OutOrStdout(), "Runtime: %s\n", node.RuntimeClass)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", node.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Presence: %s\n", node.PresenceState)
	fmt.Fprintf(cmd.OutOrStdout(), "Enrollment: %s\n", node.EnrollmentStatus)
	fmt.Fprintf(cmd.OutOrStdout(), "Credentials: %s\n", node.CredentialStatus)
	fmt.Fprintf(cmd.OutOrStdout(), "Last heartbeat: %s\n", timePtrOrDash(node.LastHeartbeatAt))
	fmt.Fprintf(cmd.OutOrStdout(), "Owner actor: %s\n", ptrOrDash(node.OwnerActorID))
	fmt.Fprintf(cmd.OutOrStdout(), "Home scope: %s\n", ptrOrDash(node.HomeScopeID))
}

func renderNodeList(cmd *cobra.Command, nodeList []nodes.Node) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "KEY\tROLE\tKIND\tRUNTIME\tSTATUS\tPRESENCE\tLAST HEARTBEAT")
	for _, node := range nodeList {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			node.NodeKey,
			node.NodeRole,
			node.NodeKind,
			node.RuntimeClass,
			node.Status,
			node.PresenceState,
			timePtrOrDash(node.LastHeartbeatAt),
		)
	}
	_ = writer.Flush()
}

func renderNodeHealth(cmd *cobra.Command, health nodes.NodeHealth) {
	fmt.Fprintf(cmd.OutOrStdout(), "Node: %s (%s)\n", health.Node.NodeKey, health.Node.NodeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", health.Node.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Presence: %s\n", health.Node.PresenceState)
	fmt.Fprintf(cmd.OutOrStdout(), "Last heartbeat: %s\n", timePtrOrDash(health.Node.LastHeartbeatAt))
	if health.HeartbeatAgeSeconds != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Heartbeat age: %ds\n", *health.HeartbeatAgeSeconds)
	}
	if health.LastHeartbeat != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Reported status: %s\n", health.LastHeartbeat.ReportedStatus)
		fmt.Fprintf(cmd.OutOrStdout(), "Inbox backlog: %d\n", health.LastHeartbeat.InboxBacklog)
		fmt.Fprintf(cmd.OutOrStdout(), "Outbox backlog: %d\n", health.LastHeartbeat.OutboxBacklog)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Messages: pending=%d failed=%d dead_letter=%d\n", health.PendingMessages, health.FailedMessages, health.DeadLetterMessages)
}

func renderEnrollmentTokenResult(cmd *cobra.Command, result nodes.CreateEnrollmentTokenResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Enrollment token: %s\n", result.Token.NodeEnrollmentTokenID)
	fmt.Fprintf(cmd.OutOrStdout(), "Token value: %s\n", result.TokenValue)
	fmt.Fprintf(cmd.OutOrStdout(), "Hint: %s\n", result.Token.TokenHint)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", result.Token.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Expires: %s\n", result.Token.ExpiresAt.UTC().Format(time.RFC3339))
}

func renderEnrollmentRequestList(cmd *cobra.Command, requests []nodes.EnrollmentRequest) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "REQUEST ID\tNODE KEY\tSTATUS\tKIND\tRUNTIME\tCREATED\tEXPIRES")
	for _, request := range requests {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			request.NodeEnrollmentRequestID,
			request.RequestedNodeKey,
			request.Status,
			request.RequestedNodeKind,
			request.RequestedRuntimeClass,
			request.CreatedAt.UTC().Format(time.RFC3339),
			request.ExpiresAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderEnrollmentRequest(cmd *cobra.Command, request nodes.EnrollmentRequest) {
	fmt.Fprintf(cmd.OutOrStdout(), "Enrollment request: %s\n", request.NodeEnrollmentRequestID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", request.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Requested node: %s\n", request.RequestedNodeKey)
	fmt.Fprintf(cmd.OutOrStdout(), "Display name: %s\n", request.RequestedDisplayName)
	fmt.Fprintf(cmd.OutOrStdout(), "Kind: %s\n", request.RequestedNodeKind)
	fmt.Fprintf(cmd.OutOrStdout(), "Role: %s\n", request.RequestedNodeRole)
	fmt.Fprintf(cmd.OutOrStdout(), "Runtime: %s\n", request.RequestedRuntimeClass)
	fmt.Fprintf(cmd.OutOrStdout(), "Activated node: %s\n", ptrOrDash(request.ActivatedNodeID))
	fmt.Fprintf(cmd.OutOrStdout(), "Credential: %s\n", ptrOrDash(request.NodeCredentialID))
	fmt.Fprintf(cmd.OutOrStdout(), "Expires: %s\n", request.ExpiresAt.UTC().Format(time.RFC3339))
}

func renderEnrollmentApproval(cmd *cobra.Command, result nodes.ApproveEnrollmentResult) {
	renderEnrollmentRequest(cmd, result.Request)
	fmt.Fprintln(cmd.OutOrStdout(), "")
	renderNode(cmd, result.Node)
	fmt.Fprintf(cmd.OutOrStdout(), "Credential: %s\n", result.Credential.NodeCredentialID)
	fmt.Fprintf(cmd.OutOrStdout(), "Credential hint: %s\n", result.Credential.CredentialHint)
	fmt.Fprintf(cmd.OutOrStdout(), "Credential token: %s\n", result.CredentialToken)
}

func renderNodeCredentialIssue(cmd *cobra.Command, result nodes.IssueNodeCredentialResult) {
	renderNode(cmd, result.Node)
	fmt.Fprintf(cmd.OutOrStdout(), "Credential: %s\n", result.Credential.NodeCredentialID)
	fmt.Fprintf(cmd.OutOrStdout(), "Credential hint: %s\n", result.Credential.CredentialHint)
	if result.RevokedCredentials > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Revoked credentials: %d\n", result.RevokedCredentials)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Credential token: %s\n", result.CredentialToken)
}

func renderScope(cmd *cobra.Command, scope scopes.Scope) {
	fmt.Fprintf(cmd.OutOrStdout(), "Scope: %s\n", scope.ScopeKey)
	fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\n", scope.ScopeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Type: %s\n", scope.ScopeType)
	fmt.Fprintf(cmd.OutOrStdout(), "Slug: %s\n", scope.Slug)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", scope.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Owner actor: %s\n", ptrOrDash(scope.OwnerActorID))
	fmt.Fprintf(cmd.OutOrStdout(), "Home node: %s\n", ptrOrDash(scope.HomeNodeID))
}

func renderScopeList(cmd *cobra.Command, scopes []scopes.Scope) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "KEY\tTYPE\tSLUG\tSTATUS")
	for _, scope := range scopes {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", scope.ScopeKey, scope.ScopeType, scope.Slug, scope.Status)
	}
	_ = writer.Flush()
}

func renderProjectList(cmd *cobra.Command, projectList []projects.Project) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "SLUG\tSTATUS\tPROJECT ID\tSCOPE\tUPDATED")
	for _, project := range projectList {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\n",
			project.Slug,
			project.Status,
			project.ProjectID,
			project.ProjectScopeKey,
			project.UpdatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderProjectDetail(cmd *cobra.Command, detail projects.ProjectDetail) {
	project := detail.Project
	fmt.Fprintf(cmd.OutOrStdout(), "Project: %s\n", project.Slug)
	fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\n", project.ProjectID)
	fmt.Fprintf(cmd.OutOrStdout(), "Name: %s\n", project.Name)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", project.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Scope: %s (%s)\n", project.ProjectScopeKey, project.ProjectScopeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Owner actor: %s\n", project.OwnerActorID)
	fmt.Fprintf(cmd.OutOrStdout(), "Home node: %s\n", ptrOrDash(project.HomeNodeID))
	fmt.Fprintf(cmd.OutOrStdout(), "Policy: %s\n", ptrOrDash(project.DefaultPolicyRef))
	fmt.Fprintf(cmd.OutOrStdout(), "Workspace view: %s\n", ptrOrDash(project.DefaultWorkspaceViewID))
	if detail.OwnerMembership != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Owner membership: %s (%s)\n", detail.OwnerMembership.ProjectMembershipID, detail.OwnerMembership.Role)
	}
	if detail.PolicyProfile != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Policy visibility: %s\n", detail.PolicyProfile.Visibility)
	}
	if detail.WorkspaceView != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Workspace kind: %s\n", detail.WorkspaceView.ViewKind)
	}
}

func renderObjectList(cmd *cobra.Command, objectList []objects.Object) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "OBJECT ID\tTYPE\tNAME\tSCOPE\tSTATUS\tUPDATED")
	for _, object := range objectList {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			object.ObjectID,
			object.ObjectType,
			object.Name,
			ptrOrDash(object.HomeScopeID),
			object.Status,
			object.UpdatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderObjectIngest(cmd *cobra.Command, result objects.IngestFileResult) {
	renderObjectDetail(cmd, result.Object)
	if len(result.EventIDs) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Events: %s\n", strings.Join(result.EventIDs, ", "))
	}
}

func renderObjectDetail(cmd *cobra.Command, detail objects.ObjectDetail) {
	object := detail.Object
	fmt.Fprintf(cmd.OutOrStdout(), "Object: %s\n", object.ObjectID)
	fmt.Fprintf(cmd.OutOrStdout(), "Type: %s\n", object.ObjectType)
	fmt.Fprintf(cmd.OutOrStdout(), "Name: %s\n", object.Name)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", object.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Home scope: %s\n", ptrOrDash(object.HomeScopeID))
	if detail.LatestVersion != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Version: %s (%d)\n", detail.LatestVersion.ObjectVersionID, detail.LatestVersion.VersionNumber)
		fmt.Fprintf(cmd.OutOrStdout(), "Content hash: %s\n", ptrOrDash(detail.LatestVersion.ContentHash))
		fmt.Fprintf(cmd.OutOrStdout(), "Size: %s\n", int64PtrOrDash(detail.LatestVersion.SizeBytes))
		fmt.Fprintf(cmd.OutOrStdout(), "MIME: %s\n", ptrOrDash(detail.LatestVersion.MimeType))
	}
	if detail.Blob != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Blob: %s\n", detail.Blob.BlobID)
		fmt.Fprintf(cmd.OutOrStdout(), "Hash: %s\n", detail.Blob.HashURI)
		fmt.Fprintf(cmd.OutOrStdout(), "Store path: %s\n", detail.Blob.StoragePath)
	}
	if detail.File != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "File: %s\n", detail.File.LogicalName)
		fmt.Fprintf(cmd.OutOrStdout(), "Text extractable: %t\n", detail.File.TextExtractable)
		fmt.Fprintf(cmd.OutOrStdout(), "Index policy: %s\n", detail.File.IndexPolicy)
	}
	for _, link := range detail.ScopeLinks {
		fmt.Fprintf(cmd.OutOrStdout(), "Scope link: %s (%s, primary=%t)\n", link.ScopeID, link.RelationshipType, link.IsPrimary)
	}
	for _, location := range detail.Locations {
		if location.IsCanonicalLocation {
			fmt.Fprintf(cmd.OutOrStdout(), "Canonical location: %s\n", location.PathOrURI)
			break
		}
	}
}

func renderObjectVersions(cmd *cobra.Command, versions []objects.ObjectVersion) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "VERSION ID\tNUMBER\tHASH\tSIZE\tSTATUS\tCREATED")
	for _, version := range versions {
		fmt.Fprintf(
			writer,
			"%s\t%d\t%s\t%s\t%s\t%s\n",
			version.ObjectVersionID,
			version.VersionNumber,
			ptrOrDash(version.ContentHash),
			int64PtrOrDash(version.SizeBytes),
			version.Status,
			version.CreatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderSearchResultSet(cmd *cobra.Command, resultSet search.SearchResultSet) {
	fmt.Fprintf(cmd.OutOrStdout(), "Search: %s\n", resultSet.Query)
	fmt.Fprintf(cmd.OutOrStdout(), "Results: %d\n", resultSet.ResultCount)
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "RANK\tOBJECT\tVERSION\tCHUNK\tTITLE\tSNIPPET")
	for _, result := range resultSet.Results {
		fmt.Fprintf(
			writer,
			"%.4f\t%s\t%s\t%s\t%s\t%s\n",
			result.RankScore,
			result.ObjectID,
			result.ObjectVersionID,
			result.DocumentChunkID,
			dashIfEmpty(result.Title),
			compactText(result.Snippet, 120),
		)
	}
	_ = writer.Flush()
}

func renderIndexStatusList(cmd *cobra.Command, statuses []search.IndexStatus) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "INDEX TYPE\tSTATUS\tOBJECT\tVERSION\tINDEX VERSION\tCOMPLETED\tFAILED\tERROR")
	for _, status := range statuses {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			status.IndexType,
			status.Status,
			dashIfEmpty(status.ObjectID),
			dashIfEmpty(status.ObjectVersionID),
			status.IndexVersion,
			timePtrOrDash(status.CompletedAt),
			timePtrOrDash(status.FailedAt),
			compactText(status.LastErrorMessage, 80),
		)
	}
	_ = writer.Flush()
}

func renderIndexResult(cmd *cobra.Command, result search.IndexResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Index rebuild: %s\n", result.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Object: %s\n", result.ObjectID)
	fmt.Fprintf(cmd.OutOrStdout(), "Version: %s\n", result.ObjectVersionID)
	fmt.Fprintf(cmd.OutOrStdout(), "Extracted text: %s\n", dashIfEmpty(result.ExtractedTextID))
	fmt.Fprintf(cmd.OutOrStdout(), "Chunks: %d\n", result.ChunkCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Search documents: %d\n", result.SearchDocumentCount)
	if len(result.EventIDs) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Events: %s\n", strings.Join(result.EventIDs, ", "))
	}
}

func renderEventList(cmd *cobra.Command, eventList []events.Event) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "EVENT ID\tTYPE\tSTATUS\tSCOPE\tCORRELATION\tCREATED")
	for _, event := range eventList {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			event.EventID,
			event.EventType,
			ptrOrDash(event.Status),
			ptrOrDash(event.ScopeID),
			ptrOrDash(event.CorrelationID),
			event.CreatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderEvent(cmd *cobra.Command, event events.Event) {
	fmt.Fprintf(cmd.OutOrStdout(), "Event: %s\n", event.EventID)
	fmt.Fprintf(cmd.OutOrStdout(), "Type: %s\n", event.EventType)
	fmt.Fprintf(cmd.OutOrStdout(), "Level: %s\n", event.EventLevel)
	fmt.Fprintf(cmd.OutOrStdout(), "Actor: %s\n", event.ActorID)
	fmt.Fprintf(cmd.OutOrStdout(), "Origin node: %s\n", event.OriginNodeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Scope: %s\n", ptrOrDash(event.ScopeID))
	fmt.Fprintf(cmd.OutOrStdout(), "Target: %s/%s\n", ptrOrDash(event.TargetKind), ptrOrDash(event.TargetID))
	fmt.Fprintf(cmd.OutOrStdout(), "Correlation: %s\n", ptrOrDash(event.CorrelationID))
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", ptrOrDash(event.Status))
	fmt.Fprintf(cmd.OutOrStdout(), "Result: %s\n", ptrOrDash(event.Result))
	fmt.Fprintf(cmd.OutOrStdout(), "Created: %s\n", event.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Payload: %s\n", string(event.Payload))
}

func renderScriptList(cmd *cobra.Command, scriptList []scripts.Script) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "SLUG\tSTATUS\tSCRIPT ID\tACTIVE VERSION\tOWNER SCOPE\tUPDATED")
	for _, script := range scriptList {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			script.Slug,
			script.Status,
			script.ScriptID,
			ptrOrDash(script.ActiveVersionID),
			ptrOrDash(script.OwnerScopeID),
			script.UpdatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderScriptRegister(cmd *cobra.Command, result scripts.RegisterResult) {
	action := "registered"
	if !result.ScriptCreated && !result.VersionCreated {
		action = "unchanged"
	} else if result.ScriptCreated {
		action = "created"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Script %s: %s\n", action, result.Script.Slug)
	fmt.Fprintf(cmd.OutOrStdout(), "Script ID: %s\n", result.Script.ScriptID)
	fmt.Fprintf(cmd.OutOrStdout(), "Version: %s (%s)\n", result.Version.ScriptVersionID, result.Version.VersionLabel)
	fmt.Fprintf(cmd.OutOrStdout(), "Version status: %s\n", result.Version.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Activated: %t\n", result.Activated)
	fmt.Fprintf(cmd.OutOrStdout(), "Content hash: %s\n", result.Version.ContentHash)
	fmt.Fprintf(cmd.OutOrStdout(), "Package root: %s\n", result.Version.PackageRoot)
	if len(result.EventIDs) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Events: %s\n", strings.Join(result.EventIDs, ", "))
	}
}

func renderScriptDetail(cmd *cobra.Command, detail scripts.ScriptDetail) {
	script := detail.Script
	fmt.Fprintf(cmd.OutOrStdout(), "Script: %s\n", script.Slug)
	fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\n", script.ScriptID)
	fmt.Fprintf(cmd.OutOrStdout(), "Name: %s\n", script.Name)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", script.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Owner scope: %s\n", ptrOrDash(script.OwnerScopeID))
	fmt.Fprintf(cmd.OutOrStdout(), "Active version: %s\n", ptrOrDash(script.ActiveVersionID))
	if detail.ActiveVersion != nil {
		version := detail.ActiveVersion
		fmt.Fprintf(cmd.OutOrStdout(), "Active label: %s\n", version.VersionLabel)
		fmt.Fprintf(cmd.OutOrStdout(), "Manifest hash: %s\n", version.ManifestHash)
		fmt.Fprintf(cmd.OutOrStdout(), "Content hash: %s\n", version.ContentHash)
		fmt.Fprintf(cmd.OutOrStdout(), "Package root: %s\n", version.PackageRoot)
		fmt.Fprintf(cmd.OutOrStdout(), "Entrypoint: %s\n", compactText(string(version.EntrypointJSON), 160))
	}
	if len(detail.Versions) > 0 {
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "VERSION ID\tLABEL\tSTATUS\tCONTENT HASH\tCREATED\tACTIVATED")
		for _, version := range detail.Versions {
			fmt.Fprintf(
				writer,
				"%s\t%s\t%s\t%s\t%s\t%s\n",
				version.ScriptVersionID,
				version.VersionLabel,
				version.Status,
				version.ContentHash,
				version.CreatedAt.UTC().Format(time.RFC3339),
				timePtrOrDash(version.ActivatedAt),
			)
		}
		_ = writer.Flush()
	}
}

func renderScriptRun(cmd *cobra.Command, result jobs.RunResult) {
	job := result.Job.Job
	fmt.Fprintf(cmd.OutOrStdout(), "Job: %s\n", job.JobID)
	fmt.Fprintf(cmd.OutOrStdout(), "Type: %s\n", job.JobType)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", job.Status)
	if result.ExecutionMode != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Execution mode: %s\n", result.ExecutionMode)
	}
	if result.WaitResult != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Wait result: %s\n", result.WaitResult)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Script: %s\n", ptrOrDash(job.ScriptID))
	fmt.Fprintf(cmd.OutOrStdout(), "Script version: %s\n", ptrOrDash(job.ScriptVersionID))
	fmt.Fprintf(cmd.OutOrStdout(), "Source object: %s\n", ptrOrDash(job.SourceObjectID))
	fmt.Fprintf(cmd.OutOrStdout(), "Workdir: %s\n", ptrOrDash(job.WorkdirPath))
	if job.ExitCode != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Exit code: %d\n", *job.ExitCode)
	}
	if job.FailureMessage != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Failure: %s\n", *job.FailureMessage)
	}
	if len(result.Job.Outputs) > 0 {
		renderJobOutputs(cmd, result.Job.Outputs)
	}
	if len(result.Artifacts) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Artifacts: %d\n", len(result.Artifacts))
		for _, artifact := range result.Artifacts {
			fmt.Fprintf(cmd.OutOrStdout(), "- %s %s (%s)\n", artifact.Artifact.ArtifactID, artifact.Artifact.Title, artifact.Artifact.ObjectID)
		}
	}
}

func renderJobList(cmd *cobra.Command, jobList []jobs.Job) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "JOB ID\tTYPE\tSTATUS\tATTENTION\tSCRIPT\tSOURCE OBJECT\tATTEMPTS\tPRIORITY\tUPDATED")
	for _, job := range jobList {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\t%d/%d\t%d\t%s\n",
			job.JobID,
			job.JobType,
			job.Status,
			jobAttentionDisplay(job),
			ptrOrDash(job.ScriptID),
			ptrOrDash(job.SourceObjectID),
			job.AttemptCount,
			job.MaxAttempts,
			job.Priority,
			job.UpdatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderJobQueueSummary(cmd *cobra.Command, summary jobs.QueueSummary) {
	fmt.Fprintf(cmd.OutOrStdout(), "Queued jobs: %d\n", summary.QueuedCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Running jobs: %d\n", summary.RunningCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Failed jobs: %d\n", summary.FailedCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Timed-out jobs: %d\n", summary.TimedOutCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Manual action: %d\n", summary.ManualActionCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Runners: %d\n", summary.RunnerCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Idle runners: %d\n", summary.IdleRunnerCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Running runners: %d\n", summary.RunningRunnerCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Draining runners: %d\n", summary.DrainingRunnerCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Offline runners: %d\n", summary.OfflineRunnerCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Failed runners: %d\n", summary.FailedRunnerCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Current jobs: %d\n", summary.CurrentJobCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Oldest queued: %s\n", timePtrOrDash(summary.OldestQueuedAt))
}

func renderJobAction(cmd *cobra.Command, action string, job jobs.Job) {
	fmt.Fprintf(cmd.OutOrStdout(), "%s job: %s\n", action, job.JobID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", job.Status)
	if jobAttentionRelevant(job) {
		fmt.Fprintf(cmd.OutOrStdout(), "Attention: %s\n", jobs.EffectiveFailureAttentionStatus(job.FailureAttentionStatus))
		fmt.Fprintf(cmd.OutOrStdout(), "Attention updated: %s\n", timePtrOrDash(job.FailureAttentionUpdatedAt))
		if strings.TrimSpace(job.FailureAttentionNote) != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Attention note: %s\n", job.FailureAttentionNote)
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Script: %s\n", ptrOrDash(job.ScriptID))
	fmt.Fprintf(cmd.OutOrStdout(), "Attempts: %d/%d\n", job.AttemptCount, job.MaxAttempts)
	fmt.Fprintf(cmd.OutOrStdout(), "Updated: %s\n", job.UpdatedAt.UTC().Format(time.RFC3339))
}

func renderJobDetail(cmd *cobra.Command, detail jobs.JobDetail) {
	job := detail.Job
	fmt.Fprintf(cmd.OutOrStdout(), "Job: %s\n", job.JobID)
	fmt.Fprintf(cmd.OutOrStdout(), "Type: %s\n", job.JobType)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", job.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Origin actor: %s\n", job.OriginActorID)
	fmt.Fprintf(cmd.OutOrStdout(), "Origin node: %s\n", job.OriginNodeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Execution node: %s\n", job.ExecutionNodeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Scope: %s\n", ptrOrDash(job.ScopeID))
	fmt.Fprintf(cmd.OutOrStdout(), "Script: %s\n", ptrOrDash(job.ScriptID))
	fmt.Fprintf(cmd.OutOrStdout(), "Script version: %s\n", ptrOrDash(job.ScriptVersionID))
	fmt.Fprintf(cmd.OutOrStdout(), "Source object: %s\n", ptrOrDash(job.SourceObjectID))
	fmt.Fprintf(cmd.OutOrStdout(), "Workdir: %s\n", ptrOrDash(job.WorkdirPath))
	fmt.Fprintf(cmd.OutOrStdout(), "Attempts: %d/%d\n", job.AttemptCount, job.MaxAttempts)
	fmt.Fprintf(cmd.OutOrStdout(), "Created: %s\n", job.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Started: %s\n", timePtrOrDash(job.StartedAt))
	fmt.Fprintf(cmd.OutOrStdout(), "Completed: %s\n", timePtrOrDash(job.CompletedAt))
	fmt.Fprintf(cmd.OutOrStdout(), "Failed: %s\n", timePtrOrDash(job.FailedAt))
	if job.ExitCode != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Exit code: %d\n", *job.ExitCode)
	}
	if job.FailureMessage != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Failure: %s\n", *job.FailureMessage)
	}
	if jobAttentionRelevant(job) {
		fmt.Fprintf(cmd.OutOrStdout(), "Attention: %s\n", jobs.EffectiveFailureAttentionStatus(job.FailureAttentionStatus))
		fmt.Fprintf(cmd.OutOrStdout(), "Attention updated: %s\n", timePtrOrDash(job.FailureAttentionUpdatedAt))
		fmt.Fprintf(cmd.OutOrStdout(), "Attention actor: %s\n", ptrOrDash(job.FailureAttentionUpdatedByActorID))
		if strings.TrimSpace(job.FailureAttentionNote) != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Attention note: %s\n", job.FailureAttentionNote)
		}
	}
	if len(detail.Attempts) > 0 {
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "ATTEMPT\tSTATUS\tWORKDIR\tEXIT\tSTARTED\tCOMPLETED")
		for _, attempt := range detail.Attempts {
			exitCode := "-"
			if attempt.ExitCode != nil {
				exitCode = fmt.Sprintf("%d", *attempt.ExitCode)
			}
			fmt.Fprintf(
				writer,
				"%d\t%s\t%s\t%s\t%s\t%s\n",
				attempt.AttemptNumber,
				attempt.Status,
				attempt.WorkdirPath,
				exitCode,
				timePtrOrDash(attempt.StartedAt),
				timePtrOrDash(attempt.CompletedAt),
			)
		}
		_ = writer.Flush()
	}
	if len(detail.Outputs) > 0 {
		renderJobOutputs(cmd, detail.Outputs)
	}
	if len(detail.Artifacts) > 0 {
		renderArtifactList(cmd, detail.Artifacts)
	}
	if len(detail.Logs) > 0 {
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "LOG STREAM\tBYTES\tPATH")
		for _, log := range detail.Logs {
			fmt.Fprintf(writer, "%s\t%d\t%s\n", log.Stream, log.ByteCount, log.Path)
		}
		_ = writer.Flush()
	}
}

func jobAttentionRelevant(job jobs.Job) bool {
	if jobs.JobNeedsFailureAttention(job) {
		return true
	}
	switch jobs.EffectiveFailureAttentionStatus(job.FailureAttentionStatus) {
	case jobs.FailureAttentionStatusAcknowledged, jobs.FailureAttentionStatusArchived:
		return true
	default:
		return false
	}
}

func jobAttentionDisplay(job jobs.Job) string {
	if !jobAttentionRelevant(job) {
		return "-"
	}
	return jobs.EffectiveFailureAttentionStatus(job.FailureAttentionStatus)
}

func renderJobOutputs(cmd *cobra.Command, outputList []jobs.JobOutput) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "OUTPUT\tTYPE\tSTATUS\tARTIFACT\tVALUE")
	for _, output := range outputList {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\n",
			output.OutputKey,
			output.OutputType,
			output.Status,
			ptrOrDash(output.ArtifactID),
			compactText(string(output.ValueJSON), 100),
		)
	}
	_ = writer.Flush()
}

func renderJobLogs(cmd *cobra.Command, logs []jobs.JobLog, stream string, tail int) {
	stream = strings.TrimSpace(stream)
	if stream == "" {
		stream = "stdout"
	}
	for _, log := range logs {
		if stream != "all" && log.Stream != stream {
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "== %s (%d bytes) ==\n", log.Stream, log.ByteCount)
		text := log.TailText
		if tail > 0 {
			text = lastLines(text, tail)
		}
		if !strings.HasSuffix(text, "\n") && text != "" {
			text += "\n"
		}
		fmt.Fprint(cmd.OutOrStdout(), text)
	}
}

func renderRunnerList(cmd *cobra.Command, runnerList []jobs.Runner) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "RUNNER ID\tKEY\tSTATUS\tNODE\tCURRENT JOB\tHEARTBEAT\tUPDATED")
	for _, runner := range runnerList {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			runner.RunnerID,
			runner.RunnerKey,
			runner.Status,
			runner.NodeID,
			ptrOrDash(runner.CurrentJobID),
			timePtrOrDash(runner.LastHeartbeatAt),
			runner.UpdatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderRunnerDetail(cmd *cobra.Command, runner jobs.Runner) {
	fmt.Fprintf(cmd.OutOrStdout(), "Runner: %s\n", runner.RunnerKey)
	fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\n", runner.RunnerID)
	fmt.Fprintf(cmd.OutOrStdout(), "Node: %s\n", runner.NodeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Type: %s\n", runner.RunnerType)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", runner.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Current job: %s\n", ptrOrDash(runner.CurrentJobID))
	fmt.Fprintf(cmd.OutOrStdout(), "Last heartbeat: %s\n", timePtrOrDash(runner.LastHeartbeatAt))
	fmt.Fprintf(cmd.OutOrStdout(), "Version: %s\n", dashIfEmpty(runner.Version))
	fmt.Fprintf(cmd.OutOrStdout(), "Updated: %s\n", runner.UpdatedAt.UTC().Format(time.RFC3339))
}

func renderArtifactList(cmd *cobra.Command, artifactList []artifacts.Artifact) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ARTIFACT ID\tTYPE\tSTATUS\tTITLE\tOBJECT\tJOB\tSIZE\tCREATED")
	for _, artifact := range artifactList {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			artifact.ArtifactID,
			artifact.ArtifactType,
			artifact.Status,
			dashIfEmpty(artifact.Title),
			artifact.ObjectID,
			artifact.JobID,
			int64PtrOrDash(artifact.SizeBytes),
			artifact.CreatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderArtifactDetail(cmd *cobra.Command, detail artifacts.ArtifactDetail) {
	artifact := detail.Artifact
	fmt.Fprintf(cmd.OutOrStdout(), "Artifact: %s\n", artifact.ArtifactID)
	fmt.Fprintf(cmd.OutOrStdout(), "Type: %s\n", artifact.ArtifactType)
	fmt.Fprintf(cmd.OutOrStdout(), "Title: %s\n", artifact.Title)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", artifact.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Object: %s\n", artifact.ObjectID)
	fmt.Fprintf(cmd.OutOrStdout(), "Object version: %s\n", ptrOrDash(artifact.ObjectVersionID))
	fmt.Fprintf(cmd.OutOrStdout(), "Blob: %s\n", ptrOrDash(artifact.BlobID))
	fmt.Fprintf(cmd.OutOrStdout(), "Job: %s\n", artifact.JobID)
	fmt.Fprintf(cmd.OutOrStdout(), "Script: %s\n", ptrOrDash(artifact.ScriptID))
	fmt.Fprintf(cmd.OutOrStdout(), "Script version: %s\n", ptrOrDash(artifact.ScriptVersionID))
	fmt.Fprintf(cmd.OutOrStdout(), "Source object: %s\n", ptrOrDash(artifact.SourceObjectID))
	fmt.Fprintf(cmd.OutOrStdout(), "Hash: %s\n", ptrOrDash(artifact.HashURI))
	fmt.Fprintf(cmd.OutOrStdout(), "MIME: %s\n", ptrOrDash(artifact.MimeType))
	fmt.Fprintf(cmd.OutOrStdout(), "Size: %s\n", int64PtrOrDash(artifact.SizeBytes))
	fmt.Fprintf(cmd.OutOrStdout(), "Created: %s\n", artifact.CreatedAt.UTC().Format(time.RFC3339))
	if detail.Object.Object.ObjectID != "" {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		renderObjectDetail(cmd, detail.Object)
	}
}

func ptrOrDash(value *string) string {
	if value == nil || *value == "" {
		return "-"
	}
	return *value
}

func int64PtrOrDash(value *int64) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *value)
}

func timePtrOrDash(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func dashIfEmpty(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func compactText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "-"
	}
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func lastLines(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	lines := strings.Split(value, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= limit {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-limit:], "\n")
}
