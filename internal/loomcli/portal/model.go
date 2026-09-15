package portal

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"loom.local/loom/internal/loomcli/actions"
	"loom.local/loom/internal/loomcli/ui"
	"loom.local/loom/internal/search"
)

type KeyMap struct {
	Quit         key.Binding
	Search       key.Binding
	Escape       key.Binding
	Help         key.Binding
	Up           key.Binding
	Down         key.Binding
	Enter        key.Binding
	Refresh      key.Binding
	Details      key.Binding
	DBSearch     key.Binding
	Back         key.Binding
	ScrollUp     key.Binding
	ScrollDown   key.Binding
	ScrollTop    key.Binding
	ScrollBottom key.Binding
}

func DefaultKeyMap() KeyMap {
	return KeyMap{
		Quit:         key.NewBinding(key.WithKeys("q", "ctrl+c")),
		Search:       key.NewBinding(key.WithKeys("/")),
		Escape:       key.NewBinding(key.WithKeys("esc")),
		Help:         key.NewBinding(key.WithKeys("?")),
		Up:           key.NewBinding(key.WithKeys("up", "k")),
		Down:         key.NewBinding(key.WithKeys("down", "j")),
		Enter:        key.NewBinding(key.WithKeys("enter")),
		Refresh:      key.NewBinding(key.WithKeys("r")),
		Details:      key.NewBinding(key.WithKeys("tab")),
		DBSearch:     key.NewBinding(key.WithKeys("s")),
		Back:         key.NewBinding(key.WithKeys("h", "backspace")),
		ScrollUp:     key.NewBinding(key.WithKeys("pgup", "ctrl+u")),
		ScrollDown:   key.NewBinding(key.WithKeys("pgdown", "ctrl+d")),
		ScrollTop:    key.NewBinding(key.WithKeys("home")),
		ScrollBottom: key.NewBinding(key.WithKeys("end")),
	}
}

type ModelOptions struct {
	Mode            ui.Mode
	Client          Client
	CorrelationID   string
	Snapshot        Snapshot
	Registry        actions.Registry
	StartScreen     string
	NoBootAnimation bool
	CommandRunner   CommandRunner
	SnapshotOptions SnapshotOptions
}

type Model struct {
	mode             ui.Mode
	client           Client
	correlationID    string
	homeSnapshot     Snapshot
	registry         actions.Registry
	screen           string
	screenStates     map[string]ScreenState
	searchOpen       bool
	searchText       string
	searchIndex      int
	dbSearchOpen     bool
	dbSearchText     string
	dbSearchIndex    int
	notesSearchOpen  bool
	notesSearchText  string
	notesSearchIndex int
	actionPanel      ActionPanelState
	commandMode      CommandModeState
	commandPanel     CommandPanelState
	commandRunner    CommandRunner
	snapshotOptions  SnapshotOptions
	scopedLoading    map[string]bool
	keymap           KeyMap
	help             bool
	width            int
	height           int
	logo             PortalLogo
	boot             BootState
	viewports        map[string]ViewportState
}

const viewportWheelStepLines = 3

func NewModel(mode ui.Mode, snapshot Snapshot, registry actions.Registry, startScreen string) Model {
	return NewModelWithOptions(ModelOptions{
		Mode:        mode,
		Snapshot:    snapshot,
		Registry:    registry,
		StartScreen: startScreen,
	})
}

func NewModelWithOptions(opts ModelOptions) Model {
	screen := NormalizeScreen(opts.StartScreen)
	screenState := ScreenStateFromSnapshot(screen, opts.Snapshot)
	if opts.Client != nil && screen != ScreenHome {
		screenState = screenState.MarkLoading()
	}
	logo := SelectPortalLogoForViewport(opts.Mode.TTY.Width, opts.Mode.TTY.Height, nil)
	boot := BootState{Logo: logo}
	if opts.Mode.CanAnimate() && opts.Mode.CanUsePortal() && !opts.NoBootAnimation {
		boot = newBootState(logo)
	}
	return Model{
		mode:            opts.Mode,
		client:          opts.Client,
		correlationID:   opts.CorrelationID,
		homeSnapshot:    opts.Snapshot,
		registry:        opts.Registry,
		commandRunner:   opts.CommandRunner,
		snapshotOptions: opts.SnapshotOptions,
		scopedLoading:   map[string]bool{},
		screen:          screen,
		screenStates: map[string]ScreenState{
			screen: screenState,
		},
		keymap:    DefaultKeyMap(),
		width:     opts.Mode.TTY.Width,
		height:    opts.Mode.TTY.Height,
		logo:      logo,
		boot:      boot,
		viewports: map[string]ViewportState{},
	}
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{}
	if m.boot.Active {
		cmds = append(cmds, bootTickCmd(m.boot.FrameDelay))
	}
	if m.shouldLoadInitialScreen() {
		cmds = append(cmds, loadScreenCmd(m.client, m.correlationID, m.screen, m.homeSnapshot, m.snapshotOptions))
	}
	if len(cmds) == 1 {
		return cmds[0]
	}
	if len(cmds) > 0 {
		return tea.Batch(cmds...)
	}
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if !m.boot.Active && len(m.logo.Lines) > 0 && (!m.logo.Fits(m.width) || !m.logo.FitsHeight(m.height)) {
			m.logo = SelectPortalLogoForViewport(m.width, m.height, nil)
		}
		m.clampActiveViewport()
		return m, nil
	case bootTickMsg:
		m.boot = m.boot.Advance(msg.At)
		if m.boot.Active {
			return m, bootTickCmd(m.boot.FrameDelay)
		}
		return m, nil
	case screenLoadedMsg:
		result := ScreenLoadResult(msg)
		previousState, ok := m.screenStates[NormalizeScreen(result.Screen)]
		if !ok {
			previousState = ScreenStateFromSnapshot(result.Screen, m.homeSnapshot)
		}
		availabilityRefreshed := mainAvailabilityWasRefreshed(m.homeSnapshot.MainAvailability, result.Snapshot.MainAvailability)
		if availabilityRefreshed {
			m.replaceCachedStatesForAvailability(result.Screen, result.Snapshot)
			m.reconcileOpenPanelsWithAvailability(result.Snapshot.MainAvailability)
		}
		m.homeSnapshot = result.Snapshot
		m.setScreenState(result.Screen, preserveScreenSelection(previousState, result.State))
		if m.scopedLoading != nil {
			m.scopedLoading[NormalizeScreen(result.Screen)] = false
		}
		return m, nil
	case actionExecutedMsg:
		return m.handleActionExecuted(msg)
	case commandExecutedMsg:
		return m.handleCommandExecuted(msg)
	case databaseSearchLoadedMsg:
		return m.handleDatabaseSearchLoaded(msg)
	case notesSearchLoadedMsg:
		return m.handleNotesSearchLoaded(msg)
	case tea.MouseMsg:
		if m.boot.Active {
			return m, nil
		}
		if updated, ok := m.updateViewportMouseScroll(msg); ok {
			return updated, nil
		}
		return m, nil
	}
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	if key.Matches(keyMsg, m.keymap.Quit) {
		return m, tea.Quit
	}
	if m.boot.Active {
		if key.Matches(keyMsg, m.keymap.Enter) {
			m.boot = m.boot.Complete()
		}
		return m, nil
	}
	if updated, ok := m.updateViewportScroll(keyMsg); ok {
		return updated, nil
	}
	if m.actionPanelActive() {
		return m.updateActionPanel(keyMsg)
	}
	if m.commandPanelActive() {
		return m.updateCommandPanel(keyMsg)
	}
	if m.dbSearchOpen {
		return m.updateDatabaseSearch(keyMsg)
	}
	if m.notesSearchOpen {
		return m.updateNotesSearch(keyMsg)
	}
	if m.searchOpen {
		return m.updateSearch(keyMsg)
	}

	switch {
	case key.Matches(keyMsg, m.keymap.Search):
		m.searchOpen = true
		m.searchText = ""
		m.searchIndex = 0
		m = m.resetViewport("search")
		return m, nil
	case key.Matches(keyMsg, m.keymap.DBSearch) && NormalizeScreen(m.screen) == ScreenDatabase:
		m.dbSearchOpen = true
		m.dbSearchText = m.currentScreenState().Data.Database.Search.Query
		m.dbSearchIndex = 0
		m = m.resetViewport("database-search")
		return m, nil
	case key.Matches(keyMsg, m.keymap.DBSearch) && NormalizeScreen(m.screen) == ScreenNotes:
		if m.homeSnapshot.MainAvailability.State == MainAvailabilityOffline || m.currentScreenState().Status == ScreenLoadUnavailable {
			return m, nil
		}
		m.notesSearchOpen = true
		m.notesSearchText = m.currentScreenState().Data.Notes.Search.Query
		m.notesSearchIndex = 0
		m = m.resetViewport("notes-search")
		return m, nil
	case key.Matches(keyMsg, m.keymap.Escape):
		if m.help {
			m.help = false
			return m, nil
		}
		if updated, ok := m.capabilityExplorerBack(); ok {
			return updated, nil
		}
		if updated, ok := m.projectExplorerBack(); ok {
			return updated, nil
		}
		if updated, ok := m.operationalActionGroupBack(); ok {
			return updated, nil
		}
		if NormalizeScreen(m.screen) != ScreenHome {
			m.openScreen(ScreenHome)
			return m, nil
		}
		return m, nil
	case key.Matches(keyMsg, m.keymap.Help):
		m.help = !m.help
		return m, nil
	case key.Matches(keyMsg, m.keymap.Refresh):
		return m.refreshCurrentScreen()
	case key.Matches(keyMsg, m.keymap.Details):
		state := m.currentScreenState().ToggleRawDetails()
		m.setScreenState(m.screen, state)
		return m, nil
	case keyMsg.Type == tea.KeySpace:
		if updated, ok := m.toggleCapabilityExplorerActions(); ok {
			return updated, nil
		}
		if updated, ok := m.toggleProjectExplorerActions(); ok {
			return updated, nil
		}
		return m, nil
	case key.Matches(keyMsg, m.keymap.Back):
		if updated, ok := m.capabilityExplorerBack(); ok {
			return updated, nil
		}
		if updated, ok := m.projectExplorerBack(); ok {
			return updated, nil
		}
		if updated, ok := m.operationalActionGroupBack(); ok {
			return updated, nil
		}
		return m, nil
	case key.Matches(keyMsg, m.keymap.Enter):
		if NormalizeScreen(m.screen) == ScreenHome {
			screens := HomeNavigationScreens()
			if len(screens) == 0 {
				return m, nil
			}
			selected := clampIndex(m.currentScreenState().SelectedIndex, len(screens))
			return m, m.openScreenWithLoad(screens[selected].ID)
		}
		if updated, ok := m.openCapabilityExplorerSelection(); ok {
			return updated, nil
		}
		if updated, cmd, ok := m.openProjectSelection(); ok {
			return updated, cmd
		}
		if updated, ok := m.toggleOperationalActionGroupSelection(); ok {
			return updated, nil
		}
		if action, ok := SelectedPortalAction(m.currentScreenState()); ok {
			m.actionPanel = ActionPanelState{Lifecycle: ActionLifecyclePreview, Action: action}
			return m, nil
		}
		return m, nil
	case key.Matches(keyMsg, m.keymap.Up):
		state := m.currentScreenState().WithSelection(-1, m.currentSelectableCount())
		m.setScreenState(m.screen, state)
		return m, nil
	case key.Matches(keyMsg, m.keymap.Down):
		state := m.currentScreenState().WithSelection(1, m.currentSelectableCount())
		m.setScreenState(m.screen, state)
		return m, nil
	default:
		return m, nil
	}
}

