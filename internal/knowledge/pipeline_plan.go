package knowledge

import (
	"encoding/json"
	"fmt"

	"loom.local/loom/internal/projectcontracts"
)

type CompiledPipelineStage struct {
	StageKey            string   `json:"stage_key"`
	ContractVersion     string   `json:"contract_version"`
	Ordinal             int      `json:"ordinal"`
	ExecutionClass      string   `json:"execution_class"`
	Dependencies        []string `json:"dependencies,omitempty"`
	InputArtifactKinds  []string `json:"input_artifact_kinds,omitempty"`
	OutputArtifactKinds []string `json:"output_artifact_kinds,omitempty"`
	QuietWindow         bool     `json:"quiet_window,omitempty"`
	Required            bool     `json:"required"`
	ImplementationKey   string   `json:"implementation_key"`
	Selected            bool     `json:"selected"`
	SkipReason          string   `json:"skip_reason,omitempty"`
}

type CompiledPipelinePlan struct {
	Enrichment              *PipelineEnrichment                     `json:"enrichment,omitempty"`
	HeavyQuietWindowSeconds int                                     `json:"heavy_quiet_window_seconds,omitempty"`
	SchemaVersion           string                                  `json:"schema_version"`
	DefinitionKey           string                                  `json:"definition_key"`
	DefinitionVersion       string                                  `json:"definition_version"`
	FileFamily              string                                  `json:"file_family"`
	Stages                  []CompiledPipelineStage                 `json:"stages"`
	SourcePolicy            *projectcontracts.KnowledgeSourcePolicy `json:"source_policy,omitempty"`
	EffectivePolicy         *PipelinePolicy                         `json:"effective_policy,omitempty"`
}

func CompilePipelinePlan(object KnowledgeObject, policy PipelinePolicy) (CompiledPipelinePlan, error) {
	return compilePipelinePlan(object, policy, false)
}

func compilePipelinePlan(object KnowledgeObject, policy PipelinePolicy, selectiveEnrichment bool) (CompiledPipelinePlan, error) {
	definition := PipelineDefinitionForObject(object)
	if err := ValidatePipelineDefinition(definition); err != nil {
		return CompiledPipelinePlan{}, err
	}
	plan := CompiledPipelinePlan{SchemaVersion: "knowledge.pipeline_plan.v1", DefinitionKey: definition.Key, DefinitionVersion: definition.Version, FileFamily: definition.FileFamily}
	if definition.FileFamily == "pdf" || definition.FileFamily == "image" {
		plan.SchemaVersion = "knowledge.pipeline_plan.v2"
	}
	source, search, err := knowledgeSourcePolicyAndSearch(object)
	if err != nil {
		return CompiledPipelinePlan{}, err
	}
	if source != nil {
		policy = effectiveSourcePipelinePolicy(policy, source)
		plan.SourcePolicy = source
	}
	if source != nil || plan.SchemaVersion == "knowledge.pipeline_plan.v2" {
		plan.EffectivePolicy = &policy
	}
	for index, stage := range definition.Stages {
		compiled := CompiledPipelineStage{
			StageKey: stage.StageKey, ContractVersion: stage.ContractVersion, Ordinal: index + 1,
			ExecutionClass: stage.ExecutionClass, Dependencies: append([]string(nil), stage.Dependencies...),
			InputArtifactKinds: append([]string(nil), stage.InputArtifactKinds...), OutputArtifactKinds: append([]string(nil), stage.OutputArtifactKinds...),
			QuietWindow: stage.QuietWindow, Required: stage.Required, ImplementationKey: stage.ImplementationKey, Selected: true,
		}
		switch stage.StageKey {
		case FilePipelineStageImageOCR:
			compiled.Selected = policy.ImageOCREnabled
			if !compiled.Selected {
				compiled.SkipReason = "disabled_by_policy"
			}
		case FilePipelineStagePDFOCR:
			compiled.Selected = policy.PDFOCREnabled
			if !compiled.Selected {
				compiled.SkipReason = "disabled_by_policy"
			}
		case FilePipelineStageImageDescription:
			compiled.Selected = policy.ImageDescriptionsEnabled
			if !compiled.Selected {
				compiled.SkipReason = "disabled_by_policy"
			}
		case FilePipelineStageEmbedding:
			compiled.Selected = policy.EmbeddingsEnabled
			if !compiled.Selected {
				compiled.SkipReason = "disabled_by_policy"
			}
		}
		if source != nil && !compiled.Selected {
			compiled.SkipReason = "disabled_by_host"
			if sourceStageDisabled(stage.StageKey, source) {
				compiled.SkipReason = "disabled_by_source"
			}
		}
		if source != nil && source.Refresh != nil {
			// Whole-refresh debounce is paid once, at metadata admission, rather
			// than imposing another quiet interval on each expensive stage.
			compiled.QuietWindow = stage.StageKey == FilePipelineStageMetadata
		}
		plan.Stages = append(plan.Stages, compiled)
	}
	if len(plan.Stages) == 0 || plan.Stages[0].StageKey != FilePipelineStageMetadata {
		return CompiledPipelinePlan{}, fmt.Errorf("%w: compiled pipeline must begin with metadata", ErrInvalid)
	}
	selectUsefulPipelineStages(&plan)
	if !selectiveEnrichment && (search == "metadata" || search == "none") {
		for i := range plan.Stages {
			stage := &plan.Stages[i]
			switch stage.StageKey {
			case FilePipelineStageMetadata, FilePipelineStageFinalize:
				continue
			case FilePipelineStageLexicalIndex:
				if search == "metadata" {
					continue
				}
			}
			stage.Selected, stage.SkipReason = false, "disabled_by_source_search"
		}
	}
	return plan, nil
}

func (plan CompiledPipelinePlan) Snapshot() (json.RawMessage, error) {
	if len(plan.Stages) == 0 {
		return nil, fmt.Errorf("%w: compiled pipeline stages are required", ErrInvalid)
	}
	payload, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

// Preserve explicit policy skips in the saved plan, but admit only stages with
// a consumer. Called again after a version-specific enrichment selects stages.
func selectUsefulPipelineStages(plan *CompiledPipelinePlan) {
	selected := func(key string) bool {
		for _, stage := range plan.Stages {
			if stage.StageKey == key {
				return stage.Selected
			}
		}
		return false
	}
	body := plan.FileFamily != "image" || selected(FilePipelineStageImageOCR) || selected(FilePipelineStageImageDescription)
	for i := range plan.Stages {
		stage := &plan.Stages[i]
		useful := true
		switch stage.StageKey {
		case FilePipelineStagePDFPageAnalysis:
			useful = selected(FilePipelineStagePDFOCR)
		case FilePipelineStageConsolidateText, FilePipelineStageChunk:
			useful = body
		case FilePipelineStageEmbedding:
			if !body && stage.Selected {
				stage.Selected, stage.SkipReason = false, "no_body_producer"
			}
			continue
		default:
			continue
		}
		stage.Selected = useful
		stage.SkipReason = ""
		if !useful {
			stage.SkipReason = "no_selected_consumer"
		}
	}
}

func effectiveCheapPipelinePlan(raw json.RawMessage) bool {
	var plan CompiledPipelinePlan
	return json.Unmarshal(raw, &plan) == nil && plan.SchemaVersion == "knowledge.pipeline_plan.v2"
}
