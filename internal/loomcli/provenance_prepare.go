package loomcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/provenance"
)

func newProvenanceReviewPrepareCommand(opts *options) *cobra.Command {
	var producerFile, output, supersedes, relationshipContext string
	cmd := &cobra.Command{Use: "prepare <candidate-id>", Args: cobra.ExactArgs(1), Short: "Save a reviewable acceptance batch without mutating Provenance",
		Long: "Read the exact pending candidate and its linked receipts, preserve original attribution, and save a new review batch without acceptance. --producer-file is your actual {producer_id,producer_kind,task_id,run_id?} context. Review the saved file before its explicit operations apply command. A saved batch is stable for retries; preparation is not approval or an authorization grant. --supersedes adds an explicit same-scope relationship, requiring --relationship-context.",
		RunE: func(cmd *cobra.Command, args []string) error {
			invalid := func(err error) error {
				message := "Supply exact evidence, actual producer context and a new output path."
				if err != nil {
					message = "Review preparation refused: " + err.Error()
				}
				return renderError(cmd, opts, opts.correlationID, loomerrors.Wrap("provenance.prepare_invalid", "provenance", "review_prepare", message, err))
			}
			id, err := provenance.ParseSemanticID(args[0])
			if err != nil || producerFile == "" || output == "" {
				return invalid(err)
			}
			var reviewer provenance.ProducerIdentity
			if err := readProvenanceJSON(producerFile, provenance.MaximumDocumentBytes, &reviewer); err != nil {
				return invalid(err)
			}
			cc, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, opts.correlationID, err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), provenance.FoundationRequestTimeout)
			defer cancel()
			candidate, err := cc.Client.GetProvenanceCandidateWithSources(ctx, cc.CorrelationID, id, provenance.MaximumSourcesPerObject)
			if err != nil {
				return renderError(cmd, opts, cc.CorrelationID, err)
			}
			if !candidate.OK || candidate.Data.Candidate.ID != id {
				return invalid(fmt.Errorf("candidate exact read is incomplete"))
			}
			var target *provenance.RecordWithSources
			if supersedes != "" {
				targetID, e := provenance.ParseSemanticID(supersedes)
				if e != nil {
					return invalid(e)
				}
				result, e := cc.Client.GetProvenanceRecordWithSources(ctx, cc.CorrelationID, targetID, provenance.MaximumSourcesPerObject)
				if e != nil {
					return renderError(cmd, opts, cc.CorrelationID, e)
				}
				if !result.OK || result.Data.Record.ID != targetID {
					return invalid(fmt.Errorf("supersedes exact read is incomplete"))
				}
				target = &result.Data
			}
			prepared, err := provenance.PrepareCandidateReview(candidate.Data, reviewer, target, relationshipContext, time.Now().UTC())
			if err != nil {
				return invalid(err)
			}
			raw, err := json.MarshalIndent(prepared.Request, "", "  ")
			if err != nil {
				return invalid(err)
			}
			raw = append(raw, '\n')
			if err = writeNewPreparedReview(output, raw); err != nil {
				return invalid(err)
			}
			digest := sha256.Sum256(raw)
			key := "review-" + hex.EncodeToString(digest[:])
			return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				SchemaVersion      string                 `json:"schema_version"`
				Path               string                 `json:"path"`
				Hash               string                 `json:"sha256"`
				RecordID           provenance.SemanticID  `json:"record_id"`
				RelationshipID     *provenance.SemanticID `json:"relationship_id,omitempty"`
				LifecycleTruncated bool                   `json:"lifecycle_truncated"`
				Mutation           bool                   `json:"mutation"`
				ApplyCommand       string                 `json:"apply_command"`
			}{"loom.provenance.review-preparation.v1", output, "sha256:" + hex.EncodeToString(digest[:]), prepared.RecordID,
				prepared.RelationshipID, prepared.LifecycleTruncated, false,
				fmt.Sprintf("loom provenance operations apply --file %s --idempotency-key %s --yes --json", quoteDeclarationArg(output), key)})
		}}
	cmd.Flags().StringVar(&producerFile, "producer-file", "", "JSON actual reviewer producer ID/kind/task/run context")
	cmd.Flags().StringVar(&output, "out", "", "new mode0600 saved review batch; existing files are never overwritten")
	cmd.Flags().StringVar(&supersedes, "supersedes", "", "exact accepted record ID to supersede within the same scope")
	cmd.Flags().StringVar(&relationshipContext, "relationship-context", "", "explicit meaning and scope of the supersession")
	return cmd
}

func writeNewPreparedReview(path string, raw []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	ok := false
	owned, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	defer func() {
		file.Close()
		if !ok {
			if current, err := os.Lstat(path); err == nil && os.SameFile(owned, current) {
				os.Remove(path)
			}
		}
	}()
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}
