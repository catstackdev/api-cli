package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"cybercat/api/internal/httpfile"
	"cybercat/api/internal/ui"

	"github.com/spf13/cobra"
)

// requestsCmd manages the files `api openapi pick` and `api gql --pick` keep in
// .api/requests/. Nothing else prunes them: `cache clear` never touches them.
func (a *app) requestsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "requests",
		Short: "List or delete the saved requests of this project (" + requestsDir + "/)",
		Long: `The requests ` + "`api openapi pick` and `api gql --pick`" + ` keep under ` + requestsDir + `/, with the
method and URL of each. ` + "`rm`" + ` deletes a saved request together with its .bak (your
previous edit) and .orig (the generated skeleton). History entries that point at
a deleted request just report that it is gone.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(a.requestsLsCmd(), a.requestsRmCmd())
	return cmd
}

func (a *app) requestsLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List the saved requests with their method and URL",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			names := listHTTPFiles()
			if len(names) == 0 {
				a.say("  (no saved requests: api openapi pick and api gql --pick make them)")
				return
			}
			sort.Strings(names)
			root := findRoot()
			rows := make([][]string, len(names))
			for i, n := range names {
				rows[i] = []string{n, "?", "(unreadable)"}
				path, err := savedRequestPath(root, n)
				if err != nil {
					continue
				}
				data, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				file, err := httpfile.Parse(data)
				if err != nil || len(file.Requests) == 0 {
					continue
				}
				r := file.Requests[0]
				rows[i][1], rows[i][2] = strings.ToUpper(r.Method), r.URL
				if n := len(file.Requests); n > 1 {
					rows[i][2] += fmt.Sprintf("  (+%d more)", n-1)
				}
			}
			ui.Table(a.io.Out, []string{"NAME", "METHOD", "URL"}, rows, a.colorOut())
		},
	}
}

func (a *app) requestsRmCmd() *cobra.Command {
	var all, yes bool
	cmd := &cobra.Command{
		Use:   "rm <name>... | --all",
		Short: "Delete saved requests (and their .bak / .orig)",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			names := args
			switch {
			case all && len(args) > 0:
				return &ExitError{Code: exitUsage, Msg: "✗ give names or --all, not both"}
			case all:
				names = listHTTPFiles()
			case len(args) == 0:
				return &ExitError{Code: exitUsage, Msg: "✗ which request? (api requests ls; --all deletes every one)"}
			}
			if len(names) == 0 {
				a.say("  (no saved requests)")
				return nil
			}
			if all && !yes && !a.confirm(fmt.Sprintf("✱ delete all %d saved request(s) in %s/?", len(names), requestsDir)) {
				a.say("  (nothing deleted)")
				return &ExitError{Code: exitGeneric}
			}
			root, failed := findRoot(), false
			for _, n := range names {
				n = strings.TrimSuffix(strings.TrimSuffix(n, ".http"), ".rest")
				// the same rule `api again` uses: a name, never a path, and a regular
				// file (a symlink is not followed out of the project)
				path, err := savedRequestPath(root, n)
				if err != nil {
					a.say("✗ " + err.Error())
					failed = true
					continue
				}
				if err := os.Remove(path); err != nil {
					a.say("✗ " + n + ": " + err.Error())
					failed = true
					continue
				}
				for _, side := range []string{".bak", ".orig"} {
					if st, err := os.Lstat(path + side); err == nil && st.Mode().IsRegular() {
						_ = os.Remove(path + side)
					}
				}
				a.say("✓ deleted " + requestsDir + "/" + n)
			}
			if failed {
				return &ExitError{Code: exitGeneric}
			}
			return nil
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			given := map[string]bool{}
			for _, x := range args {
				given[x] = true
			}
			var out []cobra.Completion
			for _, n := range listHTTPFiles() {
				if !given[n] && strings.HasPrefix(n, toComplete) {
					out = append(out, n)
				}
			}
			return out, cobra.ShellCompDirectiveNoFileComp
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "delete every saved request (asks first)")
	cmd.Flags().BoolVar(&yes, "yes", false, "with --all: do not ask")
	return cmd
}
