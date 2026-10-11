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
	var all, runs bool
	cmd := &cobra.Command{
		Use:   "history",
		Short: "List recent requests to the current server (method, path, filter: never bodies or tokens)",
		Long: `Lists what you requested from the server the shell points at. The history file
is shared by every project, so other servers' requests are hidden unless you
pass -a/--all (which also shows older entries that were saved without a server).
Only requests that succeeded (status below 400) are remembered. The numbers are
the ones ` + "`api again N`" + ` uses; a long path continues on the next line.`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			entries := a.historyEntries(all)
			if len(entries) == 0 {
				a.say("  (" + a.emptyHistoryMessage(all) + ")")
				return
			}
			// numbered from the whole list, so a number is the same with or without --runs
			nums := make([]int, len(entries))
			for i := range entries {
				nums[i] = i + 1
			}
			if runs {
				if entries, nums = savedRuns(entries); len(entries) == 0 {
					a.say("  (no saved requests: api openapi pick and api gql --pick make them)")
					return
				}
			}
			if limit > 0 && len(entries) > limit {
				entries, nums = entries[:limit], nums[:limit]
			}
			header := []string{"#", "METHOD", "PATH", "FILTER"}
			rows := make([][]string, len(entries))
			for i, e := range entries {
				rows[i] = []string{strconv.Itoa(nums[i]), e.Method, e.Path, e.Filter}
				if all {
					rows[i] = append(rows[i], orDefault(e.Origin, "(no server)"))
				}
			}
			if all {
				header = append(header, "SERVER")
			} else if n := len(history.Entries(0)) - len(history.ForOrigin(a.currentOrigin(), 0)); n > 0 {
				defer a.say(fmt.Sprintf("  (%d more for other servers or saved without one: api history -a)", n))
			}
			ui.TableWrap(a.io.Out, header, rows, a.colorOut(), 2, ui.TermWidth(a.io.Out)) // a long path continues on the next line
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "how many entries to show")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "also show other servers' requests and entries saved without a server")
	cmd.Flags().BoolVar(&runs, "runs", false, "only saved requests (the ones again --edit accepts); numbers stay those of the full list")

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
	var pick, all, edit, yes, fresh bool
	cmd := &cobra.Command{
		Use:   "again [N] [.filter] [--pick] [--all] [--edit] [--default]",
		Short: "Repeat the Nth most recent request: a GET, or a saved pick (default: the last one)",
		Long: `Repeats a request from the history. A GET is repeated from its path. A request
you picked with ` + "`api openapi pick` or `api gql --pick`" + ` is kept as a file under
.api/requests/ and the history points at it (RUN): it runs again from the project
it was saved in, and asks again before anything that changes data (--yes skips
that); --edit changes it first (your saved edits are kept), and --default starts
that edit from the skeleton the pick generated. Anything else (a POST typed by hand) is not
repeated: bodies and tokens are never stored. The
current env and token are used. The numbers are those of ` + "`api history`" + `, so only
requests to the server the shell points at are listed, and an entry for another
server is refused rather than sent to this one (--all lists them to see why).
A .filter argument replaces the stored one. All the usual flags (-i, -c,
--dry, ...) work. --pick chooses the entry with fzf.`,
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
			entries := a.historyEntries(all)
			if len(entries) == 0 {
				return &ExitError{Code: exitGeneric, Msg: "✗ " + a.emptyHistoryMessage(all)}
			}
			if pick {
				// --edit / --default only apply to saved requests: offer just those,
				// keeping the numbers `api history` shows
				choices, nums := entries, []int(nil)
				if edit || fresh {
					if choices, nums = savedRuns(entries); len(choices) == 0 {
						return &ExitError{Code: exitGeneric, Msg: "✗ no saved requests in the history to edit (api openapi pick and api gql --pick make them)"}
					}
				}
				n, err := a.pickHistory(choices)
				if err != nil {
					return err
				}
				idx = n
				if nums != nil {
					idx = nums[n-1]
				}
			}
			if idx > len(entries) {
				return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ only %d entries in the history", len(entries))}
			}
			e := entries[idx-1]
			if e.Method == history.RunMethod {
				if filter == "" {
					filter = e.Filter
				}
				return a.againRun(cmd, e, againOpts{filter: filter, edit: edit, yes: yes, fresh: fresh}, o)
			}
			if edit || fresh {
				return &ExitError{Code: exitUsage, Msg: "✗ --edit / --default apply to saved requests (api openapi pick, api gql --pick); a plain GET has nothing to edit"}
			}
			if e.Method != "GET" {
				return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ %s %s: only GET can be repeated (bodies and tokens are never stored); run it by hand", e.Method, e.Path)}
			}
			switch cur := a.currentOrigin(); {
			case e.Origin == "":
				return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ GET %s was saved without a server, so it cannot be told where it belongs: run it by hand", e.Path)}
			case e.Origin != cur:
				return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ GET %s went to %s, but the shell points at %s: switch (api env use ...) or run it by hand", e.Path, e.Origin, orUnset(cur))}
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
			entries := a.historyEntries(all)
			if len(entries) > 15 {
				entries = entries[:15]
			}
			for i, e := range entries {
				out = append(out, cobra.CompletionWithDesc(strconv.Itoa(i+1), strings.TrimSpace(e.Method+" "+e.Path+" "+e.Filter)))
			}
			return out, cobra.ShellCompDirectiveNoFileComp
		},
	}
	a.bindFlags(o, cmd, false)
	a.bindViewerFlag(cmd, o)
	cmd.Flags().BoolVar(&pick, "pick", false, "choose the entry with fzf")
	cmd.Flags().BoolVar(&edit, "edit", false, "saved request: edit a copy in your editor first (unsaved = cancel; the file is not changed)")
	cmd.Flags().BoolVar(&fresh, "default", false, "saved request: edit starting from the skeleton the pick generated, not your last version (that stays as <file>.bak)")
	cmd.Flags().BoolVar(&yes, "yes", false, "saved request: do not ask before running one that changes data")
	cmd.Flags().BoolVar(&all, "all", false, "number the entries of every server, as `api history -a` does")
	return cmd
}

