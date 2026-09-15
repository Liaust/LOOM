package loomcli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/enrollmentflow"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/setup"
)

type setupPlanFlags struct {
	specPath             string
	planPath             string
	kind                 string
	role                 string
	runtimeClass         string
	nodeKey              string
	displayName          string
	mainURL              string
	boxPath              string
	boxProfile           string
	installMode          string
	serviceManager       string
	packageMode          string
	dataDir              string
	objectStore          string
	serviceRoot          string
	storageRoot          string
	importsRoot          string
	userBackupsRoot      string
	archiveRoot          string
	generatedRoot        string
	boxStateRoot         string
	socketPath           string
	httpListenAddr       string
	bootstrapMode        string
	configDir            string
	stateDir             string
	logDir               string
	homeDir              string
	userName             string
	sourcePath           string
	sourceCommit         string
	migrationsDir        string
	dbURL                string
	nodeAgentConfigPath  string
	nodeAgentStatePath   string
	nodeAgentDataDir     string
	providerMode         string
	manifestPath         string
	safeRoots            []string
	fixIDs               []string
	yes                  bool
	strict               bool
	dryRun               bool
	resume               bool
	skipEnroll           bool
	enroll               bool
	enrollmentTTLSeconds int
	approveEnrollment    bool
	verifyHeartbeat      bool
}

func newSetupCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Plan and inspect local LOOM node setup",
	}
	cmd.AddCommand(newSetupPlanCommand(opts))
	cmd.AddCommand(newSetupApplyCommand(opts))
	cmd.AddCommand(newSetupStatusCommand(opts))
	cmd.AddCommand(newSetupDoctorCommand(opts))
	cmd.AddCommand(newSetupRepairCommand(opts))
	cmd.AddCommand(newSetupCleanupCommand(opts))
	cmd.AddCommand(newSetupManifestCommand(opts))
	cmd.AddCommand(newSetupUninstallCommand(opts))
	cmd.AddCommand(newSetupWorkspaceCommand(opts))
	return cmd
}

type setupWorkspaceInstallFlags struct {
	nodeKey              string
	displayName          string
	mainHost             string
	mainURL              string
	boxPath              string
	homeDir              string
	userName             string
	sourcePath           string
	releaseID            string
	manifestPath         string
	installMode          string
	serviceManager       string
	dryRun               bool
	yes                  bool
	resume               bool
	skipEnroll           bool
	enrollmentTTLSeconds int
}

func newSetupWorkspaceCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Install and manage this machine as a LOOM workspace",
	}
	cmd.AddCommand(newSetupWorkspaceInstallCommand(opts))
	return cmd
}

func newSetupWorkspaceInstallCommand(opts *options) *cobra.Command {
	flags := setupWorkspaceInstallFlags{}
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install this Mac as a local production LOOM workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			input := setup.WorkspaceInstallInput{
				NodeKey:              flags.nodeKey,
				DisplayName:          flags.displayName,
				MainHost:             flags.mainHost,
				MainURL:              flags.mainURL,
				BoxPath:              flags.boxPath,
				HomeDir:              flags.homeDir,
				UserName:             flags.userName,
				SourcePath:           flags.sourcePath,
				ReleaseID:            flags.releaseID,
				ManifestPath:         flags.manifestPath,
				InstallMode:          flags.installMode,
				ServiceManager:       flags.serviceManager,
				DryRun:               flags.dryRun,
				Yes:                  flags.yes,
				Resume:               flags.resume,
				NoInteractive:        opts.noInteractive,
				SkipEnroll:           flags.skipEnroll,
				EnrollmentTTLSeconds: flags.enrollmentTTLSeconds,
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			result, err := setup.RunWorkspaceInstall(ctx, input)
			if opts.jsonOutput {
				encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result)
				if err != nil {
					return err
				}
				if result.Refused {
					return fmt.Errorf("%s", result.Refusal)
				}
				if result.Status == "blocked" {
					return fmt.Errorf("workspace install blocked")
				}
				return encodeErr
			}
			setup.RenderWorkspaceInstall(cmd.OutOrStdout(), result)
			if err != nil {
				return err
			}
			if result.Refused {
				return fmt.Errorf("%s", result.Refusal)
			}
			if result.Status == "blocked" {
				return fmt.Errorf("workspace install blocked")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&flags.nodeKey, "node-key", setup.DefaultWorkspaceNodeKey, "workspace node key")
	cmd.Flags().StringVar(&flags.displayName, "display-name", setup.DefaultWorkspaceDisplayName, "workspace display name")
	cmd.Flags().StringVar(&flags.mainHost, "main-host", "", "main host hint used to derive --main-url when omitted")
	cmd.Flags().StringVar(&flags.mainURL, "main-url", "", "production main private URL; defaults to the MacBook workspace private main URL")
	cmd.Flags().StringVar(&flags.boxPath, "box-path", "", "LOOM Box path; defaults to ~/loom-box")
	cmd.Flags().StringVar(&flags.homeDir, "home-dir", "", "target user home directory")
	cmd.Flags().StringVar(&flags.userName, "user-name", "", "target user name")
	cmd.Flags().StringVar(&flags.sourcePath, "source-path", "", "LOOM source path to build from; defaults to current directory")
	cmd.Flags().StringVar(&flags.releaseID, "release-id", "", "release id under ~/.local/share/loom/releases")
	cmd.Flags().StringVar(&flags.manifestPath, "manifest", "", "install manifest path override")
	cmd.Flags().StringVar(&flags.installMode, "install-mode", setup.InstallModeService, "install mode")
	cmd.Flags().StringVar(&flags.serviceManager, "service-manager", setup.ServiceManagerLaunchd, "service manager")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "preview release staging and setup without writing")
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm local workspace installation")
	cmd.Flags().BoolVar(&flags.resume, "resume", true, "resume from existing local enrollment/setup state where possible")
	cmd.Flags().BoolVar(&flags.skipEnroll, "skip-enroll", false, "install locally without enrolling this workspace")
	cmd.Flags().IntVar(&flags.enrollmentTTLSeconds, "enrollment-ttl-seconds", 1800, "one-time enrollment token lifetime in seconds")
	return cmd
}

