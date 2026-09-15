package nodeagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/response"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
)

func TestClientEnrollSendsExpectedRequest(t *testing.T) {
	t.Parallel()
	var gotCorrelation string
	var gotIdempotency string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/node-agent/enroll" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		gotCorrelation = r.Header.Get(correlation.Header)
		gotIdempotency = r.Header.Get(idempotency.Header)
		var input nodes.CreateEnrollmentRequestInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode input: %v", err)
		}
		if input.EnrollmentToken != "node_enroll_test" || input.RequestedNodeKey != "workspace-test" {
			t.Fatalf("unexpected input %#v", input)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(response.SuccessWithIdempotency("corr_test", gotIdempotency, nodes.EnrollmentRequest{
			NodeEnrollmentRequestID: "node_enrollment_request_test",
			RequestedNodeKey:        "workspace-test",
			Status:                  nodes.EnrollmentRequestPending,
		}))
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	envelope, err := client.Enroll(context.Background(), "corr_test", "idem_test", nodes.CreateEnrollmentRequestInput{
		EnrollmentToken:  "node_enroll_test",
		RequestedNodeKey: "workspace-test",
	})
	if err != nil {
		t.Fatalf("Enroll failed: %v", err)
	}
	if envelope.Data.NodeEnrollmentRequestID != "node_enrollment_request_test" {
		t.Fatalf("unexpected enrollment response %#v", envelope.Data)
	}
	if gotCorrelation != "corr_test" || gotIdempotency != "idem_test" {
		t.Fatalf("missing request headers correlation=%q idempotency=%q", gotCorrelation, gotIdempotency)
	}
}

func TestClientHeartbeatSendsExpectedRequest(t *testing.T) {
	t.Parallel()
	reportedAt := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/node-agent/heartbeat" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var input nodes.HeartbeatInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode input: %v", err)
		}
		if input.NodeRef != "node_test" || input.CredentialToken != "node_cred_secret" || input.ReportedStatus != "ok" {
			t.Fatalf("unexpected input %#v", input)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(response.Success("corr_test", nodes.Heartbeat{
			NodeHeartbeatID: "node_heartbeat_test",
			NodeID:          "node_test",
			ReportedStatus:  "ok",
			PresenceState:   nodes.PresenceOnline,
			ReceivedAt:      reportedAt,
		}))
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	envelope, err := client.Heartbeat(context.Background(), "corr_test", nodes.HeartbeatInput{
		NodeRef:         "node_test",
		CredentialToken: "node_cred_secret",
		ReportedStatus:  "ok",
	})
	if err != nil {
		t.Fatalf("Heartbeat failed: %v", err)
	}
	if envelope.Data.PresenceState != nodes.PresenceOnline {
		t.Fatalf("unexpected heartbeat response %#v", envelope.Data)
	}
}

