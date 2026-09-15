package portal

import (
	"bytes"
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"loom.local/loom/internal/loomcli/actions"
	"loom.local/loom/internal/loomcli/ui"
	"loom.local/loom/internal/search"
)

func TestV07PortalSurfaceRenderMatrix(t *testing.T) {
	for _, screen := range Screens() {
		want := screen.Title
		if screen.ID == ScreenHome {
			want = "LOOM Portal"
		}
		t.Run(screen.ID, func(t *testing.T) {
			output := runV07OneShotPortal(t, Options{StartScreen: screen.ID})
			assertV07PortalRender(t, output, want)
			if !strings.Contains(output, "Keys:") {
				t.Fatalf("surface %s missing footer key hints:\n%s", screen.ID, output)
			}
			t.Logf("%s rendered %d lines", screen.ID, v07PortalLineCount(output))
		})
	}
}

func TestV07PortalOverlayRenderMatrix(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want []string
	}{
		{
			name: "navigation_search",
			opts: Options{SearchQuery: "status"},
			want: []string{"Command Palette", "Search", "[screen]"},
		},
		{
			name: "scope_picker",
			opts: Options{SearchQuery: "#"},
			want: []string{"Scoped Search", "#capabilities", "#database"},
		},
		{
			name: "capability_scoped_search",
			opts: Options{SearchQuery: "#capabilities main@"},
			want: []string{"Capability Search", "main@system"},
		},
		{
			name: "storage_scoped_search",
			opts: Options{SearchQuery: "#storage report"},
			want: []string{"Storage Search"},
		},
		{
			name: "command_mode_search",
			opts: Options{SearchQuery: "$ list capabilities"},
			want: []string{"LOOM Command", "Canonical: loom capabilities list"},
		},
		{
			name: "command_completion",
			opts: Options{CommandCompleteInput: "$ cap"},
			want: []string{"LOOM Command", "Completions", "capabilities"},
		},
		{
			name: "command_preview",
			opts: Options{CommandPreviewInput: "$ health"},
			want: []string{"Command Preview", "Canonical: loom health"},
		},
		{
			name: "database_search",
			opts: Options{DatabaseSearchQuery: "portal"},
			want: []string{"Object Diagnostics Search", "Portal Test Object"},
		},
		{
			name: "action_preview",
			opts: Options{PreviewActionID: "raw.workers.list"},
			want: []string{"Action Preview", "Raw: loom workers list"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output := runV07OneShotPortal(t, tc.opts)
			assertV07PortalRender(t, output, tc.want...)
			if !strings.Contains(output, "Keys:") {
				t.Fatalf("overlay %s missing footer key hints:\n%s", tc.name, output)
			}
			t.Logf("%s rendered %d lines", tc.name, v07PortalLineCount(output))
		})
	}
}

func TestV07PortalRenderLineCountGuardrails(t *testing.T) {
	budgets := map[string]int{
		ScreenHome:         140,
		ScreenDoctor:       80,
		ScreenTimeline:     180,
		ScreenBox:          120,
		ScreenNotes:        140,
		ScreenStorage:      160,
		ScreenProjects:     160,
		ScreenServices:     120,
		ScreenDatabase:     140,
		ScreenBackground:   260,
		ScreenAutomations:  180,
		ScreenJobs:         260,
		ScreenNodes:        140,
		ScreenCapabilities: 100,
	}
	for _, screen := range Screens() {
		t.Run(screen.ID, func(t *testing.T) {
			output := runV07OneShotPortal(t, Options{StartScreen: screen.ID})
			lines := v07PortalLineCount(output)
			budget := budgets[screen.ID]
			if budget == 0 {
				t.Fatalf("missing line budget for screen %s", screen.ID)
			}
			if lines > budget {
				t.Fatalf("screen %s rendered %d lines, over baseline guardrail %d", screen.ID, lines, budget)
			}
		})
	}
}

