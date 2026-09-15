package ui

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

type Renderer struct {
	Mode   Mode
	Styles Styles
	Out    io.Writer
	Err    io.Writer
}

type KeyValue struct {
	Key   string
	Value string
}

type Table struct {
	Headers []string
	Rows    [][]string
}

type CommandPreview struct {
	Command []string
	Effect  string
	Risk    string
}

func NewRenderer(mode Mode, out, errOut io.Writer) Renderer {
	return Renderer{Mode: mode, Styles: NewStyles(mode), Out: out, Err: errOut}
}

func (r Renderer) Section(title string) {
	fmt.Fprintln(r.Out, r.Styles.Header.Render(title))
}

func (r Renderer) KeyValues(rows []KeyValue) {
	writer := tabwriter.NewWriter(r.Out, 0, 0, 2, ' ', 0)
	for _, row := range rows {
		fmt.Fprintf(writer, "%s\t%s\n", row.Key+":", row.Value)
	}
	_ = writer.Flush()
}

func (r Renderer) Table(table Table) {
	writer := tabwriter.NewWriter(r.Out, 0, 0, 2, ' ', 0)
	if len(table.Headers) > 0 {
		fmt.Fprintln(writer, strings.Join(table.Headers, "\t"))
	}
	for _, row := range table.Rows {
		fmt.Fprintln(writer, strings.Join(row, "\t"))
	}
	_ = writer.Flush()
}

func (r Renderer) Status(label, status string) {
	style := r.Styles.Info
	switch strings.ToLower(status) {
	case "ok", "healthy", "completed", "succeeded", "active", "allowed":
		style = r.Styles.Success
	case "warning", "pending", "queued", "degraded", "lagging":
		style = r.Styles.Warning
	case "failed", "error", "critical", "denied", "disabled":
		style = r.Styles.Danger
	}
	fmt.Fprintf(r.Out, "%s: %s\n", label, style.Render(status))
}

func (r Renderer) CommandPreview(preview CommandPreview) {
	if len(preview.Command) > 0 {
		fmt.Fprintf(r.Out, "Raw: %s\n", r.Styles.Code.Render(strings.Join(preview.Command, " ")))
	}
	if preview.Effect != "" {
		fmt.Fprintf(r.Out, "Effect: %s\n", preview.Effect)
	}
	if preview.Risk != "" {
		fmt.Fprintf(r.Out, "Risk: %s\n", preview.Risk)
	}
}

func (r Renderer) ErrorBlock(code, summary string) {
	fmt.Fprintf(r.Err, "Error: %s: %s\n", code, summary)
}

func ContainsANSI(value string) bool {
	return strings.Contains(value, "\x1b[")
}
