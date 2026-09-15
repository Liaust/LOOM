package loomcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/version"
)

type options struct {
	configFile    string
	socketPath    string
	correlationID string
	jsonOutput    bool
	plainOutput   bool
	verboseOutput bool
	noInteractive bool
	noColor       bool
	noAnimation   bool
	theme         string
	compact       bool
}

func NewRootCommand() *cobra.Command {
	return newRootCommand(&options{})
}

func newRootCommand(opts *options) *cobra.Command {
	var versionFlag bool

	cmd := &cobra.Command{
		Use:           "loom",
		Short:         "Operate LOOM",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if versionFlag {
				return renderVersion(cmd, opts)
			}
			return cmd.Help()
		},
	}

	cmd.Flags().BoolVar(&versionFlag, "version", false, "print CLI version")
	cmd.PersistentFlags().StringVar(&opts.configFile, "config", "", "path to LOOM config file")
	cmd.PersistentFlags().StringVar(&opts.socketPath, "socket", "", "Unix socket path")
	cmd.PersistentFlags().StringVar(&opts.correlationID, "correlation-id", "", "request correlation id")
	cmd.PersistentFlags().BoolVar(&opts.jsonOutput, "json", false, "print machine-readable JSON")
	cmd.PersistentFlags().BoolVar(&opts.plainOutput, "plain", false, "print plain script-friendly output where supported")
	cmd.PersistentFlags().BoolVar(&opts.verboseOutput, "verbose", false, "print expanded human-readable output")
	cmd.PersistentFlags().BoolVar(&opts.noInteractive, "no-interactive", false, "disable prompts and interactive selection")
	cmd.PersistentFlags().BoolVar(&opts.noColor, "no-color", false, "disable colored output")
	cmd.PersistentFlags().BoolVar(&opts.noAnimation, "no-animation", false, "disable spinners and animations")
	cmd.PersistentFlags().StringVar(&opts.theme, "theme", "", "CLI theme name")

	cmd.AddCommand(newHealthCommand(opts))
	cmd.AddCommand(newStatusCommand(opts))
	cmd.AddCommand(newEnterCommand(opts))
	cmd.AddCommand(newDocsCommand(opts))
	cmd.AddCommand(newSetupCommand(opts))
	cmd.AddCommand(newBootstrapCommand(opts))
	cmd.AddCommand(newBoxCommand(opts))
	cmd.AddCommand(newAgentCommand(opts))
	cmd.AddCommand(newActorCommand(opts))
	cmd.AddCommand(newNodeCommand(opts))
	cmd.AddCommand(newScopeCommand(opts))
	cmd.AddCommand(newProjectCommand(opts))
	cmd.AddCommand(newRepositoryStateCommand(opts))
	cmd.AddCommand(newObjectCommand(opts))
	cmd.AddCommand(newSearchCommand(opts))
	cmd.AddCommand(newStorageCommand(opts))
	cmd.AddCommand(newNotesCommand(opts))
	cmd.AddCommand(newProvenanceCommand(opts))
	cmd.AddCommand(newIndexCommand(opts))
	cmd.AddCommand(newIndexesCommand(opts))
	cmd.AddCommand(newScriptCommand(opts))
	cmd.AddCommand(newScriptsCommand(opts))
	cmd.AddCommand(newJobCommand(opts))
	cmd.AddCommand(newJobsCommand(opts))
	cmd.AddCommand(newRunnerCommand(opts))
	cmd.AddCommand(newRunnersCommand(opts))
	cmd.AddCommand(newArtifactCommand(opts))
	cmd.AddCommand(newArtifactsCommand(opts))
	cmd.AddCommand(newAutomationsCommand(opts))
	cmd.AddCommand(newAutomationCommand(opts))
	cmd.AddCommand(newIntegrationsCommand(opts))
	cmd.AddCommand(newIntegrationCommand(opts))
	cmd.AddCommand(newDirectEventsCommand(opts))
	cmd.AddCommand(newDirectEventCommand(opts))
	cmd.AddCommand(newSchedulesCommand(opts))
	cmd.AddCommand(newScheduleCommand(opts))
	cmd.AddCommand(newInvocationsCommand(opts))
	cmd.AddCommand(newInvocationCommand(opts))
	cmd.AddCommand(newProviderAdvertisementsCommand(opts))
	cmd.AddCommand(newProviderAdvertisementCommand(opts))
	cmd.AddCommand(newProviderCommand(opts))
	cmd.AddCommand(newProvidersCommand(opts))
	cmd.AddCommand(newCapabilityCommand(opts))
	cmd.AddCommand(newCapabilitiesCommand(opts))
	cmd.AddCommand(newServicesCommand(opts))
	cmd.AddCommand(newServiceCommand(opts))
	cmd.AddCommand(newModuleCommand(opts))
	cmd.AddCommand(newModulesCommand(opts))
	cmd.AddCommand(newPolicyCommand(opts))
	cmd.AddCommand(newApprovalsCommand(opts))
	cmd.AddCommand(newApprovalCommand(opts))
	cmd.AddCommand(newGrantsCommand(opts))
	cmd.AddCommand(newGrantCommand(opts))
	cmd.AddCommand(newRoutesCommand(opts))
	cmd.AddCommand(newRouteCommand(opts))
	cmd.AddCommand(newCapabilityCallsCommand(opts))
	cmd.AddCommand(newCapabilityCallCommand(opts))
	cmd.AddCommand(newCommunicationCommand(opts))
	cmd.AddCommand(newMessagesCommand(opts))
	cmd.AddCommand(newMessageCommand(opts))
	cmd.AddCommand(newRealtimeCommand(opts))
	cmd.AddCommand(newSyncCommand(opts))
	cmd.AddCommand(newWatchedRootsCommand(opts))
	cmd.AddCommand(newSecurityCommand(opts))
	cmd.AddCommand(newEventsCommand(opts))
	cmd.AddCommand(newEventCommand(opts))
	cmd.AddCommand(newWorkersCommand(opts))
	cmd.AddCommand(newWorkerCommand(opts))
	cmd.AddCommand(newBackupCommand(opts))
	cmd.AddCommand(newCloudCommand(opts))
	cmd.AddCommand(newSupportCommand(opts))
	cmd.AddCommand(newLaneCommand(opts))
	cmd.AddCommand(newIgnoreCommand(opts))
	cmd.AddCommand(newDatabaseCommand(opts))
	cmd.AddCommand(newUpdateCommand(opts))
	cmd.AddCommand(newMaintenanceCommand(opts))
	cmd.AddCommand(newCompletionCommand(opts))
	cmd.AddCommand(newVersionCommand(opts))

	return cmd
}

func newHealthCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Show LOOM daemon health",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.Health(ctx, commandCtx.CorrelationID)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not reach local loomd.", err))
			}

			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}

			report := envelope.Data
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), report.Status)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "LOOM daemon: %s\n", report.Status)
			fmt.Fprintf(cmd.OutOrStdout(), "Node: %s (%s)\n", report.Node.ID, report.Node.Role)
			fmt.Fprintf(cmd.OutOrStdout(), "Database: %s\n", report.Checks.Database.Status)
			fmt.Fprintf(cmd.OutOrStdout(), "Storage: %s\n", report.Checks.Storage.Status)
			fmt.Fprintf(cmd.OutOrStdout(), "Migrations: %s\n", report.Checks.Migrations.Status)
			fmt.Fprintf(cmd.OutOrStdout(), "Bootstrap: %s\n", report.Checks.Bootstrap.Status)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
}

func newStatusCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show concise LOOM system status",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.Status(ctx, commandCtx.CorrelationID)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not reach local loomd status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			report := envelope.Data
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), report.Status)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "LOOM: %s\n", report.Status)
			fmt.Fprintf(cmd.OutOrStdout(), "Node: %s (%s)\n", report.Health.Node.ID, report.Health.Node.Role)
			fmt.Fprintf(cmd.OutOrStdout(), "Database: %s\n", report.Health.Checks.Database.Status)
			fmt.Fprintf(cmd.OutOrStdout(), "Storage: %s\n", report.Health.Checks.Storage.Status)
			fmt.Fprintf(cmd.OutOrStdout(), "Migrations: %s\n", report.Health.Checks.Migrations.Status)
			fmt.Fprintf(cmd.OutOrStdout(), "Bootstrap: %s\n", report.Health.Checks.Bootstrap.Status)
			fmt.Fprintf(cmd.OutOrStdout(), "Jobs: running=%d failed_recent=%d completed_recent=%d\n", report.Jobs.Running, report.Jobs.FailedRecent, report.Jobs.CompletedRecent)
			fmt.Fprintf(cmd.OutOrStdout(), "Search: failed_recent=%d\n", report.Search.FailedRecent)
			fmt.Fprintf(cmd.OutOrStdout(), "Events: recent=%d\n", report.Events.Recent)
			fmt.Fprintf(cmd.OutOrStdout(), "Runner: %s", report.Runner.Status)
			if report.Runner.RunnerKey != "" {
				fmt.Fprintf(cmd.OutOrStdout(), " (%s)", report.Runner.RunnerKey)
			}
			fmt.Fprintln(cmd.OutOrStdout())
			if opts.verboseOutput && len(report.NextInspection) > 0 {
				for _, next := range report.NextInspection {
					fmt.Fprintf(cmd.OutOrStdout(), "Next: %s\n", next)
				}
			}
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
}

func newVersionCommand(opts *options) *cobra.Command {
	return &cobra.Command{Use: "version", Short: "Print CLI version", RunE: func(cmd *cobra.Command, args []string) error { return renderVersion(cmd, opts) }}
}
func renderVersion(cmd *cobra.Command, opts *options) error {
	if _, err := resolveOutputMode(opts); err != nil {
		return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
	}
	info := version.Current()
	if opts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(info)
	}
	if opts.plainOutput {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), info.Version)
		return err
	}
	if info.Commit != "" || info.BuildDate != "" {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "loom %s commit=%s build_date=%s\n", info.Version, info.Commit, info.BuildDate)
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "loom %s\n", info.Version)
	return err
}