func (m Model) updateSearch(keyMsg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if isCommandModeInput(m.searchText) || (keyMsg.Type == tea.KeyRunes && strings.HasPrefix(strings.TrimLeft(string(keyMsg.Runes), " \t"), "$")) {
		return m.updateCommandSearch(keyMsg)
	}
	results := m.searchResults()
	switch {
	case key.Matches(keyMsg, m.keymap.Escape):
		m.searchOpen = false
		return m, nil
	case keyMsg.Type != tea.KeyRunes && key.Matches(keyMsg, m.keymap.Up):
		m.searchIndex = clampIndex(m.searchIndex-1, len(results))
		m = m.ensureViewportLineVisible("search", portalSearchSelectedBodyLine(results, m.searchIndex))
		return m, nil
	case keyMsg.Type != tea.KeyRunes && key.Matches(keyMsg, m.keymap.Down):
		m.searchIndex = clampIndex(m.searchIndex+1, len(results))
		m = m.ensureViewportLineVisible("search", portalSearchSelectedBodyLine(results, m.searchIndex))
		return m, nil
	case key.Matches(keyMsg, m.keymap.Enter):
		if len(results) == 0 {
			return m, nil
		}
		m.searchIndex = clampIndex(m.searchIndex, len(results))
		action := results[m.searchIndex].Action
		if action.TargetKind == "search_scope" && action.TargetRef != "" {
			m.searchText = "#" + action.TargetRef + " "
			m.searchIndex = 0
			m = m.resetViewport("search")
			return m, m.maybeLoadScopedSearchData()
		}
		m.searchOpen = false
		if action.Executor.Kind == PortalExecutorNavigate && action.Risk == ActionRiskInspect && strings.HasSuffix(action.ID, ".open") {
			return m, m.openScreenWithLoad(action.Executor.Target)
		}
		m.actionPanel = ActionPanelState{Lifecycle: ActionLifecyclePreview, Action: action}
		return m, nil
	default:
		switch keyMsg.Type {
		case tea.KeyBackspace:
			if len(m.searchText) > 0 {
				m.searchText = m.searchText[:len(m.searchText)-1]
			}
			m.searchIndex = 0
		case tea.KeySpace:
			m.searchText += " "
			m.searchIndex = 0
		case tea.KeyRunes:
			m.searchText += string(keyMsg.Runes)
			m.searchIndex = 0
		}
		m = m.resetViewport("search")
		if isCommandModeInput(m.searchText) {
			m = m.resetViewport("command-search")
			m.syncCommandMode(false)
		}
		return m, m.maybeLoadScopedSearchData()
	}
}

func (m Model) updateDatabaseSearch(keyMsg tea.KeyMsg) (tea.Model, tea.Cmd) {
	state := m.currentScreenState()
	searchData := state.Data.Database.Search
	results := searchData.ResultSet.Results
	query := strings.TrimSpace(m.dbSearchText)
	switch {
	case key.Matches(keyMsg, m.keymap.Escape):
		m.dbSearchOpen = false
		return m, nil
	case key.Matches(keyMsg, m.keymap.Up):
		m.dbSearchIndex = clampIndex(m.dbSearchIndex-1, len(results))
		m = m.ensureViewportLineVisible("database-search", databaseSearchSelectedBodyLine(searchData, m.dbSearchIndex))
		return m, nil
	case key.Matches(keyMsg, m.keymap.Down):
		m.dbSearchIndex = clampIndex(m.dbSearchIndex+1, len(results))
		m = m.ensureViewportLineVisible("database-search", databaseSearchSelectedBodyLine(searchData, m.dbSearchIndex))
		return m, nil
	case key.Matches(keyMsg, m.keymap.Enter):
		if query == "" {
			return m, nil
		}
		if searchData.Status == ScreenLoadLoaded && strings.EqualFold(strings.TrimSpace(searchData.Query), query) && len(results) > 0 {
			m.dbSearchIndex = clampIndex(m.dbSearchIndex, len(results))
			action := NewSearchResultInspectAction(results[m.dbSearchIndex])
			if action.Disabled() {
				return m, nil
			}
			m.dbSearchOpen = false
			m.actionPanel = ActionPanelState{Lifecycle: ActionLifecyclePreview, Action: action}
			return m, nil
		}
		searchData = DatabaseSearchData{Query: query, Status: ScreenLoadLoading}
		state.Data.Database.Search = searchData
		m.setScreenState(ScreenDatabase, state)
		m.dbSearchIndex = 0
		m = m.resetViewport("database-search")
		return m, databaseSearchCmd(m.client, m.correlationID, query)
	default:
		switch keyMsg.Type {
		case tea.KeyBackspace:
			if len(m.dbSearchText) > 0 {
				m.dbSearchText = m.dbSearchText[:len(m.dbSearchText)-1]
			}
			m.dbSearchIndex = 0
		case tea.KeySpace:
			m.dbSearchText += " "
			m.dbSearchIndex = 0
		case tea.KeyRunes:
			m.dbSearchText += string(keyMsg.Runes)
			m.dbSearchIndex = 0
		}
		m = m.resetViewport("database-search")
		return m, nil
	}
}

func (m Model) updateNotesSearch(keyMsg tea.KeyMsg) (tea.Model, tea.Cmd) {
	state := m.currentScreenState()
	searchData := state.Data.Notes.Search
	results := searchData.ResultSet.Results
	query := strings.TrimSpace(m.notesSearchText)
	input := notesSearchInputFromQuery(query)
	switch {
	case key.Matches(keyMsg, m.keymap.Escape):
		m.notesSearchOpen = false
		return m, nil
	case keyMsg.Type != tea.KeyRunes && key.Matches(keyMsg, m.keymap.Up):
		m.notesSearchIndex = clampIndex(m.notesSearchIndex-1, len(results))
		m = m.ensureViewportLineVisible("notes-search", notesSearchSelectedBodyLine(searchData, m.notesSearchIndex))
		return m, nil
	case keyMsg.Type != tea.KeyRunes && key.Matches(keyMsg, m.keymap.Down):
		m.notesSearchIndex = clampIndex(m.notesSearchIndex+1, len(results))
		m = m.ensureViewportLineVisible("notes-search", notesSearchSelectedBodyLine(searchData, m.notesSearchIndex))
		return m, nil
	case key.Matches(keyMsg, m.keymap.Enter):
		if input.Query == "" {
			return m, nil
		}
		if m.homeSnapshot.MainAvailability.State == MainAvailabilityOffline || state.Status == ScreenLoadUnavailable {
			return m, nil
		}
		if searchData.Status == ScreenLoadLoaded && notesSearchInputEqual(searchData.Input, input) && len(results) > 0 {
			m.notesSearchIndex = clampIndex(m.notesSearchIndex, len(results))
			action := NewNotesSearchResultInspectAction(results[m.notesSearchIndex])
			if action.Disabled() {
				return m, nil
			}
			m.notesSearchOpen = false
			m.actionPanel = ActionPanelState{Lifecycle: ActionLifecyclePreview, Action: action}
			return m, nil
		}
		searchData = NotesSearchData{Query: query, Input: input, Status: ScreenLoadLoading}
		state.Data.Notes.Search = searchData
		m.setScreenState(ScreenNotes, state)
		m.notesSearchIndex = 0
		m = m.resetViewport("notes-search")
		return m, notesSearchCmd(m.client, m.correlationID, query)
	default:
		switch keyMsg.Type {
		case tea.KeyBackspace:
			if len(m.notesSearchText) > 0 {
				m.notesSearchText = m.notesSearchText[:len(m.notesSearchText)-1]
			}
			m.notesSearchIndex = 0
		case tea.KeySpace:
			m.notesSearchText += " "
			m.notesSearchIndex = 0
		case tea.KeyRunes:
			m.notesSearchText += string(keyMsg.Runes)
			m.notesSearchIndex = 0
		}
		m = m.resetViewport("notes-search")
		return m, nil
	}
}