func TestV07PortalSearchModeBoundaries(t *testing.T) {
	capabilityState := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenCapabilities, Snapshot{}).State
	defaultResults := SearchPortal(actions.DefaultRegistry(), capabilityState, "main@system", 20)
	for _, result := range defaultResults {
		if result.Category == "capability" || result.Category == "provider" {
			t.Fatalf("default navigation search should not expose scoped capability/provider records: %#v", defaultResults)
		}
	}

	scopedResults := SearchPortal(actions.DefaultRegistry(), capabilityState, "#capabilities main@", 20)
	if len(scopedResults) == 0 {
		t.Fatal("expected scoped capability results")
	}
	foundCapabilityOrProvider := false
	for _, result := range scopedResults {
		if result.Category == "capability" || result.Category == "provider" {
			foundCapabilityOrProvider = true
			break
		}
	}
	if !foundCapabilityOrProvider {
		t.Fatalf("scoped capability search should expose capability/provider records: %#v", scopedResults)
	}

	rawResults := SearchPortalActions(actions.DefaultRegistry(), "raw", 20, false)
	for _, result := range rawResults {
		if strings.HasPrefix(result.Action.ID, "raw.") || result.Category == "raw" {
			t.Fatalf("default portal search should hide raw actions: %#v", rawResults)
		}
	}
}

func TestV07ScopedSearchShowsOnlyCoreScopes(t *testing.T) {
	state := ScreenStateFromSnapshot(ScreenHome, fakeSnapshot())
	results := SearchPortal(actions.DefaultRegistry(), state, "#", 20)
	want := []string{"#doctor", "#box", "#projects", "#capabilities", "#storage", "#database", "#workers", "#automation", "#jobs", "#nodes"}
	for _, scope := range want {
		if !searchResultsContainTitle(results, scope) {
			t.Fatalf("core scope %s missing from scoped search: %#v", scope, results)
		}
	}
	for _, oldScope := range []string{"#objects", "#archives", "#transfers", "#cloud", "#schedules", "#indexes"} {
		if searchResultsContainTitle(results, oldScope) {
			t.Fatalf("legacy alias %s should not be a first-class scope: %#v", oldScope, results)
		}
	}
}

func TestRetiredIntakeNamesAreNotBoxSearchAliases(t *testing.T) {
	for _, input := range []string{"#dropzone status", "#lane pending", "#loom-lane pending", "#dropzone-transfer failed"} {
		parsed := ParsePortalSearchInput(input)
		if parsed.Scope == "box" {
			t.Fatalf("retired intake search %q still aliases #box: %#v", input, parsed)
		}
	}
	boxScope := portalSearchScopes()[1]
	joined := strings.ToLower(boxScope.Description + " " + strings.Join(boxScope.Keywords, " "))
	for _, retired := range []string{"dropzone", "loom lane"} {
		if strings.Contains(joined, retired) {
			t.Fatalf("#box search metadata exposes retired intake %q: %#v", retired, boxScope)
		}
	}
}

func TestV07PortalSearchRowsAreCompactByDefault(t *testing.T) {
	results := SearchPortal(actions.DefaultRegistry(), ScreenStateFromSnapshot(ScreenHome, fakeSnapshot()), "storage", 8)
	output := RenderPortalSearchWithSelection(testMode(), "storage", results, 0, 80, 20)
	if strings.Contains(output, "inspect") {
		t.Fatalf("safe inspect risk labels should not clutter navigation search:\n%s", output)
	}
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	selectedDetailSeen := false
	nonSelectedDetailSeen := false
	for idx, line := range lines {
		if strings.HasPrefix(line, "> ") && idx+1 < len(lines) && strings.HasPrefix(lines[idx+1], "  ") {
			selectedDetailSeen = true
			continue
		}
		if strings.HasPrefix(line, "  ") && strings.Contains(line, "Open") {
			nonSelectedDetailSeen = true
		}
	}
	if !selectedDetailSeen {
		t.Fatalf("selected result should expose its description:\n%s", output)
	}
	if nonSelectedDetailSeen {
		t.Fatalf("non-selected rows should stay compact:\n%s", output)
	}
}

