package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/agents"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
)

func newAgentCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage LOOM agent-facing access records",
	}
	cmd.AddCommand(newAgentAccessSessionCommand(opts))
	cmd.AddCommand(newAgentAccessSessionsCommand(opts))
	cmd.AddCommand(newAgentWorkContextCommand(opts))
	cmd.AddCommand(newAgentWorkContextsCommand(opts))
	cmd.AddCommand(newAgentToolViewCommand(opts))
	cmd.AddCommand(newAgentToolsCommand(opts))
	cmd.AddCommand(newAgentToolCommand(opts))
	cmd.AddCommand(newAgentCallCommand(opts))
	cmd.AddCommand(newAgentWorklogCommand(opts))
	cmd.AddCommand(newAgentToolCallCommand(opts))
	cmd.AddCommand(newAgentPackCommand(opts))
	return cmd
}

func newAgentAccessSessionCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "access-session",
		Short: "Create or inspect a LOOM agent access session",
	}
	createInput := agents.CreateAccessSessionInput{}
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a LOOM access session for an agent actor",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.CreateAgentAccessSession(ctx, correlationID, createInput)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not create agent access session.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderAgentAccessSession(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	createCmd.Flags().StringVar(&createInput.ActorRef, "actor", "", "agent actor ID or key")
	createCmd.Flags().StringVar(&createInput.AccessSessionKey, "key", "", "optional stable access session key")
	createCmd.Flags().StringVar(&createInput.OriginKind, "origin-kind", agents.OriginKindLocalCLI, "origin kind")
	createCmd.Flags().StringVar(&createInput.OriginNodeRef, "origin-node", "", "origin node ID or key")
	createCmd.Flags().StringVar(&createInput.OriginClientID, "origin-client", "", "origin client identifier")
	createCmd.Flags().StringVar(&createInput.RuntimeNodeRef, "runtime-node", "", "runtime node ID or key")
	createCmd.Flags().StringVar(&createInput.HomeNodeRef, "home-node", "", "home node ID or key")
	createCmd.Flags().StringVar(&createInput.ScopeRef, "scope", "", "current scope ID, key, or slug")
	createCmd.Flags().StringVar(&createInput.ProjectRef, "project", "", "active project ID or slug")
	cmd.AddCommand(createCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <access-session-ref>",
		Short: "Inspect a LOOM agent access session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetAgentAccessSession(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect agent access session.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderAgentAccessSession(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func newAgentAccessSessionsCommand(opts *options) *cobra.Command {
	filter := agents.AccessSessionFilter{}
	cmd := &cobra.Command{
		Use:   "access-sessions",
		Short: "List LOOM agent access sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListAgentAccessSessions(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list agent access sessions.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderAgentAccessSessionList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of access sessions to return")
	cmd.Flags().StringVar(&filter.ActorRef, "actor", "", "filter by agent actor ID or key")
	cmd.Flags().StringVar(&filter.Status, "status", "", "filter by status")
	return cmd
}

func newAgentWorkContextCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "work-context",
		Short: "Create or inspect a LOOM agent work context",
	}
	createInput := agents.CreateWorkContextInput{}
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a LOOM-scoped work context for an agent access session",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.CreateAgentWorkContext(ctx, correlationID, createInput)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not create agent work context.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderAgentWorkContextDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	createCmd.Flags().StringVar(&createInput.AccessSessionRef, "access-session", "", "agent access session ID or key")
	createCmd.Flags().StringVar(&createInput.WorkContextKey, "key", "", "optional stable work context key")
	createCmd.Flags().StringVar(&createInput.Objective, "objective", "", "work objective")
	createCmd.Flags().StringVar(&createInput.ScopeRef, "scope", "", "current scope ID, key, or slug")
	createCmd.Flags().StringVar(&createInput.ProjectRef, "project", "", "active project ID or slug")
	createCmd.Flags().StringVar(&createInput.RuntimeNodeRef, "runtime-node", "", "runtime node ID or key")
	createCmd.Flags().StringVar(&createInput.HomeNodeRef, "home-node", "", "home node ID or key")
	cmd.AddCommand(createCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <work-context-ref>",
		Short: "Inspect a LOOM agent work context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetAgentWorkContext(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect agent work context.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderAgentWorkContextDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func newAgentWorkContextsCommand(opts *options) *cobra.Command {
	filter := agents.WorkContextFilter{}
	cmd := &cobra.Command{
		Use:   "work-contexts",
		Short: "List LOOM agent work contexts",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListAgentWorkContexts(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list agent work contexts.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderAgentWorkContextList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of work contexts to return")
	cmd.Flags().StringVar(&filter.ActorRef, "actor", "", "filter by agent actor ID or key")
	cmd.Flags().StringVar(&filter.AccessSessionRef, "access-session", "", "filter by access session ID or key")
	cmd.Flags().StringVar(&filter.Status, "status", "", "filter by status")
	return cmd
}

func newAgentToolViewCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tool-view",
		Short: "Inspect LOOM agent tool views",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <tool-view-id>",
		Short: "Inspect a bounded LOOM tool view",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetAgentToolView(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect agent tool view.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderAgentToolViewDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func newAgentToolsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tools",
		Short: "Search agent-visible LOOM tools",
	}
	searchInput := agents.ToolSearchInput{}
	includeRequestable := true
	searchCmd := &cobra.Command{
		Use:   "search --work-context <work-context-ref> <query>",
		Short: "Search compact tool candidates for an agent work context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			searchInput.Query = args[0]
			searchInput.IncludeRequestable = &includeRequestable
			envelope, err := client.SearchAgentTools(ctx, correlationID, searchInput)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not search agent tools.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderAgentToolSearchResult(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	searchCmd.Flags().StringVar(&searchInput.WorkContextRef, "work-context", "", "agent work context ID or key")
	searchCmd.Flags().IntVar(&searchInput.Limit, "limit", 10, "maximum number of compact candidates to return")
	searchCmd.Flags().BoolVar(&includeRequestable, "include-requestable", true, "include capabilities that require approval")
	searchCmd.Flags().BoolVar(&searchInput.IncludeRedacted, "include-redacted", false, "reserved; hidden capabilities are not revealed")
	searchCmd.Flags().StringVar(&searchInput.Form, "form", "", "filter by capability form")
	searchCmd.Flags().StringVar(&searchInput.ProviderRef, "provider", "", "filter by provider ID, key, or compact address")
	searchCmd.Flags().StringVar(&searchInput.ScopeRef, "scope", "", "filter by scope ID, key, or slug")
	cmd.AddCommand(searchCmd)
	return cmd
}

func newAgentToolCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tool",
		Short: "Inspect selected agent-visible LOOM tools",
	}
	inspectInput := agents.ToolInspectInput{}
	inspectCmd := &cobra.Command{
		Use:   "inspect --work-context <work-context-ref> <tool-ref>",
		Short: "Inspect one selected tool and retrieve its exact schema",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			inspectInput.ToolRef = args[0]
			envelope, err := client.InspectAgentTool(ctx, correlationID, inspectInput)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect agent tool.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderAgentToolInspection(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	inspectCmd.Flags().StringVar(&inspectInput.WorkContextRef, "work-context", "", "agent work context ID or key")
	inspectCmd.Flags().StringVar(&inspectInput.Query, "query", "", "optional query used to choose usage-document sections")
	inspectCmd.Flags().StringVar(&inspectInput.UsageSectionLabel, "usage-section", "", "optional usage-document section label")
	inspectCmd.Flags().IntVar(&inspectInput.MaxSections, "max-sections", 3, "maximum usage-document sections to return")
	inspectCmd.Flags().IntVar(&inspectInput.MaxCharsPerSection, "max-chars-per-section", 800, "maximum characters per usage-document section")
	cmd.AddCommand(inspectCmd)
	return cmd
}

func newAgentCallCommand(opts *options) *cobra.Command {
	input := agents.AgentToolCallInput{}
	var inputText string
	var inputFile string
	cmd := &cobra.Command{
		Use:   "call --work-context <work-context-ref> <tool-ref>",
		Short: "Call an agent-visible LOOM tool",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			input.ToolRef = args[0]
			rawInput, err := readCapabilityInput(inputText, inputFile)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("agent_tool.input_invalid", "agents", "input", "Agent tool input is invalid.", err))
			}
			input.Input = rawInput
			envelope, err := client.CallAgentTool(ctx, correlationID, input)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not call agent tool.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.AgentToolCallID)
				return nil
			}
			renderAgentToolCallOutcome(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&input.WorkContextRef, "work-context", "", "agent work context ID or key")
	cmd.Flags().StringVar(&inputText, "input", "", "agent tool input JSON object")
	cmd.Flags().StringVar(&inputFile, "input-file", "", "path to agent tool input JSON object")
	cmd.Flags().BoolVar(&input.RequestApproval, "request-approval", false, "create an approval request when approval is required")
	cmd.Flags().StringVar(&input.ApprovalReason, "approval-reason", "", "approval reason to store if approval is requested")
	cmd.Flags().StringVar(&input.IdempotencyKey, "idempotency-key", "", "retry key for this agent tool call")
	return cmd
}

func newAgentWorklogCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worklog",
		Short: "Write and list LOOM agent worklog entries",
	}
	writeInput := agents.WriteWorklogInput{}
	writeCmd := &cobra.Command{
		Use:   "write --work-context <work-context-ref>",
		Short: "Write a worklog entry for an agent work context",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if err := requireAgentWorkContext(writeInput.WorkContextRef); err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.WriteAgentWorklog(ctx, correlationID, writeInput)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not write agent worklog.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderAgentWorklogEntry(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	writeCmd.Flags().StringVar(&writeInput.WorkContextRef, "work-context", "", "agent work context ID or key")
	writeCmd.Flags().StringVar(&writeInput.EntryKind, "kind", agents.WorklogEntryKindNote, "worklog kind")
	writeCmd.Flags().StringVar(&writeInput.Summary, "summary", "", "worklog summary")
	writeCmd.Flags().StringVar(&writeInput.Body, "body", "", "worklog body")
	writeCmd.Flags().StringVar(&writeInput.ObjectRef, "object", "", "optional object reference")
	cmd.AddCommand(writeCmd)

	filter := agents.WorklogFilter{}
	listCmd := &cobra.Command{
		Use:   "list --work-context <work-context-ref>",
		Short: "List worklog entries for an agent work context",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if err := requireAgentWorkContext(filter.WorkContextRef); err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListAgentWorklog(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list agent worklog.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderAgentWorklogList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().StringVar(&filter.WorkContextRef, "work-context", "", "agent work context ID or key")
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of worklog entries to return")
	cmd.AddCommand(listCmd)
	return cmd
}

func requireAgentWorkContext(ref string) error {
	if strings.TrimSpace(ref) != "" {
		return nil
	}
	return loomerrors.New(
		"agents.work_context_required",
		"agents",
		"work_context",
		"Pass --work-context with an agent work context ID or key.",
	)
}

func newAgentToolCallCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tool-call",
		Short: "Inspect LOOM agent tool calls",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <agent-tool-call-id>",
		Short: "Inspect an agent tool call audit record",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetAgentToolCall(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect agent tool call.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderAgentToolCall(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func renderAgentAccessSession(cmd *cobra.Command, session agents.AccessSession) {
	fmt.Fprintf(cmd.OutOrStdout(), "Agent access session: %s\n", session.AgentAccessSessionID)
	fmt.Fprintf(cmd.OutOrStdout(), "Key: %s\n", session.AccessSessionKey)
	fmt.Fprintf(cmd.OutOrStdout(), "Actor: %s\n", session.ActorID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", session.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Runtime node: %s\n", ptrOrDash(session.RuntimeNodeID))
	fmt.Fprintf(cmd.OutOrStdout(), "Home node: %s\n", ptrOrDash(session.HomeNodeID))
	fmt.Fprintf(cmd.OutOrStdout(), "Scope: %s\n", ptrOrDash(session.CurrentScopeID))
	fmt.Fprintf(cmd.OutOrStdout(), "Created: %s\n", session.CreatedAt.UTC().Format(time.RFC3339))
}

func renderAgentAccessSessionList(cmd *cobra.Command, sessions []agents.AccessSession) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "SESSION\tACTOR\tSTATUS\tRUNTIME\tSCOPE\tCREATED")
	for _, session := range sessions {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			session.AgentAccessSessionID,
			session.ActorID,
			session.Status,
			ptrOrDash(session.RuntimeNodeID),
			ptrOrDash(session.CurrentScopeID),
			session.CreatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderAgentWorkContextDetail(cmd *cobra.Command, detail agents.WorkContextDetail) {
	workContext := detail.WorkContext
	fmt.Fprintf(cmd.OutOrStdout(), "Agent work context: %s\n", workContext.AgentWorkContextID)
	fmt.Fprintf(cmd.OutOrStdout(), "Key: %s\n", workContext.WorkContextKey)
	fmt.Fprintf(cmd.OutOrStdout(), "Access session: %s\n", workContext.AgentAccessSessionID)
	fmt.Fprintf(cmd.OutOrStdout(), "Actor: %s\n", workContext.ActorID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", workContext.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Objective: %s\n", workContext.Objective)
	fmt.Fprintf(cmd.OutOrStdout(), "Active tool view: %s\n", ptrOrDash(workContext.ActiveToolViewID))
	if detail.ToolView != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Tool entries: %d\n", len(detail.ToolView.Entries))
	}
}

func renderAgentWorkContextList(cmd *cobra.Command, contexts []agents.WorkContext) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "WORK_CONTEXT\tSESSION\tACTOR\tSTATUS\tTOOL_VIEW\tOBJECTIVE")
	for _, workContext := range contexts {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			workContext.AgentWorkContextID,
			workContext.AgentAccessSessionID,
			workContext.ActorID,
			workContext.Status,
			ptrOrDash(workContext.ActiveToolViewID),
			compactText(workContext.Objective, 80),
		)
	}
	_ = writer.Flush()
}

func renderAgentToolViewDetail(cmd *cobra.Command, detail agents.ToolViewDetail) {
	toolView := detail.ToolView
	fmt.Fprintf(cmd.OutOrStdout(), "Tool view: %s\n", toolView.ToolViewID)
	fmt.Fprintf(cmd.OutOrStdout(), "Actor: %s\n", toolView.ActorID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", toolView.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Version: %d\n", toolView.Version)
	fmt.Fprintf(cmd.OutOrStdout(), "Entries: %d / %d\n", len(detail.Entries), toolView.MaxEntries)
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "TOOL\tKIND\tVISIBILITY\tAUTH\tAPPROVAL\tCAPABILITY")
	for _, entry := range detail.Entries {
		auth := "-"
		if entry.ExecutionAuthorizationLevel != nil {
			auth = fmt.Sprintf("%d", *entry.ExecutionAuthorizationLevel)
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			entry.ToolName,
			entry.EntryKind,
			entry.VisibilityState,
			auth,
			entry.ApprovalHint,
			dashIfEmpty(entry.CapabilityAddress),
		)
	}
	_ = writer.Flush()
}