func (m Model) View() string {
	if m.help {
		return m.fitViewFor("help", RenderHelp(m.mode))
	}
	if m.boot.Active {
		return m.fitViewFor("boot", RenderBoot(renderContext{Mode: m.mode, Styles: NewPortalStyles(m.mode), Width: m.width, Height: m.height, Logo: m.logo}, m.boot))
	}
	if m.actionPanelActive() {
		return m.fitViewFor("action:"+string(m.actionPanel.Lifecycle), m.renderActionPanel())
	}
	if m.commandPanelActive() {
		return m.fitViewFor("command-panel:"+string(m.commandPanel.Lifecycle), m.renderCommandPanel())
	}
	if m.searchOpen {
		if isCommandModeInput(m.searchText) {
			m.syncCommandMode(true)
			return m.fitViewFor("command-search", RenderCommandPalette(m.mode, m.commandMode, m.width, m.height))
		}
		return m.fitViewFor("search", RenderPortalSearchWithSelection(m.mode, m.searchText, m.searchResults(), m.searchIndex, m.width, m.height))
	}
	if m.dbSearchOpen {
		return m.fitViewFor("database-search", RenderDatabaseSearchWithSelection(m.mode, m.dbSearchText, m.currentScreenState().Data.Database.Search, m.dbSearchIndex, m.width, m.height))
	}
	if m.notesSearchOpen {
		return m.fitViewFor("notes-search", RenderNotesSearchWithSelection(m.mode, m.notesSearchText, m.currentScreenState().Data.Notes.Search, m.notesSearchIndex, m.width, m.height))
	}
	return m.fitViewFor("screen:"+NormalizeScreen(m.screen), RenderScreenWithState(RenderInput{
		Mode:         m.mode,
		HomeSnapshot: m.homeSnapshot,
		Registry:     m.registry,
		Screen:       m.screen,
		State:        m.currentScreenState(),
		Width:        m.width,
		Height:       m.height,
		Logo:         m.logo,
	}))
}

func (m Model) fitView(output string) string {
	return m.fitViewFor(m.activeViewportKey(), output)
}

func (m Model) fitViewFor(key string, output string) string {
	if !m.mode.CanUsePortal() {
		return output
	}
	if m.viewports == nil {
		return output
	}
	state := m.viewports[key]
	rendered, next := renderPortalViewport(output, m.height, state)
	m.viewports[key] = next
	return rendered
}

func (m Model) updateViewportScroll(keyMsg tea.KeyMsg) (Model, bool) {
	viewportKey := m.activeViewportKey()
	state := m.viewportState(viewportKey)
	page := m.viewportPageSize(state)
	switch {
	case key.Matches(keyMsg, m.keymap.ScrollUp):
		state.Offset -= page
	case key.Matches(keyMsg, m.keymap.ScrollDown):
		state.Offset += page
	case key.Matches(keyMsg, m.keymap.ScrollTop) || m.isViewportTopRune(keyMsg):
		state.Offset = 0
	case key.Matches(keyMsg, m.keymap.ScrollBottom) || m.isViewportBottomRune(keyMsg):
		state.Offset = 1 << 30
	default:
		return m, false
	}
	if state.Offset < 0 {
		state.Offset = 0
	}
	if m.viewports == nil {
		m.viewports = map[string]ViewportState{}
	}
	m.viewports[viewportKey] = state
	return m, true
}

func (m Model) updateViewportMouseScroll(mouseMsg tea.MouseMsg) (Model, bool) {
	viewportKey := m.activeViewportKey()
	state := m.viewportState(viewportKey)
	switch mouseMsg.Type {
	case tea.MouseWheelUp:
		state.Offset -= viewportWheelStepLines
	case tea.MouseWheelDown:
		state.Offset += viewportWheelStepLines
	default:
		return m, false
	}
	if state.Offset < 0 {
		state.Offset = 0
	}
	if m.viewports == nil {
		m.viewports = map[string]ViewportState{}
	}
	m.viewports[viewportKey] = state
	return m, true
}

func (m Model) activeViewportKey() string {
	switch {
	case m.help:
		return "help"
	case m.boot.Active:
		return "boot"
	case m.actionPanelActive():
		return "action:" + string(m.actionPanel.Lifecycle)
	case m.commandPanelActive():
		return "command-panel:" + string(m.commandPanel.Lifecycle)
	case m.searchOpen && isCommandModeInput(m.searchText):
		return "command-search"
	case m.searchOpen:
		return "search"
	case m.dbSearchOpen:
		return "database-search"
	case m.notesSearchOpen:
		return "notes-search"
	default:
		return "screen:" + NormalizeScreen(m.screen)
	}
}

func (m Model) viewportState(key string) ViewportState {
	if m.viewports == nil {
		return ViewportState{}
	}
	return m.viewports[key]
}

func (m Model) viewportPageSize(state ViewportState) int {
	if state.Height > 1 {
		return state.Height
	}
	if m.height > 6 {
		return m.height - 6
	}
	return 1
}

func (m Model) clampActiveViewport() {
	if m.viewports == nil {
		return
	}
	key := m.activeViewportKey()
	state := m.viewports[key]
	state.Offset = clampViewportOffset(state.Offset, state.TotalLines, state.Height)
	m.viewports[key] = state
}

func (m Model) resetViewport(key string) Model {
	if m.viewports != nil {
		delete(m.viewports, key)
	}
	return m
}

func (m Model) ensureViewportLineVisible(key string, line int) Model {
	if line < 0 {
		return m
	}
	state := m.viewportState(key)
	height := state.Height
	if height <= 0 {
		height = m.viewportPageSize(state)
	}
	if height <= 0 {
		height = 1
	}
	if line < state.Offset {
		state.Offset = line
	} else if line >= state.Offset+height {
		state.Offset = line - height + 1
	}
	if state.Offset < 0 {
		state.Offset = 0
	}
	if m.viewports == nil {
		m.viewports = map[string]ViewportState{}
	}
	m.viewports[key] = state
	return m
}

func (m Model) isViewportTopRune(keyMsg tea.KeyMsg) bool {
	if m.textInputActive() || keyMsg.Type != tea.KeyRunes {
		return false
	}
	return string(keyMsg.Runes) == "g"
}

func (m Model) isViewportBottomRune(keyMsg tea.KeyMsg) bool {
	if m.textInputActive() || keyMsg.Type != tea.KeyRunes {
		return false
	}
	return string(keyMsg.Runes) == "G"
}

func (m Model) textInputActive() bool {
	return m.searchOpen || m.dbSearchOpen || m.notesSearchOpen || m.actionPanelEditingInput()
}

func (m Model) updateActionPanel(keyMsg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(keyMsg, m.keymap.Escape):
		switch m.actionPanel.Lifecycle {
		case ActionLifecycleRunning, ActionLifecyclePreflighting, ActionLifecycleMutation:
			return m, nil
		case ActionLifecycleSucceeded, ActionLifecycleFailed, ActionLifecycleCancelled:
			m.actionPanel = ActionPanelState{}
			return m, nil
		default:
			m.actionPanel = ActionPanelState{
				Lifecycle: ActionLifecycleCancelled,
				Action:    m.actionPanel.Action,
				Result: PortalActionResult{
					ActionID: m.actionPanel.Action.ID,
					Title:    m.actionPanel.Action.Label,
					Status:   ActionLifecycleCancelled,
					Summary:  "Action cancelled before execution.",
				},
			}
			return m, nil
		}
	case key.Matches(keyMsg, m.keymap.Details):
		m.actionPanel.RawDetails = !m.actionPanel.RawDetails
		return m, nil
	case m.actionPanelEditingInput() && key.Matches(keyMsg, m.keymap.Up):
		m.actionPanel.SelectedField = clampIndex(m.actionPanel.SelectedField-1, len(m.actionPanel.Action.InputFields))
		return m, nil
	case m.actionPanelEditingInput() && key.Matches(keyMsg, m.keymap.Down):
		m.actionPanel.SelectedField = clampIndex(m.actionPanel.SelectedField+1, len(m.actionPanel.Action.InputFields))
		return m, nil
	case key.Matches(keyMsg, m.keymap.Enter):
		switch m.actionPanel.Lifecycle {
		case ActionLifecyclePreview, ActionLifecycleNeedsInput:
			return m.advanceActionPreview()
		case ActionLifecycleReview:
			if m.actionPanel.Result.Blocking {
				return m, nil
			}
			m.actionPanel.Lifecycle = ActionLifecycleNeedsConfirmation
			return m, nil
		case ActionLifecycleNeedsConfirmation:
			m.actionPanel.Lifecycle = ActionLifecycleRunning
			if m.actionPanel.Action.Executor.Kind == PortalExecutorProtectedFolderProtect {
				m.actionPanel.Lifecycle = ActionLifecycleMutation
			}
			return m, executeActionCmd(m.client, m.correlationID, m.actionPanel.Action, m.homeSnapshot.MainAvailability)
		case ActionLifecycleWaiting:
			if m.actionPanel.Action.Executor.Kind == PortalExecutorProtectedFolderProtect {
				m.actionPanel.Lifecycle = ActionLifecyclePreflighting
				return m, executeActionCmd(m.client, m.correlationID, m.actionPanel.Action, m.homeSnapshot.MainAvailability)
			}
			m.actionPanel = ActionPanelState{}
			return m, nil
		case ActionLifecycleSucceeded, ActionLifecycleFailed, ActionLifecycleCancelled:
			m.actionPanel = ActionPanelState{}
			return m, nil
		}
	case m.actionPanelEditingInput():
		return m.updateActionPanelInput(keyMsg), nil
	}
	return m, nil
}

