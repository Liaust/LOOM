package loomcli

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/loomcli/actions"
	"loom.local/loom/internal/loomcli/portal"
	"loom.local/loom/internal/loomcli/ui"
	"loom.local/loom/internal/update"
)

func newEnterCommand(opts *options) *cobra.Command {
	var compact bool
	var startScreen string
	var noBootAnimation bool
	var exitAfterRender bool
	var searchQuery string
	var databaseSearchQuery string
	var previewActionID string
	var runActionID string
	var commandPreviewInput string
	var commandRunInput string
	var commandCompleteInput string
	var confirmAction bool

	cmd := &cobra.Command{
		Use:   "enter",
		Short: "Open the LOOM terminal portal",
		Long: strings.TrimSpace(`
Open the human-facing LOOM terminal portal.

The portal is intentionally separate from the raw CLI contract. JSON, plain, and
noninteractive paths fail cleanly instead of printing prompts, banners, or
terminal control sequences.
`),
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.jsonOutput || opts.plainOutput {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New(
					"portal.machine_mode_unsupported",
					"cli",
					"enter",
					"The terminal portal is not available with --json or --plain.",
				))
			}

			previousCompact := opts.compact
			previousNoAnimation := opts.noAnimation
			opts.compact = compact
			if noBootAnimation {
				opts.noAnimation = true
			}
			defer func() {
				opts.compact = previousCompact
				opts.noAnimation = previousNoAnimation
			}()

			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			if !exitAfterRender && !commandCtx.Mode.CanUsePortal() {
				return renderError(cmd, opts, commandCtx.CorrelationID, ui.UnsupportedPortalError())
			}
			mainTransport, mainTarget := selectedPortalMainTransport(commandCtx.Client)
			boxResolved, boxResolveErr := box.Resolve(box.ResolveInput{
				ConfiguredPath:   commandCtx.Config.BoxPath,
				RuntimeStateRoot: commandCtx.Config.BoxStateRoot,
				ConfigProfile:    commandCtx.Config.BoxProfile,
				NodeID:           commandCtx.Config.NodeID,
				NodeRole:         commandCtx.Config.NodeRole,
			})
			var boxResolvedPtr *box.Resolved
			if boxResolveErr == nil {
				boxResolvedPtr = &boxResolved
			}

			if err := portal.Run(cmd.Context(), portal.Options{
				Mode:                 commandCtx.Mode,
				Client:               commandCtx.Client,
				CorrelationID:        commandCtx.CorrelationID,
				BoxResolved:          boxResolvedPtr,
				UpdateStateDir:       update.DefaultStateDir(commandCtx.Config.DataDir),
				In:                   cmd.InOrStdin(),
				Out:                  cmd.OutOrStdout(),
				Err:                  cmd.ErrOrStderr(),
				StartScreen:          startScreen,
				SearchQuery:          searchQuery,
				DatabaseSearchQuery:  databaseSearchQuery,
				PreviewActionID:      previewActionID,
				RunActionID:          runActionID,
				CommandPreviewInput:  commandPreviewInput,
				CommandRunInput:      commandRunInput,
				CommandCompleteInput: commandCompleteInput,
				ConfirmAction:        confirmAction,
				ExitAfterRender:      exitAfterRender,
				NoBootAnimation:      noBootAnimation,
				Registry:             actions.DefaultRegistry(),
				CommandRunner:        newPortalCommandRunner(opts, commandCtx.Mode, commandCtx.Client),
				MainTransport:        mainTransport,
				MainTarget:           mainTarget,
			}); err != nil {
				if errors.Is(err, portal.ErrActionConfirmationRequired) {
					return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.New(
						"portal.action_confirmation_required",
						"portal",
						"confirmation",
						"Confirm the portal action before execution.",
					))
				}
				if errors.Is(err, portal.ErrCommandConfirmationRequired) {
					return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.New(
						"portal.command_confirmation_required",
						"portal",
						"confirmation",
						"This portal command can change LOOM state. Confirm it before execution.",
					))
				}
				if portal.IsStartupError(err) {
					return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap(
						"portal.start_failed",
						"cli",
						"terminal",
						"Could not start the LOOM terminal portal.",
						err,
					))
				}
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap(
					"portal.operation_failed",
					"portal",
					safeCLITransportTarget(mainTarget),
					"The portal operation could not complete.",
					err,
				))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&compact, "compact", false, "render a denser portal layout")
	cmd.Flags().StringVar(&startScreen, "start", portal.ScreenHome, "initial portal screen ("+portalStartScreenHelp()+")")
	cmd.Flags().BoolVar(&noBootAnimation, "no-boot-animation", false, "skip portal boot animation")
	cmd.Flags().BoolVar(&exitAfterRender, "exit-after-render", false, "render one portal view and exit for smoke tests")
	cmd.Flags().StringVar(&searchQuery, "search", "", "render command palette search results and exit")
	cmd.Flags().StringVar(&databaseSearchQuery, "database-search", "", "render database search results and exit")
	cmd.Flags().StringVar(&previewActionID, "preview-action", "", "render one action preview and exit")
	cmd.Flags().StringVar(&runActionID, "run-action", "", "run one confirmed portal action and exit")
	cmd.Flags().StringVar(&commandPreviewInput, "command-preview", "", "render one portal command preview and exit")
	cmd.Flags().StringVar(&commandRunInput, "command-run", "", "run one portal command and exit")
	cmd.Flags().StringVar(&commandCompleteInput, "command-complete", "", "render portal command completions and exit")
	cmd.Flags().BoolVar(&confirmAction, "confirm", false, "confirm a test-mode portal action")
	_ = cmd.Flags().MarkHidden("search")
	_ = cmd.Flags().MarkHidden("database-search")
	_ = cmd.Flags().MarkHidden("preview-action")
	_ = cmd.Flags().MarkHidden("run-action")
	_ = cmd.Flags().MarkHidden("command-preview")
	_ = cmd.Flags().MarkHidden("command-run")
	_ = cmd.Flags().MarkHidden("command-complete")
	_ = cmd.Flags().MarkHidden("confirm")
	_ = cmd.RegisterFlagCompletionFunc("start", completePortalScreen)
	return cmd
}

func selectedPortalMainTransport(client localclient.Client) (portal.MainTransportKind, string) {
	if target := strings.TrimSpace(client.SocketPath); target != "" {
		return portal.MainTransportUnix, target
	}
	if target := strings.TrimSpace(client.BaseURL); target != "" {
		return portal.MainTransportHTTP, target
	}
	return portal.MainTransportUnknown, ""
}

func completePortalScreen(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	screens := portal.Screens()
	values := make([]string, 0, len(screens))
	for _, screen := range screens {
		values = append(values, screen.ID)
	}
	return values, cobra.ShellCompDirectiveNoFileComp
}

func portalStartScreenHelp() string {
	screens := portal.Screens()
	values := make([]string, 0, len(screens))
	for _, screen := range screens {
		values = append(values, screen.ID)
	}
	return strings.Join(values, ", ")
}
