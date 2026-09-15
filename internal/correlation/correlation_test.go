package correlation

import "testing"

func TestNormalizeGeneratesIDWhenEmpty(t *testing.T) {
	id := Normalize("")
	if id == "" {
		t.Fatal("Normalize returned empty id")
	}
	if id == "corr_unavailable" {
		t.Fatal("random source unavailable")
	}
}