type setupUninstallFlags struct {
	setupPlanFlags
	mode                string
	skipMainRevoke      bool
	strictMainRevoke    bool
	removeCredentials   bool
	removeBox           bool
	removeBackups       bool
	removeDB            bool
	removeObjectStore   bool
	allowProductionMain bool
	confirmNode         string
	backupRef           string
}

func newSetupUninstallCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Plan or apply local LOOM uninstall and decommission",
	}
	cmd.AddCommand(newSetupUninstallPlanCommand(opts))
	cmd.AddCommand(newSetupUninstallApplyCommand(opts))
	return cmd
}

func newSetupUninstallPlanCommand(opts *options) *cobra.Command {
	flags := setupUninstallFlags{}
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Derive a read-only LOOM uninstall/decommission plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := setupUninstallInputFromFlags(flags, opts)
			if err != nil {
				return err
			}
			plan, err := setup.PlanUninstall(input)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), plan.PlanHash)
				return nil
			}
			setup.RenderUninstallPlan(cmd.OutOrStdout(), plan)
			return nil
		},
	}
	addSetupUninstallFlags(cmd, &flags, false)
	return cmd
}

func newSetupUninstallApplyCommand(opts *options) *cobra.Command {
	flags := setupUninstallFlags{}
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply local LOOM uninstall/decommission changes",
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := setupUninstallInputFromFlags(flags, opts)
			if err != nil {
				return err
			}
			input.ServiceRunner = setup.DefaultUninstallServiceRunner{}
			result, err := setup.ApplyUninstall(input)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result)
				if result.Refused {
					return fmt.Errorf("%s", result.Refusal)
				}
				if len(result.Blocked) > 0 {
					return fmt.Errorf("setup uninstall blocked")
				}
				return encodeErr
			}
			setup.RenderUninstallResult(cmd.OutOrStdout(), result)
			if result.Refused {
				return fmt.Errorf("%s", result.Refusal)
			}
			if len(result.Blocked) > 0 {
				return fmt.Errorf("setup uninstall blocked")
			}
			return nil
		},
	}
	addSetupUninstallFlags(cmd, &flags, true)
	return cmd
}

func newSetupApplyCommand(opts *options) *cobra.Command {
	flags := setupPlanFlags{}
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply local LOOM setup for this node",
		RunE: func(cmd *cobra.Command, args []string) error {
			var plan *setup.SetupPlan
			var spec setup.SetupSpec
			var err error
			if strings.TrimSpace(flags.planPath) != "" {
				loaded, loadErr := setup.LoadPlanFile(flags.planPath)
				if loadErr != nil {
					return renderSetupCommandError(cmd, opts, "apply", loadErr)
				}
				plan = &loaded
			} else {
				spec, err = setupSpecFromFlags(flags)
				if err != nil {
					return renderSetupCommandError(cmd, opts, "apply", err)
				}
			}
			confirmed := flags.yes
			if !confirmed && !flags.dryRun && !opts.noInteractive {
				prompted, accepted, promptErr := promptSetupApply(cmd)
				if promptErr != nil {
					return renderSetupCommandError(cmd, opts, "apply", promptErr)
				}
				if prompted && !accepted {
					return renderSetupCommandError(cmd, opts, "apply", fmt.Errorf("setup apply cancelled"))
				}
				if accepted {
					confirmed = true
				}
			}
			result, err := setup.Apply(setup.ApplyInput{
				Plan:          plan,
				Spec:          spec,
				ManifestPath:  flags.manifestPath,
				DryRun:        flags.dryRun,
				Yes:           confirmed,
				Resume:        flags.resume,
				NoInteractive: opts.noInteractive,
				CollectFacts:  true,
			})
			if err != nil {
				return renderSetupCommandError(cmd, opts, "apply", err)
			}
			if opts.jsonOutput {
				encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result)
				if result.Refused {
					return fmt.Errorf("%s", result.Refusal)
				}
				if len(result.Blocked) > 0 {
					return fmt.Errorf("setup apply blocked")
				}
				return encodeErr
			}
			setup.RenderApply(cmd.OutOrStdout(), result)
			if result.Refused {
				return fmt.Errorf("%s", result.Refusal)
			}
			if len(result.Blocked) > 0 {
				return fmt.Errorf("setup apply blocked")
			}
			return nil
		},
	}
	addSetupPlanFlags(cmd, &flags)
	cmd.Flags().StringVar(&flags.planPath, "plan", "", "setup plan YAML or JSON path")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "preview local setup mutations without writing")
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm local setup mutations")
	cmd.Flags().BoolVar(&flags.resume, "resume", false, "resume from existing local setup state where possible")
	return cmd
}

