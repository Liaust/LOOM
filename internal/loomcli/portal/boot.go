package portal

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type BootCheck struct {
	ID       string
	Label    string
	Required bool
}

type BootResult struct {
	ID      string
	Label   string
	Status  string
	Detail  string
	Errored bool
}

type BootState struct {
	Active       bool
	StartedAt    time.Time
	Logo         PortalLogo
	VisibleLines int
	Duration     time.Duration
	FrameDelay   time.Duration
}

type bootTickMsg struct {
	At time.Time
}

func RunBootChecks(ctx context.Context, client Client, correlationID string, options ...SnapshotOptions) ([]BootResult, Snapshot, error) {
	snapshot, err := CollectSnapshot(ctx, client, correlationID, options...)
	if err != nil {
		return []BootResult{{
			ID:      "required",
			Label:   "Required boot checks",
			Status:  "failed",
			Detail:  err.Error(),
			Errored: true,
		}}, snapshot, err
	}
	results := bootAvailabilityResults(snapshot)
	for _, partial := range snapshot.PartialErrors {
		if partial.Source == "main_availability" || (partial.Source == "status" && snapshot.Status.Status == "") {
			continue
		}
		results = append(results, BootResult{
			ID:      partial.Source,
			Label:   partial.Source,
			Status:  "partial",
			Detail:  partial.Message,
			Errored: true,
		})
	}
	return results, snapshot, nil
}

func bootAvailabilityResults(snapshot Snapshot) []BootResult {
	switch snapshot.MainAvailability.State {
	case MainAvailabilityOffline:
		detail := snapshot.MainAvailability.Summary
		if snapshot.MainAvailability.Target != "" {
			detail += " Target: " + snapshot.MainAvailability.Target
		}
		return []BootResult{{
			ID:      "main_availability",
			Label:   "Main connection",
			Status:  "warning",
			Detail:  detail,
			Errored: true,
		}}
	case MainAvailabilityDegraded:
		status := BootResult{ID: "status", Label: "LOOM status", Status: "ok", Detail: snapshot.Status.Status}
		if snapshot.Status.Status == "" {
			status.Status = "warning"
			status.Detail = snapshot.MainAvailability.Summary
			status.Errored = true
		}
		return []BootResult{
			{ID: "health", Label: "LOOM health", Status: "ok", Detail: snapshot.Health.Status},
			status,
		}
	default:
		return []BootResult{
			{ID: "health", Label: "LOOM health", Status: "ok", Detail: snapshot.Health.Status},
			{ID: "status", Label: "LOOM status", Status: "ok", Detail: snapshot.Status.Status},
		}
	}
}

func newBootState(logo PortalLogo) BootState {
	if len(logo.Lines) == 0 {
		logo = CompactPortalLogo()
	}
	duration := time.Second
	frameDelay := duration / time.Duration(len(logo.Lines)+1)
	if frameDelay < 50*time.Millisecond {
		frameDelay = 50 * time.Millisecond
	}
	return BootState{
		Active:       true,
		StartedAt:    time.Now(),
		Logo:         logo,
		VisibleLines: 0,
		Duration:     duration,
		FrameDelay:   frameDelay,
	}
}

func bootTickCmd(delay time.Duration) tea.Cmd {
	if delay <= 0 {
		delay = 100 * time.Millisecond
	}
	return tea.Tick(delay, func(t time.Time) tea.Msg {
		return bootTickMsg{At: t}
	})
}

func (b BootState) Complete() BootState {
	b.Active = false
	b.VisibleLines = len(b.Logo.Lines)
	return b
}

func (b BootState) Advance(now time.Time) BootState {
	if !b.Active {
		return b
	}
	if b.StartedAt.IsZero() {
		b.StartedAt = now
	}
	if b.VisibleLines < len(b.Logo.Lines) {
		b.VisibleLines++
	}
	if b.VisibleLines >= len(b.Logo.Lines) || now.Sub(b.StartedAt) >= b.Duration {
		return b.Complete()
	}
	return b
}

func RenderBoot(ctx renderContext, boot BootState) string {
	logo := boot.Logo
	if len(logo.Lines) == 0 {
		logo = CompactPortalLogo()
	}
	visible := boot.VisibleLines
	if visible <= 0 {
		visible = 1
	}
	lines := logo.RenderLines(visible)
	var builder strings.Builder
	for _, line := range lines {
		builder.WriteString(ctx.Styles.Logo.Render(line))
		builder.WriteByte('\n')
	}
	builder.WriteByte('\n')
	builder.WriteString(ctx.Styles.Muted.Render("Keys: enter skip  q quit"))
	builder.WriteByte('\n')
	return builder.String()
}
