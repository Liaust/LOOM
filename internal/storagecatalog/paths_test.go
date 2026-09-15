package storagecatalog

import (
	"errors"
	"testing"
)

func TestNormalizeLogicalPath(t *testing.T) {
	got, err := NormalizeLogicalPath(` Documents\Reports\Q1.md `)
	if err != nil {
		t.Fatalf("NormalizeLogicalPath returned error: %v", err)
	}
	if got != "Documents/Reports/Q1.md" {
		t.Fatalf("path = %q, want Documents/Reports/Q1.md", got)
	}
}

func TestNormalizeLogicalPathRejectsEscapes(t *testing.T) {
	for _, path := range []string{"/absolute.md", "../escape.md", "safe/../../escape.md"} {
		t.Run(path, func(t *testing.T) {
			_, err := NormalizeLogicalPath(path)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestNormalizeOptionalViewPathAllowsEmpty(t *testing.T) {
	got, err := NormalizeOptionalViewPath(" ")
	if err != nil {
		t.Fatalf("NormalizeOptionalViewPath returned error: %v", err)
	}
	if got != "" {
		t.Fatalf("path = %q, want empty", got)
	}
}
