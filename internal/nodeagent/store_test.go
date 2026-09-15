package nodeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/filesystemconnector"
	loomsync "loom.local/loom/internal/sync"
)

func TestStoreConfigAndStateRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := Store{
		ConfigPath: filepath.Join(dir, "config", "config.json"),
		StatePath:  filepath.Join(dir, "state", "state.json"),
		DataDir:    filepath.Join(dir, "data"),
	}
	config := Config{
		MainURL:     "http://10.44.0.2:8080/",
		NodeKey:     "workspace-test",
		DisplayName: "Workspace Test",
		Filesystem: filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
			filesystemconnector.DefaultSafeRoot("slice13", dir),
		}},
	}
	if err := store.EnsureDataDirs(); err != nil {
		t.Fatalf("EnsureDataDirs failed: %v", err)
	}
	if err := store.SaveConfig(config); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}
	loadedConfig, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if loadedConfig.MainURL != "http://10.44.0.2:8080" {
		t.Fatalf("expected normalized main URL, got %q", loadedConfig.MainURL)
	}
	if loadedConfig.NodeKind != defaultNodeKind || loadedConfig.NodeRole != defaultNodeRole || loadedConfig.RuntimeClass != defaultRuntimeClass {
		t.Fatalf("expected default node classification, got %#v", loadedConfig)
	}
	if len(loadedConfig.Filesystem.SafeRoots) != 1 || loadedConfig.Filesystem.SafeRoots[0].RootKey != "slice13" {
		t.Fatalf("filesystem config did not round-trip: %#v", loadedConfig.Filesystem)
	}
	assertFileMode(t, store.ConfigPath, 0o600)

	state := State{
		EnrollmentRequestID: "node_enrollment_request_test",
		NodeID:              "node_test",
		NodeCredentialID:    "node_cred_test",
		CredentialToken:     "node_cred_secret",
		LastHeartbeat: &HeartbeatState{
			NodeHeartbeatID: "node_heartbeat_test",
			PresenceState:   "online",
			ReportedStatus:  "ok",
			ReceivedAt:      time.Now().UTC(),
		},
	}
	if err := store.SaveState(state); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}
	loadedState, err := store.LoadState()
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if loadedState.NodeID != state.NodeID || loadedState.CredentialToken != state.CredentialToken {
		t.Fatalf("state did not round-trip: %#v", loadedState)
	}
	assertFileMode(t, store.StatePath, 0o600)
}

func TestRootOptionsStoreUsesEnvironmentPathDefaults(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	statePath := filepath.Join(dir, "state.json")
	dataDir := filepath.Join(dir, "data")
	t.Setenv("LOOM_NODE_AGENT_CONFIG", configPath)
	t.Setenv("LOOM_NODE_AGENT_STATE", statePath)
	t.Setenv("LOOM_NODE_AGENT_DATA_DIR", dataDir)

	store, err := (&rootOptions{}).store()
	if err != nil {
		t.Fatalf("store returned error: %v", err)
	}
	if store.ConfigPath != configPath || store.StatePath != statePath || store.DataDir != dataDir {
		t.Fatalf("store paths = %#v", store)
	}

	overrideConfig := filepath.Join(dir, "override-config.json")
	override, err := (&rootOptions{configPath: overrideConfig}).store()
	if err != nil {
		t.Fatalf("override store returned error: %v", err)
	}
	if override.ConfigPath != overrideConfig || override.StatePath != statePath || override.DataDir != dataDir {
		t.Fatalf("override store paths = %#v", override)
	}
}

func TestDefaultConfigPathUsesXDGStyleConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	path, err := DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath returned error: %v", err)
	}
	if path != filepath.Join(home, ".config", "loom-node-agent", "config.json") {
		t.Fatalf("default config path = %q", path)
	}
}

func TestSaveConfigRejectsInvalidMainURL(t *testing.T) {
	t.Parallel()
	store := Store{
		ConfigPath: filepath.Join(t.TempDir(), "config.json"),
		StatePath:  filepath.Join(t.TempDir(), "state.json"),
		DataDir:    filepath.Join(t.TempDir(), "data"),
	}
	err := store.SaveConfig(Config{
		MainURL:     "10.44.0.2:8080",
		NodeKey:     "workspace-test",
		DisplayName: "Workspace Test",
	})
	if err == nil {
		t.Fatal("expected invalid URL error")
	}
}

