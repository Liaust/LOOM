package projectcontracts

import (
	"fmt"

	"loom.local/loom/internal/filepolicy"
)

// DeclarationKnowledgePolicy is the single compiler/evidence reader for new
// folder defaults. Legacy documents retain their exact existing encoding.
func DeclarationKnowledgePolicy(d ProjectDeclaration, key ResourceKey) (*KnowledgeSourcePolicy, error) {
	knowledge := d.Resources[key].Knowledge
	if knowledge == nil {
		return nil, fmt.Errorf("resource %s is not knowledge", key)
	}
	policy, err := NormalizeKnowledgeSourcePolicy(*knowledge)
	if err != nil || d.SchemaVersion != ProjectSchemaV06 {
		return policy, err
	}
	if policy == nil {
		policy = &KnowledgeSourcePolicy{}
	}
	overrides := d.Files
	if knowledge.ApplicationData != nil {
		overrides = nil
	}
	defaults, err := filepolicy.WorkspaceFolderDefaults(overrides)
	if err != nil {
		return nil, err
	}
	policy.Files = &defaults
	if policy.Workspace == nil {
		policy.Workspace = &KnowledgeWorkspacePolicy{Include: DefaultDeviceFileIncludes(), Exclude: DefaultDeviceFileExcludes()}
	}
	return policy, nil
}

func DefaultDeviceFileIncludes() []string {
	return []string{"**/*.md", "**/*.markdown", "**/*.mdown", "**/*.txt", "**/*.text", "**/*.canvas", "**/*.base", "**/*.png", "**/*.jpg", "**/*.jpeg", "**/*.gif", "**/*.webp", "**/*.svg"}
}

func DefaultDeviceFileExcludes() []string {
	return append(filepolicy.DefaultExcludePatterns(), ".loom/**", "**/.loom/**", "credentials/**", "**/credentials/**", "private_no_index/**", "**/private_no_index/**", ".env*", "**/.env*")
}
