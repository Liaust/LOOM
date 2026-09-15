package ui

import (
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

func DefaultStreams() Streams {
	return Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}
}

type ColorProfile string

const (
	ColorNone      ColorProfile = "none"
	ColorANSI      ColorProfile = "ansi"
	ColorANSI256   ColorProfile = "ansi256"
	ColorTrueColor ColorProfile = "truecolor"
)

type TerminalInfo struct {
	StdinTTY     bool         `json:"stdin_tty"`
	StdoutTTY    bool         `json:"stdout_tty"`
	StderrTTY    bool         `json:"stderr_tty"`
	Width        int          `json:"width"`
	Height       int          `json:"height"`
	Term         string       `json:"term"`
	ColorProfile ColorProfile `json:"color_profile"`
	TrueColor    bool         `json:"true_color"`
}

type fdReader interface {
	Fd() uintptr
}

func DetectTerminal(streams Streams, env Env) TerminalInfo {
	info := TerminalInfo{
		Term: env.Get("TERM"),
	}
	if in, ok := streams.In.(fdReader); ok {
		info.StdinTTY = term.IsTerminal(int(in.Fd()))
	}
	if out, ok := streams.Out.(fdReader); ok {
		fd := int(out.Fd())
		info.StdoutTTY = term.IsTerminal(fd)
		if info.StdoutTTY {
			info.Width, info.Height, _ = term.GetSize(fd)
		}
	}
	if errOut, ok := streams.Err.(fdReader); ok {
		fd := int(errOut.Fd())
		info.StderrTTY = term.IsTerminal(fd)
		if info.Width == 0 || info.Height == 0 {
			info.Width, info.Height, _ = term.GetSize(fd)
		}
	}
	info.ColorProfile, info.TrueColor = detectColorProfile(info, env)
	return info
}

func detectColorProfile(info TerminalInfo, env Env) (ColorProfile, bool) {
	if !info.StdoutTTY && !info.StderrTTY {
		return ColorNone, false
	}
	if strings.EqualFold(info.Term, "dumb") || env.IsSet("NO_COLOR") || env.IsSet("LOOM_NO_COLOR") {
		return ColorNone, false
	}
	if strings.Contains(strings.ToLower(env.Get("COLORTERM")), "truecolor") || strings.Contains(strings.ToLower(env.Get("COLORTERM")), "24bit") {
		return ColorTrueColor, true
	}
	if strings.Contains(info.Term, "256color") {
		return ColorANSI256, false
	}
	return ColorANSI, false
}