func renderSetupCommandError(cmd *cobra.Command, opts *options, operation string, err error) error {
	if err == nil {
		return nil
	}
	return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap(
		"setup."+operation+".failed",
		"setup",
		operation,
		err.Error(),
		err,
	))
}

type setupFDReader interface {
	Fd() uintptr
}

func promptSetupApply(cmd *cobra.Command) (prompted bool, accepted bool, err error) {
	input := cmd.InOrStdin()
	fd, ok := input.(setupFDReader)
	if !ok || !term.IsTerminal(int(fd.Fd())) {
		return false, false, nil
	}
	fmt.Fprint(cmd.OutOrStdout(), "Apply local LOOM setup changes? Type yes to continue: ")
	reader := bufio.NewReader(input)
	line, readErr := reader.ReadString('\n')
	if readErr != nil && readErr != io.EOF {
		return true, false, readErr
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "yes", "y":
		return true, true, nil
	default:
		return true, false, nil
	}
}

func newSetupPlanCommand(opts *options) *cobra.Command {
	flags := setupPlanFlags{}
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Derive a read-only LOOM setup plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := setupSpecFromFlags(flags)
			if err != nil {
				return renderSetupCommandError(cmd, opts, "plan", err)
			}
			plan, err := setup.Plan(setup.PlannerInput{
				Spec:         spec,
				CollectFacts: true,
				ManifestPath: flags.manifestPath,
			})
			if err != nil {
				return renderSetupCommandError(cmd, opts, "plan", err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), plan.PlanHash)
				return nil
			}
			setup.RenderPlan(cmd.OutOrStdout(), plan)
			return nil
		},
	}
	addSetupPlanFlags(cmd, &flags)
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "accepted for symmetry; plan is always read-only")
	return cmd
}

func newSetupStatusCommand(opts *options) *cobra.Command {
	flags := setupPlanFlags{}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show local LOOM setup status",
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := setupStatusInputFromFlags(flags)
			if err != nil {
				return renderSetupCommandError(cmd, opts, "status", err)
			}
			status, err := setup.Status(input)
			if err != nil {
				return renderSetupCommandError(cmd, opts, "status", err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), status.Summary.Status)
				return nil
			}
			setup.RenderStatus(cmd.OutOrStdout(), status, opts.verboseOutput)
			return nil
		},
	}
	addSetupInspectionFlags(cmd, &flags)
	return cmd
}

func newSetupDoctorCommand(opts *options) *cobra.Command {
	flags := setupPlanFlags{}
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose local LOOM setup issues",
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := setupStatusInputFromFlags(flags)
			if err != nil {
				return renderSetupCommandError(cmd, opts, "doctor", err)
			}
			report, err := setup.Doctor(setup.DoctorInput{StatusInput: input, Strict: flags.strict})
			if err != nil {
				return renderSetupCommandError(cmd, opts, "doctor", err)
			}
			if opts.jsonOutput {
				encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(report)
				if reportHasBlockingFindings(report) {
					return fmt.Errorf("setup doctor found blocking issues")
				}
				return encodeErr
			}
			setup.RenderDoctor(cmd.OutOrStdout(), report, opts.verboseOutput)
			if reportHasBlockingFindings(report) {
				return fmt.Errorf("setup doctor found blocking issues")
			}
			return nil
		},
	}
	addSetupInspectionFlags(cmd, &flags)
	cmd.Flags().BoolVar(&flags.strict, "strict", false, "treat warnings as important in future strict checks")
	return cmd
}

func newSetupRepairCommand(opts *options) *cobra.Command {
	flags := setupPlanFlags{}
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Apply explicit safe local LOOM setup repairs",
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := setupStatusInputFromFlags(flags)
			if err != nil {
				return renderSetupCommandError(cmd, opts, "repair", err)
			}
			result, err := setup.Repair(setup.RepairInput{
				StatusInput:   input,
				FixIDs:        flags.fixIDs,
				DryRun:        flags.dryRun,
				Yes:           flags.yes,
				NoInteractive: opts.noInteractive,
			})
			if err != nil {
				return renderSetupCommandError(cmd, opts, "repair", err)
			}
			if opts.jsonOutput {
				encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result)
				if result.Refused {
					return fmt.Errorf("%s", result.Refusal)
				}
				return encodeErr
			}
			setup.RenderRepair(cmd.OutOrStdout(), result)
			if result.Refused {
				return fmt.Errorf("%s", result.Refusal)
			}
			return nil
		},
	}
	addSetupInspectionFlags(cmd, &flags)
	cmd.Flags().StringArrayVar(&flags.fixIDs, "fix", nil, "repair id to apply; repeatable, or all-safe")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "show safe repairs without writing")
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm local repair mutations")
	return cmd
}

func newSetupManifestCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "manifest",
		Short: "Locate and inspect the local LOOM install manifest",
	}
	pathFlags := setupPlanFlags{}
	pathCmd := &cobra.Command{
		Use:   "path",
		Short: "Print the resolved install manifest path",
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := setupSpecFromFlags(pathFlags)
			if err != nil {
				return err
			}
			path, source, err := setup.ResolveManifestPath(setup.ManifestPathInput{
				ExplicitPath: pathFlags.manifestPath,
				Spec:         spec,
			})
			if err != nil {
				return err
			}
			result := setup.ManifestPathResult{Path: path, Source: source, InstallMode: spec.InstallMode}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			fmt.Fprintln(cmd.OutOrStdout(), result.Path)
			return nil
		},
	}
	addSetupManifestPathFlags(pathCmd, &pathFlags)
	cmd.AddCommand(pathCmd)

	inspectFlags := setupPlanFlags{}
	inspectCmd := &cobra.Command{
		Use:   "inspect",
		Short: "Inspect the local install manifest if present",
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := setupSpecFromFlags(inspectFlags)
			if err != nil {
				return err
			}
			path, _, err := setup.ResolveManifestPath(setup.ManifestPathInput{
				ExplicitPath: inspectFlags.manifestPath,
				Spec:         spec,
			})
			if err != nil {
				return err
			}
			inspection := setup.InspectManifest(path)
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(inspection)
			}
			setup.RenderManifestInspection(cmd.OutOrStdout(), inspection)
			return nil
		},
	}
	addSetupManifestPathFlags(inspectCmd, &inspectFlags)
	cmd.AddCommand(inspectCmd)

	recordFlags := setupPlanFlags{}
	var enrollmentResultPath string
	recordCmd := &cobra.Command{
		Use:    "record-enrollment",
		Short:  "Record a redacted enrollment result in the local install manifest",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(enrollmentResultPath) == "" {
				return fmt.Errorf("--from-file is required")
			}
			spec, err := setupSpecFromFlags(recordFlags)
			if err != nil {
				return err
			}
			manifestPath, _, err := setup.ResolveManifestPath(setup.ManifestPathInput{
				ExplicitPath: recordFlags.manifestPath,
				Spec:         spec,
			})
			if err != nil {
				return err
			}
			payload, err := os.ReadFile(enrollmentResultPath)
			if err != nil {
				return err
			}
			var enrollmentResult enrollmentflow.Result
			if err := json.Unmarshal(payload, &enrollmentResult); err != nil {
				return err
			}
			result, err := setup.RecordEnrollment(setup.RecordEnrollmentInput{
				ManifestPath: manifestPath,
				Result:       enrollmentResult,
			})
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "recorded enrollment status=%s manifest=%s\n", result.Manifest.Enrollment.Status, manifestPath)
			return nil
		},
	}
	addSetupManifestPathFlags(recordCmd, &recordFlags)
	recordCmd.Flags().StringVar(&enrollmentResultPath, "from-file", "", "enrollmentflow result JSON path")
	cmd.AddCommand(recordCmd)
	return cmd
}

