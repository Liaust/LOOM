package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/capabilityruntime"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/routing"
)

func newProvidersCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "providers",
		Short: "List LOOM capability providers",
	}
	filter := capabilities.ProviderFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List registered capability providers",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListProviders(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list providers.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProviderList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of providers to return")
	listCmd.Flags().StringVar(&filter.NodeRef, "node", "", "filter by node ID or key")
	listCmd.Flags().StringVar(&filter.ScopeRef, "scope", "", "filter by scope ID, key, or slug")
	listCmd.Flags().StringVar(&filter.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	listCmd.Flags().StringVar(&filter.ProviderType, "type", "", "filter by provider type")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by provider status")
	listCmd.Flags().StringVar(&filter.Health, "health", "", "filter by provider health")
	cmd.AddCommand(listCmd)
	return cmd
}

func newProviderCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Inspect LOOM capability providers",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <provider-ref>",
		Short: "Inspect a provider by ID, compact address, or unambiguous key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetProvider(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect provider.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProviderInspection(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "health <provider-ref>",
		Short: "Inspect provider health",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetProviderHealth(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect provider health.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProviderHealth(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func newProviderAdvertisementsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider-advertisements",
		Short: "List remote provider advertisements",
	}
	filter := capabilities.ProviderAdvertisementFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List remote provider advertisements awaiting review or already reviewed",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListProviderAdvertisements(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list provider advertisements.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProviderAdvertisementList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of advertisements to return")
	listCmd.Flags().StringVar(&filter.NodeRef, "node", "", "filter by origin node ID or key")
	listCmd.Flags().StringVar(&filter.ProviderRef, "provider", "", "filter by provider ID, key, or compact address")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by advertisement status")
	cmd.AddCommand(listCmd)
	return cmd
}

func newProviderAdvertisementCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider-advertisement",
		Short: "Inspect and review remote provider advertisements",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <advertisement-ref>",
		Short: "Inspect a provider advertisement",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetProviderAdvertisement(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect provider advertisement.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProviderAdvertisementInspection(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	approveInput := capabilities.ApproveProviderAdvertisementInput{}
	approveCmd := &cobra.Command{
		Use:   "approve <advertisement-ref>",
		Short: "Approve a provider advertisement and activate its endpoints",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ApproveProviderAdvertisement(ctx, correlationID, args[0], approveInput)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not approve provider advertisement.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProviderAdvertisementInspection(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	approveCmd.Flags().StringVar(&approveInput.Reason, "reason", "", "review reason")
	cmd.AddCommand(approveCmd)

	rejectInput := capabilities.RejectProviderAdvertisementInput{}
	rejectCmd := &cobra.Command{
		Use:   "reject <advertisement-ref>",
		Short: "Reject a provider advertisement",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.RejectProviderAdvertisement(ctx, correlationID, args[0], rejectInput)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not reject provider advertisement.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProviderAdvertisementInspection(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	rejectCmd.Flags().StringVar(&rejectInput.Reason, "reason", "", "rejection reason")
	_ = rejectCmd.MarkFlagRequired("reason")
	cmd.AddCommand(rejectCmd)
	return cmd
}

func newCapabilitiesCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "capabilities",
		Short: "List and search LOOM capabilities",
	}
	filter := capabilities.CapabilityFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List registered capability endpoints",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListCapabilities(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list capabilities.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderCapabilityList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of capabilities to return")
	listCmd.Flags().StringVar(&filter.ProviderRef, "provider", "", "filter by provider ID, key, or address")
	listCmd.Flags().StringVar(&filter.ClassRef, "class", "", "filter by capability class")
	listCmd.Flags().StringVar(&filter.NodeRef, "node", "", "filter by node ID or key")
	listCmd.Flags().StringVar(&filter.ScopeRef, "scope", "", "filter by scope ID, key, or slug")
	listCmd.Flags().StringVar(&filter.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	listCmd.Flags().StringVar(&filter.Form, "form", "", "filter by capability form")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by endpoint status")
	listCmd.Flags().StringVar(&filter.Risk, "risk", "", "filter by risk level")
	listCmd.Flags().IntVar(&filter.AuthorizationLevel, "authorization-level", 0, "filter by execution authorization level")
	cmd.AddCommand(listCmd)

	searchInput := capabilities.CapabilitySearchInput{}
	searchCmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search registered capability endpoints",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			input := searchInput
			input.Query = args[0]
			envelope, err := client.SearchCapabilities(ctx, correlationID, input)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not search capabilities.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderCapabilityCandidates(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	searchCmd.Flags().IntVar(&searchInput.Limit, "limit", 10, "maximum number of candidates to return")
	searchCmd.Flags().StringVar(&searchInput.ProviderRef, "provider", "", "filter by provider ID, key, or address")
	searchCmd.Flags().StringVar(&searchInput.ScopeRef, "scope", "", "filter by scope ID, key, or slug")
	searchCmd.Flags().StringVar(&searchInput.Form, "form", "", "filter by capability form")
	searchCmd.Flags().StringVar(&searchInput.Status, "status", "", "filter by endpoint status")
	cmd.AddCommand(searchCmd)
	return cmd
}

func newCapabilityCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "capability",
		Short: "Inspect LOOM capabilities",
	}
	addCapabilityCallCommand(cmd, opts)
	addCapabilityRuntimeBindingCommands(cmd, opts)
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <capability-ref>",
		Short: "Inspect a capability by endpoint ID or compact address",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetCapability(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect capability.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderCapabilityInspection(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "usage-docs <capability-ref>",
		Short: "Print usage documents for a capability",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetCapabilityUsageDocs(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect capability usage docs.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderUsageDocuments(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func addCapabilityRuntimeBindingCommands(cmd *cobra.Command, opts *options) {
	bindingCmd := &cobra.Command{
		Use:   "runtime-binding",
		Short: "Register and inspect endpoint runtime bindings",
	}

	registerInput := capabilities.RegisterRuntimeBindingInput{}
	var configJSON, inputMappingJSON, outputMappingJSON, metadataJSON string
	registerCmd := &cobra.Command{
		Use:   "register <endpoint-version-ref>",
		Short: "Register or update a runtime binding for an endpoint version",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			input := registerInput
			input.EndpointVersionRef = args[0]
			input.RuntimeConfigJSON, err = jsonObjectText(configJSON, "config-json")
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("request.invalid_json", "capabilities", "config-json", "Runtime config must be a JSON object.", err))
			}
			input.InputMappingJSON, err = jsonObjectText(inputMappingJSON, "input-mapping-json")
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("request.invalid_json", "capabilities", "input-mapping-json", "Input mapping must be a JSON object.", err))
			}
			input.OutputMappingJSON, err = jsonObjectText(outputMappingJSON, "output-mapping-json")
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("request.invalid_json", "capabilities", "output-mapping-json", "Output mapping must be a JSON object.", err))
			}
			input.Metadata, err = jsonObjectText(metadataJSON, "metadata-json")
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("request.invalid_json", "capabilities", "metadata-json", "Metadata must be a JSON object.", err))
			}

			envelope, err := client.RegisterRuntimeBinding(ctx, correlationID, input)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not register runtime binding.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderRuntimeBindingInspection(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	registerCmd.Flags().StringVar(&registerInput.RuntimeKind, "kind", "", "runtime kind")
	registerCmd.Flags().StringVar(&registerInput.Status, "status", capabilities.RuntimeBindingStatusRegistered, "runtime binding status")
	registerCmd.Flags().StringVar(&configJSON, "config-json", "{}", "runtime config JSON object")
	registerCmd.Flags().StringVar(&inputMappingJSON, "input-mapping-json", "{}", "input mapping JSON object")
	registerCmd.Flags().StringVar(&outputMappingJSON, "output-mapping-json", "{}", "output mapping JSON object")
	registerCmd.Flags().StringVar(&metadataJSON, "metadata-json", "{}", "metadata JSON object")
	_ = registerCmd.MarkFlagRequired("kind")
	bindingCmd.AddCommand(registerCmd)

	bindingCmd.AddCommand(&cobra.Command{
		Use:   "inspect <binding-ref>",
		Short: "Inspect a runtime binding by binding ID, endpoint version, or capability",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetRuntimeBinding(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect runtime binding.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderRuntimeBindingInspection(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	bindingCmd.AddCommand(&cobra.Command{
		Use:   "validate <binding-ref>",
		Short: "Validate a runtime binding config",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ValidateRuntimeBinding(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not validate runtime binding.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderRuntimeBindingValidation(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	var testInputFile string
	var allowInactive bool
	testCmd := &cobra.Command{
		Use:   "test <binding-ref>",
		Short: "Execute a runtime binding directly for diagnostics",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			inputJSON, err := readCapabilityInput("", testInputFile)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("request.invalid_json", "capabilities", "input-file", "Runtime binding test input must be a JSON object.", err))
			}
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.TestRuntimeBinding(ctx, correlationID, args[0], routing.RuntimeBindingTestInput{
				Input:         inputJSON,
				AllowInactive: allowInactive,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not test runtime binding.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderRuntimeBindingTest(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	testCmd.Flags().StringVar(&testInputFile, "input-file", "", "path to runtime input JSON object")
	testCmd.Flags().BoolVar(&allowInactive, "allow-inactive", false, "allow testing inactive runtime bindings")
	bindingCmd.AddCommand(testCmd)

	bindingsCmd := &cobra.Command{
		Use:   "runtime-bindings",
		Short: "List endpoint runtime bindings",
	}
	filter := capabilities.RuntimeBindingFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List endpoint runtime bindings",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListRuntimeBindings(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list runtime bindings.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderRuntimeBindingList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of runtime bindings to return")
	listCmd.Flags().StringVar(&filter.CapabilityRef, "capability", "", "filter by capability endpoint ID or address")
	listCmd.Flags().StringVar(&filter.EndpointVersionRef, "endpoint-version", "", "filter by endpoint version ID or active capability")
	listCmd.Flags().StringVar(&filter.ProviderRef, "provider", "", "filter by provider ID, key, or address")
	listCmd.Flags().StringVar(&filter.RuntimeKind, "runtime-kind", "", "filter by runtime kind")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by runtime binding status")
	bindingsCmd.AddCommand(listCmd)

	cmd.AddCommand(bindingCmd)
	cmd.AddCommand(bindingsCmd)
}

func renderProviderList(cmd *cobra.Command, providerList []capabilities.ProviderListItem) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ADDRESS\tTYPE\tSTATUS\tHEALTH\tNODE")
	for _, provider := range providerList {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\n",
			provider.CompactAddress,
			provider.ProviderType,
			provider.Status,
			provider.HealthStatus,
			provider.NodeID,
		)
	}
	_ = writer.Flush()
}

func renderProviderInspection(cmd *cobra.Command, detail capabilities.ProviderInspection) {
	provider := detail.Provider
	fmt.Fprintf(cmd.OutOrStdout(), "Provider: %s\n", provider.CompactAddress)
	fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\n", provider.ProviderID)
	fmt.Fprintf(cmd.OutOrStdout(), "Key: %s\n", provider.ProviderKey)
	fmt.Fprintf(cmd.OutOrStdout(), "Type: %s\n", provider.ProviderType)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", provider.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Node: %s\n", provider.NodeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Scope: %s\n", provider.ScopeID)
	if detail.Health != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Health: %s (%s)\n", detail.Health.HealthStatus, detail.Health.AvailabilityStatus)
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Health: -")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Endpoints: %d\n", len(detail.Endpoints))
	fmt.Fprintf(cmd.OutOrStdout(), "Usage docs: %d\n", len(detail.UsageDocuments))
}

func renderProviderHealth(cmd *cobra.Command, health capabilities.ProviderHealth) {
	fmt.Fprintf(cmd.OutOrStdout(), "Provider: %s\n", health.ProviderID)
	fmt.Fprintf(cmd.OutOrStdout(), "Health: %s\n", health.HealthStatus)
	fmt.Fprintf(cmd.OutOrStdout(), "Availability: %s\n", health.AvailabilityStatus)
	fmt.Fprintf(cmd.OutOrStdout(), "Checked: %s\n", timePtrOrDash(health.LastCheckedAt))
	fmt.Fprintf(cmd.OutOrStdout(), "Last OK: %s\n", timePtrOrDash(health.LastOKAt))
	if strings.TrimSpace(health.Message) != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Message: %s\n", health.Message)
	}
}

func renderProviderAdvertisementList(cmd *cobra.Command, advertisements []capabilities.ProviderAdvertisement) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ADVERTISEMENT\tSTATUS\tNODE\tPROVIDER\tHASH\tRECEIVED")
	for _, ad := range advertisements {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			ad.ProviderAdvertisementID,
			ad.Status,
			ad.OriginNodeID,
			ptrOrDash(ad.ProviderID),
			ad.AdvertisementHash,
			ad.ReceivedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderProviderAdvertisementInspection(cmd *cobra.Command, detail capabilities.ProviderAdvertisementInspection) {
	ad := detail.Advertisement
	fmt.Fprintf(cmd.OutOrStdout(), "Provider advertisement: %s\n", ad.ProviderAdvertisementID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", ad.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Origin node: %s\n", ad.OriginNodeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Provider: %s\n", ptrOrDash(ad.ProviderID))
	fmt.Fprintf(cmd.OutOrStdout(), "Communication message: %s\n", ptrOrDash(ad.CommunicationMessageID))
	fmt.Fprintf(cmd.OutOrStdout(), "Hash: %s\n", ad.AdvertisementHash)
	fmt.Fprintf(cmd.OutOrStdout(), "Received: %s\n", ad.ReceivedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Validated: %s\n", timePtrOrDash(ad.ValidatedAt))
	fmt.Fprintf(cmd.OutOrStdout(), "Reviewed: %s\n", timePtrOrDash(ad.ReviewedAt))
	if strings.TrimSpace(ad.RejectionReason) != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Rejection: %s\n", ad.RejectionReason)
	}
	if detail.Provider != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Provider address: %s\n", detail.Provider.CompactAddress)
		fmt.Fprintf(cmd.OutOrStdout(), "Provider status: %s\n", detail.Provider.Status)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Endpoints: %d\n", len(detail.Endpoints))
	for _, endpoint := range detail.Endpoints {
		fmt.Fprintf(cmd.OutOrStdout(), "- %s [%s]\n", endpoint.CompactAddress, endpoint.Status)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Usage docs: %d\n", len(detail.UsageDocuments))
}

func renderCapabilityList(cmd *cobra.Command, capabilityList []capabilities.CapabilityListItem) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ADDRESS\tCLASS\tFORM\tRISK\tAUTH\tSTATUS\tHEALTH")
	for _, capability := range capabilityList {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			capability.CompactAddress,
			capability.ClassName,
			capability.Form,
			capability.RiskLevel,
			capability.ExecutionAuthorizationLevel,
			capability.Status,
			capability.ProviderHealth,
		)
	}
	_ = writer.Flush()
}

func renderCapabilityInspection(cmd *cobra.Command, detail capabilities.CapabilityInspection) {
	endpoint := detail.Endpoint
	fmt.Fprintf(cmd.OutOrStdout(), "Capability: %s\n", endpoint.CompactAddress)
	fmt.Fprintf(cmd.OutOrStdout(), "Endpoint ID: %s\n", endpoint.CapabilityEndpointID)
	fmt.Fprintf(cmd.OutOrStdout(), "Class: %s.%s (%s)\n", detail.Class.Namespace, detail.Class.Name, detail.Class.CapabilityClassID)
	fmt.Fprintf(cmd.OutOrStdout(), "Provider: %s (%s)\n", detail.Provider.CompactAddress, detail.Provider.ProviderID)
	if detail.ProviderHealth != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Provider health: %s\n", detail.ProviderHealth.HealthStatus)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Form: %s\n", endpoint.Form)
	fmt.Fprintf(cmd.OutOrStdout(), "Risk: %s\n", endpoint.RiskLevel)
	fmt.Fprintf(cmd.OutOrStdout(), "Auth: %d\n", endpoint.ExecutionAuthorizationLevel)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", endpoint.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Input schema: %s\n", compactText(string(endpoint.InputSchemaJSON), 160))
	fmt.Fprintf(cmd.OutOrStdout(), "Output schema: %s\n", compactText(string(endpoint.OutputSchemaJSON), 160))
	fmt.Fprintf(cmd.OutOrStdout(), "Side effects: %s\n", compactText(string(endpoint.SideEffectsJSON), 160))
	fmt.Fprintf(cmd.OutOrStdout(), "Usage docs: %d\n", len(detail.UsageDocuments))
	if detail.ActiveVersion != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Active version: %s (%s)\n", detail.ActiveVersion.CapabilityEndpointVersionID, detail.ActiveVersion.VersionLabel)
	}
	if detail.RuntimeBinding != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Runtime: %s %s\n", detail.RuntimeBinding.RuntimeKind, detail.RuntimeBinding.Status)
		if scriptRef, projectRef := scriptRuntimeBindingRefs(detail.RuntimeBinding.RuntimeConfigJSON); scriptRef != "" || projectRef != "" {
			if scriptRef != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Script: %s\n", scriptRef)
			}
			if projectRef != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Project: %s\n", projectRef)
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Runtime binding: %s [%s/%s]\n", detail.RuntimeBinding.RuntimeBindingID, detail.RuntimeBinding.RuntimeKind, detail.RuntimeBinding.Status)
	}
}

func renderRuntimeBindingList(cmd *cobra.Command, bindings []capabilities.RuntimeBindingInspection) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "BINDING\tKIND\tSTATUS\tCAPABILITY\tENDPOINT_VERSION")
	for _, item := range bindings {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\n",
			item.Binding.RuntimeBindingID,
			item.Binding.RuntimeKind,
			item.Binding.Status,
			item.Endpoint.CompactAddress,
			item.EndpointVersion.CapabilityEndpointVersionID,
		)
	}
	_ = writer.Flush()
}

