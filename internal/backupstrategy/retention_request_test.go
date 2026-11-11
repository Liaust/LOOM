package backupstrategy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalRetentionRequestCoalescingAndCompletionWake(t *testing.T) {
	root := t.TempDir()
	if err := RequestLocalRecoveryRetention(root, "completed-one", "history-one", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	_, first, err := readLocalRetentionRequest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := RequestLocalRecoveryRetention(root, "completed-one", "history-one", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	_, same, _ := readLocalRetentionRequest(root)
	if first != same {
		t.Fatal("duplicate completion rewrote the request")
	}
	if err := WakePendingLocalRecoveryRetention(root); err != nil {
		t.Fatal(err)
	}
	_, pending, _ := readLocalRetentionRequest(root)
	if pending == first {
		t.Fatal("pending request was not woken")
	}
	ack := []byte(`{"schema":"loom.local_retention.ack.v1","status":"planned","request_sha256":"` + pending + `"}`)
	if err := os.WriteFile(filepath.Join(root, "backups/local-retention-ack.json"), ack, 0644); err != nil {
		t.Fatal(err)
	}
	if err := WakePendingLocalRecoveryRetention(root); err != nil {
		t.Fatal(err)
	}
	_, done, _ := readLocalRetentionRequest(root)
	if done != pending {
		t.Fatal("acknowledged request was woken again")
	}
	if err := RequestLocalRecoveryRetention(root, "completed-two", "history-two", strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	request, _, _ := readLocalRetentionRequest(root)
	if request.WorkerRunID != "completed-two" {
		t.Fatal("latest completion was lost")
	}
}
