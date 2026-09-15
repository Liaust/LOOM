package portal

import (
	"fmt"
	"strings"
	"testing"

	"loom.local/loom/internal/loomcli/actions"
)

func TestPortalFootersAndHelpAdvertiseImplementedKeys(t *testing.T) {
	snapshot := fakeSnapshot()
	home := RenderScreenWithState(RenderInput{
		Mode:         testMode(),
		HomeSnapshot: snapshot,
		Registry:     actions.DefaultRegistry(),
		Screen:       ScreenHome,
		State:        ScreenStateFromSnapshot(ScreenHome, snapshot),
	})
	for _, want := range []string{"/ search", "j/k move", "enter open", "esc back", "r refresh", "tab details", "? help", "q quit"} {
		if !strings.Contains(home, want) {
			t.Fatalf("home footer missing %q:\n%s", want, home)
		}
	}

	database := RenderScreenWithState(RenderInput{
		Mode:     testMode(),
		Registry: actions.DefaultRegistry(),
		Screen:   ScreenDatabase,
		State:    LoadScreenFromSnapshot(ScreenDatabase, snapshot).State,
	})
	if !strings.Contains(database, "s object diagnostics search") || !strings.Contains(database, "? help") {
		t.Fatalf("database footer missing keys:\n%s", database)
	}

	command := RenderCommandPalette(testMode(), CommandModeState{Input: "$ health", Preview: BuildCommandPreview("$ health")}, 80, 24)
	if !strings.Contains(command, "enter preview/run") || !strings.Contains(command, "tab complete") {
		t.Fatalf("command footer missing keys:\n%s", command)
	}

	help := RenderHelp(testMode())
	for _, want := range []string{"search screens and navigation", "#capabilities", "raw LOOM command mode", "object diagnostics search", "preview / confirm", "complete in command mode"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q:\n%s", want, help)
		}
	}
}

func TestNarrowPortalRendersDoNotPanicAndKeepCoreLabels(t *testing.T) {
	mode := testMode()
	mode.TTY.Width = 40
	snapshot := fakeSnapshot()
	for _, tc := range []struct {
		screen string
		state  ScreenState
		want   string
	}{
		{ScreenHome, ScreenStateFromSnapshot(ScreenHome, snapshot), "Home"},
		{ScreenBackground, LoadScreenFromSnapshot(ScreenBackground, snapshot).State, "Background Operations"},
		{ScreenDatabase, LoadScreenFromSnapshot(ScreenDatabase, snapshot).State, "Object Store Diagnostics"},
	} {
		t.Run(tc.screen, func(t *testing.T) {
			output := RenderScreenWithState(RenderInput{Mode: mode, HomeSnapshot: snapshot, Registry: actions.DefaultRegistry(), Screen: tc.screen, State: tc.state, Width: 40, Height: 20})
			if !strings.Contains(output, tc.want) {
				t.Fatalf("narrow render missing %q:\n%s", tc.want, output)
			}
		})
	}

	command := RenderCommandPalette(mode, CommandModeState{Input: "$ health", Preview: BuildCommandPreview("$ health")}, 40, 20)
	if !strings.Contains(command, "LOOM Command") || !strings.Contains(command, "Command Palette") {
		t.Fatalf("narrow command render missing core labels:\n%s", command)
	}

	action := PortalActionFromRegistry(actions.Action{ID: "test.inspect", Title: "Inspect", Description: "Inspect test data", Risk: actions.RiskReadOnly, Enabled: true, ExecutionKind: PortalExecutorUnsupported})
	preview := RenderPortalActionPreview(mode, ActionPanelState{Lifecycle: ActionLifecyclePreview, Action: action})
	if !strings.Contains(preview, "Action Preview") || !strings.Contains(preview, "Inspect") {
		t.Fatalf("narrow action preview missing core labels:\n%s", preview)
	}
}

func TestHomeBodyAndFooterWrapWithinNarrowWidths(t *testing.T) {
	snapshot := fakeSnapshot()
	for _, width := range []int{60, 80} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			mode := testMode()
			mode.TTY.Width = width
			output := RenderScreenWithState(RenderInput{
				Mode:         mode,
				HomeSnapshot: snapshot,
				Registry:     actions.DefaultRegistry(),
				Screen:       ScreenHome,
				State:        ScreenStateFromSnapshot(ScreenHome, snapshot),
				Width:        width,
				Height:       80,
			})
			view := splitPortalRenderedView(output)
			for _, line := range append(append([]string{}, view.Body...), view.Footer...) {
				if got := len(stripANSI(line)); got > width {
					t.Fatalf("line width = %d, want <= %d:\n%q\n\n%s", got, width, line, output)
				}
			}
		})
	}
}

