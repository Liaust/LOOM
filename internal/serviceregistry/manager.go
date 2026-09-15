package serviceregistry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

const (
	DefaultManagerTimeout     = 20 * time.Second
	ServiceManagerRuntimeKind = "service_manager"
)

type ManagerRequest struct {
	AllowlistKey string    `json:"allowlist_key"`
	Operation    Operation `json:"operation"`
	LogLines     int       `json:"log_lines,omitempty"`
	LogMaxBytes  int       `json:"log_max_bytes,omitempty"`
	LogMaxAgeSec int       `json:"log_max_age_seconds,omitempty"`
}

type ManagerRunner interface {
	Run(context.Context, AllowlistRecord, ManagerRequest) (ManagerResult, error)
}

type ManagerService struct {
	Allowlist Allowlist
	Systemd   ManagerRunner
	Launchd   ManagerRunner
	Timeout   time.Duration
}

func (service ManagerService) Execute(ctx context.Context, request ManagerRequest) (ManagerResult, error) {
	record, ok := service.Allowlist.Lookup(request.AllowlistKey)
	if !ok {
		return ManagerResult{}, fmt.Errorf("service is not present in the reviewed node allowlist")
	}
	if _, err := StandardOperationPolicy(request.Operation); err != nil {
		return ManagerResult{}, err
	}
	if !operationAllowed(record.Operations, request.Operation) {
		return ManagerResult{}, fmt.Errorf("operation %s is not allowed for service", request.Operation)
	}
	if request.Operation == OperationLogs {
		limits, _ := record.LogLimits.Normalize()
		if err := ValidateLogSelectors(request.LogLines, request.LogMaxBytes, request.LogMaxAgeSec, limits); err != nil {
			return ManagerResult{}, err
		}
		if request.LogLines == 0 {
			request.LogLines = limits.MaxLines
		}
		if request.LogMaxBytes == 0 {
			request.LogMaxBytes = limits.MaxBytes
		}
		if request.LogMaxAgeSec == 0 {
			request.LogMaxAgeSec = limits.MaxAgeSeconds
		}
	} else if request.LogLines != 0 || request.LogMaxBytes != 0 || request.LogMaxAgeSec != 0 {
		return ManagerResult{}, fmt.Errorf("log selectors are valid only for the logs operation")
	}
	runner := service.Systemd
	if record.Manager == ManagerLaunchd {
		runner = service.Launchd
	}
	if runner == nil {
		return ManagerResult{}, fmt.Errorf("service manager runner is unavailable")
	}
	timeout := service.Timeout
	if timeout <= 0 {
		timeout = DefaultManagerTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := runner.Run(runCtx, record, request)
	if err != nil {
		return ManagerResult{}, fmt.Errorf("service manager operation failed: %s", redactManagerText(err.Error(), 1024))
	}
	result.Operation = request.Operation
	redacted, _, err := RedactManagerResult(result, record.LogLimits)
	return redacted, err
}

func redactManagerText(value string, maxBytes int) string {
	report := ResultRedactionReport{}
	return redactServiceText(value, maxBytes, &report)
}

func operationAllowed(values []Operation, operation Operation) bool {
	for _, value := range values {
		if value == operation {
			return true
		}
	}
	return false
}

func DecodeManagerRequest(raw json.RawMessage) (ManagerRequest, error) {
	var request ManagerRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return ManagerRequest{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ManagerRequest{}, fmt.Errorf("manager request must contain one JSON object")
	}
	return request, nil
}
