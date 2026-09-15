package portal

import (
	"context"
	"io"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/loomcli/actions"
	"loom.local/loom/internal/loomcli/ui"
	"loom.local/loom/internal/search"
)

type Options struct {
	Mode                 ui.Mode
	Client               Client
	CorrelationID        string
	BoxResolved          *box.Resolved
	UpdateStateDir       string
	In                   io.Reader
	Out                  io.Writer
	Err                  io.Writer
	StartScreen          string
	SearchQuery          string
	DatabaseSearchQuery  string
	PreviewActionID      string
	RunActionID          string
	CommandPreviewInput  string
	CommandRunInput      string
	CommandCompleteInput string
	ConfirmAction        bool
	ExitAfterRender      bool
	NoBootAnimation      bool
	Registry             actions.Registry
	CommandRunner        CommandRunner
	MainTransport        MainTransportKind
	MainTarget           string
}

func Run(ctx context.Context, opts Options) error {
	if opts.Registry.All() == nil {
		opts.Registry = actions.DefaultRegistry()
	}
	snapshotOptions := SnapshotOptions{
		BoxResolved:    opts.BoxResolved,
		UpdateStateDir: opts.UpdateStateDir,
		MainTransport:  opts.MainTransport,
		MainTarget:     opts.MainTarget,
	}
	if opts.ExitAfterRender {
		if opts.CommandPreviewInput != "" {
			snapshot := Snapshot{}
			if opts.Client != nil {
				var err error
				snapshot, err = collectWithTimeout(ctx, opts.Client, opts.CorrelationID, snapshotOptions)
				if err != nil {
					return err
				}
			}
			preview := applyMainAvailabilityToCommandPreview(BuildCommandPreview(opts.CommandPreviewInput), snapshot.MainAvailability)
			_, err := io.WriteString(opts.Out, RenderCommandPreview(opts.Mode, preview, true))
			return err
		}
		if opts.CommandCompleteInput != "" {
			actionCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			var suggestions []CommandSuggestion
			var err error
			if opts.CommandRunner != nil {
				suggestions, err = opts.CommandRunner.Complete(actionCtx, CommandCompletionRequest{
					Input:         opts.CommandCompleteInput,
					Cursor:        len(opts.CommandCompleteInput),
					CorrelationID: opts.CorrelationID,
					CurrentScreen: NormalizeScreen(opts.StartScreen),
				})
			}
			if err != nil || len(suggestions) == 0 {
				suggestions = CompleteCommand(actionCtx, opts.Client, opts.CorrelationID, opts.CommandCompleteInput, len(opts.CommandCompleteInput))
			}
			_, err = io.WriteString(opts.Out, RenderCommandCompletions(opts.Mode, opts.CommandCompleteInput, suggestions, 0))
			return err
		}
		if opts.CommandRunInput != "" {
			snapshot, err := collectWithTimeout(ctx, opts.Client, opts.CorrelationID, snapshotOptions)
			if err != nil {
				return err
			}
			preview := applyMainAvailabilityToCommandPreview(BuildCommandPreview(opts.CommandRunInput), snapshot.MainAvailability)
			actionCtx, cancel := context.WithTimeout(ctx, portalCommandExecutionTimeout(preview.CanonicalTokens))
			defer cancel()
			request := CommandRequest{
				Input:               opts.CommandRunInput,
				Tokens:              append([]string{}, preview.CanonicalTokens...),
				CanonicalTokens:     append([]string{}, preview.CanonicalTokens...),
				Classification:      preview.Classification,
				Confirmed:           opts.ConfirmAction,
				CorrelationID:       opts.CorrelationID,
				CurrentScreen:       NormalizeScreen(opts.StartScreen),
				ExecutionDependency: preview.ExecutionDependency,
				MainAvailability:    snapshot.MainAvailability,
			}
			result, err := ExecutePortalCommand(actionCtx, opts.CommandRunner, request)
			if err != nil {
				return err
			}
			_, err = io.WriteString(opts.Out, RenderCommandResult(opts.Mode, result, true))
			return err
		}
		if opts.RunActionID != "" {
			snapshot, err := collectWithTimeout(ctx, opts.Client, opts.CorrelationID, snapshotOptions)
			if err != nil {
				return err
			}
			resolveCtx, resolveCancel := context.WithTimeout(ctx, 15*time.Second)
			action, err := ResolvePortalAction(resolveCtx, ActionResolver{
				Registry:        opts.Registry,
				Client:          opts.Client,
				CorrelationID:   opts.CorrelationID,
				SnapshotOptions: snapshotOptions,
			}, opts.RunActionID, opts.StartScreen, snapshot)
			resolveCancel()
			if err != nil {
				return err
			}
			actionCtx, cancel := context.WithTimeout(ctx, portalActionExecutionTimeout(action))
			defer cancel()
			result, err := ExecutePortalAction(actionCtx, opts.Client, opts.CorrelationID, action, opts.ConfirmAction, snapshot.MainAvailability)
			if err != nil {
				return err
			}
			_, err = io.WriteString(opts.Out, RenderPortalActionResult(opts.Mode, ActionPanelState{
				Lifecycle:  result.Status,
				Action:     action,
				Result:     result,
				RawDetails: true,
			}))
			return err
		}
		if opts.PreviewActionID != "" {
			snapshot := Snapshot{}
			if opts.Client != nil {
				var err error
				snapshot, err = collectWithTimeout(ctx, opts.Client, opts.CorrelationID, snapshotOptions)
				if err != nil {
					return err
				}
			}
			actionCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			action, err := ResolvePortalAction(actionCtx, ActionResolver{
				Registry:        opts.Registry,
				Client:          opts.Client,
				CorrelationID:   opts.CorrelationID,
				SnapshotOptions: snapshotOptions,
			}, opts.PreviewActionID, opts.StartScreen, snapshot)
			if err != nil {
				return err
			}
			_, err = io.WriteString(opts.Out, RenderPortalActionPreview(opts.Mode, ActionPanelState{Lifecycle: ActionLifecyclePreview, Action: action, RawDetails: true}))
			return err
		}
		if opts.SearchQuery != "" {
			if isCommandModeInput(opts.SearchQuery) {
				actionCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()
				suggestions := []CommandSuggestion{}
				var err error
				if opts.CommandRunner != nil {
					suggestions, err = opts.CommandRunner.Complete(actionCtx, CommandCompletionRequest{
						Input:         opts.SearchQuery,
						Cursor:        len(opts.SearchQuery),
						CorrelationID: opts.CorrelationID,
						CurrentScreen: NormalizeScreen(opts.StartScreen),
					})
				}
				if err != nil || len(suggestions) == 0 {
					suggestions = CompleteCommand(actionCtx, opts.Client, opts.CorrelationID, opts.SearchQuery, len(opts.SearchQuery))
				}
				state := CommandModeState{
					Active:             true,
					Input:              opts.SearchQuery,
					Cursor:             len(opts.SearchQuery),
					Parse:              ParseCommandInput(opts.SearchQuery),
					Preview:            BuildCommandPreview(opts.SearchQuery),
					Suggestions:        suggestions,
					SelectedSuggestion: 0,
					CompletionOpen:     len(suggestions) > 0,
				}
				_, err = io.WriteString(opts.Out, RenderCommandPalette(opts.Mode, state, opts.Mode.TTY.Width, opts.Mode.TTY.Height))
				return err
			}
			searchInput := ParsePortalSearchInput(opts.SearchQuery)
			screen := NormalizeScreen(opts.StartScreen)
			searchState := ScreenStateFromSnapshot(screen, Snapshot{})
			if searchInput.Mode == PortalSearchModeScoped && searchInput.ScopeComplete {
				if scopedScreen := searchScopeScreen(searchInput.Scope); scopedScreen != "" {
					screen = scopedScreen
				}
			}
			if opts.Client != nil && screen != "" && screen != ScreenHome {
				screenCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()
				result := LoadScreen(screenCtx, opts.Client, opts.CorrelationID, screen, Snapshot{}, snapshotOptions)
				if result.Err != nil && result.State.Status == ScreenLoadFailed {
					return result.Err
				}
				searchState = result.State
			}
			results := SearchPortal(opts.Registry, searchState, opts.SearchQuery, 8)
			_, err := io.WriteString(opts.Out, RenderPortalSearch(opts.Mode, opts.SearchQuery, results))
			return err
		}
		if opts.DatabaseSearchQuery != "" {
			actionCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if opts.Client == nil {
				return ErrMissingClientFor("run database search")
			}
			envelope, err := opts.Client.Search(actionCtx, opts.CorrelationID, search.SearchInput{Query: opts.DatabaseSearchQuery, Limit: 10})
			if err != nil {
				return err
			}
			data := DatabaseSearchData{
				Query:     opts.DatabaseSearchQuery,
				ResultSet: envelope.Data,
				Status:    ScreenLoadLoaded,
				LoadedAt:  time.Now().UTC(),
			}
			_, err = io.WriteString(opts.Out, RenderDatabaseSearchWithSelection(opts.Mode, opts.DatabaseSearchQuery, data, 0, opts.Mode.TTY.Width, opts.Mode.TTY.Height))
			return err
		}
		screen := NormalizeScreen(opts.StartScreen)
		snapshot, err := collectWithTimeout(ctx, opts.Client, opts.CorrelationID, snapshotOptions)
		if err != nil {
			return err
		}
		var result ScreenLoadResult
		if screen == ScreenHome || screen == ScreenDoctor || screen == ScreenBox || snapshot.MainAvailability.State == MainAvailabilityOffline {
			result = LoadScreenFromSnapshot(screen, snapshot)
		} else {
			result = LoadScreen(ctx, opts.Client, opts.CorrelationID, screen, snapshot, snapshotOptions)
			if result.Err != nil && result.State.Status == ScreenLoadFailed {
				return result.Err
			}
			snapshot = result.Snapshot
		}
		_, err = io.WriteString(opts.Out, RenderScreenWithState(RenderInput{
			Mode:         opts.Mode,
			HomeSnapshot: snapshot,
			Registry:     opts.Registry,
			Screen:       screen,
			State:        result.State,
			Width:        opts.Mode.TTY.Width,
			Height:       opts.Mode.TTY.Height,
			Logo:         SelectPortalLogoForViewport(opts.Mode.TTY.Width, opts.Mode.TTY.Height, nil),
		}))
		return err
	}

	snapshot, err := collectWithTimeout(ctx, opts.Client, opts.CorrelationID, snapshotOptions)
	if err != nil {
		return err
	}
	model := NewModelWithOptions(ModelOptions{
		Mode:            opts.Mode,
		Client:          opts.Client,
		CorrelationID:   opts.CorrelationID,
		Snapshot:        snapshot,
		Registry:        opts.Registry,
		StartScreen:     opts.StartScreen,
		NoBootAnimation: opts.NoBootAnimation,
		CommandRunner:   opts.CommandRunner,
		SnapshotOptions: snapshotOptions,
	})
	programOptions := []tea.ProgramOption{}
	if opts.In != nil {
		programOptions = append(programOptions, tea.WithInput(opts.In))
	}
	if opts.Out != nil {
		programOptions = append(programOptions, tea.WithOutput(opts.Out))
	}
	if opts.Mode.CanUsePortal() {
		programOptions = append(programOptions, tea.WithAltScreen())
		programOptions = append(programOptions, tea.WithMouseCellMotion())
	}
	program := tea.NewProgram(model, programOptions...)
	_, err = program.Run()
	if err != nil {
		return &StartupError{Cause: err}
	}
	return nil
}

func collectWithTimeout(ctx context.Context, client Client, correlationID string, options ...SnapshotOptions) (Snapshot, error) {
	if client == nil {
		return Snapshot{}, ErrMissingClientFor("collect portal snapshot")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return CollectSnapshot(ctx, client, correlationID, options...)
}
