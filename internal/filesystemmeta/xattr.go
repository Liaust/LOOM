package filesystemmeta

import "strings"

func splitNullSeparated(raw []byte) []string {
	var names []string
	start := 0
	for i, b := range raw {
		if b != 0 {
			continue
		}
		if i > start {
			name := strings.TrimSpace(string(raw[start:i]))
			if name != "" {
				names = append(names, name)
			}
		}
		start = i + 1
	}
	if start < len(raw) {
		name := strings.TrimSpace(string(raw[start:]))
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}
