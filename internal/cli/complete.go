package cli

import (
	"path/filepath"
	"strings"

	"cybercat/api/internal/history"

	"github.com/spf13/cobra"
)

// pathCompletion offers recently requested paths for the first argument.
// Completion has to be offline and fast, so it never touches the network.
func pathCompletion(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []cobra.Completion
	for _, p := range history.Paths(toComplete) {
		out = append(out, p)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

var commonHeaders = []string{
	"Accept: ", "Authorization: ", "Cache-Control: ", "Content-Type: application/json",
	"If-None-Match: ", "User-Agent: ", "X-Request-Id: ",
}

func headerCompletion(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	var out []cobra.Completion
	for _, h := range commonHeaders {
		if strings.HasPrefix(strings.ToLower(h), strings.ToLower(toComplete)) {
			out = append(out, h)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

// endpointCompletion offers GraphQL endpoints from $API_GQL_ENDPOINTS
// (space or comma separated) and from the ugql schema cache (~/.cache/ugql).
func (a *app) endpointCompletion(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	seen := map[string]bool{}
	var out []cobra.Completion
	add := func(e string) {
		if e != "" && !seen[e] && strings.HasPrefix(e, toComplete) {
			seen[e] = true
			out = append(out, e)
		}
	}
	_, prof := a.resolve()
	for _, e := range prof.Endpoints {
		add(e)
	}
	for _, e := range strings.FieldsFunc(a.getenv("API_GQL_ENDPOINTS"), func(r rune) bool { return r == ' ' || r == ',' }) {
		add(e)
	}
	files, _ := filepath.Glob(filepath.Join(a.ugqlCacheDir(), "*.json"))
	for _, f := range files {
		add("v2/" + strings.TrimSuffix(filepath.Base(f), ".json"))
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
