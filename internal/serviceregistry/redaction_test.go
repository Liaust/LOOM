package serviceregistry

import (
	"strings"
	"testing"
)

func TestRedactManagerResultRemovesSensitiveLogData(t *testing.T) {
	result, report, err := RedactManagerResult(ManagerResult{
		Operation: OperationLogs, Success: true, ProcessState: ProcessStateRunning,
		Message: "read /Users/leonardo/private/service.log at https://main.invalid/logs\x00",
		LogLines: []string{
			"Authorization: Bearer abc123",
			"postgres://loom:secret@example.invalid/loom",
			"opened /srv/loom/private/data.db",
			"public status ok",
		},
	}, LogLimits{})
	if err != nil {
		t.Fatal(err)
	}
	combined := result.Message + strings.Join(result.LogLines, "\n")
	for _, leaked := range []string{"abc123", "loom:secret", "/Users/", "/srv/", "https://", "example.invalid", "\x00"} {
		if strings.Contains(combined, leaked) {
			t.Fatalf("sensitive value %q leaked: %s", leaked, combined)
		}
	}
	for _, marker := range []string{RedactedPath, RedactedURI, RedactedControl} {
		if !strings.Contains(combined, marker) {
			t.Fatalf("expected marker %q in %s", marker, combined)
		}
	}
	if report.Secrets < 2 || report.Paths < 2 || report.URIs < 1 || report.Controls < 1 {
		t.Fatalf("redaction report incomplete: %#v", report)
	}
}

func TestRedactManagerResultEnforcesLineAndByteBounds(t *testing.T) {
	tests := []struct {
		name   string
		limits LogLimits
		lines  []string
		check  func(t *testing.T, result ManagerResult, report ResultRedactionReport)
	}{
		{
			name: "line count", limits: LogLimits{MaxLines: 2, MaxBytes: 100, MaxLineBytes: 50, MaxAgeSeconds: 60}, lines: []string{"one", "two", "three"},
			check: func(t *testing.T, result ManagerResult, report ResultRedactionReport) {
				if len(result.LogLines) != 2 || report.LinesDropped != 1 || !result.Truncated {
					t.Fatalf("line bounds mismatch: result=%#v report=%#v", result, report)
				}
			},
		},
		{
			name: "byte count", limits: LogLimits{MaxLines: 3, MaxBytes: 5, MaxLineBytes: 5, MaxAgeSeconds: 60}, lines: []string{"12345", "67890"},
			check: func(t *testing.T, result ManagerResult, report ResultRedactionReport) {
				if len(result.LogLines) != 1 || len(result.LogLines[0]) != 5 || report.LinesDropped != 1 || !result.Truncated {
					t.Fatalf("byte bounds mismatch: result=%#v report=%#v", result, report)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, report, err := RedactManagerResult(ManagerResult{Operation: OperationLogs, Success: true, ProcessState: ProcessStateRunning, LogLines: test.lines}, test.limits)
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, result, report)
		})
	}
}

func TestRedactManagerResultHandlesCommonPathBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		message   string
		forbidden string
		wantPaths int
	}{
		{name: "colon-prefixed POSIX", message: "path:/srv/loom/private/data.db", forbidden: "/srv/loom", wantPaths: 1},
		{name: "bracketed POSIX", message: "[/srv/loom/private/data.db]", forbidden: "/srv/loom", wantPaths: 1},
		{name: "Windows drive", message: `path:C:\Users\the operator\private.log`, forbidden: `C:\Users`, wantPaths: 1},
		{name: "Windows UNC", message: `[\\fileserver\private\service.log]`, forbidden: `\\fileserver`, wantPaths: 1},
		{name: "ordinary slash-free text", message: "service.status loom-project-api.service status:running", wantPaths: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, report, err := RedactManagerResult(ManagerResult{Operation: OperationStatus, Success: true, ProcessState: ProcessStateRunning, Message: test.message}, LogLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if test.forbidden != "" && strings.Contains(result.Message, test.forbidden) {
				t.Fatalf("path leaked in %q", result.Message)
			}
			if report.Paths != test.wantPaths {
				t.Fatalf("path redactions = %d, want %d; message=%q", report.Paths, test.wantPaths, result.Message)
			}
			if test.wantPaths > 0 && !strings.Contains(result.Message, RedactedPath) {
				t.Fatalf("redaction marker missing: %q", result.Message)
			}
			if test.wantPaths == 0 && result.Message != test.message {
				t.Fatalf("ordinary text changed: got %q want %q", result.Message, test.message)
			}
		})
	}
}

func TestRedactManagerResultRemovesUnicodeC1Controls(t *testing.T) {
	tests := []struct {
		name      string
		character rune
	}{
		{name: "next line", character: '\u0085'},
		{name: "control sequence introducer", character: '\u009b'},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := "before" + string(test.character) + "after"
			result, report, err := RedactManagerResult(ManagerResult{
				Operation: OperationStatus, Success: true, ProcessState: ProcessStateRunning, Message: message,
			}, LogLimits{})
			if err != nil {
				t.Fatal(err)
			}
			if strings.ContainsRune(result.Message, test.character) {
				t.Fatalf("Unicode control U+%04X leaked in %q", test.character, result.Message)
			}
			if !strings.Contains(result.Message, RedactedControl) || report.Controls != 1 {
				t.Fatalf("Unicode control redaction mismatch: result=%q report=%#v", result.Message, report)
			}
			if strings.ContainsAny(result.Message, "\r\n") {
				t.Fatalf("manager result is not single-line: %q", result.Message)
			}
		})
	}
}

func TestRedactManagerResultRejectsInvalidStateAndLimits(t *testing.T) {
	tests := []struct {
		name   string
		result ManagerResult
		limits LogLimits
	}{
		{name: "provider state used as process state", result: ManagerResult{Operation: OperationStatus, ProcessState: ObservedProcessState(ProviderStateActive)}},
		{name: "arbitrary operation", result: ManagerResult{Operation: "exec", ProcessState: ProcessStateUnknown}},
		{name: "unbounded age", result: ManagerResult{Operation: OperationLogs, ProcessState: ProcessStateUnknown}, limits: LogLimits{MaxAgeSeconds: MaximumLogAgeSeconds + 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := RedactManagerResult(test.result, test.limits); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
