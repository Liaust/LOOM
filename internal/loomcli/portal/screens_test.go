package portal

import (
	"strings"
	"testing"

	"loom.local/loom/internal/loomcli/actions"
)

func TestScreenGroupsCoverEveryScreenOnce(t *testing.T) {
	screenIDs := map[string]bool{}
	for _, screen := range Screens() {
		if screen.ID == "" || screen.Title == "" {
			t.Fatalf("screen has empty ID or title: %#v", screen)
		}
		screenIDs[screen.ID] = false
	}

	groupedCount := 0
	for _, group := range ScreenGroups() {
		if group.ID == "" || group.Title == "" {
			t.Fatalf("screen group has empty ID or title: %#v", group)
		}
		if len(group.Screens) == 0 {
			t.Fatalf("screen group %s has no screens", group.ID)
		}
		for _, screen := range group.Screens {
			seen, ok := screenIDs[screen.ID]
			if !ok {
				t.Fatalf("screen group %s contains unknown screen %q", group.ID, screen.ID)
			}
			if seen {
				t.Fatalf("screen %q appears in more than one group", screen.ID)
			}
			screenIDs[screen.ID] = true
			groupedCount++
		}
	}
	if groupedCount != len(screenIDs) {
		t.Fatalf("grouped screens = %d, want %d", groupedCount, len(screenIDs))
	}
	for screenID, seen := range screenIDs {
		if !seen {
			t.Fatalf("screen %q is not assigned to a group", screenID)
		}
	}
}

func TestHomeNavigationScreensFollowRenderedGroupOrder(t *testing.T) {
	got := HomeNavigationScreens()
	want := []string{
		ScreenHome,
		ScreenDoctor,
		ScreenProjects,
		ScreenServices,
		ScreenNotes,
		ScreenBox,
		ScreenStorage,
		ScreenTimeline,
		ScreenAutomations,
		ScreenJobs,
		ScreenNodes,
		ScreenCapabilities,
		ScreenBackground,
		ScreenDatabase,
	}
	if len(got) != len(want) {
		t.Fatalf("home navigation screens = %d, want %d: %#v", len(got), len(want), got)
	}
	for i, screen := range got {
		if screen.ID != want[i] {
			t.Fatalf("home navigation screen %d = %q, want %q", i, screen.ID, want[i])
		}
	}
}

func TestHomeNavigationRendersScreenGroups(t *testing.T) {
	output := RenderScreen(testMode(), fakeSnapshot(), actions.DefaultRegistry(), ScreenHome)
	for _, want := range []string{
		"Navigation",
		"Daily",
		"Doctor",
		"Data",
		"Automation",
		"Network And Admin",
		"Projects",
		"LOOM Notes",
		"LOOM Box",
		"Object Store Diagnostics",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("home navigation missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "Use / for search, # for scoped search, or Enter to open a section.") {
		t.Fatalf("home navigation should not duplicate footer command hints:\n%s", output)
	}
}

func TestNavigationSearchKeepsGroupedScreensReachable(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  string
	}{
		{"notes", ScreenNotes},
		{"doctor", ScreenDoctor},
		{"repair", ScreenDoctor},
		{"attention", ScreenDoctor},
		{"health", ScreenDoctor},
		{"projects", ScreenProjects},
		{"storage", ScreenStorage},
		{"automation", ScreenAutomations},
		{"nodes", ScreenNodes},
		{"diagnostics", ScreenDatabase},
	} {
		t.Run(tc.query, func(t *testing.T) {
			results := SearchPortalNavigation(actions.DefaultRegistry(), tc.query, 20)
			for _, result := range results {
				if result.Action.TargetRef == tc.want || result.Action.Executor.Target == tc.want {
					return
				}
			}
			t.Fatalf("navigation search for %q did not return screen %q: %#v", tc.query, tc.want, results)
		})
	}
}
