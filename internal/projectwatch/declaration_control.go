package projectwatch

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/ids"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
)

const DeclarationWatchSchemaVersion = "project.watch.control.v1"
const DeclarationWatchSourceKind = "project_declaration"

type DeclarationWatchPayload = projects.DeclarationWatchControlPayload
type DeclarationWatchAck = projects.DeclarationWatchAcknowledgement

func DeclarationSafeRootKey(projectID string) (string, error) {
	if ids.Validate(ids.ProjectPrefix, projectID) != nil {
		return "", fmt.Errorf("invalid declaration project identity")
	}
	return "declaration_" + strings.ToLower(projectID), nil
}
func DeclarationGroupHash(group projects.DeclarationWatchGroup) (string, error) {
	raw, err := json.Marshal(group)
	if err != nil {
		return "", err
	}
	v, err := pc.DecodeDeclarationEvidenceJSON(raw)
	if err != nil {
		return "", err
	}
	raw, err = json.Marshal(v)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), nil
}
func declarationControlEqual(a, b any) bool {
	ar, ea := json.Marshal(a)
	br, eb := json.Marshal(b)
	if ea != nil || eb != nil {
		return false
	}
	av, ea := pc.DecodeDeclarationEvidenceJSON(ar)
	bv, eb := pc.DecodeDeclarationEvidenceJSON(br)
	return ea == nil && eb == nil && reflect.DeepEqual(av, bv)
}

