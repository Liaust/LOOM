package actions

import "loom.local/loom/internal/loomcli/ui"

const ExecutionWorkerRunOnce = "worker.run_once"

type Registry struct {
	actions []Action
	byID    map[string]Action
}

func NewRegistry(actions []Action) Registry {
	registry := Registry{
		actions: append([]Action{}, actions...),
		byID:    map[string]Action{},
	}
	for _, action := range registry.actions {
		registry.byID[action.ID] = action
	}
	return registry
}

func DefaultRegistry() Registry {
	return NewRegistry([]Action{
		{
			ID:          "home.open",
			Title:       "Home",
			Description: "Open the portal home screen.",
			Domain:      "portal",
			Keywords:    []string{"overview", "status", "attention"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "enter"},
			StartScreen: "home",
			Enabled:     true,
		},
		{
			ID:          "timeline.open",
			Title:       "Timeline",
			Description: "Inspect running, recent, and failed LOOM operations.",
			Domain:      "portal",
			Keywords:    []string{"timeline", "operations", "running", "recent", "failed", "progress"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "enter", "--start", "timeline"},
			StartScreen: "timeline",
			Enabled:     true,
		},
		{
			ID:          "doctor.open",
			Title:       "Doctor",
			Description: "Inspect active LOOM issues, grouped findings, safe next actions, and repair routing.",
			Domain:      "portal",
			Keywords:    []string{"doctor", "health", "repair", "attention", "problems", "fix", "issues"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "enter", "--start", "doctor"},
			StartScreen: "doctor",
			Enabled:     true,
		},
		{
			ID:          "background.open",
			Title:       "Background Operations",
			Description: "Inspect workers, maintenance, and background attention.",
			Domain:      "workers",
			Keywords:    []string{"worker", "workers", "maintenance", "background", "operations"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "workers", "list"},
			StartScreen: "background",
			Enabled:     true,
		},
		{
			ID:          "box.open",
			Title:       "LOOM Box",
			Description: "Inspect the local Box filesystem workspace, default project path, and watched Box folders.",
			Domain:      "box",
			Keywords:    []string{"box", "loom box", "documents", "notes", "projects folder"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "box", "status"},
			StartScreen: "box",
			Enabled:     true,
		},
		{
			ID:          "storage.open",
			Title:       "LOOM Main Storage",
			Description: "Inspect main storage, retention, export, mounts, and cloud status.",
			Domain:      "storage",
			Keywords:    []string{"storage", "main storage", "retention", "export", "smb", "mount", "cloud"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "storage", "status"},
			StartScreen: "storage",
			Enabled:     true,
		},
		{
			ID:          "automations.open",
			Title:       "Automation Center",
			Description: "Inspect schedules, direct events, and automation failures.",
			Domain:      "automation",
			Keywords:    []string{"schedule", "schedules", "direct event", "direct events", "gmail", "invocation"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "schedules", "status"},
			StartScreen: "automations",
			Enabled:     true,
		},
		{
			ID:          "jobs_search.open",
			Title:       "Jobs",
			Description: "Inspect jobs, runners, indexing, and job attention.",
			Domain:      "jobs",
			Keywords:    []string{"job", "jobs", "runner", "search", "index", "indexes", "indexing"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "jobs", "status"},
			StartScreen: "jobs",
			Enabled:     true,
		},
		{
			ID:          "nodes_watched_roots.open",
			Title:       "Nodes And Watched Roots",
			Description: "Inspect nodes, sync, watched roots, and watched-root backups.",
			Domain:      "nodes",
			Keywords:    []string{"node", "nodes", "watched root", "watched roots", "sync", "backup", "backups"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "watched-roots", "status"},
			StartScreen: "nodes",
			Enabled:     true,
		},
		{
			ID:          "database.open",
			Title:       "Database And Objects",
			Description: "Inspect objects, text search, and sync records.",
			Domain:      "database",
			Keywords:    []string{"database", "objects", "object", "search", "sync", "knowledge"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "objects", "list"},
			StartScreen: "database",
			Enabled:     true,
		},
		{
			ID:          "capabilities.open",
			Title:       "Capabilities And Providers",
			Description: "Inspect providers and capability discovery surfaces.",
			Domain:      "capabilities",
			Keywords:    []string{"capability", "capabilities", "provider", "providers", "tools"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "capabilities", "list"},
			StartScreen: "capabilities",
			Enabled:     true,
		},
		{
			ID:              "worker.selfcheck.run_once",
			Title:           "Run Worker Selfcheck Once",
			Description:     "Run the low-risk worker substrate selfcheck one time.",
			Domain:          "workers",
			Keywords:        []string{"worker", "selfcheck", "run once", "safe"},
			Risk:            RiskSafeRun,
			RawCommand:      []string{"loom", "worker", "run", "main.worker_selfcheck", "--once"},
			StartScreen:     "background",
			ExecutionKind:   ExecutionWorkerRunOnce,
			ExecutionTarget: "main.worker_selfcheck",
			Enabled:         true,
		},
		{
			ID:              "worker.indexer_text.run_once",
			Title:           "Run Text Indexer Once",
			Description:     "Run the text indexing worker once.",
			Domain:          "search",
			Keywords:        []string{"index", "indexer", "search", "run once"},
			Risk:            RiskSafeRun,
			RawCommand:      []string{"loom", "worker", "run", "main.indexer_text", "--once"},
			StartScreen:     "jobs",
			ExecutionKind:   ExecutionWorkerRunOnce,
			ExecutionTarget: "main.indexer_text",
			Enabled:         true,
		},
		{
			ID:              "worker.automation_scheduler.run_once",
			Title:           "Run Automation Scheduler Once",
			Description:     "Run the schedule scanner once.",
			Domain:          "automation",
			Keywords:        []string{"schedule", "scheduler", "automation", "run once"},
			Risk:            RiskSafeRun,
			RawCommand:      []string{"loom", "worker", "run", "main.automation_scheduler", "--once"},
			StartScreen:     "automations",
			ExecutionKind:   ExecutionWorkerRunOnce,
			ExecutionTarget: "main.automation_scheduler",
			Enabled:         true,
		},
		{
			ID:              "worker.automation_dispatcher.run_once",
			Title:           "Run Automation Dispatcher Once",
			Description:     "Dispatch pending automation invocations once.",
			Domain:          "automation",
			Keywords:        []string{"dispatcher", "invocation", "automation", "run once"},
			Risk:            RiskSafeRun,
			RawCommand:      []string{"loom", "worker", "run", "main.automation_dispatcher", "--once"},
			StartScreen:     "automations",
			ExecutionKind:   ExecutionWorkerRunOnce,
			ExecutionTarget: "main.automation_dispatcher",
			Enabled:         true,
		},
		{
			ID:              "worker.direct_event_ingest.run_once",
			Title:           "Run Direct Event Ingest Once",
			Description:     "Run direct-event ingest processing once.",
			Domain:          "automation",
			Keywords:        []string{"direct event", "ingest", "automation", "run once"},
			Risk:            RiskSafeRun,
			RawCommand:      []string{"loom", "worker", "run", "main.direct_event_ingest", "--once"},
			StartScreen:     "automations",
			ExecutionKind:   ExecutionWorkerRunOnce,
			ExecutionTarget: "main.direct_event_ingest",
			Enabled:         true,
		},
		{
			ID:              "worker.job_runner.run_once",
			Title:           "Run Job Runner Once",
			Description:     "Run the job runner once.",
			Domain:          "jobs",
			Keywords:        []string{"job", "jobs", "runner", "run once"},
			Risk:            RiskSafeRun,
			RawCommand:      []string{"loom", "worker", "run", "main.job_runner", "--once"},
			StartScreen:     "jobs",
			ExecutionKind:   ExecutionWorkerRunOnce,
			ExecutionTarget: "main.job_runner",
			Enabled:         true,
		},
		{
			ID:              "worker.job_sweeper.run_once",
			Title:           "Run Job Sweeper Once",
			Description:     "Run the job sweeper once.",
			Domain:          "jobs",
			Keywords:        []string{"job", "jobs", "sweeper", "run once"},
			Risk:            RiskSafeRun,
			RawCommand:      []string{"loom", "worker", "run", "main.job_sweeper", "--once"},
			StartScreen:     "jobs",
			ExecutionKind:   ExecutionWorkerRunOnce,
			ExecutionTarget: "main.job_sweeper",
			Enabled:         true,
		},
		{
			ID:              "worker.cloud_snapshot_upload.run_once",
			Title:           "Run Cloud Snapshot Upload Once",
			Description:     "Upload the latest verified main backup to configured cloud storage if it is not already present.",
			Domain:          "maintenance",
			Keywords:        []string{"cloud", "snapshot", "backup", "upload", "retention", "run once"},
			Risk:            RiskSafeRun,
			RawCommand:      []string{"loom", "worker", "run", "main.cloud_snapshot_upload", "--once"},
			StartScreen:     "background",
			ExecutionKind:   ExecutionWorkerRunOnce,
			ExecutionTarget: "main.cloud_snapshot_upload",
			Enabled:         true,
		},
		{
			ID:          "raw.status",
			Title:       "Show Raw Status",
			Description: "Print the concise raw LOOM status command.",
			Domain:      "raw",
			Keywords:    []string{"status", "system", "health"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "status"},
			StartScreen: "home",
			Enabled:     true,
		},
		{
			ID:          "raw.health",
			Title:       "Show Raw Health",
			Description: "Print the raw LOOM daemon health command.",
			Domain:      "raw",
			Keywords:    []string{"health", "daemon", "migrations"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "health"},
			StartScreen: "home",
			Enabled:     true,
		},
		{
			ID:          "raw.workers.list",
			Title:       "List Workers",
			Description: "List registered background workers.",
			Domain:      "workers",
			Keywords:    []string{"worker", "workers", "background"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "workers", "list"},
			StartScreen: "background",
			Enabled:     true,
		},
		{
			ID:          "raw.maintenance.status",
			Title:       "Show Maintenance Status",
			Description: "Show main-node maintenance status.",
			Domain:      "maintenance",
			Keywords:    []string{"maintenance", "backup", "database", "object store"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "maintenance", "status"},
			StartScreen: "background",
			Enabled:     true,
		},
		{
			ID:          "raw.cloud.snapshot.status",
			Title:       "Show Cloud Snapshot Status",
			Description: "Show configured cloud storage and cloud snapshot status.",
			Domain:      "maintenance",
			Keywords:    []string{"cloud", "snapshot", "backup", "retention", "status"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "cloud", "snapshot", "status"},
			StartScreen: "background",
			Enabled:     true,
		},
		{
			ID:          "raw.schedules.status",
			Title:       "Show Schedule Status",
			Description: "Show schedule worker status.",
			Domain:      "automation",
			Keywords:    []string{"schedule", "schedules", "scheduler", "automation"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "schedules", "status"},
			StartScreen: "automations",
			Enabled:     true,
		},
		{
			ID:          "raw.direct_events.status",
			Title:       "Show Direct Event Status",
			Description: "Show direct-event ingestion and dispatch status.",
			Domain:      "automation",
			Keywords:    []string{"direct event", "direct events", "webhook", "gmail", "automation"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "direct-events", "status"},
			StartScreen: "automations",
			Enabled:     true,
		},
		{
			ID:          "raw.indexes.failures",
			Title:       "List Index Failures",
			Description: "List failed indexing work.",
			Domain:      "search",
			Keywords:    []string{"index", "indexes", "indexing", "search", "failure"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "indexes", "failures"},
			StartScreen: "jobs",
			Enabled:     true,
		},
		{
			ID:          "raw.jobs.failures",
			Title:       "List Job Failures",
			Description: "List failed jobs.",
			Domain:      "jobs",
			Keywords:    []string{"job", "jobs", "failure", "runner"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "jobs", "failures"},
			StartScreen: "jobs",
			Enabled:     true,
		},
		{
			ID:          "raw.objects.list",
			Title:       "List Objects",
			Description: "List objects in the main database.",
			Domain:      "database",
			Keywords:    []string{"object", "objects", "database", "knowledge"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "objects", "list"},
			StartScreen: "database",
			Enabled:     true,
		},
		{
			ID:          "raw.search",
			Title:       "Search Objects",
			Description: "Run raw text search over indexed object content.",
			Domain:      "database",
			Keywords:    []string{"search", "text search", "objects", "database", "knowledge"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "search", "<query>"},
			StartScreen: "database",
			Enabled:     true,
		},
		{
			ID:          "raw.watched_roots.status",
			Title:       "Show Watched Root Status",
			Description: "Show watched-root scanner reports recorded on main.",
			Domain:      "nodes",
			Keywords:    []string{"watched root", "watched roots", "node", "sync"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "watched-roots", "status"},
			StartScreen: "nodes",
			Enabled:     true,
		},
		{
			ID:          "raw.watched_roots.backups.status",
			Title:       "Show Watched Root Backup Status",
			Description: "Show watched-root backup status recorded on main.",
			Domain:      "nodes",
			Keywords:    []string{"watched root", "backup", "backups", "node"},
			Risk:        RiskReadOnly,
			RawCommand:  []string{"loom", "watched-roots", "backups", "status"},
			StartScreen: "nodes",
			Enabled:     true,
		},
	})
}

func (r Registry) All() []Action {
	return append([]Action{}, r.actions...)
}

func (r Registry) Get(id string) (Action, bool) {
	action, ok := r.byID[id]
	return action, ok
}

func (r Registry) Search(query string, limit int) []SearchResult {
	candidates := make([]ui.Candidate, 0, len(r.actions))
	actionByID := map[string]Action{}
	for _, action := range r.actions {
		candidate := action.Candidate()
		candidates = append(candidates, candidate)
		actionByID[action.ID] = action
	}
	ranked := ui.RankFuzzy(query, candidates, limit)
	results := make([]SearchResult, 0, len(ranked))
	for _, result := range ranked {
		action, ok := actionByID[result.Candidate.ID]
		if !ok {
			continue
		}
		results = append(results, SearchResult{Action: action, Score: result.Score})
	}
	return results
}
