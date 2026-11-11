package knowledge

import (
	"errors"
	"strings"
	"testing"
)

func TestFileReadResolvesReferenceBeforeVisibility(t *testing.T) {
	query := fileReadTargetSQL()
	if !strings.HasPrefix(query, "WITH matching_files AS MATERIALIZED (") {
		t.Fatal("exact file references must be materialized before custody checks")
	}
	boundary := strings.Index(query, "FROM matching_files ko")
	if boundary < 0 || strings.Index(query, visibleNotesCustodyObjectSQL("ko", false)) < boundary {
		t.Fatal("custody visibility must apply to exact matches, not the source corpus")
	}
}

func TestFileTextRange(t *testing.T) {
	for _, test := range []struct {
		name, body, want   string
		limit              int
		truncated, invalid bool
	}{
		{"complete", "plain text", "plain text", 20, false, false},
		{"bounded", "plain text", "plain", 5, true, false},
		{"unicode boundary", "a\u20acmore", "a", 3, true, false},
		{"too small", "\u20acmore", "", 1, false, true},
		{"bad prefix", "\xffmany bytes", "", 4, false, true},
		{"nul", "a\x00b", "", 10, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := FileTextRange([]byte(test.body), 7, test.limit)
			if test.invalid {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("%+v %v", got, err)
				}
				return
			}
			if err != nil || got.Text != test.want || got.Truncated != test.truncated || got.NextOffset != 7+int64(len(test.want)) {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}
