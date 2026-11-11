package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/storagearchive"
)

// The existing daemon owns database credentials and the journal. Only the
// bounded filesystem rename runs as the explicit local root operator.
func runRetainedOperator(cmd *cobra.Command, opts *options, action string, args []string, hash string) error {
	if os.Geteuid() != 0 || opts.configFile != "/etc/loom/loom.env" {
		return fmt.Errorf("operator relocation requires local root and --config /etc/loom/loom.env")
	}
	cc, err := resolveCommandContext(opts)
	if err != nil {
		return err
	}
	if cc.Config.Env != "production" || cc.Config.NodeRole != "main" || cc.Config.NodeID != "main" {
		return fmt.Errorf("operator relocation requires the production Main owner")
	}
	client := localclient.New(cc.Config.SocketPath)
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
	defer cancel()
	input := map[string]any{"action": action, "id": args[0]}
	if action == "plan" {
		input = map[string]any{"action": "operator_plan", "from": args[0], "to": args[1]}
	}
	if action == "apply" {
		input = map[string]any{"action": "operator_prepare", "id": args[0], "plan_hash": hash, "confirm": true}
		prepared, err := client.RetainedFileMove(ctx, cc.CorrelationID, input)
		if err != nil {
			return err
		}
		var saved struct {
			State string                                `json:"state"`
			Plan  storagearchive.RetainedRelocationPlan `json:"plan"`
		}
		if json.Unmarshal(prepared.Data, &saved) != nil || saved.Plan.ID != args[0] || saved.Plan.Digest != hash || (saved.State != "prepared" && saved.State != "complete") {
			return fmt.Errorf("Main did not prepare the exact reviewed operator intent")
		}
		if saved.State != "complete" {
			confirmPrepared := func(ctx context.Context) error {
				status, err := client.RetainedFileMove(ctx, cc.CorrelationID, map[string]any{"action": "status", "id": args[0]})
				if err != nil {
					return err
				}
				var current struct {
					ID    string `json:"id"`
					Hash  string `json:"plan_hash"`
					State string `json:"state"`
				}
				if json.Unmarshal(status.Data, &current) != nil || current.ID != args[0] || current.Hash != hash || current.State != "prepared" {
					return fmt.Errorf("the exact operator request is not prepared")
				}
				return nil
			}
			if err := storagearchive.ExecuteRetainedOperator(ctx, storagearchive.TrustedWorkspaceRoots{StorageRoot: cc.Config.StorageRoot, BoxRoot: cc.Config.BoxPath}, saved.Plan, hash, confirmPrepared); err != nil {
				return fmt.Errorf("saved operator move needs exact recovery: %w", err)
			}
		}
		input = map[string]any{"action": "apply", "id": args[0], "plan_hash": hash, "confirm": true}
	}
	if action == "cancel" {
		input["plan_hash"], input["confirm"] = hash, true
	}
	result, err := client.RetainedFileMove(ctx, cc.CorrelationID, input)
	if err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Data)
}
