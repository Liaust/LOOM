package loomcli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/repostate"
)

type repositoryStateIdentityFlags struct {
	repositoryID   string
	repositoryName string
	aliases        []string
	role           string
	purpose        string
	topics         []string
	ownerProjectID string
	ownerSlug      string
	stableBranch   string
	defaultBranch  string
}

type repositoryStatePlanFlags struct {
	path       string
	pack       string
	apply      bool
	yes        bool
	planDigest string
	identity   repositoryStateIdentityFlags
}

func newRepositoryStateCommand(opts *options) *cobra.Command {
	command := &cobra.Command{
		Use:     "repo-state",
		Aliases: []string{"repository-state"},
		Short:   "Validate or explicitly initialize local repository development state",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newRepositoryStateValidateCommand(opts))
	command.AddCommand(newRepositoryStatePlanCommand(opts, repostate.ChangeInitialize))
	command.AddCommand(newRepositoryStatePlanCommand(opts, repostate.ChangeMigrate))
	return command
}

func newRepositoryStateValidateCommand(opts *options) *cobra.Command {
	path := ""
	identity := repositoryStateIdentityFlags{}
	command := &cobra.Command{
		Use:   "validate",
		Short: "Validate local .repo state without changing Git or files",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := explicitLocalRepositoryPath(path)
			if err != nil {
				return err
			}
			membership, err := optionalRepositoryMembership(identity)
			if err != nil {
				return err
			}
			extraction := (repostate.Extractor{}).Extract(cmd.Context(), repostate.ExtractInput{
				RepositoryRoot: root,
				Membership:     membership,
			})
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(extraction)
			}
			renderRepositoryStateValidation(cmd, extraction)
			return nil
		},
	}
	command.Flags().StringVar(&path, "path", "", "explicit local Git repository root")
	addRepositoryStateValidationFlags(command, &identity)
	return command
}

func newRepositoryStatePlanCommand(opts *options, kind repostate.ChangeKind) *cobra.Command {
	flags := repositoryStatePlanFlags{}
	use := "init"
	short := "Plan or explicitly initialize .repo from a project-local development pack"
	if kind == repostate.ChangeMigrate {
		use = "migration-plan"
		short = "Plan or explicitly create .repo from a reviewed .project transformation"
	}
	command := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flags.yes && !flags.apply {
				return errorsForRepositoryStatePlan("--yes requires --apply")
			}
			if flags.planDigest != "" && !flags.apply {
				return errorsForRepositoryStatePlan("--plan-digest requires --apply")
			}
			options, err := repositoryStateChangeOptions(flags)
			if err != nil {
				return err
			}
			if flags.apply {
				result, applyErr := repostate.ApplyRepositoryStateChange(cmd.Context(), kind, options, strings.TrimSpace(flags.planDigest), flags.yes)
				if opts.jsonOutput {
					if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
						return err
					}
				} else if result.Plan.SchemaVersion != "" {
					renderRepositoryStateApply(cmd, result)
				}
				return applyErr
			}
			var plan repostate.ChangePlan
			if kind == repostate.ChangeInitialize {
				plan, err = repostate.PlanInitialization(cmd.Context(), options)
			} else {
				plan, err = repostate.PlanProjectStateMigration(cmd.Context(), options)
			}
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
			}
			renderRepositoryStatePlan(cmd, plan)
			return nil
		},
	}
	command.Flags().StringVar(&flags.path, "path", "", "explicit local Git repository root")
	command.Flags().StringVar(&flags.pack, "pack", "", "explicit project-local .loom/agent-packs/repo-development root")
	command.Flags().BoolVar(&flags.apply, "apply", false, "create the exact reviewed plan without staging or committing")
	command.Flags().StringVar(&flags.planDigest, "plan-digest", "", "exact digest printed by the reviewed dry-run plan")
	command.Flags().BoolVar(&flags.yes, "yes", false, "confirm the digest-bound --apply operation")
	addRepositoryStateIdentityFlags(command, &flags.identity)
	return command
}

func addRepositoryStateValidationFlags(command *cobra.Command, identity *repositoryStateIdentityFlags) {
	command.Flags().StringVar(&identity.repositoryID, "repository-id", "", "authoritative typed repository ID")
	command.Flags().StringVar(&identity.role, "role", "", "authoritative owning membership role: primary or component")
	command.Flags().StringVar(&identity.ownerProjectID, "owner-project-id", "", "authoritative typed owning LOOM project ID")
	command.Flags().StringVar(&identity.ownerSlug, "owner-project-slug", "", "authoritative owning LOOM project slug")
}