func TestV07HelpMentionsScreensSearchAndGuidedActions(t *testing.T) {
	output := RenderHelp(testMode())
	for _, want := range []string{
		"start screens: home, doctor, timeline, box, notes, storage",
		"#capabilities main@system",
		"#doctor, #storage, #database, #workers",
		"type $ in search for raw LOOM command mode",
		"guided actions open as previews or forms before they run",
		"timeline opens running work and grouped operation history with status",
		"trackpad/wheel, pgup/pgdn, or ctrl+u/ctrl+d scroll",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("help output missing %q:\n%s", want, output)
		}
	}
}

func TestV07HomeLogoSpacingAndViewportTitle(t *testing.T) {
	logo := PortalLogo{ID: "test", Name: "Test", Lines: []string{"LOOM"}, MinWidth: 4, Weight: 1}
	state := ScreenStateFromSnapshot(ScreenHome, fakeSnapshot())
	output := RenderScreenWithState(RenderInput{Mode: testMode(), HomeSnapshot: fakeSnapshot(), Registry: actions.DefaultRegistry(), Screen: ScreenHome, State: state, Width: 80, Height: 20, Logo: logo})
	if !strings.Contains(output, "LOOM\n\nLOOM Portal\n\n") {
		t.Fatalf("home logo should keep a blank line before and after the portal label:\n%s", output)
	}

	mode := v07InteractiveNoColorMode(80, 12)
	model := NewModelWithOptions(ModelOptions{
		Mode:            mode,
		Client:          newFakePortalClient(),
		CorrelationID:   "corr_test",
		Snapshot:        fakeSnapshot(),
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenHome,
		NoBootAnimation: true,
	})
	view := model.View()
	if !strings.Contains(view, "LOOM Portal") || !strings.Contains(view, "Keys:") {
		t.Fatalf("short viewport should preserve Home title and footer:\n%s", view)
	}
}

func TestV07InteractivePortalUsesViewportInsteadOfClipping(t *testing.T) {
	mode := v07InteractiveNoColorMode(80, 16)
	model := NewModelWithOptions(ModelOptions{
		Mode:            mode,
		Client:          newFakePortalClient(),
		CorrelationID:   "corr_test",
		Snapshot:        fakeSnapshot(),
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenStorage,
		NoBootAnimation: true,
	})
	model.setScreenState(ScreenStorage, LoadScreenFromSnapshot(ScreenStorage, fakeSnapshot()).State)

	first := model.View()
	if strings.Contains(first, "... output clipped") {
		t.Fatalf("viewport render should not use clipped warning:\n%s", first)
	}
	if !strings.Contains(first, "Keys:") {
		t.Fatalf("viewport render should keep footer visible:\n%s", first)
	}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	model = updated.(Model)
	second := model.View()
	if first == second {
		t.Fatalf("page down should change visible viewport output")
	}
	if !strings.Contains(second, "Keys:") || !strings.Contains(second, "Scroll:") {
		t.Fatalf("scrolled viewport should keep footer and show position:\n%s", second)
	}
}

func TestV07InteractivePortalSupportsMouseWheelScroll(t *testing.T) {
	mode := v07InteractiveNoColorMode(80, 16)
	model := NewModelWithOptions(ModelOptions{
		Mode:            mode,
		Client:          newFakePortalClient(),
		CorrelationID:   "corr_test",
		Snapshot:        fakeSnapshot(),
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenStorage,
		NoBootAnimation: true,
	})
	model.setScreenState(ScreenStorage, LoadScreenFromSnapshot(ScreenStorage, fakeSnapshot()).State)

	first := model.View()
	updated, _ := model.Update(tea.MouseMsg{Type: tea.MouseWheelDown})
	model = updated.(Model)
	second := model.View()
	if first == second {
		t.Fatalf("mouse wheel down should change visible viewport output")
	}
	if !strings.Contains(second, "Keys:") || !strings.Contains(second, "Scroll:") {
		t.Fatalf("mouse scrolled viewport should keep footer and show position:\n%s", second)
	}

	updated, _ = model.Update(tea.MouseMsg{Type: tea.MouseWheelUp})
	model = updated.(Model)
	third := model.View()
	if third == second {
		t.Fatalf("mouse wheel up should change visible viewport output")
	}
}