func renderAgentToolSearchResult(cmd *cobra.Command, result agents.ToolSearchResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Agent work context: %s\n", result.AgentWorkContextID)
	fmt.Fprintf(cmd.OutOrStdout(), "Tool view: %s\n", result.ToolViewID)
	fmt.Fprintf(cmd.OutOrStdout(), "Query: %s\n", result.Query)
	if result.Message != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Message: %s\n", result.Message)
	}
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "TOOL\tCAPABILITY\tVISIBILITY\tAUTH\tAPPROVAL\tMATCH")
	for _, candidate := range result.Candidates {
		auth := "-"
		if candidate.ExecutionAuthorizationLevel != nil {
			auth = fmt.Sprintf("%d", *candidate.ExecutionAuthorizationLevel)
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			candidate.ToolName,
			candidate.CapabilityAddress,
			candidate.VisibilityState,
			auth,
			candidate.ApprovalHint,
			compactText(candidate.MatchedUseSummary, 80),
		)
	}
	_ = writer.Flush()
}

func renderAgentToolInspection(cmd *cobra.Command, inspection agents.ToolInspection) {
	fmt.Fprintf(cmd.OutOrStdout(), "Tool: %s\n", inspection.ToolSchema.Name)
	fmt.Fprintf(cmd.OutOrStdout(), "Entry: %s\n", inspection.Entry.ToolViewEntryID)
	fmt.Fprintf(cmd.OutOrStdout(), "Kind: %s\n", inspection.Entry.EntryKind)
	fmt.Fprintf(cmd.OutOrStdout(), "Visibility: %s\n", inspection.PolicyHint.VisibilityState)
	fmt.Fprintf(cmd.OutOrStdout(), "Approval: %s\n", dashIfEmpty(inspection.PolicyHint.ApprovalHint))
	if inspection.ToolSchema.Loom.CapabilityAddress != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Capability: %s\n", inspection.ToolSchema.Loom.CapabilityAddress)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Call via: %s\n", inspection.ToolSchema.Loom.CallVia)
	fmt.Fprintf(cmd.OutOrStdout(), "Usage sections: %d\n", len(inspection.UsageSections))
	for _, section := range inspection.UsageSections {
		fmt.Fprintf(cmd.OutOrStdout(), "- %s / %s: %s\n", section.Title, section.SectionLabel, compactText(section.Excerpt, 120))
	}
}

