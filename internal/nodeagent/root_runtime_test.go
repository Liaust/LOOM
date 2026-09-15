package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/communication"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/response"
)

func TestRuntimeWorkerCommands(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := Store{
		ConfigPath: filepath.Join(dir, "config", "config.json"),
		StatePath:  filepath.Join(dir, "state", "state.json"),
		DataDir:    filepath.Join(dir, "data"),
	}
	config := Config{
		MainURL:     "http://10.44.0.2:8080",
		NodeKey:     "workspace-test",
		DisplayName: "Workspace Test",
	}
	if err := store.EnsureDataDirs(); err != nil {
		t.Fatalf("EnsureDataDirs failed: %v", err)
	}
	if err := store.SaveConfig(config); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}
	if err := store.SaveState(State{}); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	var statusOut bytes.Buffer
	opts := rootOptions{
		configPath:    store.ConfigPath,
		statePath:     store.StatePath,
		dataDir:       store.DataDir,
		jsonOutput:    true,
		correlationID: "corr-runtime-status",
		out:           &statusOut,
		errOut:        &bytes.Buffer{},
	}
	statusCmd := newRuntimeWorkersStatusCommand(&opts)
	if err := statusCmd.Execute(); err != nil {
		t.Fatalf("workers status failed: %v", err)
	}
	var statusEnvelope response.Envelope[noderuntime.Status]
	if err := json.Unmarshal(statusOut.Bytes(), &statusEnvelope); err != nil {
		t.Fatalf("decode status: %v\n%s", err, statusOut.String())
	}
	if !statusEnvelope.OK || len(statusEnvelope.Data.Workers) != 7 {
		t.Fatalf("unexpected workers status envelope %#v", statusEnvelope)
	}

	var runOut bytes.Buffer
	opts.out = &runOut
	runCmd := newRuntimeWorkersRunCommand(&opts)
	runCmd.SetArgs([]string{noderuntime.WorkerKeyLocalQueueReporter, "--once"})
	if err := runCmd.Execute(); err != nil {
		t.Fatalf("workers run failed: %v", err)
	}
	var runEnvelope response.Envelope[noderuntime.RunOutput]
	if err := json.Unmarshal(runOut.Bytes(), &runEnvelope); err != nil {
		t.Fatalf("decode run: %v\n%s", err, runOut.String())
	}
	if !runEnvelope.OK || runEnvelope.Data.Run.Status != noderuntime.RunStatusSucceeded {
		t.Fatalf("unexpected run envelope %#v", runEnvelope)
	}

	var outboxOut bytes.Buffer
	opts.out = &outboxOut
	outboxCmd := newRuntimeOutboxStatusCommand(&opts)
	if err := outboxCmd.Execute(); err != nil {
		t.Fatalf("outbox status failed: %v", err)
	}
	var outboxEnvelope response.Envelope[noderuntime.OutboxSummary]
	if err := json.Unmarshal(outboxOut.Bytes(), &outboxEnvelope); err != nil {
		t.Fatalf("decode outbox: %v\n%s", err, outboxOut.String())
	}
	if !outboxEnvelope.OK || outboxEnvelope.Data.Counts[noderuntime.OutboxStatusPending] != 0 {
		t.Fatalf("unexpected outbox envelope %#v", outboxEnvelope)
	}
}

