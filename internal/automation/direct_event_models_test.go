package automation

import (
	"encoding/json"
	"testing"
)

func TestDirectEventJSONOmitsRawBody(t *testing.T) {
	event := DirectEvent{
		DirectEventID: "direct_event_test",
		Status:        DirectEventStatusAccepted,
		RawBodyJSON:   json.RawMessage(`{"secret":"stored separately"}`),
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if _, ok := object["raw_body_json"]; ok {
		t.Fatalf("raw_body_json should be omitted from normal direct event JSON: %s", string(raw))
	}
}

func TestNormalizeCommunicationProfileDefaultsSyncWaitTimeout(t *testing.T) {
	raw, err := normalizeCommunicationProfile(json.RawMessage(`{}`), DirectEventResponseSyncWait)
	if err != nil {
		t.Fatalf("normalizeCommunicationProfile returned error: %v", err)
	}
	var profile CommunicationProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if profile.ResponseMode != DirectEventResponseSyncWait {
		t.Fatalf("ResponseMode = %q, want %q", profile.ResponseMode, DirectEventResponseSyncWait)
	}
	if profile.SyncWaitTimeoutSeconds != defaultDirectEventSyncWaitSeconds {
		t.Fatalf("SyncWaitTimeoutSeconds = %d, want %d", profile.SyncWaitTimeoutSeconds, defaultDirectEventSyncWaitSeconds)
	}
}

func TestNormalizeCommunicationProfileRejectsLargeSyncWaitTimeout(t *testing.T) {
	_, err := normalizeCommunicationProfile(json.RawMessage(`{"sync_wait_timeout_seconds":301}`), DirectEventResponseSyncWait)
	if err == nil {
		t.Fatal("normalizeCommunicationProfile accepted an excessive sync_wait_timeout_seconds")
	}
}
