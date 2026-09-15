package capabilityruntime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"loom.local/loom/internal/redaction"
)

const (
	KindScript         = "script"
	KindCommand        = "command"
	KindHTTP           = "http"
	KindNodeAgent      = "node_agent"
	KindServiceManager = "service_manager"

	ValidationModeRegister ValidationMode = "register"
	ValidationModeExecute  ValidationMode = "execute"

	DefaultTimeoutSeconds   = 30
	DefaultMaxStdoutBytes   = 65536
	DefaultMaxStderrBytes   = 32768
	DefaultMaxHTTPBodyBytes = 65536
)

type ValidationMode string

type Diagnostic struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Field      string `json:"field,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
}

type ValidationResult struct {
	RuntimeKind    string          `json:"runtime_kind"`
	Valid          bool            `json:"valid"`
	Config         json.RawMessage `json:"config,omitempty"`
	RedactedConfig json.RawMessage `json:"redacted_config,omitempty"`
	Warnings       []Diagnostic    `json:"warnings,omitempty"`
	Errors         []Diagnostic    `json:"errors,omitempty"`
}

type EnvRef struct {
	Env       string `json:"env" yaml:"env"`
	Sensitive bool   `json:"sensitive,omitempty" yaml:"sensitive"`
	Required  bool   `json:"required,omitempty" yaml:"required"`
}

type InputSpec struct {
	Mode string `json:"mode,omitempty" yaml:"mode"`
}

type CommandOutputSpec struct {
	Mode           string `json:"mode,omitempty" yaml:"mode"`
	MaxStdoutBytes int    `json:"max_stdout_bytes,omitempty" yaml:"max_stdout_bytes"`
	MaxStderrBytes int    `json:"max_stderr_bytes,omitempty" yaml:"max_stderr_bytes"`
}

type CommandConfig struct {
	Argv             []string          `json:"argv" yaml:"argv"`
	WorkingDir       string            `json:"working_dir,omitempty" yaml:"working_dir"`
	TimeoutSeconds   int               `json:"timeout_seconds,omitempty" yaml:"timeout_seconds"`
	Stdin            InputSpec         `json:"stdin,omitempty" yaml:"stdin"`
	Output           CommandOutputSpec `json:"output,omitempty" yaml:"output"`
	Env              map[string]string `json:"env,omitempty" yaml:"env"`
	EnvRefs          map[string]EnvRef `json:"env_refs,omitempty" yaml:"env_refs"`
	AllowedExitCodes []int             `json:"allowed_exit_codes,omitempty" yaml:"allowed_exit_codes"`
	Metadata         map[string]any    `json:"metadata,omitempty" yaml:"metadata"`
}

type HTTPBodySpec struct {
	Mode string `json:"mode,omitempty" yaml:"mode"`
}

type HTTPResponseSpec struct {
	Mode            string `json:"mode,omitempty" yaml:"mode"`
	MaxBodyBytes    int    `json:"max_body_bytes,omitempty" yaml:"max_body_bytes"`
	SuccessStatuses []int  `json:"success_statuses,omitempty" yaml:"success_statuses"`
}

type HTTPNetworkSpec struct {
	AllowPublic  bool     `json:"allow_public,omitempty" yaml:"allow_public"`
	AllowedHosts []string `json:"allowed_hosts,omitempty" yaml:"allowed_hosts"`
}

type HTTPConfig struct {
	Method         string            `json:"method,omitempty" yaml:"method"`
	URL            string            `json:"url" yaml:"url"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty" yaml:"timeout_seconds"`
	Headers        map[string]string `json:"headers,omitempty" yaml:"headers"`
	HeadersFromEnv map[string]EnvRef `json:"headers_from_env,omitempty" yaml:"headers_from_env"`
	Body           HTTPBodySpec      `json:"body,omitempty" yaml:"body"`
	Response       HTTPResponseSpec  `json:"response,omitempty" yaml:"response"`
	Network        HTTPNetworkSpec   `json:"network,omitempty" yaml:"network"`
	Metadata       map[string]any    `json:"metadata,omitempty" yaml:"metadata"`
}

