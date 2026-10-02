package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/MohammadMD1383/modharbor/internal/ui"
	"github.com/MohammadMD1383/modharbor/internal/version"
	"github.com/spf13/cobra"
)

// newVersionCmd prints detailed build information.
func newVersionCmd() *cobra.Command {
	var short bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if short {
				fmt.Println(version.Version)
				return nil
			}
			if flagJSON {
				return printJSON(map[string]string{
					"version": version.Version,
					"commit":  version.Commit,
					"date":    version.Date,
					"go":      version.GoVersion(),
				})
			}
			ui.Line(version.Banner())
			return nil
		},
	}
	cmd.Flags().BoolVar(&short, "short", false, "print only the version number")
	return cmd
}

// newCompletionCmd generates shell completion scripts.
func newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion <shell>",
		Short: "Generate a shell completion script",
		Long: strings.TrimSpace(`
Generate a completion script for your shell.

  bash        source <(modharbor completion bash)
  zsh         modharbor completion zsh > "${fpath[1]}/_modharbor"
  fish        modharbor completion fish > ~/.config/fish/completions/modharbor.fish
  powershell  modharbor completion powershell | Out-String | Invoke-Expression
`),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		Args:      cobra.ExactValidArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := cmd.Root()
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(os.Stdout, true)
			case "zsh":
				return root.GenZshCompletion(os.Stdout)
			case "fish":
				return root.GenFishCompletion(os.Stdout, true)
			case "powershell":
				return root.GenPowerShellCompletionWithDesc(os.Stdout)
			default:
				return fail("unsupported shell %q", args[0])
			}
		},
	}
	return cmd
}