func TestHeartbeatWorkerSendsHeartbeatAndRecordsRun(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	reportedAt := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/node-agent/heartbeat" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var input nodes.HeartbeatInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode heartbeat: %v", err)
		}
		if input.NodeRef != "node_test" || input.CredentialToken != "node_cred_secret" {
			t.Fatalf("unexpected heartbeat input %#v", input)
		}
		var storage map[string]any
		if err := json.Unmarshal(input.StorageStatusJSON, &storage); err != nil {
			t.Fatalf("decode storage status: %v", err)
		}
		if _, ok := storage["runtime"]; !ok {
			t.Fatalf("expected runtime summary in storage status: %#v", storage)
		}
		_ = json.NewEncoder(w).Encode(response.Success("corr-heartbeat", nodes.Heartbeat{
			NodeHeartbeatID: "node_heartbeat_test",
			NodeID:          "node_test",
			ReportedStatus:  "ok",
			PresenceState:   nodes.PresenceOnline,
			ReceivedAt:      reportedAt,
		}))
	}))
	defer server.Close()

	store := testNodeAgentStore(t, dir, server.URL, State{
		NodeID:           "node_test",
		NodeCredentialID: "node_credential_test",
		CredentialToken:  "node_cred_secret",
	})
	runtimeStore := noderuntime.NewStore(store.DataDir)
	if err := runtimeStore.EnsureDefaultInstances(noderuntime.DefaultInstanceInput{}); err != nil {
		t.Fatalf("EnsureDefaultInstances failed: %v", err)
	}
	env := noderuntime.RuntimeEnv(store.ConfigPath, store.StatePath, store.DataDir, "node_test", "workspace-test", server.URL, true)
	output, err := nodeAgentRuntimeRegistry().RunOnce(context.Background(), runtimeStore, env, noderuntime.WorkerKeyHeartbeat, "corr-heartbeat")
	if err != nil {
		t.Fatalf("heartbeat worker failed: %v", err)
	}
	if output.Run.Status != noderuntime.RunStatusSucceeded || output.Health.Status != noderuntime.WorkerStatusHealthy {
		t.Fatalf("unexpected heartbeat worker output %#v", output)
	}
	state, err := store.LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if state.LastHeartbeat == nil || state.LastHeartbeat.NodeHeartbeatID != "node_heartbeat_test" {
		t.Fatalf("expected last heartbeat state, got %#v", state.LastHeartbeat)
	}
}