func (m Model) actionPanelEditingInput() bool {
	if !m.actionPanelActive() || len(m.actionPanel.Action.InputFields) == 0 {
		return false
	}
	switch m.actionPanel.Lifecycle {
	case ActionLifecyclePreview, ActionLifecycleNeedsInput:
		return true
	default:
		return false
	}
}

func (m Model) updateActionPanelInput(keyMsg tea.KeyMsg) Model {
	fields := m.actionPanel.Action.InputFields
	if len(fields) == 0 {
		return m
	}
	selected := clampIndex(m.actionPanel.SelectedField, len(fields))
	m.actionPanel.SelectedField = selected
	field := fields[selected]
	m.actionPanel.Action.EnsureInputValues()
	current := m.actionPanel.Action.FieldValue(field)
	switch keyMsg.Type {
	case tea.KeyBackspace:
		if len(current) > 0 {
			m.actionPanel.Action.SetFieldValue(field.Name, current[:len(current)-1])
		}
	case tea.KeySpace:
		if field.Kind == ActionFieldSelect {
			m.actionPanel.Action.SetFieldValue(field.Name, nextSelectFieldOption(field, current, 1))
		} else if field.Kind == ActionFieldBoolean {
			m.actionPanel.Action.ToggleBooleanField(field.Name)
		} else {
			m.actionPanel.Action.SetFieldValue(field.Name, current+" ")
		}
	case tea.KeyRunes:
		if field.Kind == ActionFieldReadonly {
			return m
		}
		if field.Kind == ActionFieldBoolean {
			raw := strings.ToLower(strings.TrimSpace(string(keyMsg.Runes)))
			switch raw {
			case "t", "y", "1":
				m.actionPanel.Action.SetFieldValue(field.Name, "true")
			case "f", "n", "0":
				m.actionPanel.Action.SetFieldValue(field.Name, "false")
			}
			return m
		}
		if field.Kind == ActionFieldSelect {
			raw := strings.ToLower(strings.TrimSpace(string(keyMsg.Runes)))
			for _, option := range field.Options {
				if strings.HasPrefix(strings.ToLower(option), raw) {
					m.actionPanel.Action.SetFieldValue(field.Name, option)
					return m
				}
			}
			return m
		}
		m.actionPanel.Action.SetFieldValue(field.Name, current+string(keyMsg.Runes))
	}
	return m
}

func nextSelectFieldOption(field PortalActionField, current string, direction int) string {
	if len(field.Options) == 0 {
		return strings.TrimSpace(current)
	}
	current = strings.TrimSpace(current)
	index := -1
	for idx, option := range field.Options {
		if option == current {
			index = idx
			break
		}
	}
	if index < 0 {
		if direction < 0 {
			return field.Options[len(field.Options)-1]
		}
		return field.Options[0]
	}
	next := (index + direction) % len(field.Options)
	if next < 0 {
		next += len(field.Options)
	}
	return field.Options[next]
}

func (m Model) advanceActionPreview() (tea.Model, tea.Cmd) {
	action := applyMainAvailabilityToAction(m.actionPanel.Action, m.homeSnapshot.MainAvailability)
	m.actionPanel.Action = action
	if action.Disabled() {
		m.actionPanel.Lifecycle = ActionLifecycleFailed
		if action.DisabledReason == mainOfflineExecutionReason {
			m.actionPanel.Result = mainOfflineActionResult(action)
		} else {
			m.actionPanel.Result = failedActionResult(action, "portal.action_disabled", firstNonEmpty(action.DisabledReason, "This action is disabled."))
		}
		return m, nil
	}
	if action.Risk == ActionRiskDangerous || action.Risk == ActionRiskBlocked {
		m.actionPanel.Lifecycle = ActionLifecycleFailed
		m.actionPanel.Result = failedActionResult(action, "portal.action_blocked", "This action is not enabled for portal execution in this slice.")
		return m, nil
	}
	if !action.InputValid() {
		m.actionPanel.Lifecycle = ActionLifecycleNeedsInput
		return m, nil
	}
	if action.Interaction() == ActionInteractionWizard && action.Executor.Kind == PortalExecutorProtectedFolderProtect && strings.TrimSpace(action.FieldValueByName("preflight_id")) == "" {
		m.actionPanel.Lifecycle = ActionLifecyclePreflighting
		return m, executeActionCmd(m.client, m.correlationID, action, m.homeSnapshot.MainAvailability)
	}
	if action.RequiresConfirmation() {
		m.actionPanel.Lifecycle = ActionLifecycleNeedsConfirmation
		return m, nil
	}
	if action.Executor.Kind == PortalExecutorNavigate {
		cmd := m.openScreenWithLoad(action.Executor.Target)
		m.actionPanel = ActionPanelState{}
		return m, cmd
	}
	m.actionPanel.Lifecycle = ActionLifecycleRunning
	return m, executeActionCmd(m.client, m.correlationID, action, m.homeSnapshot.MainAvailability)
}

func (m Model) handleActionExecuted(msg actionExecutedMsg) (tea.Model, tea.Cmd) {
	result := msg.Result
	if msg.Err != nil {
		result = PortalActionResult{
			ActionID:     m.actionPanel.Action.ID,
			Title:        m.actionPanel.Action.Label,
			Status:       ActionLifecycleFailed,
			Summary:      "Action failed.",
			ErrorCode:    "portal.action_failed",
			ErrorMessage: msg.Err.Error(),
			RawCommand:   append([]string{}, m.actionPanel.Action.RawCommand...),
		}
	}
	if result.Status == "" {
		result.Status = ActionLifecycleSucceeded
	}
	if len(result.NextInput) > 0 {
		m.actionPanel.Action.EnsureInputValues()
		for name, value := range result.NextInput {
			m.actionPanel.Action.InputValues[name] = value
		}
	}
	m.actionPanel.Lifecycle = result.Status
	m.actionPanel.Result = result
	if result.RefreshScreen != "" && result.Status == ActionLifecycleSucceeded {
		m.commandMode.CompletionCache = nil
		refreshScreen := NormalizeScreen(result.RefreshScreen)
		m.screen = refreshScreen
		state := m.currentScreenState().MarkLoading()
		m.setScreenState(refreshScreen, state)
		return m, loadScreenCmd(m.client, m.correlationID, refreshScreen, m.homeSnapshot, m.snapshotOptions)
	}
	return m, nil
}

func (m Model) renderActionPanel() string {
	switch m.actionPanel.Lifecycle {
	case ActionLifecyclePreview, ActionLifecycleNeedsInput:
		return RenderPortalActionPreview(m.mode, m.actionPanel)
	case ActionLifecycleReview:
		return RenderPortalActionResult(m.mode, m.actionPanel)
	case ActionLifecycleNeedsConfirmation:
		return RenderPortalActionConfirmation(m.mode, m.actionPanel)
	case ActionLifecycleRunning, ActionLifecyclePreflighting, ActionLifecycleMutation:
		return RenderPortalActionRunning(m.mode, m.actionPanel)
	case ActionLifecycleWaiting, ActionLifecycleSucceeded, ActionLifecycleFailed, ActionLifecycleCancelled:
		return RenderPortalActionResult(m.mode, m.actionPanel)
	default:
		return ""
	}
}

func (m Model) actionPanelActive() bool {
	return m.actionPanel.Lifecycle != "" && m.actionPanel.Lifecycle != ActionLifecycleIdle
}

