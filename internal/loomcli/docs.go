package loomcli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/loomdocs"
	"loom.local/loom/internal/version"
)

func newDocsCommand(opts *options) *cobra.Command {
	docsDir := ""
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "Search and inspect release-matched LOOM documentation",
	}
	cmd.PersistentFlags().StringVar(&docsDir, "docs-dir", "", "documentation root override")
	cmd.AddCommand(newDocsStatusCommand(opts, &docsDir))
	cmd.AddCommand(newDocsSearchCommand(opts, &docsDir))
	cmd.AddCommand(newDocsInspectCommand(opts, &docsDir))
	cmd.AddCommand(newDocsRelatedCommand(opts, &docsDir))
	return cmd
}

func newDocsStatusCommand(opts *options, docsDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show local documentation corpus health",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			corpus, err := loadDocsCorpus(*docsDir)
			if err != nil {
				return err
			}
			status := corpus.Status()
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "LOOM documentation: %s\n", status.Root.Path)
			fmt.Fprintf(cmd.OutOrStdout(), "Source: %s\n", status.Root.Source)
			fmt.Fprintf(cmd.OutOrStdout(), "LOOM version: %s\n", status.Root.LoomVersion)
			fmt.Fprintf(cmd.OutOrStdout(), "Release matched: %t\n", status.Root.ReleaseMatched)
			fmt.Fprintf(cmd.OutOrStdout(), "Documents: %d\n", status.DocumentCount)
			fmt.Fprintf(cmd.OutOrStdout(), "Corpus hash: %s\n", status.CorpusHash)
			fmt.Fprintf(cmd.OutOrStdout(), "Parse errors: %d\n", status.ParseErrors)
			fmt.Fprintf(cmd.OutOrStdout(), "Duplicate titles: %d\n", status.DuplicateTitles)
			fmt.Fprintf(cmd.OutOrStdout(), "Duplicate aliases: %d\n", status.DuplicateAliases)
			fmt.Fprintf(cmd.OutOrStdout(), "Unresolved wikilinks: %d\n", status.UnresolvedWikilinks)
			return nil
		},
	}
}

func newDocsSearchCommand(opts *options, docsDir *string) *cobra.Command {
	filters := loomdocs.SearchFilters{}
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search LOOM documentation locally",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			corpus, err := loadDocsCorpus(*docsDir)
			if err != nil {
				return err
			}
			response, err := loomdocs.Search(corpus, args[0], filters)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(response)
			}
			if len(response.Results) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No matching documentation found.")
				return nil
			}
			for _, result := range response.Results {
				fmt.Fprintf(cmd.OutOrStdout(), "%d. %s\n", result.Rank, result.Title)
				fmt.Fprintf(cmd.OutOrStdout(), "   path: %s  status: %s\n", result.RelativePath, result.Status)
				if len(result.Tags) > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "   tags: %s\n", strings.Join(result.Tags, ", "))
				}
				fmt.Fprintf(cmd.OutOrStdout(), "   match: %s\n", strings.Join(result.Matches, ", "))
				if result.Snippet != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "   %s\n", result.Snippet)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&filters.Tag, "tag", "", "filter by exact tag")
	cmd.Flags().StringVar(&filters.Audience, "audience", "", "filter by exact audience")
	cmd.Flags().StringVar(&filters.Status, "status", "", "filter by exact document status")
	cmd.Flags().IntVar(&filters.Limit, "limit", 10, "maximum number of results (1-100)")
	return cmd
}

func newDocsInspectCommand(opts *options, docsDir *string) *cobra.Command {
	heading := ""
	maxChars := 12000
	cmd := &cobra.Command{
		Use:   "inspect <title-alias-or-path>",
		Short: "Inspect one bounded documentation page or heading",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			corpus, err := loadDocsCorpus(*docsDir)
			if err != nil {
				return err
			}
			inspection, err := loomdocs.Inspect(corpus, args[0], heading, maxChars)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(inspection)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s (%s, %s)\n", inspection.Title, inspection.RelativePath, inspection.Status)
			if inspection.Heading != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Heading: %s\n", inspection.Heading)
			}
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprint(cmd.OutOrStdout(), inspection.Content)
			if !strings.HasSuffix(inspection.Content, "\n") {
				fmt.Fprintln(cmd.OutOrStdout())
			}
			if inspection.Truncated {
				fmt.Fprintf(cmd.OutOrStdout(), "\n[truncated at %d characters]\n", maxChars)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&heading, "heading", "", "inspect one exact heading")
	cmd.Flags().IntVar(&maxChars, "max-chars", 12000, "maximum Unicode characters to return")
	return cmd
}

func newDocsRelatedCommand(opts *options, docsDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "related <title-alias-or-path>",
		Short: "List outgoing and incoming documentation links",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			corpus, err := loadDocsCorpus(*docsDir)
			if err != nil {
				return err
			}
			related, err := loomdocs.Related(corpus, args[0])
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					Root    loomdocs.ResolvedRoot      `json:"root"`
					Target  string                     `json:"target"`
					Related []loomdocs.RelatedDocument `json:"related"`
				}{Root: corpus.Root, Target: args[0], Related: related})
			}
			if len(related) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No related documentation found.")
				return nil
			}
			for _, document := range related {
				fmt.Fprintf(cmd.OutOrStdout(), "- %s (%s, %s)\n", document.Title, document.RelativePath, document.Status)
			}
			return nil
		},
	}
}

func loadDocsCorpus(explicit string) (*loomdocs.Corpus, error) {
	root, err := loomdocs.ResolveRoot(loomdocs.ResolveOptions{ExplicitPath: explicit, LoomVersion: version.Current().Version})
	if err != nil {
		return nil, err
	}
	return loomdocs.Load(root)
}
