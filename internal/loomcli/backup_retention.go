package loomcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/update"
)

func newBackupRetentionCommand(opts *options) *cobra.Command {
	parent := &cobra.Command{Use: "retention", Short: "Review and apply bounded local recovery retention"}
	for _, apply := range []bool{false, true} {
		var pins []string
		var hash string
		var yes bool
		var scope, cloudConfig string
		var capturePins []string
		name := "plan"
		if apply {
			name = "apply"
		}
		cmd := &cobra.Command{Use: name, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			cc, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			fail := func(err error) error {
				message := "Local retention refused: " + err.Error()
				if errors.Is(err, os.ErrPermission) {
					message += "; private recovery inventory requires the local operator: sudo -n /run/current-system/sw/bin/loom --config /etc/loom/loom.env backup retention plan --json"
				}
				return renderError(cmd, opts, cc.CorrelationID, loomerrors.Wrap("backup.retention_refused", "backup", "operational", message, err))
			}
			if apply && (!yes || hash == "" || os.Geteuid() != 0) {
				return fail(fmt.Errorf("apply requires the local operator (root), --yes and --plan-hash"))
			}
			if scope != "operational" && scope != "notes" {
				return fail(fmt.Errorf("scope must be operational or notes"))
			}
			root := filepath.Join(cc.Config.DataDir, "backups", "operational")
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
			defer cancel()
			if scope == "notes" {
				if len(pins) > 0 {
					return fail(fmt.Errorf("--protect is operational-only; use --protect-capture for Notes"))
				}
				cohorts, err := backupRetentionCohortRoot(cc.Config.ApplicationDataBackupRoot)
				if err != nil {
					return fail(err)
				}
				loaded, err := cloudstorage.LoadConfig(cloudConfig)
				if err != nil {
					return fail(err)
				}
				if !loaded.Exists || !loaded.Config.Enabled || loaded.Config.Snapshots.Backend != cloudstorage.SnapshotBackendBorg {
					return fail(fmt.Errorf("configured Borg protection owner is required"))
				}
				protected := map[string]string{}
				for _, id := range capturePins {
					protected[id] = "operator pin"
				}
				input := backupstrategy.NotesRecoveryRetentionInput{Root: cohorts,
					PublicationRoot: backupstrategy.NotesRecoveryPublicationRoot(cc.Config.DataDir),
					ProtectionRoot:  backupstrategy.NotesRecoveryProtectionRoot(loaded.Config.StateDir),
					Repository:      loaded.Config.Snapshots.Borg.Repository, Protected: protected}
				if apply {
					result, err := backupstrategy.ApplyNotesRecoveryRetention(ctx, root, input, hash, yes,
						func(ctx context.Context, proof backupstrategy.NotesRecoveryProtection) error {
							return backupRetentionRecheckCloud(ctx, opts.configFile, cloudConfig, loaded.Config, proof)
						})
					if err != nil {
						return fail(err)
					}
					backupRetentionPublishSummary(cmd, cc.Config.DataDir, input)
					return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
				}
				lease, err := backupstrategy.LockOperationalPackages(root, false)
				if err != nil {
					return fail(err)
				}
				defer lease.Close()
				plan, err := backupstrategy.PlanNotesRecoveryRetention(ctx, input)
				if err != nil {
					return fail(err)
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Notes candidate allocated bytes: %d (estimate, not reclaimed)\nPlan: %s\n", plan.CandidateBytes, plan.PlanHash)
				for _, entry := range plan.Entries {
					fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", entry.Action, entry.ID, entry.Reason)
				}
				return nil
			}
			if len(capturePins) > 0 {
				return fail(fmt.Errorf("--protect-capture requires --scope notes"))
			}
			resolve := func() (map[string]string, error) {
				cohorts, err := backupRetentionCohortRoot(cc.Config.ApplicationDataBackupRoot)
				if err != nil {
					return nil, err
				}
				independent, err := backupRetentionIndependentNotesCopies(ctx, cc.Config.DataDir, cohorts, opts.configFile, cloudConfig, apply)
				if err != nil {
					return nil, err
				}
				protected, err := backupRetentionFilePins(cc.Config.DataDir, cohorts, pins, independent)
				if err != nil {
					return nil, err
				}
				for _, key := range []string{"main.main_backup", "main.cloud_snapshot_upload"} {
					readCtx, stop := context.WithTimeout(ctx, 10*time.Second)
					envelope, err := cc.Client.InspectWorker(readCtx, cc.CorrelationID, key)
					stop()
					if err != nil || !envelope.OK || envelope.Data.CurrentRun != nil || envelope.Data.LatestCheckpoint == nil {
						return nil, fmt.Errorf("%s is active or its committed checkpoint is unavailable", key)
					}
					var checkpoint struct {
						Committed            bool   `json:"committed"`
						PackageID            string `json:"package_id"`
						OperationalPackageID string `json:"operational_package_id"`
					}
					if err := json.Unmarshal(envelope.Data.LatestCheckpoint.CheckpointJSON, &checkpoint); err != nil || !checkpoint.Committed {
						return nil, fmt.Errorf("%s committed package identity is unavailable", key)
					}
					id := firstNonEmptyString(checkpoint.OperationalPackageID, checkpoint.PackageID)
					if id == "" {
						return nil, fmt.Errorf("%s package identity is missing", key)
					}
					protected[id] = key + " current checkpoint"
				}
				return protected, nil
			}
			if apply {
				result, err := backupstrategy.ApplyLocalOperationalRetention(ctx, root, hash, yes, resolve)
				if err != nil {
					return fail(err)
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			lease, err := backupstrategy.LockOperationalPackages(root, false)
			if err != nil {
				return fail(err)
			}
			defer lease.Close()
			protected, err := resolve()
			if err != nil {
				return fail(err)
			}
			plan, err := backupstrategy.PlanLocalOperationalRetention(ctx, root, protected)
			if err != nil {
				return fail(err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Keep: %d verified packages; remove candidates: %d; held findings: %d\nCandidate allocated bytes: %d (estimate, not reclaimed)\nPlan: %s\n",
				len(plan.Selection.Keep), len(plan.Selection.Remove), len(plan.Selection.Findings), plan.CandidateBytes, plan.PlanHash)
			for _, entry := range plan.Selection.Keep {
				fmt.Fprintf(cmd.OutOrStdout(), "Keep %s: %s\n", entry.PackageID, entry.Reason)
			}
			for _, entry := range plan.Selection.Remove {
				fmt.Fprintf(cmd.OutOrStdout(), "Candidate %s\n", entry.PackageID)
			}
			return nil
		}}
		cmd.Flags().StringSliceVar(&pins, "protect", nil, "additional operational package IDs to retain (repeat on apply)")
		cmd.Flags().StringVar(&scope, "scope", "operational", "local recovery class: operational or notes")
		cmd.Flags().StringVar(&cloudConfig, "cloud-config", cloudstorage.DefaultConfigPath, "existing cloud configuration for exact Notes protection binding")
		cmd.Flags().StringSliceVar(&capturePins, "protect-capture", nil, "additional Notes capture IDs to retain (repeat on apply)")
		if apply {
			cmd.Flags().StringVar(&hash, "plan-hash", "", "exact reviewed current retention plan hash")
			cmd.Flags().BoolVar(&yes, "yes", false, "confirm deletion of only reviewed eligible local packages")
		}
		parent.AddCommand(cmd)
	}
	parent.AddCommand(&cobra.Command{
		Use: "verify <package-id>", Short: "Deep-verify one local package and record its retention inspection receipt",
		Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			cc, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			fail := func(err error) error {
				return renderError(cmd, opts, cc.CorrelationID, loomerrors.Wrap("backup.retention_verification_refused", "backup", "operational", err.Error(), err))
			}
			if os.Geteuid() != 0 {
				return fail(fmt.Errorf("recording verification requires the local operator (root); no private payload permissions are changed"))
			}
			id := args[0]
			if !strings.HasPrefix(id, "operational-") || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) {
				return fail(fmt.Errorf("an exact operational package ID is required, not a path"))
			}
			root := filepath.Join(cc.Config.DataDir, "backups", "operational")
			lease, err := backupstrategy.LockOperationalPackages(root, false)
			if err != nil {
				return fail(err)
			}
			defer lease.Close()
			verified, err := backupstrategy.VerifyAndRecordOperationalPackage(cmd.Context(), backupstrategy.OperationalPackageVerificationInput{PackageDir: filepath.Join(root, id), ExpectedPackageID: id})
			if err != nil {
				return fail(err)
			}
			if verified.Status != "succeeded" {
				return fail(fmt.Errorf("package verification failed; retain the package and inspect its exact verification findings"))
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(verified)
		},
	})
	for _, deep := range []bool{false, true} {
		name := "record-capture"
		if deep {
			name = "verify-capture"
		}
		parent.AddCommand(&cobra.Command{Use: name + " <capture-id>", Hidden: !deep, Args: cobra.ExactArgs(1),
			Short: "Verify one completed Notes capture and save its publication inventory", RunE: func(cmd *cobra.Command, args []string) error {
				cc, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
				}
				fail := func(err error) error {
					return renderError(cmd, opts, cc.CorrelationID, loomerrors.Wrap("backup.capture_verification_refused", "backup", "notes", err.Error(), err))
				}
				if os.Geteuid() != 0 || !backupstrategy.ValidNotesRecoveryID(args[0]) {
					return fail(fmt.Errorf("local root operator and exact capture ID required"))
				}
				root, err := backupRetentionCohortRoot(cc.Config.ApplicationDataBackupRoot)
				if err != nil {
					return fail(err)
				}
				lease, err := backupstrategy.LockOperationalPackages(filepath.Join(cc.Config.DataDir, "backups", "operational"), false)
				if err != nil {
					return fail(err)
				}
				defer lease.Close()
				publication, err := backupstrategy.RecordNotesRecoveryPublication(cmd.Context(), root, args[0], backupstrategy.NotesRecoveryPublicationRoot(cc.Config.DataDir), deep, filepath.Join(cc.Config.DataDir, "backups", "operational"))
				if err != nil {
					return fail(err)
				}
				loaded, _ := cloudstorage.LoadConfig(cloudstorage.DefaultConfigPath)
				backupRetentionPublishSummary(cmd, cc.Config.DataDir, backupstrategy.NotesRecoveryRetentionInput{Root: root,
					PublicationRoot: backupstrategy.NotesRecoveryPublicationRoot(cc.Config.DataDir),
					ProtectionRoot:  backupstrategy.NotesRecoveryProtectionRoot(loaded.Config.StateDir), Repository: loaded.Config.Snapshots.Borg.Repository})
				return json.NewEncoder(cmd.OutOrStdout()).Encode(publication)
			}})
	}
	resume := &cobra.Command{Use: "resume-capture <capture-id>", Args: cobra.ExactArgs(1),
		Short: "Resume one already-reviewed, durably recorded Notes retirement", RunE: func(cmd *cobra.Command, args []string) error {
			cc, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			fail := func(err error) error {
				return renderError(cmd, opts, cc.CorrelationID, loomerrors.Wrap("backup.retirement_resume_refused", "backup", "notes", err.Error(), err))
			}
			yes, _ := cmd.Flags().GetBool("yes")
			if os.Geteuid() != 0 || !yes {
				return fail(fmt.Errorf("resuming deletion requires the local root operator and --yes"))
			}
			root, err := backupRetentionCohortRoot(cc.Config.ApplicationDataBackupRoot)
			if err != nil {
				return fail(err)
			}
			cloudConfig, _ := cmd.Flags().GetString("cloud-config")
			loaded, err := cloudstorage.LoadConfig(cloudConfig)
			if err != nil || !loaded.Exists {
				return fail(fmt.Errorf("exact current cloud configuration required"))
			}
			receipt, err := backupstrategy.ResumeNotesRecoveryRetirement(cmd.Context(), filepath.Join(cc.Config.DataDir, "backups", "operational"), root, args[0],
				func(ctx context.Context, proof backupstrategy.NotesRecoveryProtection) error {
					return backupRetentionRecheckCloud(ctx, opts.configFile, cloudConfig, loaded.Config, proof)
				})
			if err != nil {
				return fail(err)
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(receipt)
		}}
	resume.Flags().Bool("yes", false, "resume deletion from this exact existing reviewed retirement receipt")
	resume.Flags().String("cloud-config", cloudstorage.DefaultConfigPath, "existing cloud configuration for the exact retirement")
	parent.AddCommand(resume)
	var verifyCloudConfig string
	cloudVerify := &cobra.Command{Use: "check-capture-cloud <capture-id>", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		loaded, err := cloudstorage.LoadConfig(verifyCloudConfig)
		if err != nil || !loaded.Exists {
			return fmt.Errorf("current cloud configuration unavailable")
		}
		proof, err := backupstrategy.ReadNotesRecoveryProtection(backupstrategy.NotesRecoveryProtectionRoot(loaded.Config.StateDir), args[0])
		if err != nil {
			return err
		}
		if err := cloudstorage.RecheckNotesRecoveryProtection(cmd.Context(), loaded.Config, proof); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"status": "verified", "id": args[0], "publication_sha256": proof.PublicationSHA256})
	}}
	cloudVerify.Flags().StringVar(&verifyCloudConfig, "cloud-config", cloudstorage.DefaultConfigPath, "existing cloud owner configuration")
	parent.AddCommand(cloudVerify)
	var existingArchive, existingCloudConfig string
	protect := &cobra.Command{Use: "protect-capture <capture-id>", Args: cobra.ExactArgs(1),
		Short: "Verify a saved Notes publication in an exact existing cloud archive without uploading", RunE: func(cmd *cobra.Command, args []string) error {
			cc, err := resolveCommandContext(opts)
			if err != nil {
				return err
			}
			loaded, err := cloudstorage.LoadConfig(existingCloudConfig)
			if err != nil || !loaded.Exists {
				return fmt.Errorf("current cloud owner configuration unavailable")
			}
			proof, err := cloudstorage.RecordExistingNotesRecoveryProtection(cmd.Context(), loaded.Config,
				backupstrategy.NotesRecoveryPublicationRoot(cc.Config.DataDir), args[0], existingArchive)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"status": "cloud_protected", "id": args[0],
				"archive": proof.Archive, "publication_sha256": proof.PublicationSHA256})
		}}
	protect.Flags().StringVar(&existingArchive, "archive", "", "exact existing canonical Borg archive name; no upload or prune")
	protect.Flags().StringVar(&existingCloudConfig, "cloud-config", cloudstorage.DefaultConfigPath, "existing cloud owner configuration")
	parent.AddCommand(protect)
	return parent
}

