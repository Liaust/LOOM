package knowledge

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"strings"
)

const MaxEnrichmentSelection = 100

type EnrichmentSelection struct {
	Ref            string           `json:"ref"`
	Kind           string           `json:"kind"` // object, file, or folder; paths refer to admitted source paths
	NodeKey        string           `json:"node_key,omitempty"`
	Recursive      bool             `json:"recursive"`
	Limit          int              `json:"limit"`
	After          string           `json:"after,omitempty"`
	Stages         EnrichmentStages `json:"stages"`
	SourceRevision string           `json:"source_revision,omitempty"`
	SourceHash     string           `json:"source_hash,omitempty"`
}
type EnrichmentPreviewEntry struct {
	Binding         EnrichmentBinding     `json:"binding"`
	SourcePath      string                `json:"source_path"`
	NodeKey         string                `json:"node_key"`
	FileFamily      string                `json:"file_family"`
	AutomaticPolicy PipelinePolicy        `json:"automatic_policy"`
	Supported       EnrichmentStages      `json:"supported"`
	BlockedReason   string                `json:"blocked_reason,omitempty"`
	EffectivePlan   *CompiledPipelinePlan `json:"effective_plan,omitempty"`
}
type EnrichmentPreview struct {
	HostPolicy PipelinePolicy           `json:"host_policy"`
	Selection  EnrichmentSelection      `json:"selection"`
	Entries    []EnrichmentPreviewEntry `json:"entries"`
	Truncated  bool                     `json:"truncated"`
	NextAfter  string                   `json:"next_after,omitempty"`
	Inventory  string                   `json:"inventory"`
}
type EnrichmentApplyInput struct {
	Bindings []EnrichmentBinding `json:"bindings"`
	Stages   EnrichmentStages    `json:"stages"`
	Confirm  bool                `json:"confirm"`
}
type EnrichmentResult struct {
	Binding EnrichmentBinding `json:"binding"`
	Run     *PipelineRun      `json:"run,omitempty"`
	Error   string            `json:"error,omitempty"`
}
type EnrichmentReceipt struct {
	Results []EnrichmentResult `json:"results"`
	Failed  int                `json:"failed"`
}

func NormalizeEnrichmentSelection(input EnrichmentSelection) (EnrichmentSelection, error) {
	input.Ref, input.NodeKey = strings.TrimSpace(input.Ref), strings.TrimSpace(input.NodeKey)
	if input.Ref == "" {
		return input, fmt.Errorf("%w: an object ID or admitted source path is required", ErrInvalid)
	}
	if input.Kind == "" {
		input.Kind = "object"
	}
	switch input.Kind {
	case "object":
	case "file", "folder":
		if !path.IsAbs(input.Ref) || path.Clean(input.Ref) != input.Ref || input.Ref == "/" {
			return input, fmt.Errorf("%w: use a clean absolute admitted source path below /", ErrInvalid)
		}
	default:
		return input, fmt.Errorf("%w: selection kind must be object, file, or folder", ErrInvalid)
	}
	if input.Kind != "folder" && (input.Recursive || input.After != "") {
		return input, fmt.Errorf("%w: recursion and continuation require folder selection", ErrInvalid)
	}
	if input.Kind == "folder" && (input.SourceRevision != "" || input.SourceHash != "") {
		return input, fmt.Errorf("%w: folder versions are bound by the preview entries", ErrInvalid)
	}
	if input.Limit == 0 {
		input.Limit = MaxEnrichmentSelection
	}
	if input.Limit < 1 || input.Limit > MaxEnrichmentSelection {
		return input, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalid, MaxEnrichmentSelection)
	}
	return input, nil
}

