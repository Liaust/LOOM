package projectstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	developmentStateDocumentLimit = 64 * 1024
	developmentStateNodeLimit     = 1024
)

type FileDevelopmentStateInspector struct {
	Paths PathResolver
}

func (i FileDevelopmentStateInspector) Inspect(ctx context.Context, input DevelopmentStateInput) DevelopmentStateProjection {
	rawStateRoot := strings.TrimSpace(input.StateRoot)
	if rawStateRoot == "" {
		return DevelopmentStateProjection{Posture: DevelopmentStateNotEnabled}
	}
	stateRoot := filepath.Clean(rawStateRoot)
	paths := i.Paths
	if paths == nil {
		paths = OSPathResolver{}
	}
	statePath, err := paths.ResolveWithin(input.MemberRoot, stateRoot)
	if err != nil {
		return DevelopmentStateProjection{Posture: DevelopmentStateInvalid, RelativePath: filepath.ToSlash(stateRoot), ReasonCode: developmentStateReason(err, "state_root_unavailable")}
	}
	if !statePath.Exists {
		return DevelopmentStateProjection{Posture: DevelopmentStateNotEnabled, RelativePath: filepath.ToSlash(stateRoot)}
	}
	if !statePath.Directory {
		return DevelopmentStateProjection{Posture: DevelopmentStateInvalid, RelativePath: filepath.ToSlash(stateRoot), ReasonCode: "state_root_not_directory"}
	}
	identityRelative := filepath.Join(stateRoot, "repo.yaml")
	identityPath, err := paths.ResolveWithin(input.MemberRoot, identityRelative)
	if err != nil {
		return DevelopmentStateProjection{Posture: DevelopmentStateInvalid, RelativePath: filepath.ToSlash(identityRelative), ReasonCode: developmentStateReason(err, "identity_unavailable")}
	}
	if !identityPath.Exists || identityPath.Directory {
		return DevelopmentStateProjection{Posture: DevelopmentStateInvalid, RelativePath: filepath.ToSlash(identityRelative), ReasonCode: "identity_missing"}
	}
	if err := ctx.Err(); err != nil {
		return DevelopmentStateProjection{Posture: DevelopmentStateInvalid, RelativePath: filepath.ToSlash(identityRelative), ReasonCode: "inspection_cancelled"}
	}
	payload, err := readBoundedDevelopmentState(identityPath.Path)
	if err != nil {
		return DevelopmentStateProjection{Posture: DevelopmentStateInvalid, RelativePath: filepath.ToSlash(identityRelative), ReasonCode: "identity_unreadable"}
	}
	digest := sha256.Sum256(payload)
	projection := DevelopmentStateProjection{
		Posture:      DevelopmentStateInvalid,
		RelativePath: filepath.ToSlash(identityRelative),
		SourceDigest: fmt.Sprintf("sha256:%x", digest[:]),
		ReasonCode:   "identity_malformed",
	}
	document, err := decodeBoundedDevelopmentState(payload)
	if err != nil {
		return projection
	}
	repositoryID, projectID := developmentStateBacklink(document)
	if repositoryID == "" || projectID == "" {
		return projection
	}
	if repositoryID != input.RepositoryID || projectID != input.ProjectID {
		projection.Posture = DevelopmentStateMismatch
		projection.ReasonCode = "identity_backlink_mismatch"
		return projection
	}
	projection.Posture = DevelopmentStateEnabled
	projection.ReasonCode = ""
	return projection
}

func readBoundedDevelopmentState(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, developmentStateDocumentLimit+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > developmentStateDocumentLimit {
		return nil, fmt.Errorf("repository state identity exceeds %d bytes", developmentStateDocumentLimit)
	}
	return payload, nil
}

func decodeBoundedDevelopmentState(payload []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("repository state identity must contain one document")
	}
	count := 0
	if !validDevelopmentStateNode(&document, &count) {
		return nil, fmt.Errorf("repository state identity is too complex or contains aliases")
	}
	return &document, nil
}

func validDevelopmentStateNode(node *yaml.Node, count *int) bool {
	if node == nil {
		return true
	}
	(*count)++
	if *count > developmentStateNodeLimit || node.Kind == yaml.AliasNode {
		return false
	}
	if node.Kind == yaml.MappingNode {
		if len(node.Content)%2 != 0 {
			return false
		}
		seen := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode {
				return false
			}
			identity := key.Tag + "\x00" + key.Value
			if _, exists := seen[identity]; exists {
				return false
			}
			seen[identity] = struct{}{}
		}
	}
	for _, child := range node.Content {
		if !validDevelopmentStateNode(child, count) {
			return false
		}
	}
	return true
}

func developmentStateBacklink(document *yaml.Node) (string, string) {
	root := yamlDocumentMapping(document)
	if root == nil {
		return "", ""
	}
	repository := yamlMappingValue(root, "repository")
	repositoryID := yamlScalarValue(repository, "id")
	if repositoryID == "" {
		repositoryID = yamlScalarValue(root, "repository_id")
	}
	projectID := firstDevelopmentStateValue(
		yamlNestedID(root, "owner_project"),
		yamlScalarValue(repository, "project_id"),
		yamlScalarValue(repository, "owning_project_id"),
		yamlNestedID(repository, "project"),
		yamlNestedID(repository, "owning_project"),
		yamlScalarValue(root, "project_id"),
	)
	return strings.TrimSpace(repositoryID), strings.TrimSpace(projectID)
}

func yamlDocumentMapping(document *yaml.Node) *yaml.Node {
	if document == nil {
		return nil
	}
	node := document
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	return node
}

func yamlMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func yamlScalarValue(mapping *yaml.Node, key string) string {
	value := yamlMappingValue(mapping, key)
	if value == nil || value.Kind != yaml.ScalarNode {
		return ""
	}
	return value.Value
}

func yamlNestedID(mapping *yaml.Node, key string) string {
	return yamlScalarValue(yamlMappingValue(mapping, key), "id")
}

func firstDevelopmentStateValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func developmentStateReason(err error, fallback string) string {
	if errors.Is(err, ErrPathEscape) {
		return "path_escape"
	}
	if errors.Is(err, ErrPathInvalid) {
		return "invalid_path"
	}
	return fallback
}