func (m Model) updateCommandSearch(keyMsg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.syncCommandMode(true)
	switch {
	case key.Matches(keyMsg, m.keymap.Escape):
		if m.commandMode.CompletionOpen {
			m.commandMode.CompletionOpen = false
			m.commandMode.Suggestions = nil
			return m, nil
		}
		m.searchOpen = false
		m.commandMode = CommandModeState{}
		return m, nil
	case key.Matches(keyMsg, m.keymap.Details):
		if len(m.commandMode.Suggestions) > 0 {
			m.commandMode.CompletionOpen = true
			m.commandMode.SelectedSuggestion = clampIndex(m.commandMode.SelectedSuggestion+1, len(m.commandMode.Suggestions))
			m = m.ensureViewportLineVisible("command-search", commandCompletionSelectedBodyLine(m.commandMode))
			return m, nil
		}
		m.commandMode.CompletionOpen = true
		m.commandMode.Suggestions = m.commandSuggestions()
		m.commandMode.SelectedSuggestion = 0
		m = m.resetViewport("command-search")
		return m, nil
	case keyMsg.Type != tea.KeyRunes && key.Matches(keyMsg, m.keymap.Up):
		if m.commandMode.CompletionOpen && len(m.commandMode.Suggestions) > 0 {
			m.commandMode.SelectedSuggestion = clampIndex(m.commandMode.SelectedSuggestion-1, len(m.commandMode.Suggestions))
			m = m.ensureViewportLineVisible("command-search", commandCompletionSelectedBodyLine(m.commandMode))
			return m, nil
		}
		if len(m.commandMode.History) > 0 {
			m.commandMode.HistoryIndex = clampIndex(m.commandMode.HistoryIndex-1, len(m.commandMode.History))
			m.searchText = m.commandMode.History[m.commandMode.HistoryIndex].Input
			m.syncCommandMode(false)
			m = m.resetViewport("command-search")
		}
		return m, nil
	case keyMsg.Type != tea.KeyRunes && key.Matches(keyMsg, m.keymap.Down):
		if m.commandMode.CompletionOpen && len(m.commandMode.Suggestions) > 0 {
			m.commandMode.SelectedSuggestion = clampIndex(m.commandMode.SelectedSuggestion+1, len(m.commandMode.Suggestions))
			m = m.ensureViewportLineVisible("command-search", commandCompletionSelectedBodyLine(m.commandMode))
			return m, nil
		}
		if len(m.commandMode.History) > 0 {
			m.commandMode.HistoryIndex = clampIndex(m.commandMode.HistoryIndex+1, len(m.commandMode.History))
			m.searchText = m.commandMode.History[m.commandMode.HistoryIndex].Input
			m.syncCommandMode(false)
			m = m.resetViewport("command-search")
		}
		return m, nil
	case key.Matches(keyMsg, m.keymap.Enter):
		if m.commandMode.CompletionOpen && len(m.commandMode.Suggestions) > 0 {
			suggestion := m.commandMode.Suggestions[clampIndex(m.commandMode.SelectedSuggestion, len(m.commandMode.Suggestions))]
			m.searchText = applyCommandSuggestion(m.searchText, suggestion)
			m.syncCommandMode(false)
			m.commandMode.CompletionOpen = false
			m.commandMode.Suggestions = nil
			m = m.resetViewport("command-search")
			return m, nil
		}
		preview := applyMainAvailabilityToCommandPreview(BuildCommandPreview(m.searchText), m.homeSnapshot.MainAvailability)
		if commandBlockedReason(preview) == "" {
			m.commandMode.History = appendCommandPreviewHistory(m.commandMode.History, preview)
			m.commandMode.HistoryIndex = len(m.commandMode.History)
		}
		m.searchOpen = false
		m.commandPanel = CommandPanelState{Lifecycle: ActionLifecyclePreview, Preview: preview}
		return m, nil
	default:
		switch keyMsg.Type {
		case tea.KeyBackspace:
			if len(m.searchText) > 0 {
				m.searchText = m.searchText[:len(m.searchText)-1]
			}
			m.searchIndex = 0
		case tea.KeySpace:
			m.searchText += " "
			m.searchIndex = 0
		case tea.KeyRunes:
			m.searchText += string(keyMsg.Runes)
			m.searchIndex = 0
		}
		if !isCommandModeInput(m.searchText) {
			m.commandMode = CommandModeState{}
			m = m.resetViewport("command-search")
			return m, nil
		}
		m.syncCommandMode(false)
		if m.commandMode.CompletionOpen {
			m.commandMode.Suggestions = m.commandSuggestions()
			m.commandMode.SelectedSuggestion = 0
		}
		m = m.resetViewport("command-search")
		return m, nil
	}
}

func portalSearchSelectedBodyLine(results []PortalSearchResult, selected int) int {
	if len(results) == 0 {
		return 0
	}
	return clampIndex(selected, len(results))
}

func portalSearchResultLineCount(result PortalSearchResult) int {
	return 1
}

func databaseSearchSelectedBodyLine(data DatabaseSearchData, selected int) int {
	if len(data.ResultSet.Results) == 0 {
		return 0
	}
	selected = clampIndex(selected, len(data.ResultSet.Results))
	line := 3
	for idx := 0; idx < selected; idx++ {
		line += databaseSearchResultLineCount(data.ResultSet.Results[idx], false)
	}
	return line
}

func databaseSearchResultLineCount(result search.SearchResult, raw bool) int {
	count := 2
	if strings.TrimSpace(result.Snippet) != "" {
		count++
	}
	if raw {
		count++
	}
	return count
}

func commandCompletionSelectedBodyLine(state CommandModeState) int {
	if len(state.Suggestions) == 0 {
		return 0
	}
	line := commandPreviewBodyLineCount(state.Preview)
	line += 2
	selected := clampIndex(state.SelectedSuggestion, len(state.Suggestions))
	for idx := 0; idx < selected; idx++ {
		line += commandSuggestionLineCount(state.Suggestions[idx])
	}
	return line
}

func commandPreviewBodyLineCount(preview CommandPreview) int {
	if preview.ParseError != "" {
		return 3
	}
	if preview.CanonicalCommand == "loom" {
		return 0
	}
	count := 5
	if preview.AliasApplied != "" {
		count++
	}
	if preview.EffectSummary != "" {
		count++
	}
	if reason := commandBlockedReason(preview); reason != "" {
		_ = reason
		count++
	}
	return count
}

func commandSuggestionLineCount(suggestion CommandSuggestion) int {
	count := 1
	if strings.TrimSpace(suggestion.Description) != "" {
		count++
	}
	return count
}

func (m *Model) syncCommandMode(keepSuggestions bool) {
	history := m.commandMode.History
	historyIndex := m.commandMode.HistoryIndex
	cache := m.commandMode.CompletionCache
	suggestions := m.commandMode.Suggestions
	selected := m.commandMode.SelectedSuggestion
	completionOpen := m.commandMode.CompletionOpen
	if !keepSuggestions {
		suggestions = nil
		selected = 0
		completionOpen = false
	}
	parse := ParseCommandInput(m.searchText)
	preview := applyMainAvailabilityToCommandPreview(BuildCommandPreview(m.searchText), m.homeSnapshot.MainAvailability)
	m.commandMode = CommandModeState{
		Active:             isCommandModeInput(m.searchText),
		Input:              m.searchText,
		Cursor:             len(m.searchText),
		Parse:              parse,
		Preview:            preview,
		Suggestions:        suggestions,
		SelectedSuggestion: selected,
		CompletionOpen:     completionOpen,
		CompletionCache:    cache,
		History:            history,
		HistoryIndex:       historyIndex,
	}
}

func (m *Model) commandSuggestions() []CommandSuggestion {
	cacheKey := commandCompletionCacheKey(m.searchText)
	if m.commandMode.CompletionCache != nil {
		if entry, ok := m.commandMode.CompletionCache[cacheKey]; ok && time.Since(entry.LoadedAt) < 10*time.Second {
			return m.prioritizeCommandSuggestions(append([]CommandSuggestion{}, entry.Suggestions...))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var suggestions []CommandSuggestion
	if m.commandRunner != nil {
		runnerSuggestions, err := m.commandRunner.Complete(ctx, CommandCompletionRequest{
			Input:          m.searchText,
			Cursor:         len(m.searchText),
			CorrelationID:  m.correlationID,
			CurrentScreen:  m.screen,
			SelectedRecord: m.selectedCommandRecord(),
		})
		if err == nil && len(runnerSuggestions) > 0 {
			suggestions = runnerSuggestions
		}
	}
	if len(suggestions) == 0 {
		suggestions = CompleteCommand(ctx, m.client, m.correlationID, m.searchText, len(m.searchText))
	}
	if m.commandMode.CompletionCache == nil {
		m.commandMode.CompletionCache = map[string]CommandCompletionCacheEntry{}
	}
	m.commandMode.CompletionCache[cacheKey] = CommandCompletionCacheEntry{Suggestions: append([]CommandSuggestion{}, suggestions...), LoadedAt: time.Now().UTC()}
	return m.prioritizeCommandSuggestions(suggestions)
}

func (m Model) prioritizeCommandSuggestions(suggestions []CommandSuggestion) []CommandSuggestion {
	prefix, start, end := currentCommandToken(m.searchText, len(m.searchText))
	prioritized := []CommandSuggestion{}
	if selected := selectedRecordCommandSuggestion(m.searchText, prefix, start, end, m.selectedCommandRecord()); selected.Label != "" {
		prioritized = append(prioritized, selected)
	}
	for idx := len(m.commandMode.History) - 1; idx >= 0 && len(prioritized) < 4; idx-- {
		entry := m.commandMode.History[idx]
		label := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(entry.Input), "$"))
		if label == "" || commandHasSecretFlag(label) {
			continue
		}
		if prefix != "" && !strings.Contains(strings.ToLower(label), strings.ToLower(prefix)) && !strings.Contains(strings.ToLower(entry.CanonicalCommand), strings.ToLower(prefix)) {
			continue
		}
		prioritized = append(prioritized, CommandSuggestion{
			Label:            label,
			Kind:             "history",
			Description:      "Recent command",
			InsertText:       label,
			ReplacementStart: 0,
			ReplacementEnd:   len(m.searchText),
			CanonicalPreview: entry.CanonicalCommand,
			Score:            90,
		})
	}
	prioritized = append(prioritized, suggestions...)
	return uniqueCommandSuggestions(prioritized)
}

