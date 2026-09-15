package portal

import (
	"encoding/json"
	"strings"

	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
)

func projectWorkflowIntentsFromRegistration(detail projects.ProjectRegistrationDetail) []ProjectWorkflowIntent {
	if detail.Registration == nil || len(detail.Registration.RegistrationPlan) == 0 {
		return nil
	}
	var plan projectcontracts.ProjectPlan
	if err := json.Unmarshal(detail.Registration.RegistrationPlan, &plan); err != nil {
		return nil
	}
	intents := make([]ProjectWorkflowIntent, 0, len(plan.Workflows))
	registrations := map[string]projects.ProjectWorkflowRegistration{}
	for _, registration := range detail.WorkflowRegistrations {
		registrations[registration.WorkflowKey] = registration
	}
	for _, workflow := range plan.Workflows {
		kind := strings.TrimSpace(workflow.ImplementationKind)
		key := firstNonEmpty(workflow.Key, workflow.WorkflowID)
		intent := ProjectWorkflowIntent{
			WorkflowID:             workflow.WorkflowID,
			Name:                   workflow.Name,
			Status:                 workflow.ContractStatus,
			ImplementationKind:     kind,
			ImplementationRequired: kind != "" && kind != "placeholder",
			Placeholder:            kind == "" || kind == "placeholder",
			CapabilityAddress:      workflow.CapabilityAddress,
			Executable:             workflow.Executable,
			ActivationStatus:       workflow.ActivationStatus,
			Source:                 firstNonEmpty(workflow.ManifestPath, workflow.Folder),
		}
		if registration, ok := registrations[key]; ok {
			intent.Status = firstNonEmpty(registration.ActivationStatus, intent.Status)
			intent.RuntimeKind = registration.RuntimeKind
			intent.RuntimeBindingID = ptrString(registration.RuntimeBindingID)
			intent.WorkflowPackageID = ptrString(registration.WorkflowID)
			intent.WorkflowVersionID = ptrString(registration.WorkflowVersionID)
			intent.CapabilityAddress = firstNonEmpty(registration.CapabilityAddress, intent.CapabilityAddress)
			intent.ActivationStatus = registration.ActivationStatus
			intent.Placeholder = false
			delete(registrations, key)
		}
		intents = append(intents, intent)
	}
	for _, registration := range registrations {
		workflowID := registration.WorkflowKey
		if workflowID == "" {
			workflowID = ptrString(registration.WorkflowID)
		}
		intents = append(intents, ProjectWorkflowIntent{
			WorkflowID:             workflowID,
			Name:                   workflowID,
			Status:                 registration.ActivationStatus,
			ImplementationKind:     registration.ImplementationKind,
			ImplementationRequired: registration.ImplementationKind != "" && registration.ImplementationKind != projectcontracts.WorkflowImplementationPlaceholder,
			Placeholder:            false,
			CapabilityAddress:      registration.CapabilityAddress,
			RuntimeKind:            registration.RuntimeKind,
			RuntimeBindingID:       ptrString(registration.RuntimeBindingID),
			WorkflowPackageID:      ptrString(registration.WorkflowID),
			WorkflowVersionID:      ptrString(registration.WorkflowVersionID),
			ActivationStatus:       registration.ActivationStatus,
			Executable:             registration.ImplementationKind == projectcontracts.WorkflowImplementationWorkflow,
			Source:                 firstNonEmpty(registration.WorkflowManifestPath, registration.WorkflowFolder),
		})
	}
	return intents
}

func ptrString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