func renderRuntimeBindingInspection(cmd *cobra.Command, detail capabilities.RuntimeBindingInspection) {
	binding := detail.Binding
	fmt.Fprintf(cmd.OutOrStdout(), "Runtime binding: %s\n", binding.RuntimeBindingID)
	fmt.Fprintf(cmd.OutOrStdout(), "Runtime kind: %s\n", binding.RuntimeKind)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", binding.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Capability: %s\n", detail.Endpoint.CompactAddress)
	fmt.Fprintf(cmd.OutOrStdout(), "Provider: %s (%s)\n", detail.Provider.CompactAddress, detail.Provider.ProviderID)
	fmt.Fprintf(cmd.OutOrStdout(), "Endpoint version: %s (%s)\n", detail.EndpointVersion.CapabilityEndpointVersionID, detail.EndpointVersion.VersionLabel)
	if scriptRef, projectRef := scriptRuntimeBindingRefs(binding.RuntimeConfigJSON); scriptRef != "" || projectRef != "" {
		if scriptRef != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Script: %s\n", scriptRef)
		}
		if projectRef != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Project: %s\n", projectRef)
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Config: %s\n", compactText(string(capabilityruntime.Redact(binding.RuntimeKind, binding.RuntimeConfigJSON)), 160))
	fmt.Fprintf(cmd.OutOrStdout(), "Input mapping: %s\n", compactText(string(binding.InputMappingJSON), 160))
	fmt.Fprintf(cmd.OutOrStdout(), "Output mapping: %s\n", compactText(string(binding.OutputMappingJSON), 160))
}

