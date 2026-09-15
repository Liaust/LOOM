package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/modules"
)

func newModulesCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "modules",
		Short: "List LOOM native modules",
	}
	filter := modules.ModuleFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List registered module packages",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.ListModules(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list modules.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderModuleList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", modules.DefaultModuleLimit, "maximum number of modules to return")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by module version status")
	listCmd.Flags().StringVar(&filter.ModuleID, "module-id", "", "filter by module ID")
	listCmd.Flags().StringVar(&filter.ProjectRef, "project", "", "filter by owning project slug or ID")
	cmd.AddCommand(listCmd)
	return cmd
}

func newModuleCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "module",
		Short: "Register and inspect LOOM native modules",
	}
	registerInput := modules.RegisterPackageInput{}
	registerCmd := &cobra.Command{
		Use:   "register <package-path>",
		Short: "Register a module package manifest without installing or exposing it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			registerInput.PackagePath = args[0]
			envelope, err := commandCtx.Client.RegisterModulePackage(ctx, commandCtx.CorrelationID, registerInput)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not register module package.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderModuleRegistration(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.AddCommand(registerCmd)

	installInput := modules.InstallModuleInput{}
	installCmd := &cobra.Command{
		Use:   "install <module-ref>",
		Short: "Install a registered native module on a target node without exposing capabilities",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			installInput.ModuleVersionRef = args[0]
			envelope, err := commandCtx.Client.InstallModule(ctx, commandCtx.CorrelationID, installInput)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not install module.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderModuleInstallation(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	installCmd.Flags().StringVar(&installInput.TargetNodeRef, "node", "main", "target node key or ID")
	installCmd.Flags().StringVar(&installInput.InstallScopeRef, "scope", "system", "install scope key, slug, or ID")
	cmd.AddCommand(installCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <module-ref>",
		Short: "Inspect a module by module ID or module version ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.InspectModule(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not inspect module.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderModuleInspection(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	installationCmd := &cobra.Command{
		Use:   "installation",
		Short: "Inspect module installation records",
	}
	installationCmd.AddCommand(&cobra.Command{
		Use:   "inspect <installation-ref>",
		Short: "Inspect a module installation by installation ID, module version ID, or module ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.InspectModuleInstallation(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not inspect module installation.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderModuleInstallation(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	cmd.AddCommand(installationCmd)

	cmd.AddCommand(&cobra.Command{
		Use:   "enable <installation-ref>",
		Short: "Enable a module installation while keeping its capabilities unexposed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.EnableModuleInstallation(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not enable module installation.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderModuleInstallation(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "disable <installation-ref>",
		Short: "Disable a module installation and its installed provider records",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.DisableModuleInstallation(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not disable module installation.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderModuleInstallation(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "health <installation-ref>",
		Short: "Inspect derived health for a module installation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.InspectModuleInstallationHealth(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not inspect module installation health.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderModuleHealth(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "providers <installation-ref>",
		Short: "List provider rows installed by a module installation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.ListModuleInstallationProviders(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list module installation providers.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderInstalledProviders(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "capabilities <installation-ref>",
		Short: "List disabled capability endpoint rows installed by a module installation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.ListModuleInstallationCapabilities(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list module installation capabilities.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderInstalledCapabilities(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	capabilityCmd := &cobra.Command{
		Use:   "capability",
		Short: "Expose or disable capabilities installed by a module",
	}
	capabilityCmd.AddCommand(&cobra.Command{
		Use:   "expose <installation-ref> <capability-ref>",
		Short: "Explicitly expose one installed module capability to normal LOOM routing",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.ExposeModuleCapability(ctx, commandCtx.CorrelationID, modules.ExposeModuleCapabilityInput{
				InstallationRef: args[0],
				CapabilityRef:   args[1],
			})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not expose module capability.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderModuleCapabilityExposure(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	capabilityCmd.AddCommand(&cobra.Command{
		Use:   "disable <installation-ref> <capability-ref>",
		Short: "Disable one exposed module capability",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.DisableModuleCapability(ctx, commandCtx.CorrelationID, modules.DisableModuleCapabilityInput{
				InstallationRef: args[0],
				CapabilityRef:   args[1],
			})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not disable module capability.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderModuleCapabilityExposure(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	cmd.AddCommand(capabilityCmd)

	backupExportInput := modules.ExportModuleBackupInput{}
	backupExportCmd := &cobra.Command{
		Use:   "backup-export <installation-ref>",
		Short: "Export a LOOM-owned module backup payload",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			backupExportInput.InstallationRef = args[0]
			envelope, err := commandCtx.Client.ExportModuleBackup(ctx, commandCtx.CorrelationID, backupExportInput)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not export module backup.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderModuleBackupExportDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	backupExportCmd.Flags().StringVar(&backupExportInput.Kind, "kind", modules.BackupExportKindManifest, "backup export kind")
	cmd.AddCommand(backupExportCmd)

	backupListFilter := modules.ModuleBackupExportFilter{}
	backupsCmd := &cobra.Command{
		Use:   "backups <installation-ref>",
		Short: "List module backup exports",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			backupListFilter.InstallationRef = args[0]
			envelope, err := commandCtx.Client.ListModuleBackupExports(ctx, commandCtx.CorrelationID, backupListFilter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list module backups.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderModuleBackupExports(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	backupsCmd.Flags().StringVar(&backupListFilter.Kind, "kind", "", "filter by backup export kind")
	backupsCmd.Flags().IntVar(&backupListFilter.Limit, "limit", modules.DefaultModuleLimit, "maximum number of backup exports to return")
	cmd.AddCommand(backupsCmd)

	backupCmd := &cobra.Command{
		Use:   "backup",
		Short: "Inspect module backup exports",
	}
	backupCmd.AddCommand(&cobra.Command{
		Use:   "inspect <module-backup-export-ref>",
		Short: "Inspect one module backup export",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.InspectModuleBackupExport(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not inspect module backup.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderModuleBackupExportDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	cmd.AddCommand(backupCmd)

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Inspect module versions",
	}
	versionCmd.AddCommand(&cobra.Command{
		Use:   "inspect <module-version-ref>",
		Short: "Inspect a module version by version ID, module ID, or module@version",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.InspectModuleVersion(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not inspect module version.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderModuleVersionInspection(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	cmd.AddCommand(versionCmd)
	return cmd
}

func renderModuleList(cmd *cobra.Command, moduleList []modules.ModuleListItem) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "MODULE\tVERSION\tSTATUS\tKIND\tVERSIONS\tREGISTERED")
	for _, module := range moduleList {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%d\t%s\n",
			module.ModuleID,
			module.LatestVersion,
			module.Status,
			module.ModuleKind,
			module.VersionCount,
			module.RegisteredAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderModuleRegistration(cmd *cobra.Command, registration modules.ModuleRegistration) {
	fmt.Fprintf(cmd.OutOrStdout(), "Module: %s\n", registration.Version.ModuleID)
	fmt.Fprintf(cmd.OutOrStdout(), "Version: %s (%s)\n", registration.Version.Version, registration.Version.ModuleVersionID)
	fmt.Fprintf(cmd.OutOrStdout(), "Package: %s\n", registration.Package.ModulePackageID)
	fmt.Fprintf(cmd.OutOrStdout(), "Manifest hash: %s\n", registration.Version.ManifestHash)
	fmt.Fprintf(cmd.OutOrStdout(), "Providers declared: %d\n", len(registration.Providers))
	fmt.Fprintf(cmd.OutOrStdout(), "Capabilities declared: %d\n", len(registration.Capabilities))
	fmt.Fprintf(cmd.OutOrStdout(), "Usage docs declared: %d\n", len(registration.UsageDocuments))
	fmt.Fprintf(cmd.OutOrStdout(), "Backup hooks declared: %d\n", len(registration.BackupHooks))
	if registration.Idempotent {
		fmt.Fprintln(cmd.OutOrStdout(), "Result: already registered")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Result: registered")
	}
}

func renderModuleInspection(cmd *cobra.Command, detail modules.ModuleInspection) {
	module := detail.Module
	fmt.Fprintf(cmd.OutOrStdout(), "Module: %s\n", module.ModuleID)
	fmt.Fprintf(cmd.OutOrStdout(), "Name: %s\n", module.ModuleName)
	fmt.Fprintf(cmd.OutOrStdout(), "Latest version: %s (%s)\n", module.LatestVersion, module.LatestVersionID)
	fmt.Fprintf(cmd.OutOrStdout(), "Kind: %s\n", module.ModuleKind)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", module.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Versions: %d\n", len(detail.Versions))
	fmt.Fprintf(cmd.OutOrStdout(), "Providers declared: %d\n", len(detail.Providers))
	fmt.Fprintf(cmd.OutOrStdout(), "Capabilities declared: %d\n", len(detail.Capabilities))
	fmt.Fprintf(cmd.OutOrStdout(), "Usage docs declared: %d\n", len(detail.UsageDocuments))
	fmt.Fprintf(cmd.OutOrStdout(), "Backup hooks declared: %d\n", len(detail.BackupHooks))
}

func renderModuleVersionInspection(cmd *cobra.Command, detail modules.ModuleVersionInspection) {
	version := detail.Version
	fmt.Fprintf(cmd.OutOrStdout(), "Module version: %s\n", version.ModuleVersionID)
	fmt.Fprintf(cmd.OutOrStdout(), "Module: %s\n", version.ModuleID)
	fmt.Fprintf(cmd.OutOrStdout(), "Name: %s\n", version.ModuleName)
	fmt.Fprintf(cmd.OutOrStdout(), "Version: %s\n", version.Version)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", version.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Package: %s\n", detail.Package.ModulePackageID)
	fmt.Fprintf(cmd.OutOrStdout(), "Manifest hash: %s\n", version.ManifestHash)
	fmt.Fprintf(cmd.OutOrStdout(), "Requirements: %d\n", len(detail.Requirements))
	fmt.Fprintf(cmd.OutOrStdout(), "Object types declared: %d\n", len(detail.ObjectTypes))
	fmt.Fprintf(cmd.OutOrStdout(), "Providers declared: %d\n", len(detail.Providers))
	fmt.Fprintf(cmd.OutOrStdout(), "Capabilities declared: %d\n", len(detail.Capabilities))
	fmt.Fprintf(cmd.OutOrStdout(), "Usage docs declared: %d\n", len(detail.UsageDocuments))
	fmt.Fprintf(cmd.OutOrStdout(), "Backup hooks declared: %d\n", len(detail.BackupHooks))
}

func renderModuleInstallation(cmd *cobra.Command, detail modules.ModuleInstallationDetail) {
	installation := detail.Installation
	fmt.Fprintf(cmd.OutOrStdout(), "Module installation: %s\n", installation.ModuleInstallationID)
	fmt.Fprintf(cmd.OutOrStdout(), "Module: %s\n", detail.Version.ModuleID)
	fmt.Fprintf(cmd.OutOrStdout(), "Version: %s (%s)\n", detail.Version.Version, detail.Version.ModuleVersionID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", installation.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Target node: %s\n", installation.TargetNodeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Install scope: %s\n", installation.InstallScopeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Providers installed: %d\n", len(detail.Providers))
	fmt.Fprintf(cmd.OutOrStdout(), "Capabilities installed: %d\n", len(detail.Capabilities))
	if detail.Health != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Health: %s\n", detail.Health.HealthStatus)
	}
	if detail.Idempotent {
		fmt.Fprintln(cmd.OutOrStdout(), "Result: already installed")
	}
}

func renderModuleHealth(cmd *cobra.Command, detail modules.ModuleHealthDetail) {
	fmt.Fprintf(cmd.OutOrStdout(), "Module installation: %s\n", detail.Installation.ModuleInstallationID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", detail.Installation.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Health: %s\n", detail.Health.HealthStatus)
	fmt.Fprintf(cmd.OutOrStdout(), "Target node status: %s\n", detail.Health.TargetNodeStatus)
	fmt.Fprintf(cmd.OutOrStdout(), "Providers: %d\n", detail.Health.ProviderCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Capabilities: %d\n", detail.Health.CapabilityCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Namespaces: %d\n", detail.Health.NamespaceCount)
}

func renderInstalledProviders(cmd *cobra.Command, providers []modules.InstalledProvider) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "PROVIDER\tSTATUS\tEXPOSURE\tHEALTH")
	for _, provider := range providers {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", provider.CompactAddress, provider.Status, provider.ExposureStatus, provider.HealthStatus)
	}
	_ = writer.Flush()
}

func renderInstalledCapabilities(cmd *cobra.Command, capabilityList []modules.InstalledCapability) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "CAPABILITY\tSTATUS\tVERSION_STATUS\tEXPOSURE\tAUTH")
	for _, capability := range capabilityList {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%d\n",
			capability.CompactAddress,
			capability.Status,
			capability.VersionStatus,
			capability.ExposureStatus,
			capability.ExecutionAuthorizationLevel,
		)
	}
	_ = writer.Flush()
}

func renderModuleCapabilityExposure(cmd *cobra.Command, result modules.ModuleCapabilityExposureResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Module installation: %s\n", result.Installation.ModuleInstallationID)
	fmt.Fprintf(cmd.OutOrStdout(), "Module: %s\n", result.Version.ModuleID)
	fmt.Fprintf(cmd.OutOrStdout(), "Capability: %s\n", result.Capability.CompactAddress)
	fmt.Fprintf(cmd.OutOrStdout(), "Provider: %s\n", result.Provider.CompactAddress)
	fmt.Fprintf(cmd.OutOrStdout(), "Endpoint status: %s\n", result.Capability.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Version status: %s\n", result.Capability.VersionStatus)
	fmt.Fprintf(cmd.OutOrStdout(), "Exposure: %s\n", result.Capability.ExposureStatus)
	if result.Health != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Health: %s\n", result.Health.HealthStatus)
	}
	if result.Message != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Result: %s\n", result.Message)
	}
}

func renderModuleBackupExports(cmd *cobra.Command, exports []modules.ModuleBackupExport) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "BACKUP_EXPORT\tKIND\tSTATUS\tHASH\tCREATED")
	for _, export := range exports {
		fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\n",
			export.ModuleBackupExportID,
			export.ExportKind,
			export.ExportStatus,
			export.PayloadHash,
			export.CreatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderModuleBackupExportDetail(cmd *cobra.Command, detail modules.ModuleBackupExportDetail) {
	export := detail.Export
	fmt.Fprintf(cmd.OutOrStdout(), "Module backup export: %s\n", export.ModuleBackupExportID)
	fmt.Fprintf(cmd.OutOrStdout(), "Module installation: %s\n", export.ModuleInstallationID)
	fmt.Fprintf(cmd.OutOrStdout(), "Module: %s\n", detail.Version.ModuleID)
	fmt.Fprintf(cmd.OutOrStdout(), "Version: %s (%s)\n", detail.Version.Version, detail.Version.ModuleVersionID)
	fmt.Fprintf(cmd.OutOrStdout(), "Kind: %s\n", export.ExportKind)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", export.ExportStatus)
	fmt.Fprintf(cmd.OutOrStdout(), "Payload hash: %s\n", export.PayloadHash)
	if export.StorageURI != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Storage URI: %s\n", export.StorageURI)
	}
}
