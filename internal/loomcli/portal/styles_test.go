package portal

import (
	"strings"
	"testing"
)

func TestClassifyStatusCoversPortalStatusGroups(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   statusClass
	}{
		{"success", []string{"ok", "healthy", "active", "completed", "succeeded", "indexed", "synced"}, statusClassSuccess},
		{"progress", []string{"pending", "queued", "running", "partial", "degraded", "due", "warning", "warn", "stale", "missed", "attention"}, statusClassProgress},
		{"danger", []string{"failed", "error", "critical", "danger", "denied", "unhealthy", "manual_action_required", "timed_out", "corrupt", "unavailable"}, statusClassDanger},
		{"muted", []string{"disabled", "paused", "skipped", "private_backup_only", "unknown", "-", ""}, statusClassMuted},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, value := range tc.values {
				if got := classifyStatus(value); got != tc.want {
					t.Fatalf("classifyStatus(%q) = %s, want %s", value, got, tc.want)
				}
			}
		})
	}
}

func TestRenderStatusAndCategoryArePlainInNoColorMode(t *testing.T) {
	if styles := NewPortalStyles(testMode()); styles.Enabled {
		t.Fatalf("test/no-color mode should not enable portal styles: %#v", styles)
	}
	restore := setActiveRenderContext(renderContext{Mode: testMode(), Styles: NewPortalStyles(testMode()), Width: 80, Height: 24})
	defer restore()

	if got := renderStatus("failed"); got != "failed" {
		t.Fatalf("renderStatus no-color = %q, want failed", got)
	}
	for _, tc := range []struct {
		category string
		want     string
	}{
		{"screen", "[screen]"},
		{"worker", "[worker]"},
		{"database", "[database]"},
		{"raw", "[raw]"},
		{"unknown", "[unknown]"},
		{"", "[action]"},
	} {
		if got := renderCategory(tc.category); got != tc.want {
			t.Fatalf("renderCategory(%q) = %q, want %q", tc.category, got, tc.want)
		}
	}
}

func TestPortalHierarchyHelpersArePlainInNoColorMode(t *testing.T) {
	restore := setActiveRenderContext(renderContext{Mode: testMode(), Styles: NewPortalStyles(testMode()), Width: 80, Height: 24})
	defer restore()

	var builder strings.Builder
	renderPrimarySection(&builder, "Primary")
	renderAttentionSection(&builder, "Needs Attention")
	renderSummarySection(&builder, "Summary")
	renderDetailsSection(&builder, "Details")
	renderDiagnosticsSection(&builder, "Diagnostics")
	renderHealthStrip(&builder,
		portalHealthItem{Label: "Notes", Status: "healthy", Detail: "12 files", Domain: portalDomainNotes},
		portalHealthItem{Label: "Jobs", Status: "failed", Detail: "2 failed", Domain: portalDomainJobs},
	)

	output := builder.String()
	for _, want := range []string{
		"Primary",
		"Needs Attention",
		"Summary",
		"Details",
		"Diagnostics",
		"[notes] Notes=healthy 12 files",
		"[jobs] Jobs=failed 2 failed",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("hierarchy helper output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "\x1b[") {
		t.Fatalf("hierarchy helper output should not contain ANSI in no-color mode: %q", output)
	}
}

func TestPortalBadgesAreTextualInNoColorMode(t *testing.T) {
	restore := setActiveRenderContext(renderContext{Mode: testMode(), Styles: NewPortalStyles(testMode()), Width: 80, Height: 24})
	defer restore()

	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"notes domain", renderDomainBadge(portalDomainNotes), "[notes]"},
		{"storage domain", renderDomainBadge(portalDomainStorage), "[storage]"},
		{"projects domain", renderDomainBadge(portalDomainProjects), "[projects]"},
		{"automation domain", renderDomainBadge(portalDomainAutomation), "[automation]"},
		{"jobs domain", renderDomainBadge(portalDomainJobs), "[jobs]"},
		{"network domain", renderDomainBadge(portalDomainNetwork), "[network]"},
		{"backup domain", renderDomainBadge(portalDomainBackup), "[backup]"},
		{"diagnostics domain", renderDomainBadge(portalDomainDiagnostics), "[diagnostics]"},
		{"active lifecycle", renderLifecycleBadge("active"), "[active]"},
		{"archived lifecycle", renderLifecycleBadge("archived"), "[archived]"},
		{"needs attention lifecycle", renderLifecycleBadge("needs-attention"), "[needs_attention]"},
		{"empty lifecycle", renderLifecycleBadge(""), "[unknown]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("badge = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestStripANSIAndSearchBoxWidth(t *testing.T) {
	if got := stripANSI("\x1b[31mfailed\x1b[0m"); got != "failed" {
		t.Fatalf("stripANSI = %q, want failed", got)
	}

	restore := setActiveRenderContext(renderContext{Mode: testMode(), Styles: NewPortalStyles(testMode()), Width: 32, Height: 24})
	defer restore()
	var builder strings.Builder
	renderSearchBox(&builder, "a very long query that must be trimmed before it breaks narrow terminals", 24)
	for _, line := range strings.Split(strings.TrimSpace(builder.String()), "\n") {
		if width := len(stripANSI(line)); width > 40 {
			t.Fatalf("search box line too wide (%d): %q", width, line)
		}
	}
}