// savedRuns are the entries that point at a saved request, with the position
// each has in entries: the number `api history` shows and `api again N` takes.
func savedRuns(entries []history.Entry) (runs []history.Entry, nums []int) {
	for i, e := range entries {
		if e.Method == history.RunMethod {
			runs = append(runs, e)
			nums = append(nums, i+1)
		}
	}
	return runs, nums
}

// pickHistory runs fzf over the entries and returns the chosen 1-based index.
func (a *app) pickHistory(entries []history.Entry) (int, error) {
	lines := make([]string, len(entries))
	for i, e := range entries {
		lines[i] = strings.TrimSpace(e.Method + " " + e.Path + " " + e.Filter)
	}
	return a.fzfPick("again", lines)
}

// currentOrigin is the server the shell points at ("" when none).
func (a *app) currentOrigin() string { return origin(a.session().BaseURL) }

// historyEntries is THE list `history`, `again`, its completion and --pick all
// number from, so "again 2" is always the entry history showed as 2.
func (a *app) historyEntries(all bool) []history.Entry {
	if all {
		return history.Entries(0)
	}
	return history.ForOrigin(a.currentOrigin(), 0)
}

// emptyHistoryMessage says why nothing is listed, and where the rest is.
func (a *app) emptyHistoryMessage(all bool) string {
	total := len(history.Entries(0))
	switch {
	case total == 0, all:
		return "no history yet"
	case a.currentOrigin() == "":
		return fmt.Sprintf("no server selected; api history -a shows %d entries", total)
	}
	return fmt.Sprintf("no history for %s; api history -a shows %d from other servers", a.currentOrigin(), total)
}