func TestHealthStripWrapsAndPreservesItems(t *testing.T) {
	mode := testMode()
	restore := setActiveRenderContext(renderContext{Mode: mode, Styles: NewPortalStyles(mode), Width: 52, Height: 24})
	defer restore()

	var builder strings.Builder
	renderHealthStrip(&builder,
		portalHealthItem{Domain: portalDomainStorage, Label: "box", Status: "ok", Detail: "workspace at /Users/operator/loom-box"},
		portalHealthItem{Domain: portalDomainJobs, Label: "workers", Status: "ok", Detail: "16 active, 0 attention"},
		portalHealthItem{Domain: portalDomainNetwork, Label: "events", Status: "ok", Detail: "0 active endpoints"},
	)
	output := strings.TrimRight(builder.String(), "\n")
	for _, want := range []string{"[storage] box=ok", "workspace at", "[jobs] workers=ok", "[network] events=ok"} {
		if !strings.Contains(output, want) {
			t.Fatalf("wrapped health strip missing %q:\n%s", want, output)
		}
	}
	lines := strings.Split(output, "\n")
	if len(lines) < 2 {
		t.Fatalf("health strip should wrap at narrow width:\n%s", output)
	}
	for _, line := range lines {
		if got := len(stripANSI(line)); got > 52 {
			t.Fatalf("line width = %d, want <= 52: %q", got, line)
		}
	}
}

func TestNoColorRenderMatrixHasNoANSI(t *testing.T) {
	mode := testMode()
	snapshot := fakeSnapshot()
	inspectAction := PortalActionFromRegistry(actions.Action{ID: "test.inspect", Title: "Inspect", Description: "Inspect test data", Risk: actions.RiskReadOnly, Enabled: true, ExecutionKind: PortalExecutorUnsupported})
	renders := map[string]string{
		"home":           RenderScreenWithState(RenderInput{Mode: mode, HomeSnapshot: snapshot, Registry: actions.DefaultRegistry(), Screen: ScreenHome, State: ScreenStateFromSnapshot(ScreenHome, snapshot)}),
		"background":     RenderScreenWithState(RenderInput{Mode: mode, HomeSnapshot: snapshot, Registry: actions.DefaultRegistry(), Screen: ScreenBackground, State: LoadScreenFromSnapshot(ScreenBackground, snapshot).State}),
		"database":       RenderScreenWithState(RenderInput{Mode: mode, HomeSnapshot: snapshot, Registry: actions.DefaultRegistry(), Screen: ScreenDatabase, State: LoadScreenFromSnapshot(ScreenDatabase, snapshot).State}),
		"search":         RenderPortalSearch(mode, "workers", SearchPortalActions(actions.DefaultRegistry(), "workers", 8, false)),
		"action_preview": RenderPortalActionPreview(mode, ActionPanelState{Lifecycle: ActionLifecyclePreview, Action: inspectAction}),
		"action_result":  RenderPortalActionResult(mode, ActionPanelState{Lifecycle: ActionLifecycleSucceeded, Result: PortalActionResult{ActionID: "test.inspect", Title: "Inspect", Status: ActionLifecycleSucceeded, Summary: "ok"}}),
		"command":        RenderCommandPalette(mode, CommandModeState{Input: "$ health", Preview: BuildCommandPreview("$ health")}, 80, 24),
		"command_result": RenderCommandResult(mode, CommandResult{Input: "$ health", CanonicalTokens: []string{"health"}, CanonicalCommand: "loom health", Classification: CommandClassInspect, Status: ActionLifecycleSucceeded, Summary: "ok"}, false),
	}
	for name, output := range renders {
		if strings.Contains(output, "\x1b[") {
			t.Fatalf("%s render contains ANSI: %q", name, output)
		}
	}
}

func TestNoBootAnimationOptionDisablesBootState(t *testing.T) {
	mode := colorMode()
	animated := NewModelWithOptions(ModelOptions{Mode: mode, Snapshot: fakeSnapshot(), Registry: actions.DefaultRegistry(), StartScreen: ScreenHome})
	if !animated.boot.Active {
		t.Fatal("interactive color mode should enable boot animation by default")
	}
	skipped := NewModelWithOptions(ModelOptions{Mode: mode, Snapshot: fakeSnapshot(), Registry: actions.DefaultRegistry(), StartScreen: ScreenHome, NoBootAnimation: true})
	if skipped.boot.Active {
		t.Fatal("NoBootAnimation should disable boot state")
	}
}