func renderError(cmd *cobra.Command, opts *options, correlationID string, err error) error {
	return renderOwnedCLIError(cmd, err, func(cmd *cobra.Command) error {
		var requestErr *localclient.RequestError
		var envelope response.ErrorEnvelope
		if errors.As(err, &requestErr) {
			correlationID = requestErr.CorrelationID(correlationID)
			err = requestErr.LoomError()
			envelope = requestErr.Envelope
		}
		if opts.jsonOutput {
			if envelope.Error.Code == "" {
				envelope = response.Failure(correlationID, err)
			}
			if hint := cloudServiceContextHint(cmd, envelope, err); hint != "" {
				envelope.Error.Hint = hint
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
		}

		if envelope.Error.Code == "" {
			envelope = response.Failure(correlationID, err)
		}
		errOut := cmd.ErrOrStderr()
		fmt.Fprintf(errOut, "Error: %s: %s\n", envelope.Error.Code, envelope.Error.Summary)
		if opts != nil && opts.verboseOutput {
			if cause := boundedCLIErrorCause(err); cause != "" {
				fmt.Fprintf(errOut, "Cause: %s\n", cause)
			}
		}
		hint := cloudServiceContextHint(cmd, envelope, err)
		if hint == "" {
			hint = envelope.Error.Hint
		}
		if hint = humanCLIErrorHint(hint); hint != "" {
			fmt.Fprintf(errOut, "Hint: %s\n", hint)
		}
		if envelope.Error.Domain != "" {
			fmt.Fprintf(errOut, "Domain: %s\n", envelope.Error.Domain)
		}
		if envelope.Error.Target != "" {
			fmt.Fprintf(errOut, "Target: %s\n", envelope.Error.Target)
		}
		if envelope.Meta.IdempotencyKey != "" {
			fmt.Fprintf(errOut, "Idempotency key: %s\n", envelope.Meta.IdempotencyKey)
		}
		fmt.Fprintf(errOut, "Correlation: %s\n", envelope.Meta.CorrelationID)
		return nil
	})
}

// Human hints are presentation text, not reconstructed advice or sanitized
// source identities. Keep one terminal-safe line, including its label/newline,
// within 512 UTF-8 bytes. JSON retains the original server field unchanged.
func humanCLIErrorHint(value string) string {
	value = strings.ToValidUTF8(value, " ")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	const budget = 512 - len("Hint: \n")
	if len(value) <= budget {
		return value
	}
	end := budget - len("…")
	for !utf8.RuneStart(value[end]) {
		end--
	}
	return strings.TrimSpace(value[:end]) + "…"
}

const maxCLIErrorCauseLength = 240

func boundedCLIErrorCause(err error) string {
	if err == nil {
		return ""
	}
	cause := errors.Unwrap(err)
	if cause == nil {
		return ""
	}
	return boundCLIErrorText(sanitizeCLIErrorText(cause.Error()))
}

func sanitizeCLIErrorText(value string) string {
	fields := strings.Fields(value)
	for index, field := range fields {
		candidate := strings.Trim(field, "\"'()[]{}<>,;:")
		if !strings.Contains(candidate, "://") {
			continue
		}
		safe := safeCLITransportTarget(candidate)
		if safe == "" {
			safe = "[redacted target]"
		}
		fields[index] = strings.Replace(field, candidate, safe, 1)
	}
	return strings.Join(fields, " ")
}

func safeCLITransportTarget(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if !strings.Contains(target, "://") {
		return boundCLIErrorText(target)
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return boundCLIErrorText(parsed.String())
}

func boundCLIErrorText(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxCLIErrorCauseLength {
		return value
	}
	return strings.TrimSpace(string(runes[:maxCLIErrorCauseLength-1])) + "…"
}

func cloudServiceContextHint(cmd *cobra.Command, envelope response.ErrorEnvelope, err error) string {
	if !cloudErrorContext(cmd, envelope, err) {
		return ""
	}
	return cloudstorage.ServiceContextHint(err)
}

func cloudErrorContext(cmd *cobra.Command, envelope response.ErrorEnvelope, err error) bool {
	fields := []string{
		envelope.Error.Code,
		envelope.Error.Domain,
		envelope.Error.Target,
	}
	if cmd != nil {
		fields = append(fields, cmd.CommandPath(), cmd.Use, cmd.Name())
	}
	if err != nil {
		fields = append(fields, err.Error())
	}
	text := strings.ToLower(strings.Join(fields, " "))
	for _, marker := range []string{
		"cloud",
		"rclone",
		"borg",
		"hetzner",
		"storage box",
		"/etc/loom/cloud",
		"/var/lib/loom/cloud",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