func (s *Service) PreviewEnrichment(ctx context.Context, input EnrichmentSelection) (EnrichmentPreview, error) {
	input, err := NormalizeEnrichmentSelection(input)
	if err != nil {
		return EnrichmentPreview{}, err
	}
	result := EnrichmentPreview{Selection: input, Entries: []EnrichmentPreviewEntry{}, Inventory: "Admitted Notes database entries only; not a filesystem inventory. Later arrivals are never included by apply."}
	predicate := `(($6='object' AND o.knowledge_object_id=$1) OR ($6='file' AND o.source_path=$1) OR
 ($6='folder' AND left(o.source_path,length($1)+1)=$1 || '/' AND ($3 OR position('/' in substring(o.source_path from length($1)+2))=0)))`
	limit := input.Limit + 1
	if input.Kind != "folder" {
		limit = 2
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT `+knowledgeObjectColumns()+` FROM knowledge.knowledge_objects o WHERE `+predicate+`
 AND ($2='' OR o.source_node_key=$2) AND o.knowledge_object_id>$4
 AND o.deleted_at IS NULL AND `+visibleNotesKnowledgeObjectSQL("o")+` AND `+notesCustodyWriteAllowedSQL("o")+`
 ORDER BY o.knowledge_object_id LIMIT $5`, input.Ref, input.NodeKey, input.Recursive, input.After, limit, input.Kind)
	if err != nil {
		return result, err
	}
	objects := []KnowledgeObject{}
	for rows.Next() {
		o, e := scanKnowledgeObject(rows)
		if e != nil {
			rows.Close()
			return result, e
		}
		objects = append(objects, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if input.Kind != "folder" && len(objects) > 1 {
		return result, fmt.Errorf("%w: source path is ambiguous; specify --node or use an object ID", ErrInvalid)
	}
	if len(objects) == 0 {
		return result, fmt.Errorf("%w: no currently admitted Notes entries match this selection (no filesystem scan performed)", ErrInvalid)
	}
	if len(objects) > input.Limit {
		result.Truncated = true
		objects = objects[:input.Limit]
		result.NextAfter = objects[len(objects)-1].KnowledgeObjectID
	}
	policy, err := s.GetPipelinePolicy(ctx)
	if err != nil {
		return result, err
	}
	result.HostPolicy = policy.Policy
	for _, o := range objects {
		entry := EnrichmentPreviewEntry{SourcePath: o.SourcePath, NodeKey: o.SourceNodeKey, FileFamily: pipelineFileFamily(o)}
		entry.Binding, err = enrichmentBinding(o)
		if err != nil {
			return result, err
		}
		source, e := knowledgeSourcePolicy(o)
		if e != nil {
			return result, e
		}
		entry.AutomaticPolicy = effectiveSourcePipelinePolicy(policy.Policy, source)
		if o.SourceRevision == "" || o.SourceHash == "" {
			entry.BlockedReason = "source has no stable revision/hash; wait for admission"
		}
		entry.Supported = EnrichmentStages{OCR: entry.FileFamily == "pdf" || entry.FileFamily == "image", Vision: entry.FileFamily == "image", Embeddings: entry.FileFamily != "metadata_only"}
		if o.SizeBytes != nil && *o.SizeBytes > DefaultPDFMaxSourceBytes {
			entry.BlockedReason = "source exceeds the 1 GiB preparation limit"
		}
		if (input.SourceRevision != "" && input.SourceRevision != o.SourceRevision) || (input.SourceHash != "" && input.SourceHash != o.SourceHash) {
			entry.BlockedReason = "stale requested source version"
		}
		if input.Stages.any() && entry.BlockedReason == "" {
			intent := PipelineEnrichment{Binding: entry.Binding, Requested: input.Stages, HostPolicy: policy.Policy, AutomaticPolicy: entry.AutomaticPolicy}
			plan, e := compileEnrichmentPlan(o, policy.Policy, intent)
			if e != nil {
				entry.BlockedReason = e.Error()
			} else {
				entry.EffectivePlan = &plan
			}
		}
		result.Entries = append(result.Entries, entry)
	}
	return result, nil
}

func ValidateEnrichmentApply(input EnrichmentApplyInput) error {
	if !input.Confirm || !input.Stages.any() {
		return fmt.Errorf("%w: apply requires confirmation and at least one of OCR, vision or embeddings", ErrInvalid)
	}
	if len(input.Bindings) < 1 || len(input.Bindings) > MaxEnrichmentSelection {
		return fmt.Errorf("%w: apply requires 1–%d exact preview bindings", ErrInvalid, MaxEnrichmentSelection)
	}
	seen := map[string]bool{}
	for _, b := range input.Bindings {
		if b.ObjectID == "" || b.SourceRevision == "" || b.SourceHash == "" || b.AdmissionHash == "" || seen[b.ObjectID] {
			return fmt.Errorf("%w: each preview binding must have a unique object ID, source revision/hash and admission hash", ErrInvalid)
		}
		seen[b.ObjectID] = true
	}
	return nil
}
func (s *Service) ApplyEnrichment(ctx context.Context, input EnrichmentApplyInput, actor string) (EnrichmentReceipt, error) {
	result := EnrichmentReceipt{Results: []EnrichmentResult{}}
	if err := ValidateEnrichmentApply(input); err != nil {
		return result, err
	}
	for _, binding := range input.Bindings {
		entry := EnrichmentResult{Binding: binding}
		run, err := s.EnrichPipelineObject(ctx, binding, input.Stages, actor)
		if err != nil {
			entry.Error = err.Error()
			if err == sql.ErrNoRows {
				entry.Error = "source is no longer admitted"
			}
			result.Failed++
		} else {
			redacted := redactPipelineRun(run)
			entry.Run = &redacted
		}
		result.Results = append(result.Results, entry)
	}
	return result, nil
}