func addSetupPlanFlags(cmd *cobra.Command, flags *setupPlanFlags) {
	cmd.Flags().StringVar(&flags.specPath, "spec", "", "setup spec YAML or JSON path")
	cmd.Flags().StringVar(&flags.kind, "kind", "", "node kind")
	cmd.Flags().StringVar(&flags.role, "role", "", "node role")
	cmd.Flags().StringVar(&flags.runtimeClass, "runtime-class", "", "runtime class")
	cmd.Flags().StringVar(&flags.nodeKey, "node-key", "", "node key")
	cmd.Flags().StringVar(&flags.displayName, "display-name", "", "display name")
	cmd.Flags().StringVar(&flags.mainURL, "main-url", "", "main node private URL")
	cmd.Flags().StringVar(&flags.boxPath, "box-path", "", "LOOM Box path")
	cmd.Flags().StringVar(&flags.boxProfile, "box-profile", "", "LOOM Box profile")
	cmd.Flags().StringVar(&flags.installMode, "install-mode", "", "install mode")
	cmd.Flags().StringVar(&flags.serviceManager, "service-manager", "", "service manager")
	cmd.Flags().StringVar(&flags.packageMode, "package-mode", "", "package mode")
	cmd.Flags().StringVar(&flags.dataDir, "data-dir", "", "data directory")
	cmd.Flags().StringVar(&flags.objectStore, "object-store", "", "object store path")
	addSetupCanonicalRootFlags(cmd, flags)
	cmd.Flags().StringVar(&flags.socketPath, "socket-path", "", "loomd socket path")
	cmd.Flags().StringVar(&flags.httpListenAddr, "http-listen-addr", "", "private HTTP listen address")
	cmd.Flags().StringVar(&flags.dbURL, "db-url", "", "database URL used for local migration/bootstrap during setup apply")
	cmd.Flags().StringVar(&flags.migrationsDir, "migrations-dir", "", "database migrations directory")
	cmd.Flags().StringVar(&flags.bootstrapMode, "bootstrap-mode", "", "bootstrap mode: none, dev, production")
	cmd.Flags().StringVar(&flags.configDir, "config-dir", "", "config directory")
	cmd.Flags().StringVar(&flags.stateDir, "state-dir", "", "state directory")
	cmd.Flags().StringVar(&flags.logDir, "log-dir", "", "log directory")
	cmd.Flags().StringVar(&flags.nodeAgentConfigPath, "node-agent-config", "", "node-agent config file path")
	cmd.Flags().StringVar(&flags.nodeAgentStatePath, "node-agent-state", "", "node-agent state file path")
	cmd.Flags().StringVar(&flags.nodeAgentDataDir, "node-agent-data-dir", "", "node-agent runtime data directory")
	cmd.Flags().StringVar(&flags.providerMode, "provider-mode", "", "provider exposure mode")
	cmd.Flags().StringArrayVar(&flags.safeRoots, "safe-root", nil, "filesystem safe root, repeatable; use name=/absolute/path,mode=read_only")
	cmd.Flags().BoolVar(&flags.skipEnroll, "skip-enroll", false, "prepare non-main node locally without enrollment")
	cmd.Flags().BoolVar(&flags.enroll, "enroll", false, "run local enrollment after non-main setup")
	cmd.Flags().IntVar(&flags.enrollmentTTLSeconds, "enrollment-ttl-seconds", 1800, "one-time enrollment token lifetime in seconds")
	cmd.Flags().BoolVar(&flags.approveEnrollment, "approve-enrollment", true, "approve the enrollment request using local main access")
	cmd.Flags().BoolVar(&flags.verifyHeartbeat, "verify-heartbeat", true, "send one heartbeat and verify the node on main")
	cmd.Flags().StringVar(&flags.homeDir, "home-dir", "", "target home directory")
	cmd.Flags().StringVar(&flags.userName, "user-name", "", "target user name")
	cmd.Flags().StringVar(&flags.sourcePath, "source-path", "", "LOOM source path")
	cmd.Flags().StringVar(&flags.sourceCommit, "source-commit", "", "LOOM source commit")
	cmd.Flags().StringVar(&flags.manifestPath, "manifest", "", "install manifest path override")
}

func addSetupManifestPathFlags(cmd *cobra.Command, flags *setupPlanFlags) {
	cmd.Flags().StringVar(&flags.specPath, "spec", "", "setup spec YAML or JSON path")
	cmd.Flags().StringVar(&flags.manifestPath, "manifest", "", "install manifest path override")
	cmd.Flags().StringVar(&flags.installMode, "install-mode", "", "install mode")
	cmd.Flags().StringVar(&flags.serviceManager, "service-manager", "", "service manager")
	cmd.Flags().StringVar(&flags.configDir, "config-dir", "", "config directory")
	cmd.Flags().StringVar(&flags.homeDir, "home-dir", "", "target home directory")
}

