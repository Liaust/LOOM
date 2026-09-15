package ui

import "testing"

func TestResolveModeJSONIsMachineSafe(t *testing.T) {
	mode, err := ResolveMode(ResolveModeOptions{
		Output: OutputJSON,
		Env:    Env{},
		Terminal: TerminalInfo{
			StdinTTY:     true,
			StdoutTTY:    true,
			StderrTTY:    true,
			Width:        120,
			Height:       40,
			Term:         "xterm-256color",
			ColorProfile: ColorTrueColor,
			TrueColor:    true,
		},
	})
	if err != nil {
		t.Fatalf("ResolveMode returned error: %v", err)
	}
	if !mode.IsMachine() || mode.CanPrompt() || mode.CanColor() || mode.CanAnimate() || mode.CanUsePortal() {
		t.Fatalf("json mode should be machine-safe, got %#v", mode)
	}
}

func TestResolveModePlainIsMachineSafe(t *testing.T) {
	mode, err := ResolveMode(ResolveModeOptions{
		Output: OutputPlain,
		Env:    Env{},
		Terminal: TerminalInfo{
			StdinTTY:     true,
			StdoutTTY:    true,
			StderrTTY:    true,
			Width:        120,
			Height:       40,
			Term:         "xterm-256color",
			ColorProfile: ColorTrueColor,
		},
	})
	if err != nil {
		t.Fatalf("ResolveMode returned error: %v", err)
	}
	if !mode.IsMachine() || mode.CanPrompt() || mode.CanColor() || mode.CanAnimate() || mode.CanUsePortal() {
		t.Fatalf("plain mode should be machine-safe, got %#v", mode)
	}
}

func TestResolveModeEnvironmentDisablesInteractiveColorAndAnimation(t *testing.T) {
	mode, err := ResolveMode(ResolveModeOptions{
		Output: OutputTable,
		Env: Env{
			"LOOM_NONINTERACTIVE": "1",
			"LOOM_NO_COLOR":       "1",
			"LOOM_NO_ANIMATION":   "1",
		},
		Terminal: TerminalInfo{
			StdinTTY:     true,
			StdoutTTY:    true,
			StderrTTY:    true,
			Width:        120,
			Height:       40,
			Term:         "xterm-256color",
			ColorProfile: ColorTrueColor,
		},
	})
	if err != nil {
		t.Fatalf("ResolveMode returned error: %v", err)
	}
	if mode.CanPrompt() || mode.CanColor() || mode.CanAnimate() || mode.CanUsePortal() {
		t.Fatalf("env-disabled mode should not prompt/color/animate/use portal, got %#v", mode)
	}
}

func TestResolveModeCIDisablesPrompt(t *testing.T) {
	mode, err := ResolveMode(ResolveModeOptions{
		Output: OutputTable,
		Env:    Env{"CI": "1"},
		Terminal: TerminalInfo{
			StdinTTY:     true,
			StdoutTTY:    true,
			StderrTTY:    true,
			Width:        120,
			Height:       40,
			Term:         "xterm-256color",
			ColorProfile: ColorTrueColor,
		},
	})
	if err != nil {
		t.Fatalf("ResolveMode returned error: %v", err)
	}
	if mode.CanPrompt() || mode.CanUsePortal() {
		t.Fatalf("CI should disable prompt and portal, got %#v", mode)
	}
}

func TestResolveModeTermDumbDisablesPortalAndColor(t *testing.T) {
	mode, err := ResolveMode(ResolveModeOptions{
		Output: OutputTable,
		Env:    Env{},
		Terminal: TerminalInfo{
			StdinTTY:     true,
			StdoutTTY:    true,
			StderrTTY:    true,
			Width:        120,
			Height:       40,
			Term:         "dumb",
			ColorProfile: ColorNone,
		},
	})
	if err != nil {
		t.Fatalf("ResolveMode returned error: %v", err)
	}
	if mode.CanColor() || mode.CanAnimate() || mode.CanUsePortal() {
		t.Fatalf("TERM=dumb should disable rich output, got %#v", mode)
	}
}

func TestResolveModeRejectsUnknownTheme(t *testing.T) {
	_, err := ResolveMode(ResolveModeOptions{
		Output: OutputTable,
		Env:    Env{},
		Terminal: TerminalInfo{
			StdinTTY:  true,
			StdoutTTY: true,
			StderrTTY: true,
			Width:     120,
			Height:    40,
			Term:      "xterm-256color",
		},
		ThemeName: "unknown",
	})
	if err == nil {
		t.Fatal("expected unknown theme error")
	}
}