func TestPollQueuesRuntimeInboxAndOutboxBeforeAckCompletes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/node-agent/poll":
			var input communication.PollInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode poll: %v", err)
			}
			if input.NodeRef != "node_test" || input.CredentialToken != "node_cred_secret" {
				t.Fatalf("unexpected poll input %#v", input)
			}
			_ = json.NewEncoder(w).Encode(response.Success("corr-poll", communication.PollResult{
				NodeID: "node_test",
				Messages: []communication.Message{{
					CommunicationMessageID: "communication_message_test",
					NodeID:                 "node_test",
					Direction:              communication.DirectionMainToNode,
					Kind:                   communication.KindMainPing,
					Status:                 communication.StatusDelivered,
					PayloadJSON:            json.RawMessage(`{"text":"hello"}`),
				}},
				MaxMessages: 10,
				ClaimedAt:   time.Now().UTC(),
			}))
		case "/v1/node-agent/ack":
			var input communication.AckInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode ack: %v", err)
			}
			if input.CommunicationMessageRef != "communication_message_test" || input.CredentialToken != "node_cred_secret" {
				t.Fatalf("unexpected ack input %#v", input)
			}
			_ = json.NewEncoder(w).Encode(response.Success("corr-poll", communication.AckResult{
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

	store := testNodeAgentStore(t, dir, server.URL, State{
		NodeID:           "node_test",
		NodeCredentialID: "node_credential_test",
		CredentialToken:  "node_cred_secret",
	})
	opts := rootOptions{
		configPath:    store.ConfigPath,
		statePath:     store.StatePath,
		dataDir:       store.DataDir,
		correlationID: "corr-poll",
		out:           &bytes.Buffer{},
		errOut:        &bytes.Buffer{},
	}
	result, err := opts.pollOnce(context.Background(), 10, true)
	if err != nil {
		t.Fatalf("pollOnce failed: %v", err)
	}
	if len(result.Processed) != 1 || result.Processed[0].RuntimeAckOutboxID == "" {
		t.Fatalf("expected processed message with runtime outbox id, got %#v", result)
	}
	runtimeStore := noderuntime.NewStore(store.DataDir)
	outbox, err := runtimeStore.OutboxSummary()
	if err != nil {
		t.Fatalf("OutboxSummary failed: %v", err)
	}
	if outbox.Counts[noderuntime.OutboxStatusDone] != 1 || outbox.Counts[noderuntime.OutboxStatusPending] != 0 {
		t.Fatalf("unexpected outbox summary %#v", outbox)
	}
	inbox, err := runtimeStore.InboxSummary()
	if err != nil {
		t.Fatalf("InboxSummary failed: %v", err)
	}
	if inbox.Counts[noderuntime.OutboxStatusDone] != 1 {
		t.Fatalf("unexpected inbox summary %#v", inbox)
	}
	if _, err := os.Stat(filepath.Join(store.DataDir, "outbox", "communication_ack_test.json")); err != nil {
		t.Fatalf("expected legacy ack outbox file: %v", err)
	}
}

func TestPollExecutesProtectedFolderPreflightAndSendsAuthenticatedAck(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "document.txt"), []byte("metadata only"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := backupcontracts.ProtectedFolderPreflightPayload{SchemaVersion: backupcontracts.ProtectedFolderControlSchemaVersion, PreflightID: "backup_preflight_runtime", TargetNode: "node_test", RequestedPath: target, Ignore: backupcontracts.IgnorePolicy{Profile: "managed", DiscoverUserRules: true}, Budget: backupcontracts.DefaultPreflightBudget(), ExpiresAt: time.Now().Add(time.Minute)}
	payloadJSON, _ := json.Marshal(payload)
	ackSeen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/node-agent/poll":
			_ = json.NewEncoder(w).Encode(response.Success("corr-preflight", communication.PollResult{NodeID: "node_test", Messages: []communication.Message{{CommunicationMessageID: "communication_message_preflight", NodeID: "node_test", Direction: communication.DirectionMainToNode, Kind: communication.KindProtectedFolderPreflight, Status: communication.StatusDelivered, PayloadJSON: payloadJSON}}, MaxMessages: 10, ClaimedAt: time.Now().UTC()}))
		case "/v1/node-agent/ack":
			var input communication.AckInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.CredentialToken != "node_cred_secret" || input.AckStatus != communication.AckStatusCompleted {
				t.Fatalf("unexpected preflight ack %#v", input)
			}
			var result backupcontracts.PreflightResult
			if err := json.Unmarshal(input.ResultJSON, &result); err != nil {
				t.Fatal(err)
			}
			if result.CanonicalPath == "" || result.FileCount != 1 || !result.Readable {
				t.Fatalf("unexpected preflight result %#v", result)
			}
			ackSeen = true
			_ = json.NewEncoder(w).Encode(response.Success("corr-preflight", communication.AckResult{Message: communication.Message{CommunicationMessageID: "communication_message_preflight", NodeID: "node_test", Kind: communication.KindProtectedFolderPreflight, Status: communication.StatusAcked}, Ack: communication.MessageAck{CommunicationAckID: "communication_ack_preflight", CommunicationMessageID: "communication_message_preflight", NodeID: "node_test", AckStatus: communication.AckStatusCompleted, ResultJSON: input.ResultJSON}}))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	store := testNodeAgentStore(t, dir, server.URL, State{NodeID: "node_test", NodeCredentialID: "node_credential_test", CredentialToken: "node_cred_secret"})
	opts := rootOptions{configPath: store.ConfigPath, statePath: store.StatePath, dataDir: store.DataDir, correlationID: "corr-preflight", out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	result, err := opts.pollOnce(context.Background(), 10, true)
	if err != nil {
		t.Fatal(err)
	}
	if !ackSeen || len(result.Processed) != 1 || result.Processed[0].AckStatus != communication.AckStatusCompleted {
		t.Fatalf("preflight poll result %#v ackSeen=%t", result, ackSeen)
	}
}

func TestPollReconcilesProtectedFolderAndSendsAuthenticatedAck(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := t.TempDir()
	root := desiredRootFixture(t, "runtime-folder", target, 10*1024*1024)
	payload := reconcilePayload(t, "node_test", 1, []backupcontracts.ProtectedFolderDesiredRoot{root})
	payloadJSON, _ := json.Marshal(payload)
	ackSeen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/node-agent/poll":
			_ = json.NewEncoder(w).Encode(response.Success("corr-reconcile", communication.PollResult{NodeID: "node_test", Messages: []communication.Message{{CommunicationMessageID: "communication_message_reconcile", NodeID: "node_test", Direction: communication.DirectionMainToNode, Kind: communication.KindProtectedFolderReconcile, Status: communication.StatusDelivered, PayloadJSON: payloadJSON}}, MaxMessages: 10, ClaimedAt: time.Now().UTC()}))
		case "/v1/node-agent/ack":
			var input communication.AckInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.CredentialToken != "node_cred_secret" || input.AckStatus != communication.AckStatusCompleted {
				t.Fatalf("unexpected reconcile ack %#v", input)
			}
			var result backupcontracts.ProtectedFolderAck
			if err := json.Unmarshal(input.ResultJSON, &result); err != nil {
				t.Fatal(err)
			}
			if result.Evidence.AppliedRevision != 1 || len(result.Roots) != 1 || !result.Roots[0].Applied {
				t.Fatalf("unexpected reconcile result %#v", result)
			}
			ackSeen = true
			_ = json.NewEncoder(w).Encode(response.Success("corr-reconcile", communication.AckResult{Message: communication.Message{CommunicationMessageID: "communication_message_reconcile", NodeID: "node_test", Kind: communication.KindProtectedFolderReconcile, Status: communication.StatusAcked}, Ack: communication.MessageAck{CommunicationAckID: "communication_ack_reconcile", CommunicationMessageID: "communication_message_reconcile", NodeID: "node_test", AckStatus: communication.AckStatusCompleted, ResultJSON: input.ResultJSON}}))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	store := testNodeAgentStore(t, dir, server.URL, State{NodeID: "node_test", NodeCredentialID: "node_credential_test", CredentialToken: "node_cred_secret"})
	opts := rootOptions{configPath: store.ConfigPath, statePath: store.StatePath, dataDir: store.DataDir, correlationID: "corr-reconcile", out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	result, err := opts.pollOnce(context.Background(), 10, true)
	if err != nil {
		t.Fatal(err)
	}
	if !ackSeen || len(result.Processed) != 1 || result.Processed[0].AckStatus != communication.AckStatusCompleted {
		t.Fatalf("reconcile poll result %#v ackSeen=%t", result, ackSeen)
	}
	if _, err := noderuntime.NewStore(store.DataDir).LoadInstance(noderuntime.WatchedRootWorkerKey(root.RootKey)); err != nil {
		t.Fatal(err)
	}
}

func TestPollNoFlushAndOutboxFlush(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ackCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/node-agent/poll":
			_ = json.NewEncoder(w).Encode(response.Success("corr-poll", communication.PollResult{
				NodeID: "node_test",
				Messages: []communication.Message{{
					CommunicationMessageID: "communication_message_no_flush",
					NodeID:                 "node_test",
					Direction:              communication.DirectionMainToNode,
					Kind:                   communication.KindMainPing,
					Status:                 communication.StatusDelivered,
					PayloadJSON:            json.RawMessage(`{"text":"queued"}`),
				}},
				MaxMessages: 10,
				ClaimedAt:   time.Now().UTC(),
			}))
		case "/v1/node-agent/ack":
			ackCalls++
			var input communication.AckInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatalf("decode ack: %v", err)
			}
			if input.CredentialToken != "node_cred_secret" || input.CommunicationMessageRef != "communication_message_no_flush" {
				t.Fatalf("unexpected ack input %#v", input)
			}
			_ = json.NewEncoder(w).Encode(response.Success("corr-poll", communication.AckResult{
				Message: communication.Message{
					CommunicationMessageID: "communication_message_no_flush",
					NodeID:                 "node_test",
					Status:                 communication.StatusAcked,
				},
				Ack: communication.MessageAck{
					CommunicationAckID:     "communication_ack_no_flush",
					CommunicationMessageID: "communication_message_no_flush",
					NodeID:                 "node_test",
					AckStatus:              communication.AckStatusCompleted,
				},
			}))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	store := testNodeAgentStore(t, dir, server.URL, State{
		NodeID:           "node_test",
		NodeCredentialID: "node_credential_test",
		CredentialToken:  "node_cred_secret",
	})
	opts := rootOptions{
		configPath:    store.ConfigPath,
		statePath:     store.StatePath,
		dataDir:       store.DataDir,
		correlationID: "corr-poll",
		out:           &bytes.Buffer{},
		errOut:        &bytes.Buffer{},
	}
	result, err := opts.pollOnce(context.Background(), 10, false)
	if err != nil {
		t.Fatalf("pollOnce no-flush failed: %v", err)
	}
	if ackCalls != 0 {
		t.Fatalf("expected no ack calls before flush, got %d", ackCalls)
	}
	if len(result.Processed) != 1 || result.Processed[0].RuntimeAckOutboxStatus != noderuntime.OutboxStatusPending {
		t.Fatalf("expected pending runtime outbox, got %#v", result)
	}
	runtimeStore := noderuntime.NewStore(store.DataDir)
	summary, err := runtimeStore.OutboxSummary()
	if err != nil {
		t.Fatalf("OutboxSummary failed: %v", err)
	}
	if summary.Counts[noderuntime.OutboxStatusPending] != 1 {
		t.Fatalf("expected one pending outbox item, got %#v", summary)
	}

	env := noderuntime.RuntimeEnv(store.ConfigPath, store.StatePath, store.DataDir, "node_test", "workspace-test", server.URL, true)
	flush, err := flushDueOutbox(context.Background(), env, runtimeStore, 10)
	if err != nil {
		t.Fatalf("flushDueOutbox failed: %v", err)
	}
	if ackCalls != 1 || flush.Done != 1 || flush.Summary.Counts[noderuntime.OutboxStatusPending] != 0 {
		t.Fatalf("unexpected flush result ackCalls=%d flush=%#v", ackCalls, flush)
	}
	if _, err := os.Stat(filepath.Join(store.DataDir, "outbox", "communication_ack_no_flush.json")); err != nil {
		t.Fatalf("expected legacy ack outbox file after flush: %v", err)
	}
}

func TestOutboxFlushMarksRetryableFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(response.ErrorEnvelope{
			OK: false,
			Error: response.ErrorBody{
				Code:    "temporary_failure",
				Summary: "temporary failure",
			},
			Meta: response.NewMeta("corr-fail"),
		})
	}))
	defer server.Close()

	store := testNodeAgentStore(t, dir, server.URL, State{
		NodeID:           "node_test",
		NodeCredentialID: "node_credential_test",
		CredentialToken:  "node_cred_secret",
	})
	runtimeStore := noderuntime.NewStore(store.DataDir)
	if err := runtimeStore.EnsureDefaultInstances(noderuntime.DefaultInstanceInput{}); err != nil {
		t.Fatalf("EnsureDefaultInstances failed: %v", err)
	}
	_, err := runtimeStore.QueueOutbox(noderuntime.OutboxItem{
		Kind:           noderuntime.OutboxKindMessageAck,
		Status:         noderuntime.OutboxStatusPending,
		IdempotencyKey: "idem-fail",
		PayloadJSON: mustMarshalJSON(communication.AckInput{
			NodeRef:                 "node_test",
			CredentialToken:         "[redacted]",
			CommunicationMessageRef: "communication_message_fail",
			AckStatus:               communication.AckStatusCompleted,
			IdempotencyKey:          "idem-fail",
		}),
	})
	if err != nil {
		t.Fatalf("QueueOutbox failed: %v", err)
	}
	env := noderuntime.RuntimeEnv(store.ConfigPath, store.StatePath, store.DataDir, "node_test", "workspace-test", server.URL, true)
	flush, err := flushDueOutbox(context.Background(), env, runtimeStore, 10)
	if err != nil {
		t.Fatalf("flushDueOutbox failed: %v", err)
	}
	if flush.Failed != 1 || flush.Summary.Counts[noderuntime.OutboxStatusFailed] != 1 {
		t.Fatalf("expected retryable failure, got %#v", flush)
	}
}

func testNodeAgentStore(t *testing.T, dir string, mainURL string, state State) Store {
	t.Helper()
	store := Store{
		ConfigPath: filepath.Join(dir, "config", "config.json"),
		StatePath:  filepath.Join(dir, "state", "state.json"),
		DataDir:    filepath.Join(dir, "data"),
	}
	if err := store.EnsureDataDirs(); err != nil {
		t.Fatalf("EnsureDataDirs failed: %v", err)
	}
	if err := store.SaveConfig(Config{
		MainURL:     mainURL,
		NodeKey:     "workspace-test",
		DisplayName: "Workspace Test",
	}); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}
	if err := store.SaveState(state); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}
	return store
}
