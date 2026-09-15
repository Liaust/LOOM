package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplicationResultImmutableCustodyAndIntegerFidelity(t *testing.T) {
	s := NewStore(t.TempDir())
	key := ApplicationResultKey("call-a")
	envelope := json.RawMessage(`{"payload":{"large":9007199254740993},"credential_token":""}`)
	record := ApplicationResultRecord{DispatchDigest: "dispatch-a", Envelope: envelope, Outbox: NewApplicationResultOutbox("call-a", "result-a", "correlation-a", "message-a", envelope, time.Now().UTC())}
	if e := s.PublishApplicationResult(key, record); e != nil {
		t.Fatal(e)
	}
	if e := s.PublishApplicationResult(key, record); e != nil {
		t.Fatal(e)
	}
	read, e := s.ReadApplicationResult(key)
	if e != nil || string(read.Envelope) != string(envelope) {
		t.Fatalf("envelope changed: %s %v", read.Envelope, e)
	}
	record.DispatchDigest = "different"
	if e = s.PublishApplicationResult(key, record); e == nil {
		t.Fatal("immutable result overwritten")
	}
	if !strings.Contains(string(read.Envelope), "9007199254740993") {
		t.Fatal("integer rounded")
	}
	path := filepath.Join(s.DataDir, "application-results", key+".json")
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(t.TempDir(), "unrelated")
	os.WriteFile(target, []byte(`{}`), 0600)
	if e = os.Symlink(target, path); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReadApplicationResult(key); e == nil {
		t.Fatal("symlink result read")
	}
}

func TestApplicationResultRecoveryRotatesBeyondOneBatch(t *testing.T) {
	s := NewStore(t.TempDir())
	for _, call := range []string{"a", "b", "c"} {
		envelope := json.RawMessage(`{"result":"` + call + `"}`)
		record := ApplicationResultRecord{DispatchDigest: call, Envelope: envelope, Outbox: NewApplicationResultOutbox(call, call, "correlation", "message", envelope, time.Now().UTC())}
		if e := s.PublishApplicationResult(ApplicationResultKey(call), record); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(s.DataDir, "application-results", ".publish-interrupted"), []byte("incomplete"), 0600); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		if e := s.RestoreApplicationResults(2); e != nil {
			t.Fatal(e)
		}
	}
	items, e := s.ListOutbox([]string{OutboxStatusPending}, 0)
	if e != nil || len(items) != 3 {
		t.Fatalf("bounded recovery stranded retained calls: %d %v", len(items), e)
	}
}
