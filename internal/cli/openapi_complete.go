package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

// verbPathCompletion completes the first argument of `api get|post|...`: the
// operations of the cached spec for this METHOD, then recently used paths.
// Offline and fast: one local file.
func (a *app) verbPathCompletion(method string) func(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		history, dir := a.pathCompletion(cmd, args, toComplete)
		if len(args) > 0 {
			return history, dir
		}
		const maxCandidates = 300
		var out []cobra.Completion
		seen := map[string]bool{}
		if idx := a.loadOpenAPI(); idx != nil {
			for _, op := range idx.Operations {
				if op.Method != method || !strings.HasPrefix(op.Path, toComplete) || seen[op.Path] {
					continue
				}
				seen[op.Path] = true
				if op.Summary != "" {
					out = append(out, cobra.CompletionWithDesc(op.Path, op.Summary))
				} else {
					out = append(out, op.Path)
				}
				if len(out) >= maxCandidates {
					break
				}
			}
		}
		for _, h := range history {
			if !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
		return out, dir
	}
}