func renderAgentToolCallOutcome(cmd *cobra.Command, outcome agents.AgentToolCallOutcome) {
	fmt.Fprintf(cmd.OutOrStdout(), "Agent tool call: %s\n", outcome.AgentToolCallID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", outcome.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Tool: %s\n", outcome.ToolViewEntry.ToolName)
	if outcome.ToolViewEntry.CapabilityAddress != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Capability: %s\n", outcome.ToolViewEntry.CapabilityAddress)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Route: %s\n", dashIfEmpty(outcome.RouteID))
	fmt.Fprintf(cmd.OutOrStdout(), "Capability call: %s\n", dashIfEmpty(outcome.CapabilityCallID))
	fmt.Fprintf(cmd.OutOrStdout(), "Policy decision: %s\n", dashIfEmpty(outcome.PolicyDecisionID))
	fmt.Fprintf(cmd.OutOrStdout(), "Approval: %s\n", dashIfEmpty(outcome.ApprovalID))
	if outcome.ReasonCode != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Reason: %s\n", outcome.ReasonCode)
	}
	if outcome.NextActionHint != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Next: %s\n", outcome.NextActionHint)
	}
}

func renderAgentToolCall(cmd *cobra.Command, call agents.AgentToolCall) {
	fmt.Fprintf(cmd.OutOrStdout(), "Agent tool call: %s\n", call.AgentToolCallID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", call.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Tool: %s\n", call.ToolName)
	fmt.Fprintf(cmd.OutOrStdout(), "Work context: %s\n", ptrOrDash(call.AgentWorkContextID))
	fmt.Fprintf(cmd.OutOrStdout(), "Route: %s\n", ptrOrDash(call.RouteID))
	fmt.Fprintf(cmd.OutOrStdout(), "Capability call: %s\n", ptrOrDash(call.CapabilityCallID))
	fmt.Fprintf(cmd.OutOrStdout(), "Policy decision: %s\n", ptrOrDash(call.PolicyDecisionID))
	fmt.Fprintf(cmd.OutOrStdout(), "Approval: %s\n", ptrOrDash(call.ApprovalID))
	fmt.Fprintf(cmd.OutOrStdout(), "Created: %s\n", call.CreatedAt.UTC().Format(time.RFC3339))
}

func renderAgentWorklogEntry(cmd *cobra.Command, entry agents.WorklogEntry) {
	fmt.Fprintf(cmd.OutOrStdout(), "Worklog entry: %s\n", entry.WorklogEntryID)
	fmt.Fprintf(cmd.OutOrStdout(), "Work context: %s\n", entry.AgentWorkContextID)
	fmt.Fprintf(cmd.OutOrStdout(), "Kind: %s\n", entry.EntryKind)
	fmt.Fprintf(cmd.OutOrStdout(), "Summary: %s\n", entry.Summary)
	if entry.Body != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Body: %s\n", entry.Body)
	}
}

func renderAgentWorklogList(cmd *cobra.Command, entries []agents.WorklogEntry) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "WORKLOG\tKIND\tSUMMARY\tCREATED")
	for _, entry := range entries {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n",
			entry.WorklogEntryID,
			entry.EntryKind,
			compactText(entry.Summary, 80),
			entry.CreatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}