func TestV07PortalSearchViewportKeepsSelectionVisible(t *testing.T) {
	mode := v07InteractiveNoColorMode(80, 9)
	model := NewModelWithOptions(ModelOptions{
		Mode:            mode,
		Client:          newFakePortalClient(),
		CorrelationID:   "corr_test",
		Snapshot:        fakeSnapshot(),
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenHome,
		NoBootAnimation: true,
	})
	model.searchOpen = true
	model.searchText = "#"
	if len(model.searchResults()) < 6 {
		t.Fatalf("expected enough scoped search results to test scrolling")
	}
	_ = model.View()
	for idx := 0; idx < 6; idx++ {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
		model = updated.(Model)
	}
	selected := model.searchResults()[model.searchIndex]
	selectedTitle := firstNonEmpty(selected.Title, selected.Action.Label)
	output := model.View()
	if !strings.Contains(output, "Scoped Search") || !strings.Contains(output, "Search  #") {
		t.Fatalf("search header should stay fixed while results scroll:\n%s", output)
	}
	if !strings.Contains(output, selectedTitle) {
		t.Fatalf("selected search result %q should stay visible:\n%s", selectedTitle, output)
	}
	if !strings.Contains(output, "Scroll:") || !strings.Contains(output, "Keys:") {
		t.Fatalf("scrolled search should show scroll position and footer:\n%s", output)
	}
}

func TestV07PortalSearchSupportsMouseWheelScroll(t *testing.T) {
	mode := v07InteractiveNoColorMode(80, 9)
	model := NewModelWithOptions(ModelOptions{
		Mode:            mode,
		Client:          newFakePortalClient(),
		CorrelationID:   "corr_test",
		Snapshot:        fakeSnapshot(),
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenHome,
		NoBootAnimation: true,
	})
	model.searchOpen = true
	model.searchText = "#"
	if len(model.searchResults()) < 6 {
		t.Fatalf("expected enough scoped search results to test mouse scrolling")
	}
	first := model.View()
	updated, _ := model.Update(tea.MouseMsg{Type: tea.MouseWheelDown})
	model = updated.(Model)
	second := model.View()
	if first == second {
		t.Fatalf("mouse wheel down should scroll search results")
	}
	if !strings.Contains(second, "Scoped Search") || !strings.Contains(second, "Scroll:") || !strings.Contains(second, "Keys:") {
		t.Fatalf("mouse-scrolled search should preserve header, indicator, and footer:\n%s", second)
	}
}

func TestV07CommandCompletionViewportKeepsSelectionVisible(t *testing.T) {
	mode := v07InteractiveNoColorMode(80, 10)
	model := NewModelWithOptions(ModelOptions{
		Mode:            mode,
		Client:          newFakePortalClient(),
		CorrelationID:   "corr_test",
		Snapshot:        fakeSnapshot(),
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenHome,
		NoBootAnimation: true,
	})
	model.searchOpen = true
	model.searchText = "$ "
	model.commandMode = CommandModeState{
		Active:         true,
		Input:          "$ ",
		Cursor:         len("$ "),
		Preview:        BuildCommandPreview("$ "),
		CompletionOpen: true,
		Suggestions:    CompleteCommand(context.Background(), nil, "corr_test", "$ ", len("$ ")),
	}
	if len(model.commandMode.Suggestions) < 8 {
		t.Fatalf("expected enough command suggestions to test scrolling")
	}
	_ = model.View()
	for idx := 0; idx < 8; idx++ {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
		model = updated.(Model)
	}
	selected := model.commandMode.Suggestions[model.commandMode.SelectedSuggestion]
	output := model.View()
	if !strings.Contains(output, "LOOM Command") || !strings.Contains(output, "Search  $") {
		t.Fatalf("command header should stay fixed while completions scroll:\n%s", output)
	}
	if !strings.Contains(output, selected.Label) {
		t.Fatalf("selected command suggestion %q should stay visible:\n%s", selected.Label, output)
	}
	if !strings.Contains(output, "Scroll:") || !strings.Contains(output, "Keys:") {
		t.Fatalf("scrolled command suggestions should show scroll position and footer:\n%s", output)
	}
}

