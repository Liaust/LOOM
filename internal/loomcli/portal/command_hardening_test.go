package portal

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCommandBackupContractWorkflowClassification(t *testing.T) {
	tests := []struct {
		command string
		want    CommandClassification
	}{
		{"backup contracts status", CommandClassInspect},
		{"backup contracts preflight --node macbook --path /srv/photos", CommandClassSafeRun},
		{"backup contracts enable photos", CommandClassSensitive},
		{"backup contracts enable photos --dry-run", CommandClassSafeRun},
		{"backup contracts recheck photos", CommandClassSafeRun},
		{"backup contracts retry-activation photos", CommandClassSafeRun},
	}
	for _, test := range tests {
		got, _ := ClassifyCommand(strings.Fields(test.command))
		if got != test.want {
			t.Fatalf("%q classified %s, want %s", test.command, got, test.want)
		}
	}
}

type recordingCommandRunner struct {
	executeCalls int
}

func (r *recordingCommandRunner) Preview(_ context.Context, request CommandRequest) (CommandPreview, error) {
	return BuildCommandPreview(request.Input), nil
}

func (r *recordingCommandRunner) Complete(_ context.Context, _ CommandCompletionRequest) ([]CommandSuggestion, error) {
	return []CommandSuggestion{{Label: "version", InsertText: "version"}}, nil
}

func (r *recordingCommandRunner) Execute(_ context.Context, request CommandRequest) (CommandResult, error) {
	r.executeCalls++
	now := time.Now().UTC()
	return CommandResult{
		Input:            request.Input,
		CanonicalTokens:  append([]string{}, request.CanonicalTokens...),
		CanonicalCommand: commandString(request.CanonicalTokens),
		Classification:   request.Classification,
		Status:           ActionLifecycleSucceeded,
		Summary:          "Command completed.",
		StartedAt:        now,
		CompletedAt:      now,
	}, nil
}

func TestCommandExecutionDependenciesFailClosed(t *testing.T) {
	tests := []struct {
		input string
		want  ExecutionDependency
	}{
		{input: "$ version", want: ExecutionDependencyLocal},
		{input: "$ health", want: ExecutionDependencyMain},
		{input: "$ future-command inspect", want: ExecutionDependencyMain},
	}
	for _, tc := range tests {
		preview := BuildCommandPreview(tc.input)
		if preview.ExecutionDependency != tc.want {
			t.Fatalf("dependency for %q = %q, want %q", tc.input, preview.ExecutionDependency, tc.want)
		}
	}
}

func TestOfflineCommandExecutionBlocksMainBeforeRunnerAndConfirmation(t *testing.T) {
	runner := &recordingCommandRunner{}
	offline := MainAvailability{State: MainAvailabilityOffline}
	for _, request := range []CommandRequest{
		{Input: "$ health", MainAvailability: offline},
		{
			Input:               "$ health",
			CanonicalTokens:     []string{"version"},
			Classification:      CommandClassInspect,
			ExecutionDependency: ExecutionDependencyLocal,
			MainAvailability:    offline,
		},
	} {
		result, err := ExecutePortalCommand(context.Background(), runner, request)
		if err != nil {
			t.Fatalf("ExecutePortalCommand returned error: %v", err)
		}
		if result.ErrorCode != "portal.main_offline" || result.ExecutionDependency != ExecutionDependencyMain {
			t.Fatalf("offline main command result = %#v", result)
		}
	}
	if runner.executeCalls != 0 {
		t.Fatalf("blocked command invoked runner %d time(s)", runner.executeCalls)
	}

	model := Model{
		homeSnapshot: Snapshot{MainAvailability: offline},
		commandPanel: CommandPanelState{
			Lifecycle: ActionLifecyclePreview,
			Preview:   BuildCommandPreview("$ worker run main.indexer_text --once"),
		},
	}
	updated, cmd := model.advanceCommandPreview()
	got := updated.(Model)
	if cmd != nil || got.commandPanel.Lifecycle != ActionLifecycleFailed || got.commandPanel.Result.ErrorCode != "portal.main_offline" {
		t.Fatalf("offline model did not block before confirmation: %#v cmd=%v", got.commandPanel, cmd)
	}
}

