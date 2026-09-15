package artifacts

import "testing"

func TestValidStatus(t *testing.T) {
	for _, status := range []string{
		StatusCreated,
		StatusIndexed,
		StatusFailed,
		StatusArchived,
	} {
		if !ValidStatus(status) {
			t.Fatalf("expected status %q to be valid", status)
		}
	}
	if ValidStatus("temporary") {
		t.Fatal("unexpected artifact status accepted")
	}
}