func addSetupInspectionFlags(cmd *cobra.Command, flags *setupPlanFlags) {
	cmd.Flags().StringVar(&flags.specPath, "spec", "", "setup spec YAML or JSON path")
	cmd.Flags().StringVar(&flags.manifestPath, "manifest", "", "install manifest path override")
	cmd.Flags().StringVar(&flags.kind, "kind", "", "node kind")
	cmd.Flags().StringVar(&flags.role, "role", "", "node role")
	cmd.Flags().StringVar(&flags.runtimeClass, "runtime-class", "", "runtime class")
	cmd.Flags().StringVar(&flags.nodeKey, "node-key", "", "node key")
	cmd.Flags().StringVar(&flags.displayName, "display-name", "", "display name")
	cmd.Flags().StringVar(&flags.mainURL, "main-url", "", "main node private URL")
	cmd.Flags().StringVar(&flags.boxPath, "box-path", "", "LOOM Box path")
	cmd.Flags().StringVar(&flags.boxProfile, "box-profile", "", "LOOM Box profile")
	cmd.Flags().StringVar(&flags.installMode, "install-mode", "", "install mode")
	cmd.Flags().StringVar(&flags.serviceManager, "service-manager", "", "service manager")
	cmd.Flags().StringVar(&flags.packageMode, "package-mode", "", "package mode")
	cmd.Flags().StringVar(&flags.bootstrapMode, "bootstrap-mode", "", "bootstrap mode: none, dev, production")
	cmd.Flags().StringVar(&flags.dbURL, "db-url", "", "database URL used for local setup checks")
	cmd.Flags().StringVar(&flags.migrationsDir, "migrations-dir", "", "database migrations directory")
	cmd.Flags().StringVar(&flags.configDir, "config-dir", "", "config directory")
	cmd.Flags().StringVar(&flags.dataDir, "data-dir", "", "data directory")
	cmd.Flags().StringVar(&flags.objectStore, "object-store", "", "object store path")
	addSetupCanonicalRootFlags(cmd, flags)
	cmd.Flags().StringVar(&flags.socketPath, "socket-path", "", "loomd socket path")
	cmd.Flags().StringVar(&flags.httpListenAddr, "http-listen-addr", "", "private HTTP listen address")
	cmd.Flags().StringVar(&flags.stateDir, "state-dir", "", "state directory")
	cmd.Flags().StringVar(&flags.logDir, "log-dir", "", "log directory")
	cmd.Flags().StringVar(&flags.nodeAgentConfigPath, "node-agent-config", "", "node-agent config file path")
	cmd.Flags().StringVar(&flags.nodeAgentStatePath, "node-agent-state", "", "node-agent state file path")
	cmd.Flags().StringVar(&flags.nodeAgentDataDir, "node-agent-data-dir", "", "node-agent runtime data directory")
	cmd.Flags().StringVar(&flags.providerMode, "provider-mode", "", "provider exposure mode")
	cmd.Flags().StringArrayVar(&flags.safeRoots, "safe-root", nil, "filesystem safe root, repeatable; use name=/absolute/path,mode=read_only")
	cmd.Flags().BoolVar(&flags.skipEnroll, "skip-enroll", false, "treat local non-main enrollment as intentionally skipped")
	cmd.Flags().BoolVar(&flags.enroll, "enroll", false, "expect setup state to use enrollment automation")
	cmd.Flags().IntVar(&flags.enrollmentTTLSeconds, "enrollment-ttl-seconds", 1800, "one-time enrollment token lifetime in seconds")
	cmd.Flags().BoolVar(&flags.approveEnrollment, "approve-enrollment", true, "approve enrollment through local main access")
	cmd.Flags().BoolVar(&flags.verifyHeartbeat, "verify-heartbeat", true, "verify enrollment heartbeat")
	cmd.Flags().StringVar(&flags.homeDir, "home-dir", "", "target home directory")
	cmd.Flags().StringVar(&flags.userName, "user-name", "", "target user name")
	cmd.Flags().StringVar(&flags.sourcePath, "source-path", "", "LOOM source path")
	cmd.Flags().StringVar(&flags.sourceCommit, "source-commit", "", "LOOM source commit")
}

func addSetupCanonicalRootFlags(cmd *cobra.Command, flags *setupPlanFlags) {
	cmd.Flags().StringVar(&flags.serviceRoot, "service-root", "", "canonical service root")
	cmd.Flags().StringVar(&flags.storageRoot, "storage-root", "", "canonical physical storage root")
	cmd.Flags().StringVar(&flags.importsRoot, "imports-root", "", "canonical storage imports root")
	cmd.Flags().StringVar(&flags.userBackupsRoot, "user-backups-root", "", "canonical user backups root")
	cmd.Flags().StringVar(&flags.archiveRoot, "archive-root", "", "canonical archive root")
	cmd.Flags().StringVar(&flags.generatedRoot, "generated-root", "", "canonical generated output root")
	cmd.Flags().StringVar(&flags.boxStateRoot, "box-state-root", "", "node-owned Box runtime state root")
}

func addSetupUninstallFlags(cmd *cobra.Command, flags *setupUninstallFlags, includeApply bool) {
	addSetupInspectionFlags(cmd, &flags.setupPlanFlags)
	cmd.Flags().StringVar(&flags.mode, "mode", setup.UninstallModePreserveData, "uninstall mode: disable-only, preserve-data, purge")
	cmd.Flags().BoolVar(&flags.skipMainRevoke, "skip-main-revoke", false, "skip main-side revoke and write a pending local decommission record")
	cmd.Flags().BoolVar(&flags.strictMainRevoke, "strict-main-revoke", false, "require main-side revoke before local non-main uninstall")
	cmd.Flags().BoolVar(&flags.removeCredentials, "remove-credentials", false, "remove local credential metadata after decommission")
	cmd.Flags().BoolVar(&flags.removeBox, "remove-box", false, "allow purge to remove the LOOM Box")
	cmd.Flags().BoolVar(&flags.removeBackups, "remove-backups", false, "allow purge to remove local backups when such paths are supported")
	cmd.Flags().BoolVar(&flags.removeDB, "remove-db", false, "allow purge to remove local durable database/data directory")
	cmd.Flags().BoolVar(&flags.removeObjectStore, "remove-object-store", false, "allow purge to remove the local object store")
	cmd.Flags().BoolVar(&flags.allowProductionMain, "allow-production-main", false, "allow mutating uninstall operations on production main")
	cmd.Flags().StringVar(&flags.confirmNode, "confirm-node", "", "typed node key required for purge")
	cmd.Flags().StringVar(&flags.backupRef, "backup-ref", "", "backup id or path required for production main purge")
	if includeApply {
		cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "preview uninstall mutations without writing")
		cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm uninstall mutations")
	} else {
		cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "accepted for symmetry; uninstall plan is always read-only")
	}
}