func TestOfflineLocalCommandExecutesAndDangerousPolicyWins(t *testing.T) {
	runner := &recordingCommandRunner{}
	offline := MainAvailability{State: MainAvailabilityOffline}
	result, err := ExecutePortalCommand(context.Background(), runner, CommandRequest{Input: "$ version", MainAvailability: offline})
	if err != nil {
		t.Fatalf("local version returned error: %v", err)
	}
	if result.Status != ActionLifecycleSucceeded || result.ExecutionDependency != ExecutionDependencyLocal || runner.executeCalls != 1 {
		t.Fatalf("local version result = %#v, execute calls=%d", result, runner.executeCalls)
	}

	result, err = ExecutePortalCommand(context.Background(), runner, CommandRequest{Input: "$ integration revoke gmail", MainAvailability: offline})
	if err != nil {
		t.Fatalf("dangerous command returned error: %v", err)
	}
	if result.ErrorCode != "portal.command_blocked" || !strings.Contains(result.ErrorMessage, "Dangerous commands") {
		t.Fatalf("dangerous policy was replaced by offline reason: %#v", result)
	}
	if runner.executeCalls != 1 {
		t.Fatalf("dangerous command invoked runner: calls=%d", runner.executeCalls)
	}
}

func TestOfflineCommandPreviewAndCompletionRemainAvailable(t *testing.T) {
	preview := applyMainAvailabilityToCommandPreview(BuildCommandPreview("$ health"), MainAvailability{State: MainAvailabilityOffline})
	if preview.CanonicalCommand != "loom health" || preview.ExecutionDependency != ExecutionDependencyMain {
		t.Fatalf("offline preview = %#v", preview)
	}
	if output := RenderCommandPreview(testMode(), preview, true); !strings.Contains(output, "Dependency: main") || !strings.Contains(output, "Main is offline") {
		t.Fatalf("offline preview missing dependency guidance:\n%s", output)
	}
	if suggestions := CompleteCommand(context.Background(), nil, "corr_offline", "$ ver", len("$ ver")); len(suggestions) == 0 {
		t.Fatal("static command completion should remain available offline")
	}
}

func TestCommandParserEdgeCases(t *testing.T) {
	tests := []struct {
		input          string
		wantCanonical  string
		wantError      string
		wantAlias      string
		wantTokenCount int
	}{
		{input: "$", wantCanonical: "loom", wantTokenCount: 0},
		{input: "   $ health", wantCanonical: "loom health", wantTokenCount: 1},
		{input: "$ loom health", wantCanonical: "loom health", wantTokenCount: 1},
		{input: `$ capability call main@system.status.read '{"include":true}'`, wantCanonical: `loom capability call main@system.status.read {"include":true}`, wantTokenCount: 4},
		{input: `$ capability call "unterminated`, wantError: "unterminated"},
		{input: "$ list capabilities", wantCanonical: "loom capabilities list", wantAlias: "list capabilities", wantTokenCount: 2},
		{input: "$ enter", wantCanonical: "loom enter", wantTokenCount: 1},
		{input: "$ completion zsh", wantCanonical: "loom completion zsh", wantTokenCount: 2},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			parsed := ParseCommandInput(tc.input)
			if tc.wantError != "" {
				if parsed.Error == "" || !strings.Contains(parsed.Error, tc.wantError) {
					t.Fatalf("error = %q, want containing %q", parsed.Error, tc.wantError)
				}
				return
			}
			if parsed.Error != "" {
				t.Fatalf("unexpected parse error: %s", parsed.Error)
			}
			if got := commandString(parsed.CanonicalTokens); got != tc.wantCanonical {
				t.Fatalf("canonical = %q, want %q", got, tc.wantCanonical)
			}
			if parsed.AliasApplied != tc.wantAlias {
				t.Fatalf("alias = %q, want %q", parsed.AliasApplied, tc.wantAlias)
			}
			if len(parsed.CanonicalTokens) != tc.wantTokenCount {
				t.Fatalf("token count = %d, want %d: %#v", len(parsed.CanonicalTokens), tc.wantTokenCount, parsed.CanonicalTokens)
			}
		})
	}
}