func (m Model) updateCommandPanel(keyMsg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(keyMsg, m.keymap.Escape):
		switch m.commandPanel.Lifecycle {
		case ActionLifecycleRunning:
			return m, nil
		case ActionLifecycleSucceeded, ActionLifecycleFailed, ActionLifecycleCancelled:
			m.commandPanel = CommandPanelState{}
			return m, nil
		default:
			m.commandPanel.Lifecycle = ActionLifecycleCancelled
			m.commandPanel.Result = CommandResult{Input: m.commandPanel.Preview.Input, CanonicalTokens: m.commandPanel.Preview.CanonicalTokens, CanonicalCommand: m.commandPanel.Preview.CanonicalCommand, Classification: m.commandPanel.Preview.Classification, Status: ActionLifecycleCancelled, Summary: "Command cancelled before execution."}
			return m, nil
		}
	case key.Matches(keyMsg, m.keymap.Details):
		m.commandPanel.RawDetails = !m.commandPanel.RawDetails
		return m, nil
	case key.Matches(keyMsg, m.keymap.Refresh):
		if m.commandPanel.Lifecycle == ActionLifecycleSucceeded || m.commandPanel.Lifecycle == ActionLifecycleFailed {
			preview := m.commandPanel.Preview
			m.commandPanel.Lifecycle = ActionLifecycleRunning
			return m, executeCommandCmd(m.commandRunner, m.correlationID, m.screen, m.selectedCommandRecord(), preview, true, m.homeSnapshot.MainAvailability)
		}
	case key.Matches(keyMsg, m.keymap.Enter):
		switch m.commandPanel.Lifecycle {
		case ActionLifecyclePreview:
			return m.advanceCommandPreview()
		case ActionLifecycleNeedsConfirmation:
			preview := m.commandPanel.Preview
			m.commandPanel.Lifecycle = ActionLifecycleRunning
			return m, executeCommandCmd(m.commandRunner, m.correlationID, m.screen, m.selectedCommandRecord(), preview, true, m.homeSnapshot.MainAvailability)
		case ActionLifecycleSucceeded, ActionLifecycleFailed, ActionLifecycleCancelled:
			m.commandPanel = CommandPanelState{}
			return m, nil
		}
	}
	return m, nil
}

func (m Model) advanceCommandPreview() (tea.Model, tea.Cmd) {
	preview := applyMainAvailabilityToCommandPreview(m.commandPanel.Preview, m.homeSnapshot.MainAvailability)
	m.commandPanel.Preview = preview
	if reason := commandBlockedReason(preview); reason != "" {
		m.commandPanel.Lifecycle = ActionLifecycleFailed
		m.commandPanel.Result = CommandResult{Input: preview.Input, CanonicalTokens: preview.CanonicalTokens, CanonicalCommand: preview.CanonicalCommand, Classification: preview.Classification, Status: ActionLifecycleFailed, Summary: "Command is blocked.", ErrorCode: "portal.command_blocked", ErrorMessage: reason}
		return m, nil
	}
	if preview.Classification == CommandClassDangerous || preview.Classification == CommandClassBlocked {
		m.commandPanel.Lifecycle = ActionLifecycleFailed
		m.commandPanel.Result = CommandResult{Input: preview.Input, CanonicalTokens: preview.CanonicalTokens, CanonicalCommand: preview.CanonicalCommand, Classification: preview.Classification, Status: ActionLifecycleFailed, Summary: "Command is blocked.", ErrorCode: "portal.command_blocked", ErrorMessage: commandBlockedReason(preview)}
		return m, nil
	}
	if preview.MainAvailability.State == MainAvailabilityOffline && preview.ExecutionDependency == ExecutionDependencyMain {
		m.commandPanel.Lifecycle = ActionLifecycleFailed
		m.commandPanel.Result = CommandResult{Input: preview.Input, CanonicalTokens: preview.CanonicalTokens, CanonicalCommand: preview.CanonicalCommand, Classification: preview.Classification, ExecutionDependency: preview.ExecutionDependency, MainAvailability: preview.MainAvailability, Status: ActionLifecycleFailed, Summary: "Command is unavailable while main is offline.", ErrorCode: "portal.main_offline", ErrorMessage: "Main is offline. This command requires the main node."}
		return m, nil
	}
	if preview.RequiresConfirmation {
		m.commandPanel.Lifecycle = ActionLifecycleNeedsConfirmation
		return m, nil
	}
	m.commandPanel.Lifecycle = ActionLifecycleRunning
	return m, executeCommandCmd(m.commandRunner, m.correlationID, m.screen, m.selectedCommandRecord(), preview, true, m.homeSnapshot.MainAvailability)
}

func (m Model) handleCommandExecuted(msg commandExecutedMsg) (tea.Model, tea.Cmd) {
	result := msg.Result
	if msg.Err != nil {
		result.Status = ActionLifecycleFailed
		result.Summary = "Command failed."
		result.ErrorCode = "portal.command_failed"
		result.ErrorMessage = msg.Err.Error()
	}
	if result.Status == "" {
		result.Status = ActionLifecycleSucceeded
	}
	m.commandPanel.Lifecycle = result.Status
	m.commandPanel.Result = result
	m.commandMode.History = appendCommandHistory(m.commandMode.History, result)
	m.commandMode.HistoryIndex = len(m.commandMode.History)
	if result.RefreshScreen != "" && result.Status == ActionLifecycleSucceeded {
		m.commandMode.CompletionCache = nil
		refreshScreen := NormalizeScreen(result.RefreshScreen)
		m.screen = refreshScreen
		state := m.currentScreenState().MarkLoading()
		m.setScreenState(refreshScreen, state)
		return m, loadScreenCmd(m.client, m.correlationID, refreshScreen, m.homeSnapshot, m.snapshotOptions)
	}
	return m, nil
}

func (m Model) renderCommandPanel() string {
	switch m.commandPanel.Lifecycle {
	case ActionLifecyclePreview:
		return RenderCommandPreview(m.mode, m.commandPanel.Preview, m.commandPanel.RawDetails)
	case ActionLifecycleNeedsConfirmation:
		return RenderCommandConfirmation(m.mode, m.commandPanel.Preview, m.commandPanel.RawDetails)
	case ActionLifecycleRunning:
		return RenderCommandRunning(m.mode, m.commandPanel.Preview)
	case ActionLifecycleSucceeded, ActionLifecycleFailed, ActionLifecycleCancelled:
		return RenderCommandResult(m.mode, m.commandPanel.Result, m.commandPanel.RawDetails)
	default:
		return ""
	}
}

func (m Model) commandPanelActive() bool {
	return m.commandPanel.Lifecycle != "" && m.commandPanel.Lifecycle != ActionLifecycleIdle
}

func (m *Model) openScreen(screen string) {
	m.screen = NormalizeScreen(screen)
	m.searchOpen = false
	m.searchIndex = 0
	m.dbSearchOpen = false
	m.dbSearchIndex = 0
	m.notesSearchOpen = false
	m.notesSearchIndex = 0
	if _, ok := m.screenStates[m.screen]; !ok {
		m.setScreenState(m.screen, ScreenStateFromSnapshot(m.screen, m.homeSnapshot))
	}
}

func (m *Model) openScreenWithLoad(screen string) tea.Cmd {
	m.openScreen(screen)
	if m.screen == ScreenHome || m.client == nil {
		return nil
	}
	state := m.currentScreenState().MarkLoading()
	m.setScreenState(m.screen, state)
	return loadScreenCmd(m.client, m.correlationID, m.screen, m.homeSnapshot, m.snapshotOptions)
}

func (m Model) shouldLoadInitialScreen() bool {
	return m.client != nil && NormalizeScreen(m.screen) != ScreenHome
}

type actionExecutedMsg struct {
	Result PortalActionResult
	Err    error
}

func executeActionCmd(client Client, correlationID string, action PortalAction, availability ...MainAvailability) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), portalActionExecutionTimeout(action))
		defer cancel()
		result, err := ExecutePortalAction(ctx, client, correlationID, action, true, availability...)
		return actionExecutedMsg{Result: result, Err: err}
	}
}

type commandExecutedMsg struct {
	Result CommandResult
	Err    error
}

func executeCommandCmd(runner CommandRunner, correlationID string, screen string, selected CommandSelectedRecord, preview CommandPreview, confirmed bool, availability ...MainAvailability) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), portalCommandExecutionTimeout(preview.CanonicalTokens))
		defer cancel()
		mainAvailability := MainAvailability{}
		if len(availability) > 0 {
			mainAvailability = availability[0]
		}
		result, err := ExecutePortalCommand(ctx, runner, CommandRequest{
			Input:               preview.Input,
			Tokens:              append([]string{}, preview.CanonicalTokens...),
			CanonicalTokens:     append([]string{}, preview.CanonicalTokens...),
			Classification:      preview.Classification,
			Confirmed:           confirmed,
			CorrelationID:       correlationID,
			CurrentScreen:       screen,
			SelectedRecord:      selected,
			ExecutionDependency: preview.ExecutionDependency,
			MainAvailability:    mainAvailability,
		})
		return commandExecutedMsg{Result: result, Err: err}
	}
}

func clampIndex(index, count int) int {
	if count <= 0 {
		return 0
	}
	if index < 0 {
		return 0
	}
	if index >= count {
		return count - 1
	}
	return index
}

func (m Model) currentScreenState() ScreenState {
	screen := NormalizeScreen(m.screen)
	if m.screenStates == nil {
		return ScreenStateFromSnapshot(screen, m.homeSnapshot)
	}
	if state, ok := m.screenStates[screen]; ok {
		return state
	}
	return ScreenStateFromSnapshot(screen, m.homeSnapshot)
}

func (m *Model) setScreenState(screen string, state ScreenState) {
	if m.screenStates == nil {
		m.screenStates = map[string]ScreenState{}
	}
	state.Screen = NormalizeScreen(screen)
	m.screenStates[state.Screen] = state
}

func (m Model) refreshCurrentScreen() (tea.Model, tea.Cmd) {
	m.commandMode.CompletionCache = nil
	state := m.currentScreenState().MarkLoading()
	m.setScreenState(m.screen, state)
	return m, refreshScreenCmd(m.client, m.correlationID, m.screen, m.homeSnapshot, m.snapshotOptions)
}

