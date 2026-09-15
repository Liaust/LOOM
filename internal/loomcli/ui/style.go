package ui

import (
	"fmt"
	"strconv"
	"strings"
)

const ansiReset = "\x1b[0m"

type Style struct {
	prefix string
	suffix string
}

func (s Style) Render(value string) string {
	if s.prefix == "" {
		return value
	}
	return s.prefix + value + s.suffix
}

type Styles struct {
	Header    Style
	Muted     Style
	Accent    Style
	Danger    Style
	Warning   Style
	Success   Style
	Info      Style
	ID        Style
	Timestamp Style
	Code      Style
}

func NewStyles(mode Mode) Styles {
	theme, err := ResolveTheme(mode.ThemeName)
	if err != nil || !mode.CanColor() {
		return Styles{}
	}
	return Styles{
		Header:    styleFor(theme.Color(RoleAccent), true, mode.TTY.ColorProfile),
		Muted:     styleFor(theme.Color(RoleTextMuted), false, mode.TTY.ColorProfile),
		Accent:    styleFor(theme.Color(RoleAccent), false, mode.TTY.ColorProfile),
		Danger:    styleFor(theme.Color(RoleDanger), true, mode.TTY.ColorProfile),
		Warning:   styleFor(theme.Color(RoleWarning), false, mode.TTY.ColorProfile),
		Success:   styleFor(theme.Color(RoleSuccess), false, mode.TTY.ColorProfile),
		Info:      styleFor(theme.Color(RoleInfo), false, mode.TTY.ColorProfile),
		ID:        styleFor(theme.Color(RoleID), false, mode.TTY.ColorProfile),
		Timestamp: styleFor(theme.Color(RoleTimestamp), false, mode.TTY.ColorProfile),
		Code:      styleFor(theme.Color(RoleCode), false, mode.TTY.ColorProfile),
	}
}

func styleFor(hex string, bold bool, profile ColorProfile) Style {
	if profile == ColorNone {
		return Style{}
	}
	r, g, b, ok := parseHexColor(hex)
	if !ok {
		return Style{}
	}
	codes := []string{}
	if bold {
		codes = append(codes, "1")
	}
	switch profile {
	case ColorANSI, ColorANSI256:
		codes = append(codes, fmt.Sprintf("38;5;%d", rgbToANSI256(r, g, b)))
	default:
		codes = append(codes, fmt.Sprintf("38;2;%d;%d;%d", r, g, b))
	}
	return Style{prefix: "\x1b[" + strings.Join(codes, ";") + "m", suffix: ansiReset}
}

func parseHexColor(hex string) (int, int, int, bool) {
	hex = strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(hex) != 6 {
		return 0, 0, 0, false
	}
	r, errR := strconv.ParseInt(hex[0:2], 16, 64)
	g, errG := strconv.ParseInt(hex[2:4], 16, 64)
	b, errB := strconv.ParseInt(hex[4:6], 16, 64)
	if errR != nil || errG != nil || errB != nil {
		return 0, 0, 0, false
	}
	return int(r), int(g), int(b), true
}

func rgbToANSI256(r, g, b int) int {
	if r == g && g == b {
		if r < 8 {
			return 16
		}
		if r > 248 {
			return 231
		}
		return 232 + ((r - 8) * 24 / 247)
	}
	return 16 + (36 * ansiCube(r)) + (6 * ansiCube(g)) + ansiCube(b)
}

func ansiCube(v int) int {
	if v < 48 {
		return 0
	}
	if v < 115 {
		return 1
	}
	return (v - 35) / 40
}