func renderRuntimeBindingValidation(cmd *cobra.Command, detail routing.RuntimeBindingValidation) {
	fmt.Fprintf(cmd.OutOrStdout(), "Runtime binding: %s\n", detail.Binding.Binding.RuntimeBindingID)
	fmt.Fprintf(cmd.OutOrStdout(), "Runtime kind: %s\n", detail.Binding.Binding.RuntimeKind)
	fmt.Fprintf(cmd.OutOrStdout(), "Capability: %s\n", detail.Binding.Endpoint.CompactAddress)
	fmt.Fprintf(cmd.OutOrStdout(), "Valid: %t\n", detail.Validation.Valid)
	if len(detail.Validation.Errors) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Errors:")
		for _, item := range detail.Validation.Errors {
			fmt.Fprintf(cmd.OutOrStdout(), "- %s %s\n", item.Code, item.Message)
		}
	}
	if len(detail.Validation.Warnings) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Warnings:")
		for _, item := range detail.Validation.Warnings {
			fmt.Fprintf(cmd.OutOrStdout(), "- %s %s\n", item.Code, item.Message)
		}
	}
	if len(detail.RedactedConfig) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Config: %s\n", compactText(string(detail.RedactedConfig), 160))
	}
}

func renderRuntimeBindingTest(cmd *cobra.Command, detail routing.RuntimeBindingTestResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Runtime binding: %s\n", detail.Binding.Binding.RuntimeBindingID)
	fmt.Fprintf(cmd.OutOrStdout(), "Runtime kind: %s\n", detail.Binding.Binding.RuntimeKind)
	fmt.Fprintf(cmd.OutOrStdout(), "Capability: %s\n", detail.Binding.Endpoint.CompactAddress)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", detail.Status)
	if strings.TrimSpace(detail.ErrorCode) != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Error: %s %s\n", detail.ErrorCode, detail.ErrorMessage)
	}
	if len(detail.Result) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Result: %s\n", compactText(string(detail.Result), 240))
	}
	if len(detail.ResultRefs) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Result refs: %s\n", compactText(string(detail.ResultRefs), 240))
	}
}

