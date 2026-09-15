package portal

import (
	"context"
	"strings"

	"loom.local/loom/internal/loomcli/actions"
)

type ActionResolver struct {
	Registry        actions.Registry
	Client          Client
	CorrelationID   string
	SnapshotOptions SnapshotOptions
}

func ResolvePortalAction(ctx context.Context, resolver ActionResolver, id string, screen string, snapshot Snapshot) (PortalAction, error) {
	if resolver.Registry.All() == nil {
		resolver.Registry = actions.DefaultRegistry()
	}
	if action, ok := resolver.Registry.Get(id); ok {
		return applyMainAvailabilityToAction(PortalActionFromRegistry(action), snapshot.MainAvailability), nil
	}
	for _, action := range resolver.Registry.All() {
		if strings.EqualFold(action.ID, id) {
			return applyMainAvailabilityToAction(PortalActionFromRegistry(action), snapshot.MainAvailability), nil
		}
	}
	for _, action := range WellKnownDynamicActions() {
		if strings.EqualFold(action.ID, id) {
			return applyMainAvailabilityToAction(action, snapshot.MainAvailability), nil
		}
	}
	if resolver.Client == nil {
		return PortalAction{}, ErrActionNotFoundOnScreen(id, screen)
	}
	for _, candidateScreen := range actionResolverScreens(screen) {
		result := LoadScreen(ctx, resolver.Client, resolver.CorrelationID, candidateScreen, snapshot, resolver.SnapshotOptions)
		if result.Err != nil && result.State.Status == ScreenLoadFailed {
			continue
		}
		for _, action := range ScreenActions(result.State) {
			if strings.EqualFold(action.ID, id) {
				return action, nil
			}
		}
		if NormalizeScreen(candidateScreen) == ScreenProjects {
			if action, ok := resolveProjectScopedAction(ctx, resolver, id, snapshot); ok {
				return action, nil
			}
		}
	}
	return PortalAction{}, ErrActionNotFoundOnScreen(id, screen)
}

func resolveProjectScopedAction(ctx context.Context, resolver ActionResolver, id string, snapshot Snapshot) (PortalAction, bool) {
	if resolver.Client == nil || !strings.HasPrefix(strings.ToLower(id), "project.") {
		return PortalAction{}, false
	}
	projectsEnvelope, err := resolver.Client.ListProjects(ctx, resolver.CorrelationID, 100)
	if err != nil {
		return PortalAction{}, false
	}
	for _, project := range projectsEnvelope.Data {
		ref := projectRef(project)
		if ref == "" {
			continue
		}
		prefix := "project." + safeActionID(ref) + "."
		if !strings.HasPrefix(strings.ToLower(id), strings.ToLower(prefix)) {
			continue
		}
		result := loadProjectsScreenWithSelection(ctx, resolver.Client, resolver.CorrelationID, snapshot, ref)
		if result.Err != nil && result.State.Status == ScreenLoadFailed {
			return PortalAction{}, false
		}
		for _, action := range ScreenActions(result.State) {
			if strings.EqualFold(action.ID, id) {
				return action, true
			}
		}
	}
	return PortalAction{}, false
}

func actionResolverScreens(screen string) []string {
	seen := map[string]bool{}
	result := []string{}
	add := func(value string) {
		normalized := NormalizeScreen(value)
		if normalized == ScreenHome || seen[normalized] {
			return
		}
		seen[normalized] = true
		result = append(result, normalized)
	}
	add(screen)
	add(ScreenBox)
	add(ScreenStorage)
	add(ScreenProjects)
	add(ScreenDatabase)
	add(ScreenBackground)
	add(ScreenJobs)
	add(ScreenAutomations)
	add(ScreenNodes)
	add(ScreenCapabilities)
	return result
}
