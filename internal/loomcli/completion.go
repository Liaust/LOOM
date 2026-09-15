package loomcli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newCompletionCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion <bash|zsh|fish|powershell>",
		Short: "Generate shell completion scripts",
		Args:  cobra.ExactValidArgs(1),
		ValidArgs: []string{
			"bash",
			"zsh",
			"fish",
			"powershell",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, "", err)
			}
			root := cmd.Root()
			switch args[0] {
			case "bash":
				return root.GenBashCompletion(cmd.OutOrStdout())
			case "zsh":
				return root.GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				return root.GenFishCompletion(cmd.OutOrStdout(), true)
			case "powershell":
				return root.GenPowerShellCompletion(cmd.OutOrStdout())
			default:
				return fmt.Errorf("unsupported completion shell: %s", args[0])
			}
		},
	}
	return cmd
}
