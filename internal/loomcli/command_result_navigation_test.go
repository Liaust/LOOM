package loomcli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/routing"
)

func TestCommandResultNavigationHumanAndFullJSON(t *testing.T) {
	ref := "capability_call_echo"
	call := routing.CapabilityCall{CapabilityCallID: ref, Status: "completed", Operation: "echo",
		ResultJSON: json.RawMessage(`{"outputs":{"message":"The retained answer"}}`),
		InputJSON:  json.RawMessage(`{"private":"not part of human output"}`), ResultRefsJSON: json.RawMessage(`[]`)}
	invocation := automation.Invocation{InvocationID: "invocation_echo", Status: "succeeded", CapabilityCallID: &ref}
	requests := 0
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, req *http.Request) {
		requests++
		if req.Method != http.MethodGet {
			t.Errorf("unexpected mutation: %s", req.Method)
		}
		switch req.URL.Path {
		case "/v1/capability-calls/" + ref:
			response.WriteJSON(w, 200, response.Success("original", call))
		case "/v1/invocations/invocation_echo":
			response.WriteJSON(w, 200, response.Success("original", invocation))
		default:
			t.Errorf("unexpected route: %s", req.URL.Path)
		}
	})
	defer stop()
	for _, command := range [][]string{{"invocation", "inspect", "invocation_echo"}, {"capability-call", "inspect", ref}} {
		before := requests
		out, stderr, err := executeRootCommand(append([]string{"--socket", socket}, command...)...)
		if err != nil || requests != before+1 {
			t.Fatalf("expected one read: %v %s %s", err, out, stderr)
		}
		if command[0] == "invocation" {
			if !strings.Contains(out, "Execution/result detail: loom capability-call inspect -- '"+ref+"'") || !strings.Contains(out, "status describes dispatch") || strings.Contains(out, "job inspect") {
				t.Fatal("command result requires guessing or invents a job", out)
			}
		} else {
			if !strings.Contains(out, `Recorded result: {"outputs":{"message":"The retained answer"}}`) || !strings.Contains(out, "Full result/detail: loom capability-call inspect --json -- '"+ref+"'") || strings.Contains(out, "not part of human output") {
				t.Fatal("missing answer/exact navigation or leaked input", out)
			}
		}
	}
	out, stderr, err := executeRootCommand("--socket", socket, "--json", "capability-call", "inspect", ref)
	var full response.Envelope[routing.CapabilityCall]
	decodeErr := json.Unmarshal([]byte(out), &full)
	want, _ := json.Marshal(call)
	got, _ := json.Marshal(full.Data)
	if err != nil || decodeErr != nil || !bytes.Equal(want, got) || full.Meta.CorrelationID != "original" {
		t.Fatalf("full JSON changed: %v %s %s", err, out, stderr)
	}
}

func TestCommandResultBoundsFailuresAndPendingTruth(t *testing.T) {
	code, message := "command_failed", "failure\n\x1b[31m"
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	renderCapabilityCall(cmd, routing.CapabilityCall{CapabilityCallID: "capability_call_failed", Status: "failed", ErrorCode: &code, ErrorMessage: &message})
	if !strings.Contains(out.String(), "command_failed") || strings.Contains(out.String(), "\x1b") || !strings.Contains(out.String(), `failure\n\x1b`) || !strings.Contains(out.String(), "Execution result: not recorded") {
		t.Fatal("failure hidden or unsafe", out.String())
	}
	out.Reset()
	renderCapabilityCallOutcome(cmd, routing.CapabilityCallOutcome{Status: "queued", CapabilityCall: routing.CapabilityCall{CapabilityCallID: "capability_call_pending"}})
	if !strings.Contains(out.String(), "dispatch acceptance alone does not establish execution success") || strings.Contains(out.String(), "Recorded result:") {
		t.Fatal("dispatch became execution proof", out.String())
	}
	out.Reset()
	renderCapabilityCallOutcome(cmd, routing.CapabilityCallOutcome{Status: "completed", CapabilityCall: routing.CapabilityCall{CapabilityCallID: "capability_call_done"}, Result: json.RawMessage(`{"answer":42}`)})
	if !strings.Contains(out.String(), `Recorded result: {"answer":42}`) {
		t.Fatal("immediate outcome result lost", out.String())
	}
	preview := capabilityResultExcerpt(strings.Repeat("漢", 400))
	if !utf8.ValidString(preview) || !strings.Contains(preview, "[truncated;") || len(preview) > 830 {
		t.Fatal("preview is not bounded UTF-8", preview)
	}
	if strings.Contains(capabilityResultExcerpt("untrusted\u202econtent"), "\u202e") {
		t.Fatal("terminal format character survived")
	}
}