func scriptRuntimeBindingRefs(raw json.RawMessage) (string, string) {
	var config struct {
		ScriptRef  string `json:"script_ref"`
		ProjectRef string `json:"project_ref"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &config) != nil {
		return "", ""
	}
	return strings.TrimSpace(config.ScriptRef), strings.TrimSpace(config.ProjectRef)
}

func renderUsageDocuments(cmd *cobra.Command, docs []capabilities.UsageDocument) {
	if len(docs) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No usage documents.")
		return
	}
	for i, doc := range docs {
		if i > 0 {
			fmt.Fprintln(cmd.OutOrStdout())
		}
		fmt.Fprintf(cmd.OutOrStdout(), "# %s\n", doc.Title)
		fmt.Fprintf(cmd.OutOrStdout(), "Version: %s\n", doc.VersionLabel)
		fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n\n", doc.ReviewStatus)
		fmt.Fprintln(cmd.OutOrStdout(), doc.Body)
	}
}

func renderCapabilityCandidates(cmd *cobra.Command, candidates []capabilities.CapabilityCandidate) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ADDRESS\tCLASS\tRISK\tAUTH\tHEALTH\tMATCH")
	for _, candidate := range candidates {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%d\t%s\t%s\n",
			candidate.CompactAddress,
			candidate.ClassName,
			candidate.RiskLevel,
			candidate.ExecutionAuthorizationLevel,
			candidate.ProviderHealth,
			compactText(candidate.MatchedUseSummary, 100),
		)
	}
	_ = writer.Flush()
}