func TestStoreSavesInboxMessageAndOutboxAck(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := Store{
		ConfigPath: filepath.Join(dir, "config", "config.json"),
		StatePath:  filepath.Join(dir, "state", "state.json"),
		DataDir:    filepath.Join(dir, "data"),
	}
	message := communication.Message{
		CommunicationMessageID: "communication_message_test",
		NodeID:                 "node_test",
		Direction:              communication.DirectionMainToNode,
		Kind:                   communication.KindMainPing,
		Status:                 communication.StatusDelivered,
		PayloadJSON:            json.RawMessage(`{"text":"hello"}`),
	}
	inboxPath, err := store.SaveInboxMessage(message)
	if err != nil {
		t.Fatalf("SaveInboxMessage failed: %v", err)
	}
	if filepath.Base(inboxPath) != "communication_message_test.json" {
		t.Fatalf("unexpected inbox path %s", inboxPath)
	}
	assertFileMode(t, inboxPath, 0o600)

	ackResult := communication.AckResult{
		Message: message,
		Ack: communication.MessageAck{
			CommunicationAckID:     "communication_ack_test",
			CommunicationMessageID: message.CommunicationMessageID,
			NodeID:                 message.NodeID,
			AckStatus:              communication.AckStatusCompleted,
			ResultJSON:             json.RawMessage(`{"pong":true}`),
		},
	}
	outboxPath, err := store.SaveOutboxAck(ackResult)
	if err != nil {
		t.Fatalf("SaveOutboxAck failed: %v", err)
	}
	if filepath.Base(outboxPath) != "communication_ack_test.json" {
		t.Fatalf("unexpected outbox path %s", outboxPath)
	}
	assertFileMode(t, outboxPath, 0o600)
}

func TestStoreLocalSyncQueueRoundTrip(t *testing.T) {
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
	state := State{
		NodeID:          "node_test",
		CredentialToken: "node_cred_test",
	}

	event, err := store.AppendLocalTestEvent(config, state, "hello", "", 0)
	if err != nil {
		t.Fatalf("AppendLocalTestEvent failed: %v", err)
	}
	if event.LocalSequence != 1 || event.SyncStatus != localSyncStatusPending {
		t.Fatalf("unexpected local event: %#v", event)
	}
	status, err := store.LocalSyncStatus(config, state)
	if err != nil {
		t.Fatalf("LocalSyncStatus failed: %v", err)
	}
	if status.Counts.Events != 1 || status.Counts.Pending != 1 || len(status.Cursors) != 1 || status.Cursors[0].LastQueuedSequence != 1 {
		t.Fatalf("unexpected pending local sync status: %#v", status)
	}

	err = store.ApplySyncPushResult(loomsync.PushBatchResult{
		Items: []loomsync.SyncItemResult{{
			LocalRef:  event.LocalEventID,
			ItemKind:  loomsync.ItemKindEvent,
			Status:    loomsync.ItemStatusAccepted,
			GlobalRef: "event_test",
		}},
		Cursors: []loomsync.SyncCursor{{
			StreamName:           loomsync.StreamEvents,
			LastAcceptedSequence: 1,
		}},
	})
	if err != nil {
		t.Fatalf("ApplySyncPushResult failed: %v", err)
	}
	status, err = store.LocalSyncStatus(config, state)
	if err != nil {
		t.Fatalf("LocalSyncStatus after apply failed: %v", err)
	}
	if status.Counts.Pending != 0 || status.Counts.Accepted != 1 || status.Cursors[0].LastAcceptedSequence != 1 {
		t.Fatalf("unexpected accepted local sync status: %#v", status)
	}
	assertFileMode(t, status.Paths.Events, 0o600)
	assertFileMode(t, status.Paths.Outbox, 0o600)
	assertFileMode(t, status.Paths.Cursors, 0o600)
	assertFileMode(t, status.Paths.Conflicts, 0o600)
}

func TestStoreLocalSyncObjectQueue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "note.md")
	if err := os.WriteFile(sourcePath, []byte("# Note\n\nunique object queue text\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
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
	state := State{
		NodeID:          "node_test",
		CredentialToken: "node_cred_test",
	}

	object, err := store.CreateLocalSyncObject(config, state, LocalSyncObjectCreateInput{
		Path:       sourcePath,
		ProjectRef: "project_test",
	})
	if err != nil {
		t.Fatalf("CreateLocalSyncObject failed: %v", err)
	}
	if object.LocalObjectID == "" || object.LocalVersionID == "" || object.HashURI == "" {
		t.Fatalf("unexpected local object: %#v", object)
	}
	status, err := store.LocalSyncStatus(config, state)
	if err != nil {
		t.Fatalf("LocalSyncStatus failed: %v", err)
	}
	if status.Counts.Objects != 1 || status.Counts.Pending != 1 {
		t.Fatalf("unexpected local object sync status: %#v", status)
	}
	assertFileMode(t, status.Paths.Objects, 0o600)
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s failed: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("expected %s mode %o, got %o", path, want, got)
	}
}
