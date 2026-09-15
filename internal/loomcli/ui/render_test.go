package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestRendererNoColorProducesNoANSI(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	mode := Mode{
		Output:    OutputTable,
		Color:     false,
		ThemeName: ThemeCoffee,
		TTY:       TerminalInfo{ColorProfile: ColorTrueColor},
	}
	renderer := NewRenderer(mode, &stdout, &stderr)
	renderer.Section("LOOM")
	renderer.Status("Health", "healthy")
	renderer.CommandPreview(CommandPreview{
		Command: []string{"loom", "workers", "list"},
		Effect:  "Lists workers.",
		Risk:    "read_only",
	})
	if ContainsANSI(stdout.String()) {
		t.Fatalf("expected no ANSI, got: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "loom workers list") {
		t.Fatalf("expected command preview, got: %s", stdout.String())
	}
}

func TestRendererColorCanEmitANSI(t *testing.T) {
	var stdout bytes.Buffer
	mode := Mode{
		Output:    OutputTable,
		Color:     true,
		ThemeName: ThemeCoffee,
		TTY:       TerminalInfo{ColorProfile: ColorTrueColor},
	}
	renderer := NewRenderer(mode, &stdout, &stdout)
	renderer.Section("LOOM")
	if !ContainsANSI(stdout.String()) {
		t.Fatalf("expected ANSI in colored output, got: %q", stdout.String())
	}
}