func addRepositoryStateIdentityFlags(command *cobra.Command, identity *repositoryStateIdentityFlags) {
	command.Flags().StringVar(&identity.repositoryID, "repository-id", "", "typed repository ID")
	command.Flags().StringVar(&identity.repositoryName, "repository-name", "", "human repository name")
	command.Flags().StringSliceVar(&identity.aliases, "alias", nil, "portable repository alias (repeatable or comma-separated)")
	command.Flags().StringVar(&identity.role, "role", "", "owning membership role: primary or component")
	command.Flags().StringVar(&identity.purpose, "purpose", "", "short repository purpose")
	command.Flags().StringSliceVar(&identity.topics, "topic", nil, "lowercase portable topic (repeatable or comma-separated)")
	command.Flags().StringVar(&identity.ownerProjectID, "owner-project-id", "", "typed owning LOOM project ID")
	command.Flags().StringVar(&identity.ownerSlug, "owner-project-slug", "", "owning LOOM project slug")
	command.Flags().StringVar(&identity.stableBranch, "stable-branch", "", "stable integration branch when known")
	command.Flags().StringVar(&identity.defaultBranch, "default-branch", "", "Git default branch when known")
}

func repositoryStateChangeOptions(flags repositoryStatePlanFlags) (repostate.ChangePlanOptions, error) {
	root, err := explicitLocalRepositoryPath(flags.path)
	if err != nil {
		return repostate.ChangePlanOptions{}, err
	}
	pack, err := explicitLocalPath(flags.pack, "--pack")
	if err != nil {
		return repostate.ChangePlanOptions{}, err
	}
	manifest, err := repositoryStateManifest(flags.identity)
	if err != nil {
		return repostate.ChangePlanOptions{}, err
	}
	return repostate.ChangePlanOptions{RepositoryRoot: root, PackRoot: pack, Manifest: manifest}, nil
}

func repositoryStateManifest(identity repositoryStateIdentityFlags) (repostate.RepositoryManifest, error) {
	required := []struct {
		name  string
		value string
	}{
		{"--repository-id", identity.repositoryID},
		{"--repository-name", identity.repositoryName},
		{"--role", identity.role},
		{"--purpose", identity.purpose},
		{"--owner-project-id", identity.ownerProjectID},
		{"--owner-project-slug", identity.ownerSlug},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return repostate.RepositoryManifest{}, fmt.Errorf("%s is required", field.name)
		}
	}
	aliases := normalizedFlagValues(identity.aliases)
	topics := normalizedFlagValues(identity.topics)
	manifest := repostate.RepositoryManifest{
		Kind:          repostate.RepositoryManifestKind,
		SchemaVersion: repostate.RepositorySchemaVersion,
		Repository: repostate.RepositoryIdentity{
			ID:      strings.TrimSpace(identity.repositoryID),
			Name:    strings.TrimSpace(identity.repositoryName),
			Aliases: aliases,
			Role:    repostate.RepositoryRole(strings.TrimSpace(identity.role)),
			Purpose: strings.TrimSpace(identity.purpose),
			Topics:  topics,
		},
		OwnerProject: repostate.OwnerProjectBacklink{
			ID:   strings.TrimSpace(identity.ownerProjectID),
			Slug: strings.TrimSpace(identity.ownerSlug),
		},
		Branches: repostate.RepositoryBranches{
			Stable:  strings.TrimSpace(identity.stableBranch),
			Default: strings.TrimSpace(identity.defaultBranch),
		},
		Source: repostate.RepositorySourcePolicy{Tracking: "git", StateRoot: repostate.StateRoot},
	}
	return manifest, nil
}

func optionalRepositoryMembership(identity repositoryStateIdentityFlags) (*repostate.AuthoritativeMembership, error) {
	values := []string{identity.repositoryID, identity.ownerProjectID, identity.ownerSlug, identity.role}
	provided := 0
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			provided++
		}
	}
	if provided == 0 {
		return nil, nil
	}
	if provided != len(values) {
		return nil, errorsForRepositoryStatePlan("validation membership requires --repository-id, --owner-project-id, --owner-project-slug, and --role together")
	}
	return &repostate.AuthoritativeMembership{
		Resolved:         true,
		RepositoryID:     strings.TrimSpace(identity.repositoryID),
		OwnerProjectID:   strings.TrimSpace(identity.ownerProjectID),
		OwnerProjectSlug: strings.TrimSpace(identity.ownerSlug),
		OwnerRole:        repostate.RepositoryRole(strings.TrimSpace(identity.role)),
		StateRoot:        repostate.StateRoot,
	}, nil
}

