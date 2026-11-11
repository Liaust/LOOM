package watchedroots

import "strings"

// WalkDir orders directory children before the parent's next sibling. Raw
// string comparison gets that wrong for siblings such as a.txt and a/file.
func walkPathAfter(path, after string) bool {
	a, b := strings.Split(path, "/"), strings.Split(after, "/")
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return len(a) > len(b)
}
