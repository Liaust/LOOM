package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/knowledge"
)

func newNotesEnrichCommand(opts *options) *cobra.Command {
	var input knowledge.EnrichmentSelection
	var file, folder, yes bool
	var previewFile, idempotencyKey string
	cmd := &cobra.Command{Use: "enrich [object-id or admitted-source-path]", Short: "Preview or request one-time OCR, image vision and embeddings", Long: `Preview admitted Notes objects without queuing work. Use --file or --folder for
source paths, and --recursive explicitly to include folder descendants. Folder
previews are bounded database selections, not complete filesystem inventories.
Save a JSON preview, then apply its exact entries with --preview-file and --yes:
  loom --json notes enrich /source/folder --folder --recursive --ocr > preview.json
  loom notes enrich --preview-file preview.json --yes
Later arrivals are never included and changed revisions are rejected.`, Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if file && folder {
			return fmt.Errorf("choose --file or --folder")
		}
		if previewFile != "" && (len(args) > 0 || file || folder || input.Recursive || input.After != "" || input.SourceRevision != "" || input.SourceHash != "" || input.NodeKey != "") {
			return fmt.Errorf("--preview-file supplies the exact selection; do not combine it with a new selector")
		}
		if previewFile == "" && len(args) != 1 {
			return fmt.Errorf("provide an object ID/source path or --preview-file")
		}
		if yes && folder && previewFile == "" {
			return fmt.Errorf("save a --json folder preview and apply it with --preview-file <file> --yes")
		}
		var preview knowledge.EnrichmentPreview
		if previewFile != "" {
			f, err := os.Open(previewFile)
			if err != nil {
				return err
			}
			defer f.Close()
			decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
			decoder.DisallowUnknownFields()
			if err = decoder.Decode(&preview); err != nil {
				return fmt.Errorf("read enrichment preview: %w", err)
			}
			if !cmd.Flags().Changed("ocr") && !cmd.Flags().Changed("vision") && !cmd.Flags().Changed("embeddings") {
				input.Stages = preview.Selection.Stages
			}
		} else {
			input.Ref = args[0]
			input.Kind = "object"
			if file {
				input.Kind = "file"
			}
			if folder {
				input.Kind = "folder"
			}
			normalized, err := knowledge.NormalizeEnrichmentSelection(input)
			if err != nil {
				return err
			}
			input = normalized
		}
		if yes && !input.Stages.OCR && !input.Stages.Vision && !input.Stages.Embeddings {
			return fmt.Errorf("--yes requires --ocr, --vision or --embeddings (or selected stages in the preview file)")
		}
		commandCtx, err := resolveCommandContext(opts)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
		defer cancel()
		if previewFile == "" {
			envelope, err := commandCtx.Client.PreviewKnowledgeNotesEnrichment(ctx, commandCtx.CorrelationID, input)
			if err != nil {
				return err
			}
			preview = envelope.Data
		}
		if !yes {
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(preview)
			}
			fmt.Fprintln(cmd.OutOrStdout(), preview.Inventory)
			fmt.Fprintf(cmd.OutOrStdout(), "Host enabled: PDF OCR=%t image OCR=%t vision=%t embeddings=%t\n", preview.HostPolicy.PDFOCREnabled, preview.HostPolicy.ImageOCREnabled, preview.HostPolicy.ImageDescriptionsEnabled, preview.HostPolicy.EmbeddingsEnabled)
			for _, entry := range preview.Entries {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %s\n  revision=%s hash=%s\n  supported: OCR=%t vision=%t embeddings=%t\n  automatic: OCR=%t vision=%t embeddings=%t\n", entry.Binding.ObjectID, entry.SourcePath, entry.Binding.SourceRevision, entry.Binding.SourceHash, entry.Supported.OCR, entry.Supported.Vision, entry.Supported.Embeddings, entry.AutomaticPolicy.PDFOCREnabled || entry.AutomaticPolicy.ImageOCREnabled, entry.AutomaticPolicy.ImageDescriptionsEnabled, entry.AutomaticPolicy.EmbeddingsEnabled)
				if entry.BlockedReason != "" {
					fmt.Fprintln(cmd.OutOrStdout(), "  blocked:", entry.BlockedReason)
				}
			}
			if preview.Truncated {
				fmt.Fprintf(cmd.OutOrStdout(), "Truncated; preview the next page with --after %s. Apply covers this page only.\n", preview.NextAfter)
			}
			return nil
		}
		apply := knowledge.EnrichmentApplyInput{Stages: input.Stages, Confirm: true}
		for _, entry := range preview.Entries {
			if entry.BlockedReason != "" {
				return fmt.Errorf("preview item %s is blocked: %s; preview again", entry.Binding.ObjectID, entry.BlockedReason)
			}
			apply.Bindings = append(apply.Bindings, entry.Binding)
		}
		if err := knowledge.ValidateEnrichmentApply(apply); err != nil {
			return err
		}
		client, _ := withEffectIdempotency(commandCtx.Client, idempotencyKey, "notes.enrich")
		envelope, err := client.ApplyKnowledgeNotesEnrichment(ctx, commandCtx.CorrelationID, apply)
		if err != nil {
			return err
		}
		if opts.jsonOutput {
			if err = json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data); err != nil {
				return err
			}
		} else {
			for _, result := range envelope.Data.Results {
				if result.Error != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: failed: %s\n", result.Binding.ObjectID, result.Error)
				} else if result.Run != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %s (%s)\n", result.Binding.ObjectID, result.Run.KnowledgePipelineRunID, result.Run.Status)
				}
			}
		}
		if envelope.Data.Failed > 0 {
			return &renderedCLIError{err: fmt.Errorf("%d enrichment item(s) failed; inspect the receipt, preview stale items again, or use notes pipelines retry for failed runs", envelope.Data.Failed)}
		}
		return nil
	}}
	cmd.Flags().BoolVar(&file, "file", false, "select an exact admitted source file path")
	cmd.Flags().BoolVar(&folder, "folder", false, "select admitted entries immediately in a folder")
	cmd.Flags().BoolVar(&input.Recursive, "recursive", false, "include descendant folders explicitly")
	cmd.Flags().StringVar(&input.NodeKey, "node", "", "source node key, required for ambiguous paths")
	cmd.Flags().IntVar(&input.Limit, "limit", knowledge.MaxEnrichmentSelection, "maximum preview entries (1–100)")
	cmd.Flags().StringVar(&input.After, "after", "", "preview continuation object ID")
	cmd.Flags().BoolVar(&input.Stages.OCR, "ocr", false, "request PDF sparse-page OCR or image OCR")
	cmd.Flags().BoolVar(&input.Stages.Vision, "vision", false, "request existing image description (images only)")
	cmd.Flags().BoolVar(&input.Stages.Embeddings, "embeddings", false, "request embeddings, reusing compatible extraction and chunks")
	cmd.Flags().StringVar(&input.SourceRevision, "source-revision", "", "require this exact source revision")
	cmd.Flags().StringVar(&input.SourceHash, "source-hash", "", "require this exact source hash")
	cmd.Flags().StringVar(&previewFile, "preview-file", "", "apply exact entries from a saved JSON preview")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "idempotency key for apply replay")
	cmd.Flags().BoolVar(&yes, "yes", false, "apply the selected enrichment; default is preview")
	return cmd
}
