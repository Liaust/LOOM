package projectcontracts

import (
	"bytes"
	"reflect"

	"gopkg.in/yaml.v3"
)

func ValidProjectWorkState(value string) bool {
	return value == ProjectWorkStateWorking || value == ProjectWorkStatePassive
}

// DeclarationSourcesMatchExceptWorkState compares complete captured source
// closures without changing their hashes. Callers must still authenticate the
// applied closure and enforce current project, root, policy and custody fences.
func DeclarationSourcesMatchExceptWorkState(accepted, applied []DeclarationSourceSnapshot) bool {
	index := func(sources []DeclarationSourceSnapshot) (map[string]DeclarationSourceSnapshot, bool) {
		result := make(map[string]DeclarationSourceSnapshot, len(sources))
		for _, source := range sources {
			if _, duplicate := result[source.Ref]; duplicate || !declarationPath(source.Ref, true) || source.SchemaVersion == "" ||
				source.Hash != declarationHash(source.Raw) || source.Revision != source.Hash {
				return nil, false
			}
			result[source.Ref] = source
		}
		return result, true
	}
	current, ok := index(accepted)
	if !ok {
		return false
	}
	previous, ok := index(applied)
	if !ok || len(current) != len(previous) {
		return false
	}
	root, exists := current[CanonicalRootContractPath]
	if !exists || !IsDeclarationSchemaVersion(root.SchemaVersion) {
		return false
	}
	canonical, err := ParseProjectDeclaration(root.Raw)
	if err != nil {
		return false
	}
	canonical.Project.WorkState = ""
	for ref, next := range current {
		prior, exists := previous[ref]
		if !exists || next.SchemaVersion != prior.SchemaVersion {
			return false
		}
		if ref != CanonicalRootContractPath && ref != LegacyRootContractPath {
			if next.DeclarationSource != prior.DeclarationSource || !bytes.Equal(next.Raw, prior.Raw) {
				return false
			}
			continue
		}
		if next.SchemaVersion != root.SchemaVersion {
			return false
		}
		nextDocument, nextErr := ParseProjectDeclaration(next.Raw)
		priorDocument, priorErr := ParseProjectDeclaration(prior.Raw)
		if nextErr != nil || priorErr != nil {
			return false
		}
		nextDocument.Project.WorkState, priorDocument.Project.WorkState = "", ""
		if !reflect.DeepEqual(nextDocument, canonical) || !reflect.DeepEqual(priorDocument, canonical) {
			return false
		}
	}
	return true
}

// Inspect presence as well as value so an explicit empty/null field cannot
// silently become legacy omitted intent. Never inject a working default.
func invalidProjectWorkState(raw []byte, value string) bool {
	if value != "" && !ValidProjectWorkState(value) {
		return true
	}
	var tree yaml.Node
	if yaml.Unmarshal(raw, &tree) != nil || len(tree.Content) != 1 {
		return false
	}
	root := tree.Content[0]
	for i := 0; root.Kind == yaml.MappingNode && i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "project" {
			continue
		}
		project := root.Content[i+1]
		for j := 0; project.Kind == yaml.MappingNode && j+1 < len(project.Content); j += 2 {
			if project.Content[j].Value == "work_state" {
				state := project.Content[j+1]
				return state.Kind != yaml.ScalarNode || state.Tag != "!!str" || !ValidProjectWorkState(state.Value)
			}
		}
	}
	return false
}
