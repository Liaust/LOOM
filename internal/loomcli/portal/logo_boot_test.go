package portal

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestPortalLogosFitAndRender(t *testing.T) {
	for _, logo := range PortalLogos() {
		if logo.ID == "" || logo.Name == "" || len(logo.Lines) == 0 {
			t.Fatalf("invalid logo metadata: %#v", logo)
		}
		if logo.MaxWidth() <= 0 {
			t.Fatalf("logo %s max width = %d", logo.ID, logo.MaxWidth())
		}
		fitWidth := logo.MaxWidth()
		if logo.MinWidth > fitWidth {
			fitWidth = logo.MinWidth
		}
		if !logo.Fits(fitWidth) {
			t.Fatalf("logo %s should fit width %d", logo.ID, fitWidth)
		}
		for _, line := range logo.RenderLines(len(logo.Lines)) {
			if strings.HasSuffix(line, " ") {
				t.Fatalf("logo %s rendered trailing spaces: %q", logo.ID, line)
			}
		}
	}
}

func TestSelectPortalLogoFallsBackForNarrowTerminals(t *testing.T) {
	if !CompactPortalLogo().Fits(4) {
		t.Fatal("compact logo should fit narrow terminals")
	}
	if got := SelectPortalLogo(20, rand.New(rand.NewSource(1))); !got.Fits(20) {
		t.Fatalf("selected logo does not fit width 20: %#v", got)
	}
	if got := SelectPortalLogo(80, rand.New(rand.NewSource(1))); !got.Fits(80) {
		t.Fatalf("selected logo does not fit width 80: %#v", got)
	}
	foundWide := false
	for seed := int64(1); seed <= 20; seed++ {
		if got := SelectPortalLogo(80, rand.New(rand.NewSource(seed))); got.ID != CompactPortalLogo().ID {
			foundWide = true
			break
		}
	}
	if !foundWide {
		t.Fatal("wide terminal selection never returned an ASCII logo beyond compact")
	}
}

func TestSelectPortalLogoRespectsShortTerminalHeight(t *testing.T) {
	got := SelectPortalLogoForViewport(100, 12, rand.New(rand.NewSource(1)))
	if got.ID != CompactPortalLogo().ID {
		t.Fatalf("short terminal should use compact logo, got %#v", got)
	}
	got = SelectPortalLogoForViewport(100, 18, rand.New(rand.NewSource(1)))
	if !got.FitsHeight(18) || !got.Fits(100) {
		t.Fatalf("selected logo should fit viewport: %#v", got)
	}
}

func TestBootStateAdvancesCompletesAndRendersSkipHint(t *testing.T) {
	logo := PortalLogo{ID: "test", Name: "Test", Lines: []string{"LOOM", "PORTAL"}, MinWidth: 6, Weight: 1}
	boot := newBootState(logo)
	if !boot.Active || boot.FrameDelay <= 0 || boot.Duration <= 0 {
		t.Fatalf("invalid boot state: %#v", boot)
	}
	boot.StartedAt = time.Unix(0, 0)
	boot = boot.Advance(time.Unix(0, 0).Add(boot.FrameDelay))
	if boot.VisibleLines == 0 {
		t.Fatalf("boot did not advance: %#v", boot)
	}
	boot = boot.Complete()
	if boot.Active || boot.VisibleLines != len(logo.Lines) {
		t.Fatalf("boot did not complete: %#v", boot)
	}
	output := RenderBoot(renderContext{Mode: testMode(), Styles: NewPortalStyles(testMode()), Width: 80, Height: 24, Logo: logo}, boot)
	if !strings.Contains(output, "Keys: enter skip  q quit") || !strings.Contains(output, "LOOM") {
		t.Fatalf("boot render missing skip hint/logo:\n%s", output)
	}
}