func mainAvailabilityWasRefreshed(previous MainAvailability, next MainAvailability) bool {
	if next.CheckedAt.IsZero() {
		return false
	}
	return previous.State != next.State || !previous.CheckedAt.Equal(next.CheckedAt)
}

func (m *Model) replaceCachedStatesForAvailability(loadedScreen string, snapshot Snapshot) {
	loadedScreen = NormalizeScreen(loadedScreen)
	for screen, previous := range m.screenStates {
		screen = NormalizeScreen(screen)
		if screen == loadedScreen {
			continue
		}
		if mainOwnedScreen(screen) {
			if snapshot.MainAvailability.State == MainAvailabilityOffline {
				m.screenStates[screen] = preserveScreenSelection(previous, unavailableScreenState(screen, snapshot))
			} else {
				delete(m.screenStates, screen)
			}
			continue
		}
		m.screenStates[screen] = preserveScreenSelection(previous, ScreenStateFromSnapshot(screen, snapshot))
	}
	if m.scopedLoading != nil {
		clear(m.scopedLoading)
	}
}

func (m *Model) reconcileOpenPanelsWithAvailability(availability MainAvailability) {
	if m.actionPanelActive() {
		m.actionPanel.Action = applyMainAvailabilityToAction(m.actionPanel.Action, availability)
		if availability.State == MainAvailabilityOffline && portalActionExecutionDependency(m.actionPanel.Action) == ExecutionDependencyMain {
			switch m.actionPanel.Lifecycle {
			case ActionLifecyclePreview, ActionLifecycleNeedsInput, ActionLifecycleNeedsConfirmation:
				m.actionPanel.Lifecycle = ActionLifecycleFailed
				m.actionPanel.Result = mainOfflineActionResult(m.actionPanel.Action)
			}
		}
	}
	if m.commandMode.Active {
		m.commandMode.Preview = applyMainAvailabilityToCommandPreview(m.commandMode.Preview, availability)
	}
	if m.commandPanelActive() {
		m.commandPanel.Preview = applyMainAvailabilityToCommandPreview(m.commandPanel.Preview, availability)
		if availability.State == MainAvailabilityOffline && m.commandPanel.Preview.ExecutionDependency == ExecutionDependencyMain {
			switch m.commandPanel.Lifecycle {
			case ActionLifecyclePreview, ActionLifecycleNeedsConfirmation:
				preview := m.commandPanel.Preview
				m.commandPanel.Lifecycle = ActionLifecycleFailed
				m.commandPanel.Result = CommandResult{Input: preview.Input, CanonicalTokens: preview.CanonicalTokens, CanonicalCommand: preview.CanonicalCommand, Classification: preview.Classification, ExecutionDependency: preview.ExecutionDependency, MainAvailability: availability, Status: ActionLifecycleFailed, Summary: "Command is unavailable while main is offline.", ErrorCode: "portal.main_offline", ErrorMessage: "Main is offline. This command requires the main node."}
			}
		}
	}
}

func preserveScreenSelection(previous ScreenState, next ScreenState) ScreenState {
	next.RawDetails = previous.RawDetails
	next.ExpandedActionGroup = previous.ExpandedActionGroup
	if previous.SelectedIndex <= 0 {
		return next
	}
	if NormalizeScreen(next.Screen) == ScreenHome {
		next.SelectedIndex = clampIndex(previous.SelectedIndex, len(HomeNavigationScreens()))
		return next
	}
	previousItems := ScreenSelectableItems(previous)
	if previous.SelectedIndex >= len(previousItems) {
		return next
	}
	selected := selectableItemIdentity(previousItems[previous.SelectedIndex])
	if selected == "" {
		return next
	}
	for index, item := range ScreenSelectableItems(next) {
		if selectableItemIdentity(item) == selected {
			next.SelectedIndex = index
			break
		}
	}
	return next
}

func selectableItemIdentity(item SelectableItem) string {
	if item.ActionID != "" {
		return "action:" + item.ActionID
	}
	if item.RecordKind == "" || item.RecordRef == "" {
		return ""
	}
	return "record:" + item.RecordKind + ":" + item.RecordRef
}

func (m Model) currentSelectableCount() int {
	state := m.currentScreenState()
	switch NormalizeScreen(m.screen) {
	case ScreenHome:
		return len(HomeNavigationScreens())
	case ScreenDoctor:
		return len(ScreenSelectableItems(state))
	case ScreenBox:
		return len(ScreenSelectableItems(state))
	case ScreenStorage:
		return len(ScreenSelectableItems(state))
	case ScreenProjects:
		return len(ScreenSelectableItems(state))
	case ScreenServices:
		return len(ScreenSelectableItems(state))
	case ScreenBackground:
		return len(ScreenSelectableItems(state))
	case ScreenAutomations:
		return len(ScreenSelectableItems(state))
	case ScreenJobs:
		return len(ScreenSelectableItems(state))
	case ScreenNotes:
		return len(ScreenSelectableItems(state))
	case ScreenNodes:
		return len(ScreenSelectableItems(state))
	case ScreenCapabilities:
		return len(ScreenSelectableItems(state))
	case ScreenDatabase:
		return len(ScreenSelectableItems(state))
	default:
		return 0
	}
}

func (m Model) searchResults() []PortalSearchResult {
	input := ParsePortalSearchInput(m.searchText)
	state := m.currentScreenState()
	if input.Mode == PortalSearchModeScoped {
		state = m.screenStateForSearchScope(input.Scope)
	}
	return SearchPortal(m.registry, state, strings.TrimSpace(m.searchText), 8)
}

func (m Model) screenStateForSearchScope(scope string) ScreenState {
	screen := searchScopeScreen(scope)
	if screen == "" {
		return m.currentScreenState()
	}
	if state, ok := m.screenStates[screen]; ok {
		return state
	}
	return ScreenStateFromSnapshot(screen, m.homeSnapshot)
}

func (m Model) maybeLoadScopedSearchData() tea.Cmd {
	input := ParsePortalSearchInput(m.searchText)
	if input.Mode != PortalSearchModeScoped || !input.ScopeComplete || m.client == nil {
		return nil
	}
	screen := searchScopeScreen(input.Scope)
	if screen == "" {
		return nil
	}
	if state, ok := m.screenStates[screen]; ok {
		switch state.Status {
		case ScreenLoadLoaded, ScreenLoadPartial, ScreenLoadLoading:
			return nil
		}
	}
	if m.scopedLoading == nil {
		m.scopedLoading = map[string]bool{}
	}
	if m.scopedLoading[screen] {
		return nil
	}
	m.scopedLoading[screen] = true
	return loadScreenCmd(m.client, m.correlationID, screen, m.homeSnapshot, m.snapshotOptions)
}

func (m Model) openCapabilityExplorerSelection() (Model, bool) {
	if NormalizeScreen(m.screen) != ScreenCapabilities {
		return m, false
	}
	state := m.currentScreenState()
	items := ScreenRecordItems(state)
	if len(items) == 0 {
		return m, false
	}
	if state.SelectedIndex < 0 || state.SelectedIndex >= len(items) {
		return m, false
	}
	selected := state.SelectedIndex
	item := items[selected]
	explorer := normalizeCapabilityExplorerState(state.Data.Capabilities)
	switch {
	case item.RecordKind == "capability_scope" && item.RecordRef != "":
		explorer.Level = CapabilityExplorerProviders
		explorer.SelectedScope = item.RecordRef
		explorer.SelectedProvider = ""
		explorer.ExpandedRef = ""
		state.Data.Capabilities.Explorer = explorer
		state.SelectedIndex = 0
		m.setScreenState(ScreenCapabilities, state)
		return m, true
	case item.RecordKind == "provider" && item.RecordRef != "" && explorer.Level == CapabilityExplorerProviders:
		explorer.Level = CapabilityExplorerCapabilities
		explorer.SelectedProvider = item.RecordRef
		explorer.SelectedScope = firstNonEmpty(explorer.SelectedScope, scopeFromAddress(item.RecordRef))
		explorer.ExpandedRef = ""
		state.Data.Capabilities.Explorer = explorer
		state.SelectedIndex = 0
		m.setScreenState(ScreenCapabilities, state)
		return m, true
	default:
		return m, false
	}
}

func (m Model) toggleCapabilityExplorerActions() (Model, bool) {
	if NormalizeScreen(m.screen) != ScreenCapabilities {
		return m, false
	}
	state := m.currentScreenState()
	items := ScreenRecordItems(state)
	if len(items) == 0 || state.SelectedIndex < 0 || state.SelectedIndex >= len(items) {
		return m, false
	}
	item := items[state.SelectedIndex]
	if item.Kind != SelectableKindRecord || len(item.RelatedActions) == 0 {
		return m, false
	}
	switch item.RecordKind {
	case "provider", "capability":
	default:
		return m, false
	}

	explorer := normalizeCapabilityExplorerState(state.Data.Capabilities)
	if explorer.ExpandedRef == item.RecordRef {
		explorer.ExpandedRef = ""
	} else {
		explorer.ExpandedRef = item.RecordRef
	}
	state.Data.Capabilities.Explorer = explorer
	m.setScreenState(ScreenCapabilities, state)
	return m, true
}

