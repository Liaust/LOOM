package ui

import (
	"strings"

	loomerrors "loom.local/loom/internal/errors"
)

type OutputMode string

const (
	OutputTable OutputMode = "table"
	OutputJSON  OutputMode = "json"
	OutputPlain OutputMode = "plain"
)

type ResolveModeOptions struct {
	Output        OutputMode
	NoInteractive bool
	NoColor       bool
	NoAnimation   bool
	ThemeName     string
	Compact       bool
	Env           Env
	Terminal      TerminalInfo
}

type Mode struct {
	Output        OutputMode
	Interactive   bool
	PortalAllowed bool
	Color         bool
	Animation     bool
	Compact       bool
	ThemeName     string
	TTY           TerminalInfo
	CI            bool
	Reasons       []string
}

func ResolveMode(opts ResolveModeOptions) (Mode, error) {
	env := opts.Env
	if env == nil {
		env = Env{}
	}
	output := opts.Output
	if strings.TrimSpace(string(output)) == "" {
		output = OutputTable
	}
	themeName := strings.TrimSpace(opts.ThemeName)
	if themeName == "" {
		themeName = strings.TrimSpace(env.Get("LOOM_THEME"))
	}
	if themeName == "" {
		themeName = ThemeCoffee
	}
	if _, err := ResolveTheme(themeName); err != nil {
		return Mode{}, err
	}

	mode := Mode{
		Output:      output,
		Interactive: true,
		Color:       true,
		Animation:   true,
		Compact:     opts.Compact,
		ThemeName:   themeName,
		TTY:         opts.Terminal,
		CI:          env.Get("CI") == "1" || strings.EqualFold(env.Get("CI"), "true"),
	}

	if mode.IsMachine() {
		mode.Interactive = false
		mode.Color = false
		mode.Animation = false
		mode.PortalAllowed = false
		mode.Reasons = append(mode.Reasons, "machine_output")
		return mode, nil
	}

	if opts.NoInteractive || env.Get("LOOM_NONINTERACTIVE") == "1" || strings.EqualFold(env.Get("LOOM_NONINTERACTIVE"), "true") {
		mode.Interactive = false
		mode.Reasons = append(mode.Reasons, "noninteractive_requested")
	}
	if mode.CI {
		mode.Interactive = false
		mode.Reasons = append(mode.Reasons, "ci")
	}
	if !opts.Terminal.StdinTTY {
		mode.Interactive = false
		mode.Reasons = append(mode.Reasons, "stdin_not_tty")
	}

	if opts.NoColor || env.IsSet("NO_COLOR") || env.IsSet("LOOM_NO_COLOR") || opts.Terminal.ColorProfile == ColorNone || strings.EqualFold(opts.Terminal.Term, "dumb") {
		mode.Color = false
		mode.Reasons = append(mode.Reasons, "color_disabled")
	}
	if opts.NoAnimation || env.Get("LOOM_NO_ANIMATION") == "1" || strings.EqualFold(env.Get("LOOM_NO_ANIMATION"), "true") {
		mode.Animation = false
		mode.Reasons = append(mode.Reasons, "animation_disabled")
	}
	if !mode.Color {
		mode.Animation = false
	}

	mode.PortalAllowed = mode.Interactive &&
		!strings.EqualFold(opts.Terminal.Term, "dumb") &&
		(opts.Terminal.StdoutTTY || opts.Terminal.StderrTTY) &&
		(opts.Terminal.Width == 0 || opts.Terminal.Width >= 80) &&
		(opts.Terminal.Height == 0 || opts.Terminal.Height >= 24)
	if !mode.PortalAllowed {
		mode.Reasons = append(mode.Reasons, "portal_not_available")
	}
	return mode, nil
}

func (m Mode) IsMachine() bool {
	return m.Output == OutputJSON || m.Output == OutputPlain
}

func (m Mode) CanPrompt() bool {
	return m.Interactive && !m.IsMachine()
}

func (m Mode) CanColor() bool {
	return m.Color && !m.IsMachine()
}

func (m Mode) CanAnimate() bool {
	return m.Animation && !m.IsMachine()
}

func (m Mode) CanUsePortal() bool {
	return m.PortalAllowed && !m.IsMachine()
}

func UnsupportedPortalError() error {
	return loomerrors.New("portal.unavailable", "cli", "portal", "The terminal portal is not available in this execution mode.")
}