func explicitLocalRepositoryPath(value string) (string, error) {
	return explicitLocalPath(value, "--path")
}

func explicitLocalPath(value, flag string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required; no current-directory default is used", flag)
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func normalizedFlagValues(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			normalized = append(normalized, value)
		}
	}
	sort.Strings(normalized)
	return normalized
}

func renderRepositoryStateValidation(command *cobra.Command, extraction repostate.Extraction) {
	fmt.Fprintf(command.OutOrStdout(), "Repository state: %s\n", extraction.TrackingStatus)
	fmt.Fprintf(command.OutOrStdout(), "Git tracked state: %s\n", extraction.Freshness.TrackedState)
	if len(extraction.Validation.Issues) == 0 {
		fmt.Fprintln(command.OutOrStdout(), "Issues: none")
		return
	}
	fmt.Fprintln(command.OutOrStdout(), "Issues:")
	for _, issue := range extraction.Validation.Issues {
		fmt.Fprintf(command.OutOrStdout(), "- %s %s: %s\n", issue.Code, issue.Path, issue.Detail)
	}
}

func renderRepositoryStatePlan(command *cobra.Command, plan repostate.ChangePlan) {
	fmt.Fprintf(command.OutOrStdout(), "Repository state %s plan\n", plan.Kind)
	fmt.Fprintf(command.OutOrStdout(), "Path: %s\n", plan.RepositoryRoot)
	fmt.Fprintf(command.OutOrStdout(), "Plan digest: %s\n", plan.Digest)
	fmt.Fprintf(command.OutOrStdout(), "Git: clean=%t allowed=%t head=%s\n", plan.Git.Clean, plan.Git.Allowed, dashIfEmpty(plan.Git.HeadCommit))
	fmt.Fprintf(command.OutOrStdout(), "Files: preserved=%d renamed=%d split=%d archived=%d skipped=%d conflicting=%d pending=%d\n",
		plan.Counts.Preserved, plan.Counts.Renamed, plan.Counts.Split, plan.Counts.Archived, plan.Counts.Skipped, plan.Counts.Conflicting, plan.Counts.Pending)
	for _, file := range plan.Files {
		if len(file.Targets) == 0 {
			fmt.Fprintf(command.OutOrStdout(), "- %s %s [%s] %s\n", file.Disposition, file.Source, file.Status, file.Reason)
			continue
		}
		for _, target := range file.Targets {
			fmt.Fprintf(command.OutOrStdout(), "- %s %s -> %s [%s]", file.Disposition, file.Source, target.Path, target.Status)
			if target.Reason != "" {
				fmt.Fprintf(command.OutOrStdout(), " %s", target.Reason)
			}
			fmt.Fprintln(command.OutOrStdout())
		}
	}
	if !plan.ApplyAllowed {
		fmt.Fprintln(command.OutOrStdout(), "Apply: blocked until conflicts and Git posture are reconciled")
		return
	}
	fmt.Fprintf(command.OutOrStdout(), "Apply: loom repo-state %s --path <same-path> --pack <same-pack> <same-identity-flags> --apply --plan-digest %s --yes\n", repositoryStatePlanCommandName(plan.Kind), plan.Digest)
}

func renderRepositoryStateApply(command *cobra.Command, result repostate.ApplyResult) {
	renderRepositoryStatePlan(command, result.Plan)
	fmt.Fprintf(command.OutOrStdout(), "Applied: %t\n", result.Applied)
	fmt.Fprintf(command.OutOrStdout(), "Created or replaced files: %d\n", len(result.Written))
	fmt.Fprintf(command.OutOrStdout(), "Git staged: %t\n", result.Staged)
	fmt.Fprintf(command.OutOrStdout(), "Git commit created: %t\n", result.CommitCreated)
}

func repositoryStatePlanCommandName(kind repostate.ChangeKind) string {
	if kind == repostate.ChangeMigrate {
		return "migration-plan"
	}
	return "init"
}

func errorsForRepositoryStatePlan(message string) error {
	return fmt.Errorf("repository state command: %s", message)
}
