package portal

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"loom.local/loom/internal/loomcli/actions"
)

func TestPortalInteractionSmokeHarness(t *testing.T) {
	h := newPortalSmokeHarness(t)
	h.expectViewContains("Attention")

	h.openHomeNavigation(ScreenNotes)
	h.expectScreen(ScreenNotes)
	h.expectViewContains("LOOM Notes")

	h.openSearch()
	h.typeText("capabilities")
	h.pressAndRun(tea.KeyMsg{Type: tea.KeyEnter})
	h.expectScreen(ScreenCapabilities)
	h.expectViewContains("Capability Explorer")

	h.openCommand("$ worker run main.indexer_text --once")
	h.press(tea.KeyMsg{Type: tea.KeyEnter})
	if h.model.commandPanel.Lifecycle != ActionLifecyclePreview {
		t.Fatalf("command lifecycle = %s, want preview", h.model.commandPanel.Lifecycle)
	}
	h.press(tea.KeyMsg{Type: tea.KeyEnter})
	if h.model.commandPanel.Lifecycle != ActionLifecycleNeedsConfirmation {
		t.Fatalf("command lifecycle = %s, want confirmation", h.model.commandPanel.Lifecycle)
	}
	h.press(tea.KeyMsg{Type: tea.KeyEsc})
	if h.model.commandPanel.Lifecycle != ActionLifecycleCancelled {
		t.Fatalf("command lifecycle = %s, want cancelled", h.model.commandPanel.Lifecycle)
	}
	h.press(tea.KeyMsg{Type: tea.KeyEsc})

	h.openSearch()
	h.typeText("#workers run worker selfcheck")
	h.press(tea.KeyMsg{Type: tea.KeyEnter})
	if h.model.searchOpen {
		t.Fatal("worker search result should close search after preview opens")
	}
	if h.model.actionPanel.Lifecycle != ActionLifecyclePreview {
		t.Fatalf("action lifecycle = %s, want preview", h.model.actionPanel.Lifecycle)
	}
	h.press(tea.KeyMsg{Type: tea.KeyEsc})
	if h.model.actionPanel.Lifecycle != ActionLifecycleCancelled {
		t.Fatalf("action lifecycle = %s, want cancelled", h.model.actionPanel.Lifecycle)
	}
}

func TestPortalProtectFolderKeyboardFlowReturnsToNodes(t *testing.T) {
	h := newPortalSmokeHarness(t)
	h.openHomeNavigation(ScreenNodes)
	h.expectScreen(ScreenNodes)
	h.expectViewContains("Protect Folder")

	h.press(tea.KeyMsg{Type: tea.KeyEnter})
	if h.model.actionPanel.Lifecycle != ActionLifecyclePreview || h.model.actionPanel.Action.Executor.Kind != PortalExecutorProtectedFolderProtect {
		h.t.Fatalf("selected action = %#v", h.model.actionPanel)
	}
	h.expectViewContains("Action Preview")
	h.press(tea.KeyMsg{Type: tea.KeyEsc})
	if h.model.actionPanel.Lifecycle != ActionLifecycleCancelled {
		h.t.Fatalf("cancel lifecycle = %s", h.model.actionPanel.Lifecycle)
	}
	h.press(tea.KeyMsg{Type: tea.KeyEsc})
	h.expectScreen(ScreenNodes)
	if h.model.actionPanelActive() {
		h.t.Fatalf("action panel remained active: %#v", h.model.actionPanel)
	}
}

type portalSmokeHarness struct {
	t     *testing.T
	model Model
}

func newPortalSmokeHarness(t *testing.T) *portalSmokeHarness {
	t.Helper()
	return &portalSmokeHarness{
		t: t,
		model: NewModelWithOptions(ModelOptions{
			Mode:            testMode(),
			Client:          newFakePortalClient(),
			CommandRunner:   fakeCommandRunner{},
			CorrelationID:   "corr_test",
			Snapshot:        fakeSnapshot(),
			Registry:        actions.DefaultRegistry(),
			StartScreen:     ScreenHome,
			NoBootAnimation: true,
		}),
	}
}

func (h *portalSmokeHarness) openHomeNavigation(screenID string) {
	h.t.Helper()
	screens := HomeNavigationScreens()
	for index, screen := range screens {
		if screen.ID != screenID {
			continue
		}
		state := h.model.currentScreenState()
		state.SelectedIndex = index
		h.model.setScreenState(ScreenHome, state)
		h.pressAndRun(tea.KeyMsg{Type: tea.KeyEnter})
		return
	}
	h.t.Fatalf("home navigation missing screen %q", screenID)
}

func (h *portalSmokeHarness) openSearch() {
	h.t.Helper()
	h.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if !h.model.searchOpen {
		h.t.Fatal("search did not open")
	}
}

func (h *portalSmokeHarness) openCommand(command string) {
	h.t.Helper()
	h.openSearch()
	h.typeText(command)
	if !h.model.commandMode.Active {
		h.t.Fatalf("command mode did not activate for %q", command)
	}
}

func (h *portalSmokeHarness) typeText(value string) {
	h.t.Helper()
	for _, r := range value {
		h.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func (h *portalSmokeHarness) press(msg tea.KeyMsg) tea.Cmd {
	h.t.Helper()
	updated, cmd := h.model.Update(msg)
	h.model = updated.(Model)
	return cmd
}

func (h *portalSmokeHarness) pressAndRun(msg tea.KeyMsg) {
	h.t.Helper()
	cmd := h.press(msg)
	if cmd == nil {
		return
	}
	updated, _ := h.model.Update(cmd())
	h.model = updated.(Model)
}

func (h *portalSmokeHarness) expectScreen(screen string) {
	h.t.Helper()
	if h.model.screen != screen {
		h.t.Fatalf("screen = %s, want %s", h.model.screen, screen)
	}
}

func (h *portalSmokeHarness) expectViewContains(value string) {
	h.t.Helper()
	if !strings.Contains(h.model.View(), value) {
		h.t.Fatalf("view missing %q:\n%s", value, h.model.View())
	}
}