func TestCommandParserBlocksShellSyntaxShapes(t *testing.T) {
	for _, input := range []string{
		"$ health | jq .",
		"$ health > out",
		"$ health >> out",
		"$ health < in",
		"$ health && status",
		"$ health || status",
		"$ health; status",
		"$ health &",
		"$ echo $(date)",
		"$ echo `date`",
		"$ health )",
	} {
		t.Run(input, func(t *testing.T) {
			preview := BuildCommandPreview(input)
			if preview.ParseError == "" || !strings.Contains(preview.ParseError, "Shell operators") {
				t.Fatalf("expected shell syntax rejection for %q: %#v", input, preview)
			}
			if preview.Classification != CommandClassBlocked {
				t.Fatalf("classification = %s, want blocked", preview.Classification)
			}
		})
	}
}

func TestCommandAliasTable(t *testing.T) {
	tests := map[string]string{
		"list capabilities":                      "capabilities list",
		"list providers":                         "providers list",
		"list workers":                           "workers list",
		"list objects":                           "object list",
		"list schedules":                         "schedules list",
		"list jobs":                              "jobs list",
		"list nodes":                             "node list",
		"show status":                            "status",
		"show health":                            "health",
		"show sync status":                       "sync status",
		"inspect object obj_1":                   "object inspect obj_1",
		"inspect worker worker":                  "worker inspect worker",
		"inspect capability c":                   "capability inspect c",
		"inspect provider p":                     "provider inspect p",
		"inspect schedule s":                     "schedule inspect s",
		"inspect job j":                          "job inspect j",
		"inspect node n":                         "node inspect n",
		"run worker w --once":                    "worker run w --once",
		"run indexer":                            "worker run main.indexer_text --once",
		"push lane":                              "lane send",
		"mount storage":                          "storage mount-policy enable --local --repair",
		"add facet demo notes":                   "project facet add demo notes --backend",
		"add main facet demo notes":              "project facet add demo notes --backend",
		"add project facet demo notes --dry-run": "project facet add demo notes --dry-run --backend",
		"validate main project demo":             "project validate demo --backend",
		"register main project demo":             "project register demo --backend",
		"archive project demo":                   "project archive demo --dry-run",
		"archive project demo --dry-run":         "project archive demo --dry-run",
		"inspect project archive demo":           "project archive inspect demo",
		"restore project archive demo":           "project archive restore demo --dry-run",
		"create project demo":                    "project scaffold demo",
		"create main project demo":               "project scaffold demo --backend --owner-node main --register",
		"create support bundle":                  "support bundle create",
		"retry index idx":                        "indexes retry idx",
		"retry failed indexes":                   "indexes retry-failed",
		"fire schedule sched":                    "schedule fire sched",
	}
	for input, want := range tests {
		got, _ := ApplyCommandAlias(strings.Fields(input))
		if strings.Join(got, " ") != want {
			t.Fatalf("ApplyCommandAlias(%q) = %q, want %q", input, strings.Join(got, " "), want)
		}
	}
}

