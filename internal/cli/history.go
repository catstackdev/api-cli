package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"cybercat/api/internal/history"
	"cybercat/api/internal/ui"

	"github.com/spf13/cobra"
)

func (a *app) historyCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "history",
		Short: "List recent requests (method, path, filter: never bodies or tokens)",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			entries := history.Entries(limit)
			if len(entries) == 0 {
				a.say("  (no history yet)")
				return
			}
			rows := make([][]string, len(entries))
			for i, e := range entries {
				rows[i] = []string{strconv.Itoa(i + 1), e.Method, e.Path, e.Filter}
			}
			ui.Table(a.io.Out, []string{"#", "METHOD", "PATH", "FILTER"}, rows, a.colorOut())
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "how many entries to show")

	cmd.AddCommand(&cobra.Command{
		Use:   "clear",
		Short: "Delete the history file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if p := history.Path(); p != "" {
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
			a.say("✓ history cleared")
			return nil
		},
	})
	return cmd
}

func (a *app) againCmd() *cobra.Command {
	o := &reqOpts{}
	var pick bool
	cmd := &cobra.Command{
		Use:   "again [N] [.filter] [--pick]",
		Short: "Repeat the Nth most recent GET request (default: the last one)",
		Long: `Repeats a request from the history. Only GET requests can be repeated: bodies
and tokens are never stored, so a POST would be sent without its body. The
current env and token are used, so after switching envs this runs there. A
.filter argument replaces the stored one. All the usual flags (-i, -c, --dry,
...) work. --pick chooses the entry with fzf.`,
		Example: `  api again            # repeat the last request
  api again 3 .data.id # the third most recent, with a different filter
  api again --pick`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			idx, filter := 1, ""
			for _, x := range args {
				if isFilter(x) {
					filter = x
				} else if n, err := strconv.Atoi(x); err == nil && n >= 1 {
					idx = n
				} else {
					return fmt.Errorf("unexpected argument %q (want a number or a .filter)", x)
				}
			}
			entries := history.Entries(0)
			if len(entries) == 0 {
				return &ExitError{Code: exitGeneric, Msg: "✗ no history yet"}
			}
			if pick {
				n, err := a.pickHistory(entries)
				if err != nil {
					return err
				}
				idx = n
			}
			if idx > len(entries) {
				return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ only %d entries in the history", len(entries))}
			}
			e := entries[idx-1]
			if e.Method != "GET" {
				return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ %s %s: only GET can be repeated (bodies and tokens are never stored); run it by hand", e.Method, e.Path)}
			}
			if filter == "" {
				filter = e.Filter
			}
			if filter == "" {
				filter = "."
			}
			spec, err := a.newSpec("GET", e.Path, o)
			if err != nil {
				return err
			}
			a.say(fmt.Sprintf("→ GET %s %s", e.Path, filter))
			return a.dispatch(cmd.Context(), spec, o, filter, e.Path, nil)
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			var out []cobra.Completion
			for i, e := range history.Entries(15) {
				out = append(out, cobra.CompletionWithDesc(strconv.Itoa(i+1), strings.TrimSpace(e.Method+" "+e.Path+" "+e.Filter)))
			}
			return out, cobra.ShellCompDirectiveNoFileComp
		},
	}
	a.bindFlags(o, cmd, false)
	cmd.Flags().BoolVar(&pick, "pick", false, "choose the entry with fzf")
	return cmd
}

// pickHistory runs fzf over the entries and returns the chosen 1-based index.
func (a *app) pickHistory(entries []history.Entry) (int, error) {
	lines := make([]string, len(entries))
	for i, e := range entries {
		lines[i] = strings.TrimSpace(e.Method + " " + e.Path + " " + e.Filter)
	}
	return a.fzfPick("again", lines)
}