type ServiceManagerConfig struct {
	AllowlistKey string `json:"allowlist_key" yaml:"allowlist_key"`
	Operation    string `json:"operation" yaml:"operation"`
}

func NormalizeAndValidate(kind string, raw json.RawMessage, mode ValidationMode) ValidationResult {
	kind = strings.TrimSpace(kind)
	switch kind {
	case KindCommand:
		_, result := DecodeCommand(raw, mode)
		return result
	case KindHTTP:
		_, result := DecodeHTTP(raw, mode)
		return result
	case KindServiceManager:
		_, result := DecodeServiceManager(raw)
		return result
	default:
		raw = normalizeObject(raw)
		return ValidationResult{
			RuntimeKind:    kind,
			Valid:          true,
			Config:         raw,
			RedactedConfig: Redact(kind, raw),
		}
	}
}

func DecodeServiceManager(raw json.RawMessage) (ServiceManagerConfig, ValidationResult) {
	result := ValidationResult{RuntimeKind: KindServiceManager}
	raw = normalizeObject(raw)
	var cfg ServiceManagerConfig
	if err := decodeStrict(raw, &cfg); err != nil {
		result.Errors = append(result.Errors, Diagnostic{Code: "runtime_service_manager.config_invalid", Message: "service manager runtime config must match the closed schema: " + err.Error()})
		return ServiceManagerConfig{}, finish(result, cfg)
	}
	cfg.AllowlistKey = strings.TrimSpace(cfg.AllowlistKey)
	cfg.Operation = strings.TrimSpace(cfg.Operation)
	if cfg.AllowlistKey == "" || len(cfg.AllowlistKey) > 63 {
		result.Errors = append(result.Errors, Diagnostic{Code: "runtime_service_manager.allowlist_key_invalid", Message: "allowlist_key is required and bounded"})
	}
	switch cfg.Operation {
	case "status", "start", "stop", "restart", "logs":
	default:
		result.Errors = append(result.Errors, Diagnostic{Code: "runtime_service_manager.operation_invalid", Message: "operation is not supported"})
	}
	return cfg, finish(result, cfg)
}

func DecodeCommand(raw json.RawMessage, mode ValidationMode) (CommandConfig, ValidationResult) {
	result := ValidationResult{RuntimeKind: KindCommand}
	raw = normalizeObject(raw)
	var cfg CommandConfig
	if err := decodeStrict(raw, &cfg); err != nil {
		result.Errors = append(result.Errors, Diagnostic{Code: "runtime_command.config_invalid", Message: "command runtime config must match the command runtime schema: " + err.Error()})
		return CommandConfig{}, finish(result, cfg)
	}
	cfg = normalizeCommand(cfg)
	validateCommand(cfg, mode, &result)
	return cfg, finish(result, cfg)
}

func DecodeHTTP(raw json.RawMessage, mode ValidationMode) (HTTPConfig, ValidationResult) {
	result := ValidationResult{RuntimeKind: KindHTTP}
	raw = normalizeObject(raw)
	var cfg HTTPConfig
	if err := decodeStrict(raw, &cfg); err != nil {
		result.Errors = append(result.Errors, Diagnostic{Code: "runtime_http.config_invalid", Message: "http runtime config must match the http runtime schema: " + err.Error()})
		return HTTPConfig{}, finish(result, cfg)
	}
	cfg = normalizeHTTP(cfg)
	validateHTTP(cfg, mode, &result)
	return cfg, finish(result, cfg)
}

func Redact(kind string, raw json.RawMessage) json.RawMessage {
	return redaction.JSON(normalizeObject(raw), redaction.DefaultProfile())
}

