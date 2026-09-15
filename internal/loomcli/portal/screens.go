package portal

type Screen struct {
	ID    string
	Title string
}

type ScreenGroup struct {
	ID      string
	Title   string
	Screens []Screen
}

func Screens() []Screen {
	return []Screen{
		{ID: ScreenHome, Title: "Home"},
		{ID: ScreenDoctor, Title: "Doctor"},
		{ID: ScreenTimeline, Title: "Timeline"},
		{ID: ScreenBox, Title: "LOOM Box"},
		{ID: ScreenNotes, Title: "LOOM Notes"},
		{ID: ScreenStorage, Title: "LOOM Main Storage"},
		{ID: ScreenProjects, Title: "Projects"},
		{ID: ScreenServices, Title: "Services"},
		{ID: ScreenDatabase, Title: "Object Store Diagnostics"},
		{ID: ScreenBackground, Title: "Background Operations"},
		{ID: ScreenAutomations, Title: "Automation Center"},
		{ID: ScreenJobs, Title: "Jobs"},
		{ID: ScreenNodes, Title: "Nodes And Watched Roots"},
		{ID: ScreenCapabilities, Title: "Capabilities And Providers"},
	}
}

func ScreenGroups() []ScreenGroup {
	screens := screenByID()
	return []ScreenGroup{
		{
			ID:    "daily",
			Title: "Daily",
			Screens: []Screen{
				screens[ScreenHome],
				screens[ScreenDoctor],
				screens[ScreenProjects],
				screens[ScreenServices],
				screens[ScreenNotes],
				screens[ScreenBox],
			},
		},
		{
			ID:    "data",
			Title: "Data",
			Screens: []Screen{
				screens[ScreenStorage],
				screens[ScreenTimeline],
			},
		},
		{
			ID:    "automation",
			Title: "Automation",
			Screens: []Screen{
				screens[ScreenAutomations],
				screens[ScreenJobs],
			},
		},
		{
			ID:    "network_admin",
			Title: "Network And Admin",
			Screens: []Screen{
				screens[ScreenNodes],
				screens[ScreenCapabilities],
				screens[ScreenBackground],
				screens[ScreenDatabase],
			},
		},
	}
}

func HomeNavigationScreens() []Screen {
	groups := ScreenGroups()
	screens := []Screen{}
	for _, group := range groups {
		screens = append(screens, group.Screens...)
	}
	return screens
}

func screenByID() map[string]Screen {
	screens := map[string]Screen{}
	for _, screen := range Screens() {
		screens[screen.ID] = screen
	}
	return screens
}
