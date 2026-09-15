package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loom.local/loom/internal/serviceregistry"
)

func TestServiceRoutesRejectUnsupportedMethodsAndUnknownRegistrationActions(t *testing.T) {
	handler := NewServer(Services{}, slog.Default()).Handler()
	tests := []struct {
		method, path, body string
		want               int
	}{
		{http.MethodPost, "/v1/services", "{}", http.StatusMethodNotAllowed},
		{http.MethodGet, "/v1/service-registrations/plan", "", http.StatusMethodNotAllowed},
		{http.MethodPost, "/v1/service-registrations/deploy", "{}", http.StatusNotFound},
		{http.MethodPost, "/v1/service-registrations/plan", "{\"registration\":{},\"command\":\"sh\"}", http.StatusBadRequest},
		{http.MethodPost, "/v1/services/provider_test/status/extra", "{}", http.StatusNotFound},
		{http.MethodPost, "/v1/service-registrations/plan/extra", "{}", http.StatusNotFound},
		{http.MethodPost, "/v1/services/provider_test/status", "{} {}", http.StatusBadRequest},
		{http.MethodPost, "/v1/service-registrations/plan", "{\"registration\":{}} {}", http.StatusBadRequest},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != test.want {
			t.Fatalf("%s %s status=%d body=%s", test.method, test.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestServiceOperationAPIRejectsNegativeLogSelectors(t *testing.T) {
	for _, input := range []ServiceOperationRequest{{Lines: -1}, {MaxBytes: -1}, {MaxAgeSeconds: -1}} {
		if err := validateServiceOperationRequest(serviceregistry.OperationLogs, input); err == nil {
			t.Fatalf("accepted negative selector: %#v", input)
		}
	}
	if err := validateServiceOperationRequest(serviceregistry.OperationLogs, ServiceOperationRequest{}); err != nil {
		t.Fatalf("reviewed defaults rejected: %v", err)
	}
}
