package cli

import (
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// viewerPresets are the bare names --viewer expands to a useful command line.
var viewerPresets = []string{"nvim", "vim", "jless", "fx"}

// viewerTTY says whether stdout is a terminal: a viewer opens only on one, through a
// pipe the body is printed as usual. A variable so a test can stand in for a terminal.
var viewerTTY = isTerminal

// viewerArgv turns the --viewer value into a command line. A bare preset name gets
// its usual arguments (nvim/vim: read-only, from stdin, folded by indentation, JSON
// highlighting only when the text is JSON); anything else is split into words and
// run as typed, without a shell. nil means "no viewer".
func viewerArgv(spec string, isJSON bool) []string {
	words := splitWords(spec)
	if len(words) != 1 {
		return words
	}
	switch words[0] {
	case "nvim", "vim":
		if isJSON {
			return []string{words[0], "-R", "-c", "set ft=json foldmethod=indent foldlevel=99", "-"}
		}
		return []string{words[0], "-R", "-"}
	}
	return words
}

// bindViewerFlag adds --viewer. Only the commands whose job is to look at one
// result take it (get, post, put, patch, del, form, gql, openapi pick, again), not
// the ones that print several (run, diff) or stream (watch, subscriptions). The
// viewer is chosen on the command line, never by a config file, so a cloned repo
// cannot pick a program to run, and it opens only on a terminal, never in a pipe.
func (a *app) bindViewerFlag(cmd *cobra.Command, o *reqOpts) {
	cmd.Flags().StringVar(&o.viewer, "viewer", "", "show the result in an interactive viewer instead of printing it: nvim, jless, fx or any command that reads stdin")
	_ = cmd.RegisterFlagCompletionFunc("viewer", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		var found []string
		for _, name := range viewerPresets {
			if _, err := exec.LookPath(name); err == nil {
				found = append(found, name)
			}
		}
		return found, cobra.ShellCompDirectiveNoFileComp
	})
}

// viewerName is what a warning calls the viewer.
func viewerName(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return strings.TrimSpace(argv[0])
}
