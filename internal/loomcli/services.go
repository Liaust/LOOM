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
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/serviceregistry"
)

func newServicesCommand(opts *options) *cobra.Command {
	filter := serviceregistry.ServiceFilter{Limit: 50}
	cmd := &cobra.Command{Use: "services", Short: "List registered services"}
	list := &cobra.Command{Use: "list", Short: "List service providers with separate registry and process states", RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cfg, client, err := commandClient(opts)
		correlationID := correlation.Normalize(opts.correlationID)
		if err != nil {
			return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
		}
		envelope, err := client.ListServices(ctx, correlationID, filter)
		if err != nil {
			return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list services.", err))
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
		}
		renderServiceList(cmd, envelope.Data)
		renderResponseMeta(cmd, opts, envelope.Meta)
		return nil
	}}
	list.Flags().IntVar(&filter.Limit, "limit", 50, "maximum services to return")
	list.Flags().StringVar(&filter.NodeRef, "node", "", "filter by node")
	list.Flags().StringVar(&filter.ProjectRef, "project", "", "filter by project")
	list.Flags().StringVar(&filter.ScopeRef, "scope", "", "filter by scope")
	list.Flags().StringVar((*string)(&filter.RegistryState), "status", "", "filter by registry state")
	list.Flags().StringVar(&filter.Health, "health", "", "filter by health")
	cmd.AddCommand(list)
	return cmd
}

func newServiceCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "service", Short: "Inspect, register, and operate allowlisted services"}
	cmd.AddCommand(&cobra.Command{Use: "inspect <ref>", Short: "Inspect registry state, observed process state, and external provisioning authority", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error { return runServiceInspect(cmd, opts, args[0]) }})
	for _, operation := range []serviceregistry.Operation{serviceregistry.OperationStatus, serviceregistry.OperationStart, serviceregistry.OperationStop, serviceregistry.OperationRestart, serviceregistry.OperationLogs} {
		cmd.AddCommand(newServiceOperationCommand(opts, operation))
	}
	cmd.AddCommand(newServiceRegisterCommand(opts))
	return cmd
}

func runServiceInspect(cmd *cobra.Command, opts *options, ref string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cfg, client, err := commandClient(opts)
	correlationID := correlation.Normalize(opts.correlationID)
	if err != nil {
		return renderError(cmd, opts, correlationID, err)
	}
	envelope, err := client.GetService(ctx, correlationID, ref)
	if err != nil {
		return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect service.", err))
	}
	if opts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
	}
	renderServiceInspection(cmd, envelope.Data)
	renderResponseMeta(cmd, opts, envelope.Meta)
	return nil
}

