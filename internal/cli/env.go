package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cybercat/api/internal/config"
	"cybercat/api/internal/envfile"
	"cybercat/api/internal/session"

	"github.com/spf13/cobra"
)

// findRoot returns the nearest parent directory that has a .env or a .git,
// or the working directory when there is none.
func findRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		for _, marker := range []string{".env", ".git"} {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir
			}
		}
		if dir == filepath.Dir(dir) {
			return cwd
		}
	}
}

// envSuffixes lists the <suffix> of every .env.<suffix> in the project root.
func envSuffixes() []string {
	matches, _ := filepath.Glob(filepath.Join(findRoot(), ".env.*"))
	var out []string
	for _, m := range matches {
		suffix := strings.TrimPrefix(filepath.Base(m), ".env.")
		if templateSuffixes[suffix] {
			continue
		}
		out = append(out, suffix)
	}
	sort.Strings(out)
	return out
}

func (a *app) envCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Show, load or clear the API session (API_BASE_URL, API_TOKEN, ...)",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show env, base URL and token prefix",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			s := a.session()
			fmt.Fprintf(a.io.Out, "  env   : %s\n", orUnset(s.Name))
			fmt.Fprintf(a.io.Out, "  base  : %s\n", orUnset(s.BaseURL))
			fmt.Fprintf(a.io.Out, "  gql   : %s\n", orUnset(s.GraphQLURL))
			tokenLine := session.MaskToken(s.Token)
			if exp := tokenExpiry(s.Token, time.Now()); exp != "" {
				tokenLine += "  (" + exp + ")"
			}
			fmt.Fprintf(a.io.Out, "  token : %s\n", tokenLine)
			if c := a.cacheStatus(s.Name, s.BaseURL); c != "" {
				fmt.Fprintf(a.io.Out, "  cache : %s\n", c)
			}
		},
	})

	var emit bool
	load := &cobra.Command{
		Use:   "load [suffix]",
		Short: "Load .env / .env.<suffix> from the project root into the shell",
		Long: `Finds the nearest directory with a .env or .git and loads .env (or
.env.<suffix>) with allexport, so $VAR expansion inside the file works.

The file is sourced by the shell itself, not parsed here, which keeps zsh
semantics. --emit prints the statements for the shell to eval; the zsh "api"
function does that for you.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := findRoot()
			exists := func(name string) bool { _, err := os.Stat(filepath.Join(root, name)); return err == nil }
			suffix, why := "", ""
			if len(args) == 1 {
				suffix = args[0]
			} else if s := a.cfg.Vars.EnvSuffix; s != "" {
				suffix, why = s, "vars.env_suffix in config"
			} else if !exists(".env") {
				// like `db`: with no .env, fall back to the usual local ones
				for _, cand := range []string{"local", "development", "dev"} {
					if exists(".env." + cand) {
						suffix, why = cand, "no .env here"
						break
					}
				}
			}
			file := filepath.Join(root, ".env")
			name := "default"
			if suffix != "" {
				if err := config.ValidateEnvSuffix(suffix); err != nil {
					return fmt.Errorf("bad suffix: %w", err)
				}
				file += "." + suffix
				name = suffix
			}
			if _, err := os.Stat(file); err != nil {
				msg := "✗ not found: " + file
				if have := envSuffixes(); len(have) > 0 {
					msg += "\n  available: " + strings.Join(have, ", ") + " (api env load <name>)"
				}
				return &ExitError{Code: exitGeneric, Msg: msg}
			}
			if !emit {
				return &ExitError{Code: exitUsage, Msg: "✗ a child process cannot set your shell's env: use the zsh `api env load` function, or eval \"$(api env load --emit " + suffix + ")\""}
			}
			a.say("✓ " + file)
			if why != "" {
				a.say("  picked automatically (" + why + ")")
			}
			var keys []string
			if data, err := os.ReadFile(file); err == nil {
				for _, e := range envfile.Parse(data) {
					keys = append(keys, e.Key)
				}
			}
			stmts, notes := envMapping(keys, a.cfg.Vars)
			for _, n := range notes {
				a.say("  " + n)
			}
			line := fmt.Sprintf("set -o allexport; source %s; set +o allexport; %s",
				session.Quote(file), session.Export(session.EnvName, name))
			if len(stmts) > 0 {
				line += "; " + joinStmts(stmts)
			}
			fmt.Fprintln(a.io.Out, line)
			return nil
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			var out []cobra.Completion
			for _, s := range envSuffixes() {
				out = append(out, cobra.CompletionWithDesc(s, ".env."+s))
			}
			return out, cobra.ShellCompDirectiveNoFileComp
		},
	}
	load.Flags().BoolVar(&emit, "emit", false, "print shell statements for eval")
	markShellEmit(load)

	var clearEmit bool
	clear := &cobra.Command{
		Use:   "clear",
		Short: "Unset API_BASE_URL, API_GRAPHQL_URL, API_TOKEN and API_ENV",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !clearEmit {
				return &ExitError{Code: exitUsage, Msg: "✗ use the zsh `api env clear` function, or eval \"$(api env clear --emit)\""}
			}
			a.say("✓ cleared")
			fmt.Fprintln(a.io.Out, session.Unset(session.EnvBaseURL, session.EnvGraphQLURL, session.EnvToken, session.EnvName))
			return nil
		},
	}
	clear.Flags().BoolVar(&clearEmit, "emit", false, "print shell statements for eval")
	markShellEmit(clear)

	list := &cobra.Command{
		Use:   "list",
		Short: "List the .env.<suffix> files in the project root",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			for _, s := range envSuffixes() {
				fmt.Fprintln(a.io.Out, s)
			}
		},
	}

	root := &cobra.Command{
		Use:   "root",
		Short: "Print the project root (nearest directory with a .env or .git)",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(a.io.Out, findRoot())
		},
	}

	cmd.AddCommand(load, clear, list, root, a.useEnvCmd(), a.envExampleCmd(), a.envInitCmd())
	return cmd
}

func orUnset(v string) string {
	if v == "" {
		return "<not set>"
	}
	return v
}
