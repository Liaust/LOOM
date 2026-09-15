package enrollmentflow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/response"
)

func TestHTTPMainRunnerEnrollmentFlowMethods(t *testing.T) {
	approvedAt := time.Unix(1234, 0).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/node-enrollment-tokens":
			response.WriteJSON(w, http.StatusCreated, response.Success("test", nodes.CreateEnrollmentTokenResult{
				Token: nodes.EnrollmentToken{
					NodeEnrollmentTokenID: "node_enrollment_token_test",
					TokenHint:             "hint",
					Status:                nodes.EnrollmentTokenActive,
					ExpiresAt:             time.Unix(3600, 0).UTC(),
				},
				TokenValue: "node_enroll_super_secret",
			}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/node-enrollment-requests/node_enrollment_request_test/approve":
			response.WriteJSON(w, http.StatusOK, response.Success("test", nodes.ApproveEnrollmentResult{
				Request: nodes.EnrollmentRequest{
					NodeEnrollmentRequestID: "node_enrollment_request_test",
					Status:                  nodes.EnrollmentRequestApproved,
					ApprovedAt:              &approvedAt,
				},
				Node: nodes.Node{
					NodeID:        "node_test",
					NodeKey:       "macbook",
					PresenceState: nodes.PresenceOnline,
				},
				Credential: nodes.NodeCredential{
					NodeCredentialID: "node_credential_test",
					NodeID:           "node_test",
					CredentialHint:   "cred_hint",
					Status:           nodes.NodeCredentialActive,
				},
				CredentialToken: "node_cred_super_secret",
			}))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/nodes/node_test/health":
			response.WriteJSON(w, http.StatusOK, response.Success("test", nodes.NodeHealth{
				Node: nodes.Node{
					NodeID:        "node_test",
					NodeKey:       "macbook",
					PresenceState: nodes.PresenceOnline,
				},
				LastHeartbeat: &nodes.Heartbeat{
					NodeHeartbeatID: "node_heartbeat_test",
					NodeID:          "node_test",
					PresenceState:   nodes.PresenceOnline,
					ReceivedAt:      time.Unix(4321, 0).UTC(),
				},
			}))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	runner := NewHTTPMainRunner(server.URL, "corr_test")
	token, err := runner.CreateEnrollmentToken(context.Background(), CreateTokenInput{TTLSeconds: 60})
	if err != nil {
		t.Fatalf("CreateEnrollmentToken returned error: %v", err)
	}
	if token.TokenValue != "node_enroll_super_secret" || token.TokenID != "node_enrollment_token_test" {
		t.Fatalf("token mismatch: %#v", token)
	}
	approval, err := runner.ApproveEnrollment(context.Background(), "node_enrollment_request_test")
	if err != nil {
		t.Fatalf("ApproveEnrollment returned error: %v", err)
	}
	if approval.CredentialToken != "node_cred_super_secret" || approval.NodeID != "node_test" {
		t.Fatalf("approval mismatch: %#v", approval)
	}
	health, err := runner.GetNodeHealth(context.Background(), "node_test")
	if err != nil {
		t.Fatalf("GetNodeHealth returned error: %v", err)
	}
	if health.HeartbeatID != "node_heartbeat_test" || health.PresenceState != nodes.PresenceOnline {
		t.Fatalf("health mismatch: %#v", health)
	}
}