func (m Model) capabilityExplorerBack() (Model, bool) {
	if NormalizeScreen(m.screen) != ScreenCapabilities {
		return m, false
	}
	state := m.currentScreenState()
	explorer := normalizeCapabilityExplorerState(state.Data.Capabilities)
	if strings.TrimSpace(explorer.ExpandedRef) != "" {
		parentRef := selectedCapabilityParentRef(state)
		explorer.ExpandedRef = ""
		state.Data.Capabilities.Explorer = explorer
		state.SelectedIndex = capabilityExplorerIndexForRef(state, parentRef)
		m.setScreenState(ScreenCapabilities, state)
		return m, true
	}
	switch explorer.Level {
	case CapabilityExplorerCapabilities:
		explorer.Level = CapabilityExplorerProviders
		explorer.SelectedProvider = ""
		explorer.ExpandedRef = ""
	case CapabilityExplorerProviders:
		explorer.Level = CapabilityExplorerNodes
		explorer.SelectedScope = ""
		explorer.SelectedProvider = ""
		explorer.ExpandedRef = ""
	default:
		return m, false
	}
	state.Data.Capabilities.Explorer = explorer
	state.SelectedIndex = 0
	m.setScreenState(ScreenCapabilities, state)
	return m, true
}

func selectedCapabilityParentRef(state ScreenState) string {
	items := ScreenRecordItems(state)
	if len(items) == 0 || state.SelectedIndex < 0 || state.SelectedIndex >= len(items) {
		return ""
	}
	item := items[state.SelectedIndex]
	if item.RecordKind == "capability_inline_action" {
		return item.RecordLabel
	}
	return item.RecordRef
}

func capabilityExplorerIndexForRef(state ScreenState, ref string) int {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0
	}
	items := ScreenRecordItems(state)
	for idx, item := range items {
		if item.RecordRef == ref || item.RecordLabel == ref {
			return idx
		}
	}
	return 0
}

func (m Model) openProjectSelection() (Model, tea.Cmd, bool) {
	if NormalizeScreen(m.screen) != ScreenProjects {
		return m, nil, false
	}
	state := m.currentScreenState()
	item, ok := selectedRecordItem(state)
	if !ok {
		return m, nil, false
	}
	if level, ok := projectExplorerLevelFromSectionRecordKind(item.RecordKind); ok {
		explorer := normalizeProjectExplorerState(state.Data.Projects.Explorer)
		explorer.Level = level
		explorer.ExpandedRef = ""
		state.Data.Projects.Explorer = explorer
		state.SelectedIndex = screenActionOffset(state)
		m.setScreenState(ScreenProjects, state)
		return m, nil, true
	}
	if item.RecordKind != "project" || item.RecordRef == "" {
		return m, nil, false
	}
	alreadySelected := item.RecordRef == state.Data.Projects.SelectedProjectRef
	state.Data.Projects.SelectedProjectRef = item.RecordRef
	explorer := normalizeProjectExplorerState(state.Data.Projects.Explorer)
	explorer.SelectedProjectRef = item.RecordRef
	explorer.ExpandedRef = ""
	explorer.Level = ProjectExplorerDetail
	state.Data.Projects.Explorer = explorer
	if alreadySelected {
		m.setScreenState(ScreenProjects, state)
		return m, nil, true
	}
	state = state.MarkLoading()
	m.setScreenState(ScreenProjects, state)
	return m, loadProjectsScreenCmd(m.client, m.correlationID, m.homeSnapshot, item.RecordRef), true
}

func (m Model) toggleProjectExplorerActions() (Model, bool) {
	if NormalizeScreen(m.screen) != ScreenProjects {
		return m, false
	}
	state := m.currentScreenState()
	item, ok := selectedRecordItem(state)
	if !ok {
		return m, false
	}
	if item.Kind != SelectableKindRecord || len(item.RelatedActions) == 0 {
		return m, false
	}
	if item.RecordKind != "project" && !strings.HasPrefix(item.RecordKind, "project_") {
		return m, false
	}
	explorer := state.Data.Projects.Explorer
	if explorer.ExpandedRef == item.RecordRef {
		explorer.ExpandedRef = ""
	} else {
		explorer.ExpandedRef = item.RecordRef
	}
	state.Data.Projects.Explorer = explorer
	m.setScreenState(ScreenProjects, state)
	return m, true
}

func (m Model) projectExplorerBack() (Model, bool) {
	if NormalizeScreen(m.screen) != ScreenProjects {
		return m, false
	}
	state := m.currentScreenState()
	explorer := normalizeProjectExplorerState(state.Data.Projects.Explorer)
	if strings.TrimSpace(explorer.ExpandedRef) != "" {
		parentRef := selectedProjectParentRef(state)
		explorer.ExpandedRef = ""
		state.Data.Projects.Explorer = explorer
		state.SelectedIndex = projectExplorerIndexForRef(state, parentRef)
		m.setScreenState(ScreenProjects, state)
		return m, true
	}
	switch explorer.Level {
	case ProjectExplorerHome:
		return m, false
	case ProjectExplorerDetail:
		explorer.Level = ProjectExplorerHome
		explorer.ExpandedRef = ""
		state.Data.Projects.Explorer = explorer
		state.SelectedIndex = projectExplorerIndexForRef(state, firstNonEmpty(explorer.SelectedProjectRef, state.Data.Projects.SelectedProjectRef))
	default:
		explorer.Level = ProjectExplorerDetail
		explorer.ExpandedRef = ""
		state.Data.Projects.Explorer = explorer
		state.SelectedIndex = screenActionOffset(state)
	}
	m.setScreenState(ScreenProjects, state)
	return m, true
}

func selectedProjectParentRef(state ScreenState) string {
	item, ok := selectedRecordItem(state)
	if !ok {
		return ""
	}
	if item.RecordKind == "project_inline_action" {
		return item.RecordLabel
	}
	return item.RecordRef
}

func projectExplorerIndexForRef(state ScreenState, ref string) int {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0
	}
	items := ScreenRecordItems(state)
	for idx, item := range items {
		if item.RecordRef == ref || item.RecordLabel == ref {
			return recordVisualIndex(state, idx)
		}
	}
	return 0
}

func (m Model) toggleOperationalActionGroupSelection() (Model, bool) {
	screen := NormalizeScreen(m.screen)
	if !operationalActionGroupsSupported(screen) {
		return m, false
	}
	state := m.currentScreenState()
	item, ok := selectedRecordItem(state)
	if !ok || !selectableIsOperationalActionGroup(item) {
		return m, false
	}
	if state.ExpandedActionGroup == item.RecordRef {
		state.ExpandedActionGroup = ""
	} else {
		state.ExpandedActionGroup = item.RecordRef
	}
	m.setScreenState(screen, state)
	return m, true
}

func (m Model) operationalActionGroupBack() (Model, bool) {
	screen := NormalizeScreen(m.screen)
	if !operationalActionGroupsSupported(screen) {
		return m, false
	}
	state := m.currentScreenState()
	if strings.TrimSpace(state.ExpandedActionGroup) == "" {
		return m, false
	}
	parentRef := state.ExpandedActionGroup
	state.ExpandedActionGroup = ""
	state.SelectedIndex = operationalActionGroupIndexForRef(state, parentRef)
	m.setScreenState(screen, state)
	return m, true
}

func operationalActionGroupIndexForRef(state ScreenState, ref string) int {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0
	}
	items := ScreenRecordItems(state)
	for idx, item := range items {
		if selectableIsOperationalActionGroup(item) && item.RecordRef == ref {
			return recordVisualIndex(state, idx)
		}
	}
	return 0
}

func (m Model) selectedCommandRecord() CommandSelectedRecord {
	state := m.currentScreenState()
	items := ScreenSelectableItems(state)
	if len(items) == 0 {
		return CommandSelectedRecord{}
	}
	item := items[clampIndex(state.SelectedIndex, len(items))]
	return CommandSelectedRecord{Kind: item.RecordKind, Ref: item.RecordRef, Label: firstNonEmpty(item.RecordLabel, item.Label)}
}

func appendCommandHistory(history []CommandHistoryEntry, result CommandResult) []CommandHistoryEntry {
	input := strings.TrimSpace(result.Input)
	if input == "" || commandHasSecretFlag(input) {
		return history
	}
	if len(history) > 0 && history[len(history)-1].Input == input {
		history[len(history)-1].Status = result.Status
		history[len(history)-1].At = time.Now().UTC()
		return history
	}
	return append(history, CommandHistoryEntry{Input: input, CanonicalCommand: result.CanonicalCommand, Status: result.Status, At: time.Now().UTC()})
}

func appendCommandPreviewHistory(history []CommandHistoryEntry, preview CommandPreview) []CommandHistoryEntry {
	input := strings.TrimSpace(preview.Input)
	if input == "" || commandHasSecretFlag(input) {
		return history
	}
	if len(history) > 0 && history[len(history)-1].Input == input {
		history[len(history)-1].CanonicalCommand = preview.CanonicalCommand
		history[len(history)-1].Status = ActionLifecyclePreview
		history[len(history)-1].At = time.Now().UTC()
		return history
	}
	return append(history, CommandHistoryEntry{Input: input, CanonicalCommand: preview.CanonicalCommand, Status: ActionLifecyclePreview, At: time.Now().UTC()})
}

func commandHasSecretFlag(input string) bool {
	lower := strings.ToLower(input)
	for _, needle := range []string{"--token", "--secret", "--password", "--credential", "--api-key"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func commandCompletionCacheKey(input string) string {
	prefix, _, _ := currentCommandToken(input, len(input))
	parsed := ParseCommandInput(input)
	tokens := append([]string{}, parsed.CanonicalTokens...)
	if len(tokens) > 0 {
		tokens = tokens[:len(tokens)-1]
	}
	return strings.Join(tokens, " ") + "|" + strings.ToLower(prefix)
}