func TestCommandClassifierTable(t *testing.T) {
	tests := []struct {
		input string
		want  CommandClassification
	}{
		{"$ health", CommandClassInspect},
		{"$ capabilities list", CommandClassInspect},
		{"$ worker run main.worker_selfcheck --once", CommandClassSafeRun},
		{"$ indexes retry index_1", CommandClassSafeRun},
		{"$ indexes retry-failed --limit 5", CommandClassSafeRun},
		{"$ indexes rebuild object object_1", CommandClassSafeRun},
		{"$ indexes run text --once", CommandClassSafeRun},
		{"$ schedule fire schedule_1", CommandClassSafeRun},
		{"$ maintenance backup run --once", CommandClassSafeRun},
		{"$ maintenance object-store scan --sample 10", CommandClassSafeRun},
		{"$ storage export refresh", CommandClassInspect},
		{"$ mount storage", CommandClassSafeRun},
		{"$ backup create --production", CommandClassSensitive},
		{"$ backup restore-drill /tmp/backup", CommandClassSensitive},
		{"$ capability call main@system.status.read", CommandClassSensitive},
		{"$ script run script_1", CommandClassSensitive},
		{"$ object ingest /tmp/file.md", CommandClassSensitive},
		{"$ direct-event ingest endpoint_1", CommandClassSensitive},
		{"$ job cancel job_1", CommandClassSensitive},
		{"$ job acknowledge job_1", CommandClassSensitive},
		{"$ job archive job_1", CommandClassSensitive},
		{"$ sync deletion-request approve deletion_request_1", CommandClassSensitive},
		{"$ sync deletion-request approve deletion_request_1 --dry-run", CommandClassSafeRun},
		{"$ push lane", CommandClassSensitive},
		{"$ create project demo", CommandClassSensitive},
		{"$ create main project demo", CommandClassSensitive},
		{"$ add facet demo notes", CommandClassSensitive},
		{"$ add facet demo notes --dry-run", CommandClassSafeRun},
		{"$ project facet add demo notes --backend", CommandClassSensitive},
		{"$ project facet add demo notes --backend --dry-run", CommandClassSafeRun},
		{"$ validate main project demo", CommandClassInspect},
		{"$ register main project demo", CommandClassSensitive},
		{"$ project register demo --backend", CommandClassSensitive},
		{"$ archive project demo", CommandClassSafeRun},
		{"$ inspect project archive demo", CommandClassInspect},
		{"$ restore project archive demo", CommandClassInspect},
		{"$ integration auth create gmail", CommandClassSensitive},
		{"$ setup apply --yes", CommandClassDangerous},
		{"$ bootstrap ssh loom-main --yes", CommandClassDangerous},
		{"$ update apply --yes", CommandClassDangerous},
		{"$ update rollback --yes", CommandClassDangerous},
		{"$ project archive gmail-automation --dry-run", CommandClassSafeRun},
		{"$ project archive gmail-automation", CommandClassDangerous},
		{"$ project archive inspect gmail-automation", CommandClassInspect},
		{"$ project archive restore gmail-automation --dry-run", CommandClassInspect},
		{"$ project archive migrate-runtime gmail-automation --dry-run", CommandClassInspect},
		{"$ backup contracts list", CommandClassInspect},
		{"$ backup contracts status", CommandClassInspect},
		{"$ backup contracts inspect field-data", CommandClassInspect},
		{"$ backup contracts preflight --node macbook --path /srv/field-data", CommandClassSafeRun},
		{"$ backup contracts create field-data --path /srv/field-data --dry-run", CommandClassSafeRun},
		{"$ backup contracts create field-data --path /srv/field-data", CommandClassSensitive},
		{"$ backup contracts enable field-data --dry-run", CommandClassSafeRun},
		{"$ backup contracts enable field-data", CommandClassSensitive},
		{"$ backup contracts disable field-data --dry-run", CommandClassSafeRun},
		{"$ backup contracts disable field-data", CommandClassSensitive},
		{"$ backup contracts recheck field-data", CommandClassSafeRun},
		{"$ backup contracts retry-activation field-data", CommandClassSafeRun},
		{"$ backup contracts delete field-data --dry-run", CommandClassSafeRun},
		{"$ backup contracts delete field-data --yes", CommandClassSensitive},
		{"$ database compact", CommandClassSafeRun},
		{"$ database compact --dry-run", CommandClassSafeRun},
		{"$ database compact --out /tmp/plan.json", CommandClassSafeRun},
		{"$ database compact --plan /tmp/plan.json --confirm", CommandClassDangerous},
		{"$ database compact --dry-run=false --confirm", CommandClassDangerous},
		{"$ maintenance retention apply --plan /tmp/plan.json --yes", CommandClassDangerous},
		{"$ support bundle create", CommandClassSensitive},
		{"$ support bundle create --dry-run", CommandClassSafeRun},
		{"$ backup restore /tmp/backup", CommandClassDangerous},
		{"$ integration disable gmail", CommandClassDangerous},
		{"$ integration auth revoke gmail", CommandClassDangerous},
		{"$ schedule disable sched", CommandClassDangerous},
		{"$ provider-advertisement approve ad", CommandClassDangerous},
		{"$ enter", CommandClassBlocked},
		{"$ completion zsh", CommandClassBlocked},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			preview := BuildCommandPreview(tc.input)
			if preview.Classification != tc.want {
				t.Fatalf("classification = %s, want %s: %#v", preview.Classification, tc.want, preview)
			}
		})
	}
}

func TestLaneRepairEffectDescribesCurrentCustodyWorkflow(t *testing.T) {
	for _, input := range []string{"$ lane repair lane_test", "$ lane publish lane_test"} {
		preview := BuildCommandPreview(input)
		effect := strings.ToLower(preview.EffectSummary)
		for _, want := range []string{"imports", "promotion", "catalog", "transport staging", "without re-uploading payload bytes"} {
			if !strings.Contains(effect, want) {
				t.Fatalf("%q effect missing %q: %q", input, want, preview.EffectSummary)
			}
		}
		for _, obsolete := range []string{"storage-view", "storage view", "storage export", "export refresh"} {
			if strings.Contains(effect, obsolete) {
				t.Fatalf("%q effect retained obsolete %q wording: %q", input, obsolete, preview.EffectSummary)
			}
		}
	}
}

