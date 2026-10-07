package cli

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// shimScript is the zsh wrapper that lets `login` and `env load|use|clear`
// change the caller's shell (see gotcha 1/2/12). It lives next to the Go code
// so the binary is all a user needs to install.
//
//go:embed shim.zsh
var shimScript string

// shimText returns the wrapper with the contract number stamped in, so the
// marker `api doctor` checks can never drift from shimContract.
func shimText() string {
	return strings.ReplaceAll(shimScript, "@CONTRACT@", shimContract)
}

func (a *app) shellInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "shell-init [zsh]",
		Short: "Print the shell integration (login, env load/use/clear, completion)",
		Long: `Print the zsh wrapper that lets "api login" and "api env load|use|clear"
change your current shell, and that loads tab completion. Add to ~/.zshrc:

  eval "$(command api shell-init)"

The binary works without it for requests, but commands that set session
variables only print code; the wrapper evals it.`,
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"zsh"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && args[0] != "zsh" {
				return fmt.Errorf("unsupported shell %q (only zsh)", args[0])
			}
			_, err := fmt.Fprint(a.io.Out, shimText())
			return err
		},
	}
}