func setupUninstallInputFromFlags(flags setupUninstallFlags, opts *options) (setup.UninstallInput, error) {
	statusInput, err := setupStatusInputFromFlags(flags.setupPlanFlags)
	if err != nil {
		return setup.UninstallInput{}, err
	}
	return setup.UninstallInput{
		StatusInput:         statusInput,
		Mode:                flags.mode,
		DryRun:              flags.dryRun,
		Yes:                 flags.yes,
		NoInteractive:       opts.noInteractive,
		SkipMainRevoke:      flags.skipMainRevoke,
		StrictMainRevoke:    flags.strictMainRevoke,
		RemoveCredentials:   flags.removeCredentials,
		RemoveBox:           flags.removeBox,
		RemoveBackups:       flags.removeBackups,
		RemoveDB:            flags.removeDB,
		RemoveObjectStore:   flags.removeObjectStore,
		AllowProductionMain: flags.allowProductionMain,
		ConfirmNode:         flags.confirmNode,
		BackupRef:           flags.backupRef,
	}, nil
}

func setupSpecFromFlags(flags setupPlanFlags) (setup.SetupSpec, error) {
	if flags.skipEnroll && flags.enroll {
		return setup.SetupSpec{}, fmt.Errorf("--skip-enroll and --enroll are mutually exclusive")
	}
	spec := setup.SetupSpec{}
	if flags.specPath != "" {
		loaded, err := setup.LoadSpecFile(flags.specPath)
		if err != nil {
			return setup.SetupSpec{}, err
		}
		spec = loaded
	}
	if err := applySetupFlagOverrides(&spec, flags); err != nil {
		return setup.SetupSpec{}, err
	}
	facts := setup.CollectLocalFacts()
	normalized, _, err := setup.NormalizeSpec(spec, facts)
	if err != nil {
		return setup.SetupSpec{}, err
	}
	if flags.enroll {
		normalized.RunEnrollment = true
		normalized.SkipEnroll = false
		normalized.EnrollmentTTLSeconds = flags.enrollmentTTLSeconds
		normalized.ApproveEnrollment = flags.approveEnrollment
		normalized.VerifyHeartbeat = flags.verifyHeartbeat
	}
	if flags.skipEnroll {
		normalized.SkipEnroll = true
		normalized.RunEnrollment = false
	}
	return normalized, err
}

func setupStatusInputFromFlags(flags setupPlanFlags) (setup.StatusInput, error) {
	spec, err := setupSpecFromFlags(flags)
	if err != nil {
		return setup.StatusInput{}, err
	}
	if strings.TrimSpace(flags.manifestPath) == "" && strings.TrimSpace(flags.specPath) == "" && !hasSetupSpecFlags(flags) {
		spec = setup.SetupSpec{InstallMode: flags.installMode, ConfigDir: flags.configDir, HomeDir: flags.homeDir}
	}
	return setup.StatusInput{ManifestPath: flags.manifestPath, Spec: spec, CollectFacts: true}, nil
}