func newServiceOperationCommand(opts *options, operation serviceregistry.Operation) *cobra.Command {
	var yes bool
	var lines, maxBytes, maxAge int
	var requestApproval bool
	var reason string
	command := &cobra.Command{Use: string(operation) + " <ref>", Short: "Run the allowlisted service " + string(operation) + " capability", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		policy, _ := serviceregistry.StandardOperationPolicy(operation)
		if policy.RequiresConfirmation && !yes {
			return fmt.Errorf("service %s requires --yes", operation)
		}
		if operation != serviceregistry.OperationLogs && (lines != 0 || maxBytes != 0 || maxAge != 0) {
			return fmt.Errorf("log selectors are valid only for logs")
		}
		if operation == serviceregistry.OperationLogs {
			limits := serviceregistry.LogLimits{MaxLines: serviceregistry.MaximumLogLines, MaxBytes: serviceregistry.MaximumLogBytes, MaxLineBytes: serviceregistry.MaximumLogLineBytes, MaxAgeSeconds: serviceregistry.MaximumLogAgeSeconds}
			if err := serviceregistry.ValidateLogSelectors(lines, maxBytes, maxAge, limits); err != nil {
				return fmt.Errorf("log request exceeds bounded maximum: %w", err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), defaultCapabilityCallWaitTimeoutSeconds*time.Second)
		defer cancel()
		cfg, client, err := commandClient(opts)
		correlationID := correlation.Normalize(opts.correlationID)
		if err != nil {
			return renderError(cmd, opts, correlationID, err)
		}
		input := map[string]any{"confirmed": yes, "lines": lines, "max_bytes": maxBytes, "max_age_seconds": maxAge, "request_approval": requestApproval, "approval_reason": reason}
		client, _ = withEffectIdempotency(client, "", "service."+string(operation)+"."+args[0])
		envelope, err := client.OperateService(ctx, correlationID, args[0], operation, input)
		if err != nil {
			return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not operate service.", err))
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Service operation: %s\nCapability call: %s\nStatus: %s\n", operation, envelope.Data.CapabilityCall.CapabilityCallID, envelope.Data.Status)
		renderResponseMeta(cmd, opts, envelope.Meta)
		return nil
	}}
	if policy, _ := serviceregistry.StandardOperationPolicy(operation); policy.RequiresConfirmation {
		command.Flags().BoolVar(&yes, "yes", false, "confirm the lifecycle mutation")
	}
	if operation == serviceregistry.OperationLogs {
		command.Flags().IntVar(&lines, "lines", serviceregistry.DefaultLogLines, "maximum log lines")
		command.Flags().IntVar(&maxBytes, "max-bytes", serviceregistry.DefaultLogBytes, "maximum log bytes")
		command.Flags().IntVar(&maxAge, "max-age-seconds", serviceregistry.DefaultLogAgeSeconds, "maximum log age")
	}
	command.Flags().BoolVar(&requestApproval, "request-approval", false, "request approval when policy requires it")
	command.Flags().StringVar(&reason, "approval-reason", "", "approval request reason")
	return command
}

func newServiceRegisterCommand(opts *options) *cobra.Command {
	var projectRoot, key string
	command := &cobra.Command{Use: "register <plan|apply>", Short: "Plan or apply provider registration without provisioning a process", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		action := strings.ToLower(args[0])
		if action != "plan" && action != "apply" {
			return fmt.Errorf("register action must be plan or apply")
		}
		analysis := projectcontracts.Analyze(projectRoot)
		item, err := projectcontracts.ResolveServiceRegistrationContract(analysis, key)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cfg, client, err := commandClient(opts)
		correlationID := correlation.Normalize(opts.correlationID)
		if err != nil {
			return renderError(cmd, opts, correlationID, err)
		}
		projectRef := analysis.Loaded.Contract.Project.ID
		if projectRef == "" {
			projectRef = analysis.Loaded.Contract.Project.Slug
		}
		status, err := client.GetProjectRegistrationStatus(ctx, correlationID, projectRef)
		if err != nil {
			return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not resolve registered project.", err))
		}
		project := status.Data.Project.Project
		if expectedID := analysis.Loaded.Contract.Project.ID; expectedID != "" && project.ProjectID != expectedID {
			return fmt.Errorf("registered project identity does not match the selected project")
		}
		input := serviceregistry.ProjectRegistrationFromContract(project.ProjectID, serviceregistry.ProjectRegistrationScopeKey(project.Slug), item.ContractPath, item.Contract)
		if action == "plan" {
			envelope, err := client.PlanServiceRegistration(ctx, correlationID, input)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Service registration plan: %s\nProvider: %s\nProvisioning: %s\nProcess action: none\n", item.Key, envelope.Data.ProviderAddress, envelope.Data.Provisioning)
			return nil
		}
		envelope, err := client.ApplyServiceRegistration(ctx, correlationID, input)
		if err != nil {
			return renderError(cmd, opts, correlationID, err)
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Service registration applied: %s\nProvider: %s\nRegistry state: %s\nProvisioning: external\nProcess action: none\n", item.Key, envelope.Data.Provider.CompactAddress, envelope.Data.Provider.Status)
		return nil
	}}
	command.Flags().StringVar(&projectRoot, "project", ".", "project root containing .loom/project.yaml")
	command.Flags().StringVar(&key, "contract", "", "service contract key")
	_ = command.MarkFlagRequired("contract")
	return command
}

func renderServiceList(cmd *cobra.Command, items []serviceregistry.ServiceListItem) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ADDRESS\tREGISTRY\tPROCESS\tHEALTH\tPROVISIONING\tNODE")
	for _, item := range items {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n", item.ProviderAddress, item.RegistryState, item.ProcessState, item.HealthStatus, item.Provisioning, item.NodeID)
	}
	_ = writer.Flush()
}
func renderServiceInspection(cmd *cobra.Command, item serviceregistry.ServiceInspection) {
	fmt.Fprintf(cmd.OutOrStdout(), "Service: %s\nProvider: %s\nRegistry state: %s\nObserved process state: %s\nHealth: %s (%s)\nProvisioning authority: %s\nManager: %s\nUnit: %s\nLast observation: %s\n", item.DisplayName, item.ProviderAddress, item.RegistryState, item.ProcessState, item.HealthStatus, item.AvailabilityStatus, item.Provisioning, item.RuntimeProfile.Manager, item.RuntimeProfile.Unit, timePtrOrDash(item.LastObservedAt))
	fmt.Fprintf(cmd.OutOrStdout(), "Operations: %s\n", joinServiceOperations(item.RuntimeProfile.Operations))
}
func joinServiceOperations(values []serviceregistry.Operation) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = string(value)
	}
	return strings.Join(parts, ",")
}
