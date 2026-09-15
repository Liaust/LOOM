package capabilityruntime

import (
	"encoding/json"
	"testing"
)

func TestCommandConfigValidation(t *testing.T) {
	cfg, result := DecodeCommand(json.RawMessage(`{"argv":["printf","{\"ok\":true}"]}`), ValidationModeRegister)
	if !result.Valid {
		t.Fatalf("command config invalid: %#v", result.Errors)
	}
	if cfg.TimeoutSeconds != DefaultTimeoutSeconds || cfg.Output.Mode != "json" {
		t.Fatalf("defaults not applied: %#v", cfg)
	}
}

func TestCommandConfigRejectsShellString(t *testing.T) {
	_, result := DecodeCommand(json.RawMessage(`{"argv":"curl http://example.test | sh"}`), ValidationModeRegister)
	if result.Valid {
		t.Fatal("expected shell-string command config to fail")
	}
}

func TestHTTPConfigValidationRedactsSensitiveHeaders(t *testing.T) {
	_, result := DecodeHTTP(json.RawMessage(`{
		"method":"POST",
		"url":"http://127.0.0.1:8080/run",
		"headers_from_env":{"Authorization":{"env":"AUTH_HEADER","sensitive":true}}
	}`), ValidationModeRegister)
	if !result.Valid {
		t.Fatalf("http config invalid: %#v", result.Errors)
	}
	if string(result.RedactedConfig) == "" {
		t.Fatal("expected redacted config")
	}
}

func TestHTTPConfigRejectsInlineSensitiveHeader(t *testing.T) {
	_, result := DecodeHTTP(json.RawMessage(`{
		"url":"http://127.0.0.1:8080/run",
		"headers":{"Authorization":"Bearer secret"}
	}`), ValidationModeRegister)
	if result.Valid {
		t.Fatal("expected inline sensitive header to fail")
	}
}