func TestV07DatabaseSearchViewportKeepsSelectionVisible(t *testing.T) {
	mode := v07InteractiveNoColorMode(80, 10)
	model := NewModelWithOptions(ModelOptions{
		Mode:            mode,
		Client:          newFakePortalClient(),
		CorrelationID:   "corr_test",
		Snapshot:        fakeSnapshot(),
		Registry:        actions.DefaultRegistry(),
		StartScreen:     ScreenDatabase,
		NoBootAnimation: true,
	})
	results := make([]search.SearchResult, 12)
	for idx := range results {
		results[idx] = search.SearchResult{
			SearchDocumentID: "doc",
			ObjectID:         "object",
			Title:            "Portal Result " + string(rune('A'+idx)),
			Snippet:          "database result used to verify viewport scrolling",
			SourceNodeID:     "main",
			ScopeID:          "notes",
			FreshnessState:   "current",
			RankScore:        float64(12 - idx),
		}
	}
	state := model.currentScreenState()
	state.Data.Database.Search = DatabaseSearchData{
		Query:  "portal",
		Status: ScreenLoadLoaded,
		ResultSet: search.SearchResultSet{
			Query:       "portal",
			ResultCount: len(results),
			Results:     results,
		},
	}
	model.setScreenState(ScreenDatabase, state)
	model.dbSearchOpen = true
	model.dbSearchText = "portal"
	_ = model.View()
	for idx := 0; idx < 8; idx++ {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
		model = updated.(Model)
	}
	selected := state.Data.Database.Search.ResultSet.Results[model.dbSearchIndex]
	output := model.View()
	if !strings.Contains(output, "Object Diagnostics Search") || !strings.Contains(output, "Query  portal") {
		t.Fatalf("database search header should stay fixed while results scroll:\n%s", output)
	}
	if !strings.Contains(output, selected.Title) {
		t.Fatalf("selected database result %q should stay visible:\n%s", selected.Title, output)
	}
	if !strings.Contains(output, "Scroll:") || !strings.Contains(output, "Keys:") {
		t.Fatalf("scrolled database search should show scroll position and footer:\n%s", output)
	}
}

func runV07OneShotPortal(t *testing.T, opts Options) string {
	t.Helper()
	var out bytes.Buffer
	opts.Mode = testMode()
	opts.Client = newFakePortalClient()
	opts.CorrelationID = "corr_test"
	opts.Out = &out
	opts.ExitAfterRender = true
	opts.Registry = actions.DefaultRegistry()
	if err := Run(context.Background(), opts); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	return out.String()
}

func assertV07PortalRender(t *testing.T, output string, wants ...string) {
	t.Helper()
	if strings.TrimSpace(output) == "" {
		t.Fatal("portal render was empty")
	}
	if strings.Contains(output, "\x1b[") {
		t.Fatalf("test mode render should not contain ANSI escapes:\n%q", output)
	}
	if strings.Contains(output, "... output clipped") {
		t.Fatalf("one-shot render should not be clipped:\n%s", output)
	}
	if strings.Contains(output, "Could not load") {
		t.Fatalf("portal render failed to load:\n%s", output)
	}
	for _, want := range wants {
		if !strings.Contains(output, want) {
			t.Fatalf("portal render missing %q:\n%s", want, output)
		}
	}
}

func v07PortalLineCount(output string) int {
	trimmed := strings.TrimRight(output, "\n")
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}

func v07InteractiveNoColorMode(width int, height int) ui.Mode {
	return ui.Mode{
		Output:        ui.OutputTable,
		Interactive:   true,
		PortalAllowed: true,
		Color:         false,
		Animation:     false,
		ThemeName:     ui.ThemeCoffee,
		TTY: ui.TerminalInfo{
			StdinTTY:  true,
			StdoutTTY: true,
			StderrTTY: true,
			Width:     width,
			Height:    height,
		},
	}
}