func TestCommandCompletionRankingAndFiltering(t *testing.T) {
	client := newFakePortalClient()
	ctx := context.Background()
	capabilityRoots := CompleteCommand(ctx, client, "corr_test", "$ cap", len("$ cap"))
	if !hasCommandSuggestion(capabilityRoots, "capabilities") {
		t.Fatal("missing capabilities completion")
	}
	if len(capabilityRoots) < 2 || capabilityRoots[0].Kind != "command" || capabilityRoots[1].Kind != "command" {
		t.Fatalf("short capability prefix should prefer canonical command roots: %#v", capabilityRoots)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ capabilities ", len("$ capabilities ")); !hasCommandSuggestion(suggestions, "list") || !hasCommandSuggestion(suggestions, "search") {
		t.Fatalf("missing capabilities subcommands: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ storage ", len("$ storage ")); !hasCommandSuggestion(suggestions, "export") || !hasCommandSuggestion(suggestions, "mount-policy") {
		t.Fatalf("missing storage subcommands: %#v", suggestions)
	} else if hasCommandSuggestion(suggestions, "--json") {
		t.Fatalf("storage subcommand context should not show global flags before --: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ backup ", len("$ backup ")); !hasCommandSuggestion(suggestions, "contracts") {
		t.Fatalf("missing backup contracts subcommand: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ backup contracts ", len("$ backup contracts ")); !hasCommandSuggestion(suggestions, "plan") || !hasCommandSuggestion(suggestions, "create") || !hasCommandSuggestion(suggestions, "status") || !hasCommandSuggestion(suggestions, "preflight") || !hasCommandSuggestion(suggestions, "enable") || !hasCommandSuggestion(suggestions, "recheck") || !hasCommandSuggestion(suggestions, "retry-activation") {
		t.Fatalf("missing backup contracts subcommands: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ backup contracts create field-data --p", len("$ backup contracts create field-data --p")); !hasCommandSuggestion(suggestions, "--path") {
		t.Fatalf("missing backup contract path flag: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ backup contracts create field-data --o", len("$ backup contracts create field-data --o")); !hasCommandSuggestion(suggestions, "--owner-node") {
		t.Fatalf("missing backup contract owner-node flag: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ backup contracts preflight --n", len("$ backup contracts preflight --n")); !hasCommandSuggestion(suggestions, "--node") {
		t.Fatalf("missing backup contract preflight node flag: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ backup contracts create field-data --d", len("$ backup contracts create field-data --d")); !hasCommandSuggestion(suggestions, "--dry-run") {
		t.Fatalf("missing backup contract dry-run flag: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ backup contracts delete field-data --", len("$ backup contracts delete field-data --")); !hasCommandSuggestion(suggestions, "--yes") || !hasCommandSuggestion(suggestions, "--dry-run") {
		t.Fatalf("missing backup contract delete flags: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ support ", len("$ support ")); !hasCommandSuggestion(suggestions, "acceptance") {
		t.Fatalf("missing support acceptance subcommand: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ support acceptance ", len("$ support acceptance ")); !hasCommandSuggestion(suggestions, "cleanup") {
		t.Fatalf("missing support acceptance cleanup subcommand: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ lane ", len("$ lane ")); !hasCommandSuggestion(suggestions, "publish") || !hasCommandSuggestion(suggestions, "repair") {
		t.Fatalf("missing lane repair subcommands: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ box ", len("$ box ")); hasCommandSuggestion(suggestions, "dropzone") {
		t.Fatalf("Box completion advertised retired Dropzone: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ setup ", len("$ setup ")); !hasCommandSuggestion(suggestions, "cleanup") {
		t.Fatalf("setup completion omitted cleanup inventory: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ setup cleanup ", len("$ setup cleanup ")); !hasCommandSuggestion(suggestions, "plan") || !hasCommandSuggestion(suggestions, "apply") {
		t.Fatalf("setup cleanup completion omitted reviewed-plan commands: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ maintenance retention ", len("$ maintenance retention ")); !hasCommandSuggestion(suggestions, "dry-run") || !hasCommandSuggestion(suggestions, "apply") {
		t.Fatalf("missing maintenance retention subcommands: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ database ", len("$ database ")); !hasCommandSuggestion(suggestions, "compact") || !hasCommandSuggestion(suggestions, "doctor") {
		t.Fatalf("missing database subcommands: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ database compact --p", len("$ database compact --p")); !hasCommandSuggestion(suggestions, "--plan") || !hasCommandSuggestion(suggestions, "--plan-hash") {
		t.Fatalf("missing database compact reviewed-plan flags: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ database compact --o", len("$ database compact --o")); !hasCommandSuggestion(suggestions, "--out") {
		t.Fatalf("missing database compact out flag: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ maintenance retention dry-run --o", len("$ maintenance retention dry-run --o")); !hasCommandSuggestion(suggestions, "--out") {
		t.Fatalf("missing maintenance retention dry-run out flag: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ maintenance retention apply --p", len("$ maintenance retention apply --p")); !hasCommandSuggestion(suggestions, "--plan") || !hasCommandSuggestion(suggestions, "--plan-hash") {
		t.Fatalf("missing maintenance retention apply reviewed-plan flags: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ storage --", len("$ storage --")); !hasCommandSuggestion(suggestions, "--json") || !hasCommandSuggestion(suggestions, "--correlation-id") {
		t.Fatalf("storage flags should appear after --: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ worker run main.worker_selfcheck --", len("$ worker run main.worker_selfcheck --")); !hasCommandSuggestion(suggestions, "--once") || !hasCommandSuggestion(suggestions, "--idempotency-key") {
		t.Fatalf("missing worker flags: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ project facet add portal-project notes --", len("$ project facet add portal-project notes --")); !hasCommandSuggestion(suggestions, "--backend") || !hasCommandSuggestion(suggestions, "--dry-run") || !hasCommandSuggestion(suggestions, "--force") || !hasCommandSuggestion(suggestions, "--no-register") {
		t.Fatalf("missing project facet add flags: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ project register portal-project --", len("$ project register portal-project --")); !hasCommandSuggestion(suggestions, "--backend") || !hasCommandSuggestion(suggestions, "--idempotency-key") {
		t.Fatalf("missing project register flags: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ project ", len("$ project ")); !hasCommandSuggestion(suggestions, "repos") {
		t.Fatalf("missing project repos completion: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ project repos ", len("$ project repos ")); !hasCommandSuggestion(suggestions, "list") || !hasCommandSuggestion(suggestions, "inspect") || !hasCommandSuggestion(suggestions, "status") {
		t.Fatalf("missing project repos subcommands: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ project repos status portal-project --", len("$ project repos status portal-project --")); !hasCommandSuggestion(suggestions, "--limit") || !hasCommandSuggestion(suggestions, "--after") {
		t.Fatalf("missing project repos pagination flags: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ project scaffold demo --", len("$ project scaffold demo --")); !hasCommandSuggestion(suggestions, "--backend") {
		t.Fatalf("missing project scaffold flags: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ project scaffold demo --r", len("$ project scaffold demo --r")); !hasCommandSuggestion(suggestions, "--register") {
		t.Fatalf("missing project scaffold register flag: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ support ", len("$ support ")); !hasCommandSuggestion(suggestions, "bundle") {
		t.Fatalf("missing support subcommand completion: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ support bundle ", len("$ support bundle ")); !hasCommandSuggestion(suggestions, "create") {
		t.Fatalf("missing support bundle subcommand completion: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ support bundle create --", len("$ support bundle create --")); !hasCommandSuggestion(suggestions, "--output") || !hasCommandSuggestion(suggestions, "--dry-run") {
		t.Fatalf("missing support bundle flags: %#v", suggestions)
	}
	if !hasCommandSuggestion(CompleteCommand(ctx, client, "corr_test", "$ worker run ma", len("$ worker run ma")), "main.worker_selfcheck") {
		t.Fatal("missing live worker completion")
	}
	workerSuggestions := CompleteCommand(ctx, client, "corr_test", "$ worker run ma", len("$ worker run ma"))
	if len(workerSuggestions) == 0 || workerSuggestions[0].Kind != "worker" {
		t.Fatalf("specific worker prefix should rank live worker refs first: %#v", workerSuggestions)
	}
	if !hasCommandSuggestion(CompleteCommand(ctx, client, "corr_test", "$ capability inspect main@system", len("$ capability inspect main@system")), "main@system.status.read") {
		t.Fatal("missing live capability completion")
	}
	if !hasCommandSuggestion(CompleteCommand(ctx, client, "corr_test", "$ project validate portal", len("$ project validate portal")), "portal-project") {
		t.Fatal("missing live project completion")
	}
	for _, tc := range []struct {
		input string
		want  string
	}{
		{input: "$ add", want: "add facet"},
		{input: "$ add main", want: "add main facet"},
		{input: "$ validate", want: "validate main project"},
		{input: "$ register", want: "register main project"},
		{input: "$ archive", want: "archive project"},
		{input: "$ inspect project", want: "inspect project archive"},
		{input: "$ restore", want: "restore project archive"},
		{input: "$ push", want: "push lane"},
		{input: "$ mount", want: "mount storage"},
		{input: "$ cloud", want: "cloud"},
		{input: "$ create", want: "create project"},
		{input: "$ create main", want: "create main project"},
	} {
		if suggestions := CompleteCommand(ctx, client, "corr_test", tc.input, len(tc.input)); !hasCommandSuggestion(suggestions, tc.want) {
			t.Fatalf("missing %q alias for %q: %#v", tc.want, tc.input, suggestions)
		}
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ refresh", len("$ refresh")); hasCommandSuggestion(suggestions, "refresh storage") {
		t.Fatalf("retired storage refresh alias must not be advertised: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ storage export ", len("$ storage export ")); !hasCommandSuggestion(suggestions, "status") || hasCommandSuggestion(suggestions, "refresh") || hasCommandSuggestion(suggestions, "rebuild") {
		t.Fatalf("storage export completion must remain inspection-only: %#v", suggestions)
	}
	if suggestions := CompleteCommand(ctx, client, "corr_test", "$ storage repair ", len("$ storage repair ")); hasCommandSuggestion(suggestions, "export") {
		t.Fatalf("retired storage export repair must not be advertised: %#v", suggestions)
	}
	for _, tc := range []struct {
		input     string
		canonical string
		class     CommandClassification
	}{
		{input: "$ push lane", canonical: "loom lane send", class: CommandClassSensitive},
		{input: "$ lane repair lane_test", canonical: "loom lane repair lane_test", class: CommandClassSensitive},
		{input: "$ lane acknowledge-pending payload.txt", canonical: "loom lane acknowledge-pending payload.txt", class: CommandClassSafeRun},
		{input: "$ lane acknowledge-transfer lane_failed", canonical: "loom lane acknowledge-transfer lane_failed", class: CommandClassSafeRun},
		{input: "$ maintenance retention dry-run", canonical: "loom maintenance retention dry-run", class: CommandClassSafeRun},
		{input: "$ maintenance retention apply --yes", canonical: "loom maintenance retention apply --yes", class: CommandClassDangerous},
		{input: "$ support acceptance cleanup --root /tmp/loom-box", canonical: "loom support acceptance cleanup --root /tmp/loom-box", class: CommandClassSafeRun},
		{input: "$ support acceptance cleanup --root /tmp/loom-box --yes", canonical: "loom support acceptance cleanup --root /tmp/loom-box --yes", class: CommandClassSensitive},
		{input: "$ support acceptance cleanup --root /tmp/loom-box --yes --delete-now", canonical: "loom support acceptance cleanup --root /tmp/loom-box --yes --delete-now", class: CommandClassDangerous},
		{input: "$ backup contracts create field-data --path /srv/field-data --dry-run", canonical: "loom backup contracts create field-data --path /srv/field-data --dry-run", class: CommandClassSafeRun},
		{input: "$ backup contracts delete field-data --yes", canonical: "loom backup contracts delete field-data --yes", class: CommandClassSensitive},
		{input: "$ database compact --dry-run --out /tmp/plan.json", canonical: "loom database compact --dry-run --out /tmp/plan.json", class: CommandClassSafeRun},
		{input: "$ database compact --dry-run=false --confirm", canonical: "loom database compact --dry-run=false --confirm", class: CommandClassDangerous},
		{input: "$ database compact --plan /tmp/plan.json --confirm", canonical: "loom database compact --plan /tmp/plan.json --confirm", class: CommandClassDangerous},
		{input: "$ mount storage", canonical: "loom storage mount-policy enable --local --repair", class: CommandClassSafeRun},
		{input: "$ create project demo", canonical: "loom project scaffold demo", class: CommandClassSensitive},
		{input: "$ create main project demo", canonical: "loom project scaffold demo --backend --owner-node main --register", class: CommandClassSensitive},
		{input: "$ add facet demo notes", canonical: "loom project facet add demo notes --backend", class: CommandClassSensitive},
		{input: "$ add facet demo notes --dry-run", canonical: "loom project facet add demo notes --dry-run --backend", class: CommandClassSafeRun},
		{input: "$ validate main project demo", canonical: "loom project validate demo --backend", class: CommandClassInspect},
		{input: "$ register main project demo", canonical: "loom project register demo --backend", class: CommandClassSensitive},
		{input: "$ archive project demo", canonical: "loom project archive demo --dry-run", class: CommandClassSafeRun},
		{input: "$ inspect project archive demo", canonical: "loom project archive inspect demo", class: CommandClassInspect},
		{input: "$ restore project archive demo", canonical: "loom project archive restore demo --dry-run", class: CommandClassInspect},
	} {
		preview := BuildCommandPreview(tc.input)
		if preview.CanonicalCommand != tc.canonical || preview.Classification != tc.class {
			t.Fatalf("preview for %q = %#v, want %s/%s", tc.input, preview, tc.canonical, tc.class)
		}
	}

	history := appendCommandHistory(nil, CommandResult{Input: "$ health --token secret", CanonicalCommand: "loom health", Status: ActionLifecycleSucceeded})
	if len(history) != 0 {
		t.Fatalf("secret-looking command was stored: %#v", history)
	}
	history = appendCommandHistory(history, CommandResult{Input: "$ health", CanonicalCommand: "loom health", Status: ActionLifecycleSucceeded})
	history = appendCommandHistory(history, CommandResult{Input: "$ health", CanonicalCommand: "loom health", Status: ActionLifecycleFailed})
	if len(history) != 1 || history[0].Status != ActionLifecycleFailed {
		t.Fatalf("duplicate history not collapsed: %#v", history)
	}
}

func TestSelectedRecordAndUniqueCommandSuggestions(t *testing.T) {
	input := "$ worker run ma"
	prefix, start, end := currentCommandToken(input, len(input))
	suggestion := selectedRecordCommandSuggestion(input, prefix, start, end, CommandSelectedRecord{Kind: "worker", Ref: "main.worker_selfcheck", Label: "Worker Selfcheck"})
	if suggestion.Label != "Worker Selfcheck" || suggestion.InsertText != "main.worker_selfcheck" || suggestion.Score != 100 {
		t.Fatalf("selected record suggestion = %#v", suggestion)
	}

	deduped := uniqueCommandSuggestions([]CommandSuggestion{
		{Kind: "worker", Label: "one", InsertText: "main.worker_selfcheck"},
		{Kind: "worker", Label: "two", InsertText: "main.worker_selfcheck"},
		{Kind: "command", Label: "health", InsertText: "health"},
	})
	if len(deduped) != 2 {
		t.Fatalf("deduped suggestions = %#v", deduped)
	}
}

func TestCommandHistorySuggestionsRankBeforeGenericTree(t *testing.T) {
	model := NewModelWithOptions(ModelOptions{
		Mode:            testMode(),
		Client:          newFakePortalClient(),
		Snapshot:        fakeSnapshot(),
		StartScreen:     ScreenHome,
		NoBootAnimation: true,
	})
	model.searchText = "$ hea"
	model.commandMode.History = []CommandHistoryEntry{{
		Input:            "$ health",
		CanonicalCommand: "loom health",
		Status:           ActionLifecycleSucceeded,
	}}
	suggestions := model.prioritizeCommandSuggestions([]CommandSuggestion{
		{Label: "health", Kind: "command", InsertText: "health", ReplacementStart: 2, ReplacementEnd: len("$ hea")},
	})
	if len(suggestions) == 0 || suggestions[0].Kind != "history" || suggestions[0].InsertText != "health" {
		t.Fatalf("history suggestion should rank first: %#v", suggestions)
	}
}