func IsPrivateOrAllowedHTTPHost(cfg HTTPConfig) bool {
	if cfg.Network.AllowPublic {
		return true
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return false
	}
	allowed := map[string]bool{
		"localhost": true,
		"127.0.0.1": true,
		"::1":       true,
	}
	for _, item := range cfg.Network.AllowedHosts {
		item = strings.ToLower(strings.TrimSpace(item))
		if item != "" {
			allowed[item] = true
		}
	}
	if allowed[host] {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}

func normalizeCommand(cfg CommandConfig) CommandConfig {
	for i := range cfg.Argv {
		cfg.Argv[i] = strings.TrimSpace(cfg.Argv[i])
	}
	cfg.WorkingDir = strings.TrimSpace(cfg.WorkingDir)
	if cfg.TimeoutSeconds == 0 {
		cfg.TimeoutSeconds = DefaultTimeoutSeconds
	}
	cfg.Stdin.Mode = defaultString(strings.TrimSpace(cfg.Stdin.Mode), "input_json")
	cfg.Output.Mode = defaultString(strings.TrimSpace(cfg.Output.Mode), "json")
	if cfg.Output.MaxStdoutBytes == 0 {
		cfg.Output.MaxStdoutBytes = DefaultMaxStdoutBytes
	}
	if cfg.Output.MaxStderrBytes == 0 {
		cfg.Output.MaxStderrBytes = DefaultMaxStderrBytes
	}
	if len(cfg.AllowedExitCodes) == 0 {
		cfg.AllowedExitCodes = []int{0}
	}
	sort.Ints(cfg.AllowedExitCodes)
	if cfg.Env == nil {
		cfg.Env = map[string]string{}
	}
	if cfg.EnvRefs == nil {
		cfg.EnvRefs = map[string]EnvRef{}
	}
	for key, ref := range cfg.EnvRefs {
		ref.Env = strings.TrimSpace(ref.Env)
		cfg.EnvRefs[key] = ref
	}
	return cfg
}

func validateCommand(cfg CommandConfig, mode ValidationMode, result *ValidationResult) {
	if len(cfg.Argv) == 0 {
		addError(result, "runtime_command.argv_required", "argv must contain at least one command element", "argv")
	}
	for i, part := range cfg.Argv {
		if part == "" {
			addError(result, "runtime_command.argv_empty", "argv entries must be non-empty strings", fmt.Sprintf("argv[%d]", i))
		}
	}
	if cfg.TimeoutSeconds < 1 || cfg.TimeoutSeconds > 86400 {
		addError(result, "runtime_command.timeout_invalid", "timeout_seconds must be between 1 and 86400", "timeout_seconds")
	}
	switch cfg.Stdin.Mode {
	case "", "input_json", "none":
	default:
		addError(result, "runtime_command.stdin_mode_invalid", "stdin.mode must be input_json or none", "stdin.mode")
	}
	switch cfg.Output.Mode {
	case "json", "text":
	default:
		addError(result, "runtime_command.output_mode_invalid", "output.mode must be json or text", "output.mode")
	}
	if cfg.Output.MaxStdoutBytes < 1 || cfg.Output.MaxStdoutBytes > 10485760 {
		addError(result, "runtime_command.max_stdout_invalid", "output.max_stdout_bytes must be between 1 and 10485760", "output.max_stdout_bytes")
	}
	if cfg.Output.MaxStderrBytes < 1 || cfg.Output.MaxStderrBytes > 10485760 {
		addError(result, "runtime_command.max_stderr_invalid", "output.max_stderr_bytes must be between 1 and 10485760", "output.max_stderr_bytes")
	}
	for _, code := range cfg.AllowedExitCodes {
		if code < 0 || code > 255 {
			addError(result, "runtime_command.exit_code_invalid", "allowed_exit_codes entries must be between 0 and 255", "allowed_exit_codes")
		}
	}
	for key, ref := range cfg.EnvRefs {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(ref.Env) == "" {
			addError(result, "runtime_command.env_ref_invalid", "env_refs entries require a target env var and source env name", "env_refs")
		}
	}
	_ = mode
}

func normalizeHTTP(cfg HTTPConfig) HTTPConfig {
	cfg.Method = strings.ToUpper(defaultString(strings.TrimSpace(cfg.Method), "POST"))
	cfg.URL = strings.TrimSpace(cfg.URL)
	if cfg.TimeoutSeconds == 0 {
		cfg.TimeoutSeconds = DefaultTimeoutSeconds
	}
	if cfg.Headers == nil {
		cfg.Headers = map[string]string{}
	}
	if cfg.HeadersFromEnv == nil {
		cfg.HeadersFromEnv = map[string]EnvRef{}
	}
	for key, ref := range cfg.HeadersFromEnv {
		ref.Env = strings.TrimSpace(ref.Env)
		cfg.HeadersFromEnv[key] = ref
	}
	cfg.Body.Mode = defaultString(strings.TrimSpace(cfg.Body.Mode), "input_json")
	cfg.Response.Mode = defaultString(strings.TrimSpace(cfg.Response.Mode), "json")
	if cfg.Response.MaxBodyBytes == 0 {
		cfg.Response.MaxBodyBytes = DefaultMaxHTTPBodyBytes
	}
	if len(cfg.Response.SuccessStatuses) == 0 {
		cfg.Response.SuccessStatuses = []int{200, 201, 202}
	}
	sort.Ints(cfg.Response.SuccessStatuses)
	if len(cfg.Network.AllowedHosts) == 0 {
		cfg.Network.AllowedHosts = []string{"127.0.0.1", "localhost", "::1"}
	}
	return cfg
}

func validateHTTP(cfg HTTPConfig, mode ValidationMode, result *ValidationResult) {
	switch cfg.Method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		addError(result, "runtime_http.method_invalid", "method must be GET, POST, PUT, PATCH, or DELETE", "method")
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		addError(result, "runtime_http.url_invalid", "url must be an absolute http or https URL", "url")
	} else if parsed.Scheme != "http" && parsed.Scheme != "https" {
		addError(result, "runtime_http.scheme_invalid", "url scheme must be http or https", "url")
	}
	if cfg.TimeoutSeconds < 1 || cfg.TimeoutSeconds > 300 {
		addError(result, "runtime_http.timeout_invalid", "timeout_seconds must be between 1 and 300", "timeout_seconds")
	}
	switch cfg.Body.Mode {
	case "", "input_json", "none":
	default:
		addError(result, "runtime_http.body_mode_invalid", "body.mode must be input_json or none", "body.mode")
	}
	switch cfg.Response.Mode {
	case "json", "text":
	default:
		addError(result, "runtime_http.response_mode_invalid", "response.mode must be json or text", "response.mode")
	}
	if cfg.Response.MaxBodyBytes < 1 || cfg.Response.MaxBodyBytes > 10485760 {
		addError(result, "runtime_http.max_body_invalid", "response.max_body_bytes must be between 1 and 10485760", "response.max_body_bytes")
	}
	for _, status := range cfg.Response.SuccessStatuses {
		if status < 100 || status > 599 {
			addError(result, "runtime_http.status_invalid", "response.success_statuses entries must be HTTP status codes", "response.success_statuses")
		}
	}
	for key, value := range cfg.Headers {
		if redaction.String(value, categoryForKey(key)...) == redaction.Replacement {
			addError(result, "runtime_http.inline_sensitive_header_forbidden", "sensitive HTTP headers must use headers_from_env", "headers."+key)
		}
	}
	for key, ref := range cfg.HeadersFromEnv {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(ref.Env) == "" {
			addError(result, "runtime_http.header_env_ref_invalid", "headers_from_env entries require a header name and source env name", "headers_from_env")
		}
	}
	if mode == ValidationModeExecute && !IsPrivateOrAllowedHTTPHost(cfg) {
		addError(result, "runtime_http.network_denied", "http runtime target is not allowed by network policy", "network")
	}
}

func finish[T any](result ValidationResult, cfg T) ValidationResult {
	result.Valid = len(result.Errors) == 0
	if raw, err := json.Marshal(cfg); err == nil {
		result.Config = json.RawMessage(raw)
		result.RedactedConfig = Redact(result.RuntimeKind, result.Config)
	}
	return result
}

func decodeStrict(raw json.RawMessage, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

func normalizeObject(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage(`{}`)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return raw
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return raw
	}
	return normalized
}

func addError(result *ValidationResult, code, message, field string) {
	result.Errors = append(result.Errors, Diagnostic{Code: code, Message: message, Field: field})
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func categoryForKey(key string) []redaction.Category {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
	for _, fragment := range []string{"authorization", "token", "secret", "password", "credential", "api_key", "apikey"} {
		if strings.Contains(normalized, fragment) {
			return []redaction.Category{redaction.Secret}
		}
	}
	return nil
}