func backupRetentionPublishSummary(cmd *cobra.Command, dataDir string, input backupstrategy.NotesRecoveryRetentionInput) {
	if err := backupstrategy.PublishRecoveryInventorySummary(cmd.Context(), dataDir, input); err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "Saved recovery inventory summary could not be refreshed; payload outcome is unchanged")
	}
}

func backupRetentionCohortRoot(applicationDataRoot string) (string, error) {
	if !filepath.IsAbs(applicationDataRoot) || filepath.Clean(applicationDataRoot) != applicationDataRoot || applicationDataRoot == "/" {
		return "", fmt.Errorf("a configured absolute LOOM_APPLICATION_DATA_BACKUP_ROOT is required to inspect Notes recovery references")
	}
	return filepath.Join(applicationDataRoot, "notes-recovery"), nil
}

func backupRetentionFilePins(dataDir, cohorts string, explicit []string, independent ...map[string]bool) (map[string]string, error) {
	protected := map[string]string{}
	for _, id := range explicit {
		protected[id] = "operator pin"
	}
	manifest, err := update.ReadUpdateManifest(filepath.Join(dataDir, "update", "active.yaml"))
	if err != nil {
		return nil, fmt.Errorf("current update/rollback identity unavailable: %w", err)
	}
	if manifest.Status != "succeeded" && manifest.Status != "rolled_back" {
		return nil, fmt.Errorf("update is active or unresolved: %s", manifest.Status)
	}
	// A completed service-only rollback rebuilds code; it does not restore the
	// pre-update database. Do not pin that dump forever after later backups.
	needsRestoreBackup := manifest.Rollback.RestoreRequired || !manifest.Rollback.ServiceOnlyPossible || manifest.Migrations.Pending != 0
	if manifest.BackupPath != "" && needsRestoreBackup {
		if filepath.Dir(manifest.BackupPath) == filepath.Join(dataDir, "backups", "operational") {
			protected[filepath.Base(manifest.BackupPath)] = "current update/rollback"
		} else if strings.Contains(filepath.Base(manifest.BackupPath), "operational-") {
			return nil, fmt.Errorf("update backup is outside the configured operational root")
		}
	}
	entries, err := os.ReadDir(cohorts)
	if err != nil {
		return nil, fmt.Errorf("Notes cohort inventory unavailable: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "notes-") {
			return nil, fmt.Errorf("unrecognized Notes cohort entry; inspect before retention")
		}
		raw, err := os.ReadFile(filepath.Join(cohorts, entry.Name(), "cohort.json"))
		var receipt struct {
			Schema    string `json:"schema"`
			PackageID string `json:"package_id"`
		}
		if err != nil || json.Unmarshal(raw, &receipt) != nil || (receipt.Schema != "loom.notes_recovery.v1" && receipt.Schema != "loom.notes_recovery.v2") || receipt.PackageID == "" {
			return nil, fmt.Errorf("Notes cohort reference is unreadable: %s", entry.Name())
		}
		if len(independent) > 0 && independent[0][entry.Name()] {
			continue
		}
		if protected[receipt.PackageID] == "" {
			protected[receipt.PackageID] = "retained Notes cohort " + entry.Name()
		}
	}
	return protected, nil
}

