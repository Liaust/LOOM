package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/localclient"
)

func addProjectRepositoryCommands(projectCommand *cobra.Command, opts *options) {
	repositoriesCommand := &cobra.Command{
		Use:     "repos",
		Aliases: []string{"repositories"},
		Short:   "Inspect authorized repository membership and observed state",
	}

	listOptions := localclient.ProjectRepositoryPageOptions{Limit: 50}
	listCommand := &cobra.Command{
		Use:   "list <project-ref>",
		Short: "List explicit repository members for a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ListProjectRepositories(ctx, commandCtx.CorrelationID, args[0], listOptions)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("project.repositories.list_failed", "projects", args[0], "Could not list project repositories.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, repository := range envelope.Data.Repositories {
					fmt.Fprintln(cmd.OutOrStdout(), repository.RepositoryID)
				}
				return nil
			}
			renderProjectRepositoryList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCommand.Flags().IntVar(&listOptions.Limit, "limit", 50, "maximum number of repository members to return")
	listCommand.Flags().StringVar(&listOptions.After, "after", "", "continue after this repository ID")
	repositoriesCommand.AddCommand(listCommand)

	inspectCommand := &cobra.Command{
		Use:   "inspect <project-ref> <repository-ref>",
		Short: "Inspect one explicit project repository member",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.InspectProjectRepository(ctx, commandCtx.CorrelationID, args[0], args[1])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("project.repositories.inspect_failed", "projects", args[1], "Could not inspect project repository.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Repository.RepositoryID)
				return nil
			}
			renderProjectRepositoryInspect(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	repositoriesCommand.AddCommand(inspectCommand)

	statusOptions := localclient.ProjectRepositoryPageOptions{Limit: 50}
	statusCommand := &cobra.Command{
		Use:   "status <project-ref>",
		Short: "Show bounded Git and .repo posture for project repositories",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetProjectRepositoryStatus(ctx, commandCtx.CorrelationID, args[0], statusOptions)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("project.repositories.status_failed", "projects", args[0], "Could not read project repository status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Observation.Posture)
				return nil
			}
			renderProjectRepositoryStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	statusCommand.Flags().IntVar(&statusOptions.Limit, "limit", 50, "maximum number of repository statuses to return")
	statusCommand.Flags().StringVar(&statusOptions.After, "after", "", "continue after this repository ID")
	repositoriesCommand.AddCommand(statusCommand)

	projectCommand.AddCommand(repositoriesCommand)
}

func renderProjectRepositoryList(cmd *cobra.Command, result localclient.ProjectRepositoryListResult) {
	renderProjectRepositorySurfaceHeader(cmd, result.Project, result.Source, result.ObservedAt)
	if len(result.Repositories) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Repositories: none explicitly registered")
		return
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "REPOSITORY ID\tKEY\tROLE\tPATH\tOBSERVATION")
	for _, repository := range result.Repositories {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", repository.RepositoryID, repository.Key, repository.Role, repository.RelativePath, repository.ObservationPosture)
	}
	_ = w.Flush()
	renderProjectRepositoryPage(cmd, result.Page)
}

func renderProjectRepositoryInspect(cmd *cobra.Command, result localclient.ProjectRepositoryInspectResult) {
	renderProjectRepositorySurfaceHeader(cmd, result.Project, result.Source, result.ObservedAt)
	repository := result.Repository
	fmt.Fprintf(cmd.OutOrStdout(), "Repository ID: %s\n", repository.RepositoryID)
	fmt.Fprintf(cmd.OutOrStdout(), "Owner project ID: %s\n", repository.RepositoryOwnerProjectID)
	fmt.Fprintf(cmd.OutOrStdout(), "Key: %s\n", repository.Key)
	fmt.Fprintf(cmd.OutOrStdout(), "Role: %s\n", repository.Role)
	fmt.Fprintf(cmd.OutOrStdout(), "Path: %s\n", repository.RelativePath)
	if repository.StateRoot != "" {
		fmt.Fprintf(cmd.OutOrStdout(), ".repo root: %s\n", repository.StateRoot)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Membership lifecycle: %s\n", repository.MembershipLifecycle)
	fmt.Fprintf(cmd.OutOrStdout(), "Repository lifecycle: %s\n", repository.RepositoryLifecycle)
	fmt.Fprintf(cmd.OutOrStdout(), "Freshness: %s", repository.ObservationPosture)
	if repository.ObservedAt != nil {
		fmt.Fprintf(cmd.OutOrStdout(), " at %s", repository.ObservedAt.UTC().Format(time.RFC3339))
	}
	if repository.ReasonCode != "" {
		fmt.Fprintf(cmd.OutOrStdout(), " (%s)", repository.ReasonCode)
	}
	fmt.Fprintln(cmd.OutOrStdout())
	renderProjectRepositoryDevelopmentState(cmd, repository)
	renderProjectRepositoryGit(cmd, repository)
}

func renderProjectRepositoryStatus(cmd *cobra.Command, result localclient.ProjectRepositoryStatusResult) {
	renderProjectRepositorySurfaceHeader(cmd, result.Project, result.Source, result.ObservedAt)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s (members=%d observed=%d not_observed=%d remote_unavailable=%d)\n",
		result.Observation.Posture,
		result.Observation.MemberCount,
		result.Observation.Observed,
		result.Observation.NotObserved,
		result.Observation.RemoteUnavailable,
	)
	if len(result.Repositories) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Repository status: no explicit members")
		return
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "REPOSITORY ID\tROLE\tPATH\tFRESHNESS\tGIT\t.REPO")
	for _, repository := range result.Repositories {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			repository.RepositoryID,
			repository.Role,
			repository.RelativePath,
			repository.ObservationPosture,
			projectRepositoryGitPosture(repository),
			repository.DevelopmentState.Posture,
		)
	}
	_ = w.Flush()
	renderProjectRepositoryPage(cmd, result.Page)
}

