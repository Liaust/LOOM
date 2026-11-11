package projectactivation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/projects"
)

// Historical project-script dispatchers share ProviderTypeService with actual
// host units. A disabled dispatcher has no host unit to stop or invent receipts
// for, but its exact owner and every endpoint/binding must still be verified.
func (s Service) inactiveProjectScriptArchiveAction(ctx context.Context, detail projects.ProjectRegistrationDetail, item capabilities.ProviderListItem) (*projects.ProjectDeactivationAction, error) {
	var profile struct {
		Runtime   string `json:"runtime"`
		ProjectID string `json:"project_id"`
	}
	if json.Unmarshal(item.RuntimeProfileJSON, &profile) != nil || profile.Runtime != "project_scripts" {
		return nil, nil
	}
	d := json.NewDecoder(bytes.NewReader(item.RuntimeProfileJSON))
	d.DisallowUnknownFields()
	var extra any
	if d.Decode(&profile) != nil || d.Decode(&extra) != io.EOF || profile.ProjectID != detail.Project.Project.ProjectID || item.ScopeID != detail.Project.Project.ProjectScopeID || item.Status != capabilities.ProviderStatusDisabled {
		return nil, fmt.Errorf("project-script provider must match this scope and be disabled before archive")
	}
	var owner struct {
		Source    string `json:"source"`
		Facet     string `json:"facet"`
		ProjectID string `json:"project_id"`
	}
	if json.Unmarshal(item.Metadata, &owner) != nil || owner.Source != "project.activate" || owner.Facet != "scripts" || owner.ProjectID != profile.ProjectID {
		return nil, fmt.Errorf("project-script provider ownership is not authenticated")
	}
	inspection, err := s.Capabilities.InspectProvider(ctx, item.ProviderID)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(inspection.Provider, item.Provider) {
		return nil, fmt.Errorf("project-script provider changed during archive inspection")
	}
	for _, endpoint := range inspection.Endpoints {
		if endpoint.ProviderID != item.ProviderID || endpoint.Status != capabilities.EndpointStatusDisabled {
			return nil, fmt.Errorf("project-script provider has an active or mismatched endpoint")
		}
		current, err := s.Capabilities.InspectCapability(ctx, endpoint.CapabilityEndpointID)
		if err != nil {
			return nil, err
		}
		if current.Endpoint.CapabilityEndpointID != endpoint.CapabilityEndpointID || current.Endpoint.ProviderID != item.ProviderID || current.Endpoint.Status != capabilities.EndpointStatusDisabled || (current.RuntimeBinding != nil && current.RuntimeBinding.Status == capabilities.RuntimeBindingStatusActive) {
			return nil, fmt.Errorf("project-script endpoint or runtime binding is not disabled")
		}
	}
	profileRaw, _ := json.Marshal(profile)
	digest := sha256.Sum256(profileRaw)
	metadata, _ := json.Marshal(map[string]any{"schema_version": "project.script_provider_archive.v1", "provider_id": item.ProviderID, "provider_address": item.CompactAddress, "project_id": profile.ProjectID, "runtime_profile_digest": fmt.Sprintf("sha256:%x", digest)})
	action := deactivationAction(item.ProviderKey, "service", "", "skipped", "Verified disabled project-script dispatcher; no host service unit exists.")
	action.Metadata = metadata
	return &action, nil
}