func backupRetentionIndependentNotesCopies(ctx context.Context, dataDir, cohorts, configFile, cloudConfig string, recheck bool) (map[string]bool, error) {
	independent := map[string]bool{}
	loaded, err := cloudstorage.LoadConfig(cloudConfig)
	if err != nil || !loaded.Exists || !loaded.Config.Enabled || loaded.Config.Snapshots.Backend != cloudstorage.SnapshotBackendBorg {
		return independent, nil // Missing evidence keeps every original pin.
	}
	input := backupstrategy.NotesRecoveryRetentionInput{Root: cohorts,
		PublicationRoot: backupstrategy.NotesRecoveryPublicationRoot(dataDir),
		ProtectionRoot:  backupstrategy.NotesRecoveryProtectionRoot(loaded.Config.StateDir), Repository: loaded.Config.Snapshots.Borg.Repository}
	entries, err := os.ReadDir(cohorts)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !backupstrategy.ValidNotesRecoveryID(entry.Name()) {
			continue
		}
		proof, err := backupstrategy.NotesRecoveryIndependentCopy(ctx, input, entry.Name())
		if err != nil {
			continue
		}
		if recheck {
			if err := backupRetentionRecheckCloud(ctx, configFile, cloudConfig, loaded.Config, proof); err != nil {
				return nil, fmt.Errorf("independent capture %s cloud recheck failed: %w", entry.Name(), err)
			}
		}
		independent[entry.Name()] = true
	}
	return independent, nil
}

