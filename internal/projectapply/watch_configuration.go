package projectapply

import (
	"encoding/json"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/nodeagent/watchedroots"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
)

func validateWatchConfigurationOnly(group projects.DeclarationWatchGroup, rows []projects.ProjectWatchedRootRegistration) error {
	if group.Predecessor != nil || len(group.Roots) == 0 {
		return fail(pc.DeclarationPlanStale, "watch_configuration_membership_changed")
	}
	for _, c := range group.Contributors {
		if c.Retire {
			return fail(pc.DeclarationPlanStale, "watch_configuration_retirement_required")
		}
	}
	current := map[string]projects.ProjectWatchedRootRegistration{}
	desired := map[string]bool{}
	for _, root := range group.Roots {
		desired[root.BackendRootKey] = true
	}
	for _, row := range rows {
		if !desired[row.BackendRootKey] && (row.ActivationStatus == projects.ProjectWatchedRootRegistrationStatusStale || row.ActivationStatus == projects.ProjectWatchedRootRegistrationStatusDisabled) {
			continue
		}
		if _, exists := current[row.BackendRootKey]; exists {
			return fail(pc.DeclarationPlanStale, "watch_configuration_membership_changed")
		}
		current[row.BackendRootKey] = row
	}
	if len(current) != len(group.Roots) {
		return fail(pc.DeclarationPlanStale, "watch_configuration_membership_changed")
	}
	for _, root := range group.Roots {
		row, ok := current[root.BackendRootKey]
		acknowledged := row.ActivationStatus == projects.ProjectWatchedRootRegistrationStatusApplied || row.ActivationStatus == projects.ProjectWatchedRootRegistrationStatusReported || watchConfigurationDeactivated(group, row)
		if !ok || row.ProjectID != group.ProjectID || row.NodeID != group.NodeID || row.WorkerKey != root.WorkerKey || row.LocalRootKey != root.LocalRootKey || !root.Enabled || !acknowledged || !same(row.SourceKinds, root.SourceKinds) || !watchConfigurationMetadataMatches(group, root, row) {
			return fail(pc.DeclarationPlanStale, "watch_configuration_binding_changed")
		}
		var before, after watchedroots.RootConfig
		if communication.DecodeStrictJSONObject(row.ConfigJSON, &before) != nil || communication.DecodeStrictJSONObject(root.ConfigJSON, &after) != nil || watchedroots.ConfigHash(before) != row.ConfigHash || watchedroots.ConfigHash(after) != root.ConfigHash {
			return fail(pc.DeclarationPlanStale, "watch_configuration_invalid")
		}
		before.Watch = after.Watch
		before.Scan.FullRescanInterval = after.Scan.FullRescanInterval
		if !same(before, after) {
			return fail(pc.DeclarationPlanStale, "watch_configuration_policy_changed")
		}
	}
	return nil
}

func watchConfigurationDeactivated(group projects.DeclarationWatchGroup, row projects.ProjectWatchedRootRegistration) bool {
	var audit struct {
		Source, Facet string
		ProjectID     string `json:"project_id"`
	}
	return row.ActivationStatus == projects.ProjectWatchedRootRegistrationStatusDisabled &&
		row.ProjectID == group.ProjectID && row.NodeID == group.NodeID &&
		json.Unmarshal(row.Metadata, &audit) == nil && audit.Source == "project.deactivate" &&
		audit.Facet == "watched_roots" && audit.ProjectID == group.ProjectID
}

func watchConfigurationMetadataMatches(group projects.DeclarationWatchGroup, root projects.DeclarationWatchRoot, row projects.ProjectWatchedRootRegistration) bool {
	var current, desired map[string]json.RawMessage
	if json.Unmarshal(row.Metadata, &current) != nil || json.Unmarshal(root.Metadata, &desired) != nil || current == nil || desired == nil {
		return false
	}
	var adapter struct {
		SchemaVersion       string          `json:"schema_version"`
		ProjectID           string          `json:"project_id"`
		NodeID              string          `json:"node_id"`
		EffectiveConfigHash string          `json:"effective_config_hash"`
		ApplicationData     json.RawMessage `json:"application_data"`
	}
	if json.Unmarshal(current["declaration_adapter"], &adapter) != nil || adapter.SchemaVersion != "project.watch.binding.v1" || adapter.ProjectID != group.ProjectID || adapter.NodeID != group.NodeID || adapter.EffectiveConfigHash != row.ConfigHash {
		return false
	}
	if root.ApplicationData == nil {
		if len(adapter.ApplicationData) != 0 {
			return false
		}
	} else {
		var before projects.DeclarationApplicationDataBinding
		if communication.DecodeStrictJSONObject(adapter.ApplicationData, &before) != nil || before.InstallationRevision == "" || root.ApplicationData.InstallationRevision == "" {
			return false
		}
		// The live owner query already verified an active, committed allocation.
		// Reinstallation can advance its receipt without changing that allocation.
		before.InstallationRevision = root.ApplicationData.InstallationRevision
		if before != *root.ApplicationData {
			return false
		}
	}
	delete(current, "declaration_adapter")
	if watchConfigurationDeactivated(group, row) {
		// The owner stop is retained audit history, not a compiler policy change.
		for _, key := range []string{"source", "facet", "reason", "project_id", "project_slug"} {
			if value, declared := desired[key]; declared {
				current[key] = value
			} else {
				delete(current, key)
			}
		}
	}
	var priorSources, nextSources []pc.DeclarationSourceSnapshot
	refreshSource := !same(current["declaration_sources"], desired["declaration_sources"])
	if refreshSource {
		if communication.DecodeStrictJSONObject(current["declaration_sources"], &priorSources) != nil ||
			communication.DecodeStrictJSONObject(desired["declaration_sources"], &nextSources) != nil ||
			!pc.DeclarationSourcesMatchExceptWorkState(nextSources, priorSources) {
			return false
		}
		// Compare policy without its classification audit revision. The reviewed
		// owner operation still publishes the full new snapshots and awaits ACK.
		current["declaration_sources"] = desired["declaration_sources"]
	}
	// This hash describes the compiler's watch configuration, not source policy.
	if source, exists := current["knowledge_source"]; exists {
		var before, after map[string]json.RawMessage
		if json.Unmarshal(source, &before) != nil || json.Unmarshal(desired["knowledge_source"], &after) != nil || before == nil || after == nil {
			return false
		}
		before["config_hash"] = after["config_hash"]
		if refreshSource {
			if !watchConfigurationSourceHashMatches(before, priorSources) || !watchConfigurationSourceHashMatches(after, nextSources) {
				return false
			}
			before["source_hash"] = after["source_hash"]
		}
		raw, err := json.Marshal(before)
		if err != nil {
			return false
		}
		current["knowledge_source"] = raw
	}
	return same(current, desired)
}

func watchConfigurationSourceHashMatches(metadata map[string]json.RawMessage, sources []pc.DeclarationSourceSnapshot) bool {
	var ref, hash string
	if json.Unmarshal(metadata["source_ref"], &ref) != nil || json.Unmarshal(metadata["source_hash"], &hash) != nil ||
		(ref != pc.CanonicalRootContractPath && ref != pc.LegacyRootContractPath) {
		return false
	}
	for _, source := range sources {
		if source.Ref == ref {
			return source.Hash == hash
		}
	}
	return false
}