func renderProjectRepositorySurfaceHeader(cmd *cobra.Command, project localclient.ProjectRepositoryProject, source localclient.ProjectRepositorySource, observedAt time.Time) {
	fmt.Fprintf(cmd.OutOrStdout(), "Project: %s (%s)\n", firstNonEmptyCLI(project.Slug, project.ProjectID), project.ProjectID)
	fmt.Fprintf(cmd.OutOrStdout(), "Lifecycle: %s\n", project.Lifecycle)
	fmt.Fprintf(cmd.OutOrStdout(), "Source: %s", source.Posture)
	if source.ProjectContractSchemaVersion != "" || source.ReposContractSchemaVersion != "" {
		fmt.Fprintf(cmd.OutOrStdout(), " project=%s repos=%s revision=%d", source.ProjectContractSchemaVersion, source.ReposContractSchemaVersion, source.SourceRevision)
	}
	fmt.Fprintln(cmd.OutOrStdout())
	if !observedAt.IsZero() {
		fmt.Fprintf(cmd.OutOrStdout(), "Observed: %s\n", observedAt.UTC().Format(time.RFC3339))
	}
	if strings.EqualFold(project.Lifecycle, "archived") {
		fmt.Fprintln(cmd.OutOrStdout(), "Access: read-only archived repository state; member mutation actions are unavailable")
	}
}

func renderProjectRepositoryDevelopmentState(cmd *cobra.Command, repository localclient.ProjectRepositoryItem) {
	fmt.Fprintf(cmd.OutOrStdout(), ".repo posture: %s", repository.DevelopmentState.Posture)
	if repository.DevelopmentState.RelativePath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), " (%s)", repository.DevelopmentState.RelativePath)
	}
	if repository.DevelopmentState.ReasonCode != "" {
		fmt.Fprintf(cmd.OutOrStdout(), " reason=%s", repository.DevelopmentState.ReasonCode)
	}
	fmt.Fprintln(cmd.OutOrStdout())
}

func renderProjectRepositoryGit(cmd *cobra.Command, repository localclient.ProjectRepositoryItem) {
	git := repository.Git
	if git == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "Git posture: not observed")
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Git posture: %s\n", projectRepositoryGitPosture(repository))
	fmt.Fprintf(cmd.OutOrStdout(), "Git HEAD: %s (%s)\n", firstNonEmptyCLI(git.Head, "-"), git.HeadPosture)
	fmt.Fprintf(cmd.OutOrStdout(), "Git default branch: %s (%s)\n", firstNonEmptyCLI(git.DefaultBranch, "-"), git.DefaultBranchPosture)
	if git.Upstream != "" || git.AheadBehindObserved {
		fmt.Fprintf(cmd.OutOrStdout(), "Git upstream: %s ahead=%d behind=%d observed=%t\n", firstNonEmptyCLI(git.Upstream, "-"), git.Ahead, git.Behind, git.AheadBehindObserved)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Git worktree: %t canonical_project_state=false\n", git.Worktree)
}

func projectRepositoryGitPosture(repository localclient.ProjectRepositoryItem) string {
	git := repository.Git
	if git == nil {
		return "not_observed"
	}
	branch := firstNonEmptyCLI(git.CurrentBranch, "detached")
	dirty := "clean"
	if git.Dirty.Dirty {
		dirty = "dirty"
	}
	return fmt.Sprintf("%s/%s", branch, dirty)
}

func renderProjectRepositoryPage(cmd *cobra.Command, page localclient.ProjectRepositoryPage) {
	if page.HasMore {
		fmt.Fprintf(cmd.OutOrStdout(), "Next page: --after %s --limit %d\n", page.NextAfter, page.Limit)
	}
}