func hasSetupSpecFlags(flags setupPlanFlags) bool {
	for _, value := range []string{
		flags.kind,
		flags.role,
		flags.runtimeClass,
		flags.nodeKey,
		flags.displayName,
		flags.mainURL,
		flags.boxPath,
		flags.boxProfile,
		flags.installMode,
		flags.serviceManager,
		flags.packageMode,
		flags.bootstrapMode,
		flags.dbURL,
		flags.migrationsDir,
		flags.dataDir,
		flags.objectStore,
		flags.serviceRoot,
		flags.storageRoot,
		flags.importsRoot,
		flags.userBackupsRoot,
		flags.archiveRoot,
		flags.generatedRoot,
		flags.boxStateRoot,
		flags.socketPath,
		flags.httpListenAddr,
		flags.configDir,
		flags.stateDir,
		flags.logDir,
		flags.homeDir,
		flags.userName,
		flags.sourcePath,
		flags.sourceCommit,
		flags.nodeAgentConfigPath,
		flags.nodeAgentStatePath,
		flags.nodeAgentDataDir,
		flags.providerMode,
	} {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	if len(flags.safeRoots) > 0 || flags.skipEnroll || flags.enroll || flags.enrollmentTTLSeconds != 1800 || !flags.approveEnrollment || !flags.verifyHeartbeat {
		return true
	}
	return false
}

func reportHasBlockingFindings(report setup.DoctorReport) bool {
	for _, finding := range report.Findings {
		if finding.Severity == setup.DiagnosticBlocking {
			return true
		}
	}
	return false
}

func applySetupFlagOverrides(spec *setup.SetupSpec, flags setupPlanFlags) error {
	if flags.kind != "" {
		spec.NodeKind = flags.kind
	}
	if flags.role != "" {
		spec.NodeRole = flags.role
	}
	if flags.runtimeClass != "" {
		spec.RuntimeClass = flags.runtimeClass
	}
	if flags.nodeKey != "" {
		spec.NodeKey = flags.nodeKey
	}
	if flags.displayName != "" {
		spec.DisplayName = flags.displayName
	}
	if flags.mainURL != "" {
		spec.MainURL = flags.mainURL
	}
	if flags.boxPath != "" {
		spec.BoxPath = flags.boxPath
	}
	if flags.boxProfile != "" {
		spec.BoxProfile = flags.boxProfile
	}
	if flags.installMode != "" {
		spec.InstallMode = flags.installMode
	}
	if flags.serviceManager != "" {
		spec.ServiceManager = flags.serviceManager
	}
	if flags.packageMode != "" {
		spec.PackageMode = flags.packageMode
	}
	if flags.dataDir != "" {
		spec.DataDir = flags.dataDir
	}
	if flags.objectStore != "" {
		spec.ObjectStorePath = flags.objectStore
	}
	if flags.serviceRoot != "" {
		spec.ServiceRoot = flags.serviceRoot
	}
	if flags.storageRoot != "" {
		spec.StorageRoot = flags.storageRoot
	}
	if flags.importsRoot != "" {
		spec.ImportsRoot = flags.importsRoot
	}
	if flags.userBackupsRoot != "" {
		spec.UserBackupsRoot = flags.userBackupsRoot
	}
	if flags.archiveRoot != "" {
		spec.ArchiveRoot = flags.archiveRoot
	}
	if flags.generatedRoot != "" {
		spec.GeneratedRoot = flags.generatedRoot
	}
	if flags.boxStateRoot != "" {
		spec.BoxStateRoot = flags.boxStateRoot
	}
	if flags.socketPath != "" {
		spec.SocketPath = flags.socketPath
	}
	if flags.httpListenAddr != "" {
		spec.HTTPListenAddr = flags.httpListenAddr
	}
	if flags.dbURL != "" {
		spec.DBURL = flags.dbURL
	}
	if flags.migrationsDir != "" {
		spec.MigrationsDir = flags.migrationsDir
	}
	if flags.bootstrapMode != "" {
		spec.BootstrapMode = flags.bootstrapMode
	}
	if flags.configDir != "" {
		spec.ConfigDir = flags.configDir
	}
	if flags.stateDir != "" {
		spec.StateDir = flags.stateDir
	}
	if flags.logDir != "" {
		spec.LogDir = flags.logDir
	}
	if flags.nodeAgentConfigPath != "" {
		spec.NodeAgentConfigPath = flags.nodeAgentConfigPath
	}
	if flags.nodeAgentStatePath != "" {
		spec.NodeAgentStatePath = flags.nodeAgentStatePath
	}
	if flags.nodeAgentDataDir != "" {
		spec.NodeAgentDataDir = flags.nodeAgentDataDir
	}
	if flags.providerMode != "" {
		spec.ProviderMode = flags.providerMode
	}
	if len(flags.safeRoots) > 0 {
		roots, err := parseSafeRootFlags(flags.safeRoots)
		if err != nil {
			return err
		}
		spec.SafeRoots = roots
	}
	if flags.enroll {
		spec.RunEnrollment = true
		spec.SkipEnroll = false
		spec.EnrollmentTTLSeconds = flags.enrollmentTTLSeconds
		spec.ApproveEnrollment = flags.approveEnrollment
		spec.VerifyHeartbeat = flags.verifyHeartbeat
	}
	if flags.skipEnroll {
		spec.SkipEnroll = true
		spec.RunEnrollment = false
	}
	if flags.homeDir != "" {
		spec.HomeDir = flags.homeDir
	}
	if flags.userName != "" {
		spec.UserName = flags.userName
	}
	if flags.sourcePath != "" {
		spec.SourcePath = flags.sourcePath
	}
	if flags.sourceCommit != "" {
		spec.SourceCommit = flags.sourceCommit
	}
	return nil
}

func parseSafeRootFlags(values []string) ([]setup.SafeRootSpec, error) {
	roots := make([]setup.SafeRootSpec, 0, len(values))
	for _, raw := range values {
		root, err := parseSafeRootFlag(raw)
		if err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, nil
}

func parseSafeRootFlag(raw string) (setup.SafeRootSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return setup.SafeRootSpec{}, fmt.Errorf("safe-root value cannot be empty")
	}
	root := setup.SafeRootSpec{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return setup.SafeRootSpec{}, fmt.Errorf("invalid safe-root segment %q; use name=/absolute/path,mode=read_only", part)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch key {
		case "name":
			root.Name = value
		case "path":
			root.Path = value
		case "mode":
			root.Mode = value
		default:
			if root.Name != "" || root.Path != "" {
				return setup.SafeRootSpec{}, fmt.Errorf("invalid safe-root segment %q; custom shorthand must be first", part)
			}
			root.Name = key
			root.Path = value
		}
	}
	if strings.TrimSpace(root.Name) == "" || strings.TrimSpace(root.Path) == "" {
		return setup.SafeRootSpec{}, fmt.Errorf("safe-root requires name and path")
	}
	return root, nil
}
