package portal

import (
	"fmt"
	"strings"
)

type ViewportState struct {
	Offset     int
	Height     int
	TotalLines int
}

type PortalRenderedView struct {
	Header []string
	Body   []string
	Footer []string
}

func renderPortalViewport(output string, height int, state ViewportState) (string, ViewportState) {
	if height <= 0 {
		return output, state
	}
	view := splitPortalRenderedView(output)
	totalBody := len(view.Body)
	state.TotalLines = totalBody
	state.Height = viewportBodyHeight(height, view, totalBody > 0)
	if totalBody <= state.Height {
		state.Offset = 0
		return joinPortalRenderedView(view.Header, view.Body, nil, view.Footer), state
	}
	state.Offset = clampViewportOffset(state.Offset, totalBody, state.Height)
	end := state.Offset + state.Height
	if end > totalBody {
		end = totalBody
	}
	visible := append([]string{}, view.Body[state.Offset:end]...)
	indicator := renderViewportIndicator(state.Offset, end, totalBody)
	return joinPortalRenderedView(view.Header, visible, []string{indicator}, view.Footer), state
}

func splitPortalRenderedView(output string) PortalRenderedView {
	lines := splitPortalLines(output)
	if len(lines) == 0 {
		return PortalRenderedView{}
	}
	footerStart := findPortalFooterStart(lines)
	content := lines
	footer := []string{}
	if footerStart >= 0 {
		content = lines[:footerStart]
		footer = lines[footerStart:]
	}
	headerCount := inferPortalHeaderLineCount(content)
	if headerCount > len(content) {
		headerCount = len(content)
	}
	return PortalRenderedView{
		Header: append([]string{}, content[:headerCount]...),
		Body:   append([]string{}, content[headerCount:]...),
		Footer: append([]string{}, footer...),
	}
}

func splitPortalLines(output string) []string {
	output = strings.TrimRight(output, "\n")
	if output == "" {
		return nil
	}
	return strings.Split(output, "\n")
}

func findPortalFooterStart(lines []string) int {
	for idx := len(lines) - 1; idx >= 0; idx-- {
		plain := strings.TrimSpace(stripANSI(lines[idx]))
		if strings.HasPrefix(plain, "Keys:") {
			if idx > 0 && strings.TrimSpace(stripANSI(lines[idx-1])) == "" {
				return idx - 1
			}
			return idx
		}
	}
	return -1
}

func inferPortalHeaderLineCount(lines []string) int {
	if len(lines) == 0 {
		return 0
	}
	if headerCount := inferPortalSearchBoxHeaderLineCount(lines); headerCount > 0 {
		return headerCount
	}
	for idx, line := range lines {
		if strings.TrimSpace(stripANSI(line)) == "LOOM Portal" {
			if idx+1 < len(lines) && strings.TrimSpace(stripANSI(lines[idx+1])) == "" {
				return idx + 2
			}
			return idx + 1
		}
	}
	limit := len(lines)
	if limit > 12 {
		limit = 12
	}
	for idx := 0; idx < limit; idx++ {
		if strings.TrimSpace(stripANSI(lines[idx])) == "" {
			return idx + 1
		}
	}
	if len(lines) == 1 {
		return 1
	}
	return 0
}

func inferPortalSearchBoxHeaderLineCount(lines []string) int {
	limit := len(lines)
	if limit > 16 {
		limit = 16
	}
	for idx := 0; idx < limit; idx++ {
		plain := strings.TrimSpace(stripANSI(lines[idx]))
		if !strings.HasPrefix(plain, "+") || !strings.Contains(plain, "---") {
			continue
		}
		if idx+2 >= len(lines) {
			continue
		}
		inputLine := strings.TrimSpace(stripANSI(lines[idx+1]))
		bottom := strings.TrimSpace(stripANSI(lines[idx+2]))
		if !strings.HasPrefix(inputLine, "|") || (!strings.Contains(inputLine, "Search") && !strings.Contains(inputLine, "Query")) {
			continue
		}
		if !strings.HasPrefix(bottom, "+") {
			continue
		}
		headerCount := idx + 3
		if headerCount < len(lines) && strings.TrimSpace(stripANSI(lines[headerCount])) == "" {
			headerCount++
		}
		return headerCount
	}
	return 0
}

func viewportBodyHeight(totalHeight int, view PortalRenderedView, hasBody bool) int {
	if totalHeight <= 0 {
		return 0
	}
	height := totalHeight - len(view.Header) - len(view.Footer)
	if hasBody {
		height--
	}
	if height < 1 {
		height = 1
	}
	return height
}

func clampViewportOffset(offset int, totalLines int, height int) int {
	if offset < 0 {
		return 0
	}
	if height <= 0 || totalLines <= height {
		return 0
	}
	maxOffset := totalLines - height
	if offset > maxOffset {
		return maxOffset
	}
	return offset
}

func renderViewportIndicator(start int, end int, total int) string {
	if total <= 0 {
		return ""
	}
	return portalRenderContext().Styles.Muted.Render(fmt.Sprintf("Scroll: %d-%d/%d", start+1, end, total))
}

func joinPortalRenderedView(header []string, body []string, indicator []string, footer []string) string {
	lines := []string{}
	lines = append(lines, header...)
	lines = append(lines, body...)
	lines = append(lines, indicator...)
	lines = append(lines, footer...)
	return strings.Join(lines, "\n")
}