func backupRetentionRecheckCloud(ctx context.Context, configFile, cloudConfig string, cfg cloudstorage.Config, proof backupstrategy.NotesRecoveryProtection) error {
	info, err := os.Lstat(cfg.StateDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("cloud owner directory unavailable")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cloud owner identity unavailable")
	}
	if uint32(os.Geteuid()) == owner.Uid {
		return cloudstorage.RecheckNotesRecoveryProtection(ctx, cfg, proof)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("cloud assurance requires its existing service owner")
	}
	account, err := user.LookupId(strconv.FormatUint(uint64(owner.Uid), 10))
	if err != nil {
		return fmt.Errorf("cloud service account unavailable: %w", err)
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	if configFile == "" {
		configFile = "/etc/loom/loom.env"
	}
	// Re-exec only the narrow verifier as the existing owner. It also owns the
	// remote lock and Borg cache/security state; root never changes their custody.
	command := exec.CommandContext(ctx, "runuser", "-u", account.Username, "--", binary, "--config", configFile,
		"backup", "retention", "check-capture-cloud", proof.Publication.ID, "--cloud-config", cloudConfig, "--json")
	raw, err := command.Output()
	if err != nil {
		return fmt.Errorf("cloud-service assurance failed; use check-capture-cloud as the configured cloud owner: %w", err)
	}
	var result struct {
		Status, ID        string
		PublicationSHA256 string `json:"publication_sha256"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Status != "verified" || result.ID != proof.Publication.ID || result.PublicationSHA256 != proof.PublicationSHA256 {
		return fmt.Errorf("cloud-service verification identity differs")
	}
	return nil
}
