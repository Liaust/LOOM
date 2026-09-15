package capabilityruntime

import (
	"encoding/json"
	"testing"
)

func TestServiceManagerRuntimeConfigIsClosed(t *testing.T) {
	for _, test := range []struct {
		name  string
		raw   string
		valid bool
	}{{"valid", `{"allowlist_key":"example","operation":"status"}`, true}, {"command", `{"allowlist_key":"example","operation":"exec"}`, false}, {"unit", `{"allowlist_key":"example","operation":"status","unit":"bad"}`, false}, {"argv", `{"allowlist_key":"example","operation":"status","argv":["sh"]}`, false}} {
		t.Run(test.name, func(t *testing.T) {
			result := NormalizeAndValidate(KindServiceManager, json.RawMessage(test.raw), ValidationModeRegister)
			if result.Valid != test.valid {
				t.Fatalf("valid=%v errors=%#v", result.Valid, result.Errors)
			}
		})
	}
}
