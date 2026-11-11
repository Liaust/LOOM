package loomcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/response"
)

func TestProjectTransportAdviceMatchesReadOrApply(t *testing.T) {
	for _, applying := range []bool{false, true} {
		for _, machine := range []bool{false, true} {
			for _, denied := range []bool{false, true} {
				t.Run(fmt.Sprintf("apply=%t/json=%t/denied=%t", applying, machine, denied), func(t *testing.T) {
					cause := errors.New("private transport diagnostic")
					if denied {
						cause = fmt.Errorf("wrapped: %w", os.ErrPermission)
					}
					var input *pc.DeclarationApplyRequest
					if applying {
						input = &pc.DeclarationApplyRequest{PlanID: "original", IdempotencyKey: "same-key"}
					}
					var out, stderr bytes.Buffer
					cmd := &cobra.Command{}
					cmd.SetOut(&out)
					cmd.SetErr(&stderr)
					err := renderDeclarationFailure(cmd, &options{jsonOutput: machine}, "transport-case", cause, input)
					if !errors.Is(err, cause) {
						t.Fatal("original transport cause was lost")
					}
					text := stderr.String()
					if machine {
						var envelope response.ErrorEnvelope
						if err := json.Unmarshal(out.Bytes(), &envelope); err != nil || stderr.Len() != 0 || envelope.OK || envelope.Error.Code != "transport.unavailable" || envelope.Meta.CorrelationID != "transport-case" {
							t.Fatalf("invalid error envelope: %s %s", out.String(), stderr.String())
						}
						text = envelope.Error.Summary
					}
					if strings.Contains(text, "idempotency") != applying || strings.Contains(text, "inspect its recorded state") != applying {
						t.Fatalf("advice does not match operation: %s", text)
					}
					if strings.Contains(text, "permission denied") != denied || strings.Contains(text, "private transport diagnostic") {
						t.Fatalf("cause classification leaked or hid the known denial: %s", text)
					}
				})
			}
		}
	}
}