// BindDeclarationRoot resolves only the symbolic project safe-root identity.
// Original normalized compiler bytes and hash remain independently verifiable.
func BindDeclarationRoot(projectID string, item pc.ProjectWatchedRootItem) (projects.DeclarationWatchRoot, error) {
	var original watchedroots.RootConfig
	if err := communication.DecodeStrictJSONObject(item.ConfigJSON, &original); err != nil {
		return projects.DeclarationWatchRoot{}, err
	}
	normalized := watchedroots.NormalizeRootConfig(original)
	if !declarationControlEqual(original, normalized) || original.SafeRootKey != "project" || original.RootKey != item.BackendRootKey || item.WorkerKey != noderuntime.WatchedRootWorkerKey(item.BackendRootKey) || watchedroots.ConfigHash(original) != item.ConfigHash {
		return projects.DeclarationWatchRoot{}, fmt.Errorf("invalid portable project watched root")
	}
	key, err := DeclarationSafeRootKey(projectID)
	if err != nil {
		return projects.DeclarationWatchRoot{}, err
	}
	effective := original
	effective.SafeRootKey = key
	raw, err := json.Marshal(effective)
	if err != nil {
		return projects.DeclarationWatchRoot{}, err
	}
	knowledge := json.RawMessage(nil)
	if source, ok := item.Metadata["knowledge_source"]; ok {
		knowledge, err = json.Marshal(source)
		if err != nil {
			return projects.DeclarationWatchRoot{}, err
		}
	}
	metadata, err := json.Marshal(item.Metadata)
	if err != nil {
		return projects.DeclarationWatchRoot{}, err
	}
	return projects.DeclarationWatchRoot{Metadata: metadata, LocalRootKey: item.Key, BackendRootKey: item.BackendRootKey, WorkerKey: item.WorkerKey, SourceKinds: append([]string{}, item.SourceKinds...), Enabled: item.ActivationStatus != projects.ProjectWatchedRootRegistrationStatusDisabled, CompilerConfigHash: item.ConfigHash, CompilerConfigJSON: append([]byte(nil), item.ConfigJSON...), ConfigHash: watchedroots.ConfigHash(effective), ConfigJSON: raw, KnowledgeSource: knowledge}, nil
}
func BuildDeclarationWatchGroup(analysis pc.Analysis, nodeID, nodeKey string, contributors []projects.DeclarationWatchContributor) (projects.DeclarationWatchGroup, error) {
	if analysis.Loaded == nil || analysis.Loaded.Declaration == nil || analysis.Plan.Declaration == nil {
		return projects.DeclarationWatchGroup{}, fmt.Errorf("compiled declaration required")
	}
	if err := pc.ValidateDeclarationEnrollment(*analysis.Loaded, analysis.Report, analysis.Plan); err != nil {
		return projects.DeclarationWatchGroup{}, err
	}
	expectedContributors := map[string]string{}
	compiled, err := pc.DeclarationContributors(*analysis.Loaded.Declaration)
	if err != nil {
		return projects.DeclarationWatchGroup{}, err
	}
	for _, c := range compiled {
		expectedContributors[string(c.Key)] = string(c.Owner)
	}
	actual := map[string]string{}
	for _, c := range contributors {
		if !c.Retire {
			if expectedContributors[c.Resource] != c.Owner {
				return projects.DeclarationWatchGroup{}, fmt.Errorf("undeclared watch contributor")
			}
			actual[c.Resource] = c.Owner
		} else if expectedContributors[c.Resource] == c.Owner {
			return projects.DeclarationWatchGroup{}, fmt.Errorf("active contributor cannot retire")
		}
	}
	if !reflect.DeepEqual(actual, expectedContributors) {
		return projects.DeclarationWatchGroup{}, fmt.Errorf("all watch owners must contribute")
	}
	group := projects.DeclarationWatchGroup{ProjectID: analysis.Loaded.Declaration.Project.ID, ProjectRoot: analysis.Loaded.RootPath, NodeID: nodeID, NodeKey: nodeKey, Sources: []projects.DeclarationWatchSource{}, Contributors: append([]projects.DeclarationWatchContributor{}, contributors...), Roots: []projects.DeclarationWatchRoot{}}
	for _, s := range analysis.Plan.Declaration.Sources {
		group.Sources = append(group.Sources, projects.DeclarationWatchSource{Ref: s.Ref, SchemaVersion: s.SchemaVersion, Hash: s.Hash, Revision: s.Revision})
	}
	for _, item := range analysis.Plan.WatchedRoots {
		root, err := BindDeclarationRoot(group.ProjectID, item)
		if err != nil {
			return group, err
		}
		group.Roots = append(group.Roots, root)
	}
	sort.Slice(group.Roots, func(i, j int) bool { return group.Roots[i].BackendRootKey < group.Roots[j].BackendRootKey })
	sort.Slice(group.Sources, func(i, j int) bool { return group.Sources[i].Ref < group.Sources[j].Ref })
	sort.Slice(group.Contributors, func(i, j int) bool { return group.Contributors[i].ActionID < group.Contributors[j].ActionID })
	return group, ValidateDeclarationWatchGroup(group)
}
func ValidateDeclarationWatchGroup(group projects.DeclarationWatchGroup) error {
	if ids.Validate(ids.ProjectPrefix, group.ProjectID) != nil || ids.Validate(ids.NodePrefix, group.NodeID) != nil || group.NodeKey == "" || !filepath.IsAbs(group.ProjectRoot) || filepath.Clean(group.ProjectRoot) != group.ProjectRoot || group.ProjectRoot == "/" || group.Roots == nil || group.Contributors == nil || len(group.Contributors) == 0 || group.Sources == nil || len(group.Roots) > 500 || len(group.Contributors) > 500 {
		return fmt.Errorf("invalid project watch group identity")
	}
	if group.Predecessor != nil {
		if err := projects.ValidateDeclarationLegacyPredecessor(group.Predecessor); err != nil {
			return err
		}
		p := group.Predecessor
		if p.ProjectID != group.ProjectID || p.NodeID != group.NodeID || p.NodeKey != group.NodeKey || p.ProjectRoot != group.ProjectRoot || len(p.Members) != len(group.Roots) {
			return fmt.Errorf("legacy group predecessor identity mismatch")
		}
		for i, old := range p.Members {
			root := group.Roots[i]
			r := old.Registration
			metadata := map[string]json.RawMessage{}
			if json.Unmarshal(root.Metadata, &metadata) != nil {
				return fmt.Errorf("legacy group metadata invalid")
			}
			delete(metadata, "declaration_sources")
			if r.BackendRootKey != root.BackendRootKey || r.WorkerKey != root.WorkerKey || r.LocalRootKey != root.LocalRootKey || r.ConfigHash != root.CompilerConfigHash || !declarationControlEqual(r.ConfigJSON, json.RawMessage(root.CompilerConfigJSON)) || !declarationControlEqual(r.SourceKinds, root.SourceKinds) || !declarationControlEqual(r.Metadata, metadata) || !root.Enabled {
				return fmt.Errorf("legacy group changes original member intent")
			}
		}
		// Reserve enough room for the actual fixed-size hashes, operation ID and
		// maximum decimal revision before an owner can mutate registration.
		hash := "sha256:" + strings.Repeat("0", 64)
		bound := projects.DeclarationWatchControlPayload{SchemaVersion: DeclarationWatchSchemaVersion, OperationID: "job_01ARZ3NDEKTSV4RRFFQ69G5FAV", GroupHash: hash, Group: group, Evidence: communication.DesiredStateEvidence{SchemaVersion: communication.ControlEvidenceSchemaVersion, DesiredRevision: 9223372036854775807, TargetNode: group.NodeID, ConfigHash: hash}, Tokens: []projects.DeclarationWatchToken{}}
		for _, c := range group.Contributors {
			bound.Tokens = append(bound.Tokens, projects.DeclarationWatchToken{ActionID: c.ActionID, Owner: c.Owner, Resource: c.Resource, Token: hash, InputHash: hash})
		}
		raw, err := json.Marshal(bound)
		if err != nil || len(raw) > 256*1024 {
			return fmt.Errorf("legacy watch control exceeds payload bound")
		}
	}
	seen := map[string]bool{}
	rootSource := false
	for _, s := range group.Sources {
		if seen[s.Ref] || !validDeclarationDigest(s.Hash) || s.Revision != s.Hash || s.SchemaVersion == "" {
			return fmt.Errorf("invalid project watch source")
		}
		seen[s.Ref] = true
		rootSource = rootSource || (s.Ref == pc.CanonicalRootContractPath && s.SchemaVersion == pc.ProjectSchemaV05)
	}
	if !rootSource {
		return fmt.Errorf("project watch root source required")
	}
	seen = map[string]bool{}
	for _, c := range group.Contributors {
		kind := pc.DeclarationEnrollKnowledge
		if c.Owner == string(pc.DeclarationOwnerProtection) {
			kind = pc.DeclarationReconcileProtection
		} else if c.Owner != string(pc.DeclarationOwnerKnowledge) {
			return fmt.Errorf("invalid watch owner")
		}
		id := string(kind) + ":" + c.Resource
		if c.Retire {
			id = "retire_resource:" + c.Resource + ":" + c.Owner
		}
		if c.ActionID != id || c.Resource == "" || seen[c.ActionID] {
			return fmt.Errorf("invalid watch contributor")
		}
		seen[c.ActionID] = true
	}
	seen = map[string]bool{}
	for _, root := range group.Roots {
		if seen[root.BackendRootKey] || root.LocalRootKey == "" || root.SourceKinds == nil {
			return fmt.Errorf("duplicate or incomplete watch root")
		}
		seen[root.BackendRootKey] = true
		originalItem := pc.ProjectWatchedRootItem{Key: root.LocalRootKey, BackendRootKey: root.BackendRootKey, WorkerKey: root.WorkerKey, SourceKinds: root.SourceKinds, ConfigHash: root.CompilerConfigHash, ConfigJSON: root.CompilerConfigJSON}
		bound, err := BindDeclarationRoot(group.ProjectID, originalItem)
		if err != nil || bound.ConfigHash != root.ConfigHash || !declarationControlEqual(json.RawMessage(bound.ConfigJSON), json.RawMessage(root.ConfigJSON)) {
			return fmt.Errorf("project safe-root binding differs from compiler")
		}
	}
	return nil
}
func DecodeDeclarationWatchPayload(raw []byte) (DeclarationWatchPayload, error) {
	var payload DeclarationWatchPayload
	if len(raw) > communication.MaxProtectedFolderControlPayloadBytes {
		return payload, fmt.Errorf("project watch payload too large")
	}
	if _, err := pc.DecodeDeclarationEvidenceJSON(raw); err != nil {
		return payload, err
	}
	if err := communication.DecodeStrictJSONObject(raw, &payload); err != nil {
		return payload, err
	}
	var envelope struct {
		Group map[string]json.RawMessage `json:"group"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return payload, err
	}
	if value, present := envelope.Group["predecessor"]; present && (payload.Group.Predecessor == nil || string(value) == "null") {
		return payload, fmt.Errorf("null legacy predecessor")
	}
	if payload.SchemaVersion != DeclarationWatchSchemaVersion || ids.Validate(ids.JobPrefix, payload.OperationID) != nil || payload.Evidence.Validate() != nil || payload.Evidence.TargetNode != payload.Group.NodeID || payload.Tokens == nil || len(payload.Tokens) != len(payload.Group.Contributors) {
		return payload, fmt.Errorf("invalid project watch control identity")
	}
	if err := ValidateDeclarationWatchGroup(payload.Group); err != nil {
		return payload, err
	}
	hash, err := DeclarationGroupHash(payload.Group)
	if err != nil || hash != payload.GroupHash || payload.Evidence.ConfigHash != hash {
		return payload, fmt.Errorf("project watch group hash mismatch")
	}
	seen := map[string]bool{}
	for _, token := range payload.Tokens {
		if seen[token.ActionID] || !validDeclarationDigest(token.Token) || !validDeclarationDigest(token.InputHash) {
			return payload, fmt.Errorf("invalid watch intent token")
		}
		seen[token.ActionID] = true
		matched := false
		for _, c := range payload.Group.Contributors {
			matched = matched || (c.ActionID == token.ActionID && c.Owner == token.Owner && c.Resource == token.Resource)
		}
		if !matched {
			return payload, fmt.Errorf("watch token contributor mismatch")
		}
	}
	return payload, nil
}
func validDeclarationDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range value[7:] {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
