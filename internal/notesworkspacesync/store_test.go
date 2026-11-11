package notesworkspacesync

import (
	"bytes"
	"testing"
)

func TestCanonicalRecordNoopPreservesLargeNumbers(t *testing.T) {
	a, err := canonicalRecord([]byte(`{"count":9007199254740992,"state":"held"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := canonicalRecord([]byte(`{ "state": "held", "count": 9007199254740992 }`))
	c, _ := canonicalRecord([]byte(`{"state":"held","count":9007199254740993}`))
	if !bytes.Equal(a, b) || bytes.Equal(a, c) {
		t.Fatal("no-op equality lost exact record identity")
	}
}
