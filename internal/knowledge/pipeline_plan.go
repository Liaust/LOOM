package knowledge

import (
	"encoding/json"
	"fmt"
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
	SchemaVersion     string                  `json:"schema_version"`
	DefinitionKey     string                  `json:"definition_key"`
	DefinitionVersion string                  `json:"definition_version"`
	FileFamily        string                  `json:"file_family"`
	Stages            []CompiledPipelineStage `json:"stages"`
}

func CompilePipelinePlan(object KnowledgeObject, policy PipelinePolicy) (CompiledPipelinePlan, error) {
	definition := PipelineDefinitionForObject(object)
	if err := ValidatePipelineDefinition(definition); err != nil {
		return CompiledPipelinePlan{}, err
	}
	plan := CompiledPipelinePlan{SchemaVersion: "knowledge.pipeline_plan.v1", DefinitionKey: definition.Key, DefinitionVersion: definition.Version, FileFamily: definition.FileFamily}
	for index, stage := range definition.Stages {
		compiled := CompiledPipelineStage{
			StageKey: stage.StageKey, ContractVersion: stage.ContractVersion, Ordinal: index + 1,
			ExecutionClass: stage.ExecutionClass, Dependencies: append([]string(nil), stage.Dependencies...),
			InputArtifactKinds: append([]string(nil), stage.InputArtifactKinds...), OutputArtifactKinds: append([]string(nil), stage.OutputArtifactKinds...),
			QuietWindow: stage.QuietWindow, Required: stage.Required, ImplementationKey: stage.ImplementationKey, Selected: true,
		}
		switch stage.StageKey {
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
		plan.Stages = append(plan.Stages, compiled)
	}
	if len(plan.Stages) == 0 || plan.Stages[0].StageKey != FilePipelineStageMetadata {
		return CompiledPipelinePlan{}, fmt.Errorf("%w: compiled pipeline must begin with metadata", ErrInvalid)
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
