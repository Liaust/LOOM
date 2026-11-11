package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

type watchRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w watchRecorder) Flush() { w.ResponseRecorder.Flush(); w.cancel() }

func TestFileWatchPostReachesTrustedSubscriptionWithoutOpeningOtherWrites(t *testing.T) {
	handler, _ := fileServerFixture(t)
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), localFileTransportKey{}, true))
	defer cancel()
	request := httptest.NewRequest("POST", "/v1/files/watch", strings.NewReader(`{"root":"box","paths":["."]}`)).WithContext(ctx)
	recorded := httptest.NewRecorder()
	handler.ServeHTTP(watchRecorder{recorded, cancel}, request)
	if recorded.Code != 200 || !strings.Contains(recorded.Body.String(), `"type":"ready"`) {
		t.Fatalf("POST watch refused: %d %s", recorded.Code, recorded.Body.String())
	}
	for _, route := range []string{"/v1/files/entries", "/v1/files/content", "/v1/files/stat"} {
		request := httptest.NewRequest("POST", route, nil).WithContext(context.WithValue(t.Context(), localFileTransportKey{}, true))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 405 {
			t.Fatalf("read-only interface opened: %s %d", route, response.Code)
		}
	}
}