func TestClientPollAndAckSendExpectedRequests(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/node-agent/poll":
			var input communication.PollInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode poll input: %v", err)
			}
			if input.NodeRef != "node_test" || input.CredentialToken != "node_cred_secret" || input.MaxMessages != 10 {
				t.Fatalf("unexpected poll input %#v", input)
			}
			_ = json.NewEncoder(w).Encode(response.Success("corr_test", communication.PollResult{
				NodeID: "node_test",
				Messages: []communication.Message{{
					CommunicationMessageID: "communication_message_test",
					NodeID:                 "node_test",
					Direction:              communication.DirectionMainToNode,
					Kind:                   communication.KindMainPing,
					Status:                 communication.StatusDelivered,
				}},
				MaxMessages: 10,
				ClaimedAt:   time.Now().UTC(),
			}))
		case "/v1/node-agent/ack":
			if got := r.Header.Get(idempotency.Header); got != "node-agent.ack.node_test.communication_message_test" {
				t.Fatalf("unexpected idempotency key %q", got)
			}
			var input communication.AckInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode ack input: %v", err)
			}
			if input.CommunicationMessageRef != "communication_message_test" || input.AckStatus != communication.AckStatusCompleted {
				t.Fatalf("unexpected ack input %#v", input)
			}
			_ = json.NewEncoder(w).Encode(response.Success("corr_test", communication.AckResult{
				Message: communication.Message{
					CommunicationMessageID: "communication_message_test",
					NodeID:                 "node_test",
					Status:                 communication.StatusAcked,
				},
				Ack: communication.MessageAck{
					CommunicationAckID:     "communication_ack_test",
					CommunicationMessageID: "communication_message_test",
					NodeID:                 "node_test",
					AckStatus:              communication.AckStatusCompleted,
				},
			}))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	pollEnvelope, err := client.Poll(context.Background(), "corr_test", communication.PollInput{
		NodeRef:         "node_test",
		CredentialToken: "node_cred_secret",
		MaxMessages:     10,
	})
	if err != nil {
		t.Fatalf("Poll failed: %v", err)
	}
	if len(pollEnvelope.Data.Messages) != 1 {
		t.Fatalf("expected one message, got %#v", pollEnvelope.Data.Messages)
	}
	ackEnvelope, err := client.Ack(context.Background(), "corr_test", "node-agent.ack.node_test.communication_message_test", communication.AckInput{
		NodeRef:                 "node_test",
		CredentialToken:         "node_cred_secret",
		CommunicationMessageRef: "communication_message_test",
		AckStatus:               communication.AckStatusCompleted,
	})
	if err != nil {
		t.Fatalf("Ack failed: %v", err)
	}
	if ackEnvelope.Data.Message.Status != communication.StatusAcked {
		t.Fatalf("unexpected ack response %#v", ackEnvelope.Data)
	}
}

func TestClientPushWatchedRootBackupBatchSendsExpectedRequest(t *testing.T) {
	t.Parallel()
	var gotIdempotency string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/node-agent/watched-roots/backup-batches" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		gotIdempotency = r.Header.Get(idempotency.Header)
		var input mainwatchedroots.BackupBatchInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode input: %v", err)
		}
		if input.RootKey != "notes" || len(input.Items) != 1 || input.Items[0].LocalItemRef != "local_backup_item_test" {
			t.Fatalf("unexpected input %#v", input)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(response.Success("corr_test", mainwatchedroots.BackupBatchResult{
			Batch: mainwatchedroots.BackupBatch{
				WatchedRootBackupBatchID: "watched_root_backup_batch_test",
				Status:                   mainwatchedroots.BackupBatchStatusAccepted,
			},
			Items: []mainwatchedroots.BackupItem{{
				WatchedRootBackupItemID: "watched_root_backup_item_test",
				LocalItemRef:            "local_backup_item_test",
				Status:                  mainwatchedroots.BackupItemStatusAccepted,
			}},
		}))
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	envelope, err := client.PushWatchedRootBackupBatch(context.Background(), "corr_test", "idem_backup", mainwatchedroots.BackupBatchInput{
		RootKey: "notes",
		Items: []mainwatchedroots.BackupBatchItemInput{{
			LocalItemRef: "local_backup_item_test",
			ItemKind:     mainwatchedroots.BackupItemKindFile,
		}},
	})
	if err != nil {
		t.Fatalf("PushWatchedRootBackupBatch failed: %v", err)
	}
	if gotIdempotency != "idem_backup" {
		t.Fatalf("idempotency header = %q", gotIdempotency)
	}
	if envelope.Data.Batch.WatchedRootBackupBatchID != "watched_root_backup_batch_test" {
		t.Fatalf("unexpected backup batch response %#v", envelope.Data)
	}
}

func TestClientReturnsRemoteRequestError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(response.ErrorEnvelope{
			OK: false,
			Error: response.ErrorBody{
				Code:          "node_heartbeat.failed",
				Summary:       "Could not ingest node heartbeat.",
				CorrelationID: "corr_test",
			},
			Meta: response.NewMeta("corr_test"),
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	_, err = client.Heartbeat(context.Background(), "corr_test", nodes.HeartbeatInput{})
	if err == nil {
		t.Fatal("expected remote request error")
	}
	remoteErr, ok := err.(RemoteRequestError)
	if !ok {
		t.Fatalf("expected RemoteRequestError, got %T", err)
	}
	if remoteErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("unexpected status code %d", remoteErr.StatusCode)
	}
}
