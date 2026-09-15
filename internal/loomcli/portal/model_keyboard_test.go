package portal

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"loom.local/loom/internal/loomcli/actions"
)

func TestModelKeyboardSearchCommandEscapeAndHelp(t *testing.T) {
	model := NewModelWithOptions(ModelOptions{
		Mode:            testMode(),
		Client:          newFakePortalClient(),
		CommandRunner:   fakeCommandRunner{},
		Snapshot:        fakeSnapshot(),
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenHome,
		NoBootAnimation: true,
	})

	model = updateModelWithRunes(t, model, "/")
	if !model.searchOpen {
		t.Fatal("slash should open search")
	}
	model = updateModelWithRunes(t, model, "$")
	if !strings.Contains(model.View(), "LOOM Command") {
		t.Fatalf("dollar search should switch to command mode:\n%s", model.View())
	}
	model.commandMode.CompletionOpen = true
	model.commandMode.Suggestions = []CommandSuggestion{{Label: "health", InsertText: "health"}}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if !model.searchOpen || model.commandMode.CompletionOpen {
		t.Fatalf("first escape should close command completions only: %#v", model.commandMode)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.searchOpen {
		t.Fatal("second escape should close search")
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	model = updated.(Model)
	if !model.help || !strings.Contains(model.View(), "$ in search") {
		t.Fatalf("help should mention command mode:\n%s", model.View())
	}
}

func TestModelScreenTransitionReturnsCurrentFrameOnly(t *testing.T) {
	snapshot := fakeSnapshot()
	model := NewModelWithOptions(ModelOptions{
		Mode:            testMode(),
		Client:          newFakePortalClient(),
		Snapshot:        snapshot,
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenHome,
		NoBootAnimation: true,
	})
	home := model.View()
	if !strings.Contains(home, "LOOM Portal") {
		t.Fatalf("home frame missing portal logo/title:\n%s", home)
	}

	model.screen = ScreenJobs
	model.setScreenState(ScreenJobs, LoadScreenFromSnapshot(ScreenJobs, snapshot).State)
	jobs := model.View()
	if !strings.Contains(jobs, "Jobs") {
		t.Fatalf("jobs frame missing title:\n%s", jobs)
	}

	model.screen = ScreenHome
	model.setScreenState(ScreenHome, ScreenStateFromSnapshot(ScreenHome, snapshot))
	backHome := model.View()
	if !strings.Contains(backHome, "LOOM Portal") {
		t.Fatalf("returned home frame missing portal logo/title:\n%s", backHome)
	}
	if strings.Contains(backHome, "\nJobs\n\n") || strings.HasPrefix(backHome, "Jobs\n\n") {
		t.Fatalf("returned home frame retained stale jobs title:\n%s", backHome)
	}
	if strings.Count(backHome, "LOOM Portal") != 1 {
		t.Fatalf("returned home frame should contain one portal title:\n%s", backHome)
	}
}

func TestModelKeyboardDetailsDatabaseSearchAndActionCancel(t *testing.T) {
	model := NewModelWithOptions(ModelOptions{
		Mode:            testMode(),
		Client:          newFakePortalClient(),
		Snapshot:        fakeSnapshot(),
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenDatabase,
		NoBootAnimation: true,
	})
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(Model)
	if !model.currentScreenState().RawDetails {
		t.Fatal("tab should toggle raw details on a normal screen")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	model = updated.(Model)
	if !model.dbSearchOpen {
		t.Fatal("s should open database search on database screen")
	}

	actionModel := modelWithSelfcheckPreview(t, newFakePortalClient())
	updated, cmd := actionModel.Update(tea.KeyMsg{Type: tea.KeyEsc})
	actionModel = updated.(Model)
	if cmd != nil {
		t.Fatal("escape from action preview should not execute")
	}
	if actionModel.actionPanel.Lifecycle != ActionLifecycleCancelled {
		t.Fatalf("action lifecycle = %s, want cancelled", actionModel.actionPanel.Lifecycle)
	}
}

func TestModelKeyboardNotesSearchUsesNotesAPI(t *testing.T) {
	client := newFakePortalClient()
	result := LoadScreen(context.Background(), client, "corr_test", ScreenNotes, Snapshot{})
	model := NewModelWithOptions(ModelOptions{
		Mode:            testMode(),
		Client:          client,
		Snapshot:        fakeSnapshot(),
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenNotes,
		NoBootAnimation: true,
	})
	model.setScreenState(ScreenNotes, result.State)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	model = updated.(Model)
	if !model.notesSearchOpen || model.dbSearchOpen {
		t.Fatalf("s should open notes search only: notes=%t db=%t", model.notesSearchOpen, model.dbSearchOpen)
	}
	model = updateModelWithRunes(t, model, "project:osint-tools node:main tag:osint threat intel")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("enter should run notes search")
	}
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	if client.notesSearchInput.Query != "threat intel" || client.notesSearchInput.ProjectRef != "osint-tools" || client.notesSearchInput.SourceNodeKey != "main" {
		t.Fatalf("unexpected notes search input: %#v", client.notesSearchInput)
	}
	if !strings.Contains(model.View(), "Threat Intel Runbook") {
		t.Fatalf("notes search view missing result:\n%s", model.View())
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.notesSearchOpen || model.actionPanel.Lifecycle != ActionLifecyclePreview || model.actionPanel.Action.TargetKind != "notes_search_result" {
		t.Fatalf("enter on loaded result should open inspect preview: open=%t panel=%#v", model.notesSearchOpen, model.actionPanel)
	}
}

func TestModelKeyboardNotesSearchIsUnavailableOffline(t *testing.T) {
	client := newFakePortalClient()
	snapshot := fakeSnapshot()
	snapshot.MainAvailability = MainAvailability{State: MainAvailabilityOffline}
	model := NewModelWithOptions(ModelOptions{
		Mode:            testMode(),
		Client:          client,
		Snapshot:        snapshot,
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenNotes,
		NoBootAnimation: true,
	})
	model.setScreenState(ScreenNotes, ScreenStateFromSnapshot(ScreenNotes, snapshot))

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	model = updated.(Model)
	if cmd != nil || model.notesSearchOpen {
		t.Fatalf("offline notes search became runnable: cmd=%v open=%t", cmd != nil, model.notesSearchOpen)
	}
	if !strings.Contains(model.View(), "Main is offline") {
		t.Fatalf("offline notes view does not explain unavailability:\n%s", model.View())
	}
}

func TestModelCommandResultRerunKey(t *testing.T) {
	model := NewModelWithOptions(ModelOptions{
		Mode:            testMode(),
		Client:          newFakePortalClient(),
		CommandRunner:   fakeCommandRunner{},
		Snapshot:        fakeSnapshot(),
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenHome,
		NoBootAnimation: true,
	})
	model.commandPanel = CommandPanelState{
		Lifecycle: ActionLifecycleSucceeded,
		Preview:   BuildCommandPreview("$ health"),
		Result: CommandResult{
			Input:            "$ health",
			CanonicalTokens:  []string{"health"},
			CanonicalCommand: "loom health",
			Classification:   CommandClassInspect,
			Status:           ActionLifecycleSucceeded,
			Summary:          "Command completed.",
		},
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("r should rerun from a command result")
	}
	if model.commandPanel.Lifecycle != ActionLifecycleRunning {
		t.Fatalf("lifecycle = %s, want running", model.commandPanel.Lifecycle)
	}
	if !strings.Contains(RenderCommandResult(testMode(), model.commandPanel.Result, false), "r rerun") {
		t.Fatal("command result footer should advertise rerun")
	}
}
