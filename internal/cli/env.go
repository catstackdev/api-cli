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
	"cybercat/api/internal/envmap"
	"cybercat/api/internal/session"
	"cybercat/api/internal/ui"

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
	cmd.AddCommand(
		a.envStatusCmd(), a.envLoadCmd(), a.envClearCmd(), a.envListCmd(), a.envRootCmd(),
		a.useEnvCmd(), a.envExampleCmd(), a.envInitCmd(),
	)
	return cmd
}

func (a *app) envStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show env, base URL and token prefix",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, args []string) { a.printEnvStatus() },
	}
}

// printEnvStatus shows the session and where each value came from.
func (a *app) printEnvStatus() {
	s, _, src := a.resolveWithSources(a.envName)
	color := a.colorOut()
	line := func(label, value string) {
		if value == "" {
			value = ui.Paint("<not set>", ui.Dim, color)
		}
		fmt.Fprintf(a.io.Out, "  %s : %s\n", ui.Paint(label, ui.Dim, color), value)
	}
	from := func(label string) {
		if label == "" {
			return
		}
		code := ui.Dim
		switch {
		case strings.Contains(label, "NOT toml"):
			code = ui.Yellow
		case (strings.Contains(label, "same server as") || strings.Contains(label, "(api env use)")):
			code = ui.Green
		}
		fmt.Fprintf(a.io.Out, "          %s %s\n", ui.Paint("source:", ui.Dim, color), ui.Paint(label, code, color))
	}
	name := ""
	if s.Name != "" {
		name = ui.Paint(s.Name, ui.Bold+";"+ui.Cyan, color)
	}
	line("env  ", name)
	line("base ", s.BaseURL)
	from(src.base)
	line("gql  ", s.GraphQLURL)
	from(src.gql)
	if s.GraphQLURL == "" && s.BaseURL != "" {
		from("default: /graphql on the base URL")
	}
	token := ""
	if s.Token != "" {
		token = session.MaskToken(s.Token)
		if exp := tokenExpiry(s.Token, time.Now()); exp != "" {
			token += "  (" + paintExpiry(exp, color) + ")"
		}
	}
	line("token", token)
	from(src.token)
	if c := a.cacheStatus(s.Name, s.BaseURL); c != "" {
		line("cache", c)
		if c != "none" && s.Token == "" {
			from("a cached token is attached when a request is sent")
		}
	}
}

func (a *app) envLoadCmd() *cobra.Command {
	var emit bool
	load := &cobra.Command{
		Use:   "load [suffix]",
		Short: "Load .env / .env.<suffix> from the project root into the shell",
		Long: `Finds the nearest directory with a .env or .git and loads .env (or
.env.<suffix>) with allexport, so $VAR expansion inside the file works.

The file is sourced by the shell itself, not parsed here, which keeps zsh
semantics. --emit prints the statements for the shell to eval; the zsh "api"
function does that for you.`,
		Args:              cobra.MaximumNArgs(1),
		RunE:              func(cmd *cobra.Command, args []string) error { return a.envLoad(args, emit) },
		ValidArgsFunction: completeEnvSuffixes,
	}
	load.Flags().BoolVar(&emit, "emit", false, "print shell statements for eval")
	markShellEmit(load)
	return load
}

func completeEnvSuffixes(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []cobra.Completion
	for _, s := range envSuffixes() {
		out = append(out, cobra.CompletionWithDesc(s, ".env."+s))
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// chooseEnvSuffix is the suffix `env load` uses: the argument, else the
// configured one, else a usual local file when there is no plain .env. why says
// how an automatic pick was made ("" when you named it).
func (a *app) chooseEnvSuffix(root string, args []string) (suffix, why string) {
	exists := func(name string) bool { _, err := os.Stat(filepath.Join(root, name)); return err == nil }
	if len(args) == 1 {
		return args[0], ""
	}
	if s := a.cfg.Vars.EnvSuffix; s != "" {
		return s, "vars.env_suffix in config"
	}
	if !exists(".env") {
		// like `db`: with no .env, fall back to the usual local ones
		for _, cand := range []string{"local", "development", "dev"} {
			if exists(".env." + cand) {
				return cand, "no .env here"
			}
		}
	}
	return "", ""
}

// envLoad prints the shell statements that load a project's .env file.
func (a *app) envLoad(args []string, emit bool) error {
	root := findRoot()
	suffix, why := a.chooseEnvSuffix(root, args)
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
	stmts, notes := envmap.Mapping(keys, a.cfg.Vars)
	for _, n := range notes {
		a.say("  " + n)
	}
	line := fmt.Sprintf("set -o allexport; source %s; set +o allexport; %s; %s=%s",
		session.Quote(file), session.Export(session.EnvName, name),
		session.EnvFrom, session.Quote("file:"+filepath.Base(file)))
	if len(stmts) > 0 {
		line += "; " + envmap.Join(stmts)
	}
	fmt.Fprintln(a.io.Out, line)
	return nil
}

func (a *app) envClearCmd() *cobra.Command {
	var emit bool
	clear := &cobra.Command{
		Use:   "clear",
		Short: "Unset API_BASE_URL, API_GRAPHQL_URL, API_TOKEN and API_ENV",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !emit {
				return &ExitError{Code: exitUsage, Msg: "✗ use the zsh `api env clear` function, or eval \"$(api env clear --emit)\""}
			}
			a.say("✓ cleared")
			fmt.Fprintln(a.io.Out, session.Unset(session.EnvBaseURL, session.EnvGraphQLURL, session.EnvToken, session.EnvName, session.EnvFrom))
			return nil
		},
	}
	clear.Flags().BoolVar(&emit, "emit", false, "print shell statements for eval")
	markShellEmit(clear)
	return clear
}

func (a *app) envListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the .env.<suffix> files in the project root",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			for _, s := range envSuffixes() {
				fmt.Fprintln(a.io.Out, s)
			}
		},
	}
}

func (a *app) envRootCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "root",
		Short: "Print the project root (nearest directory with a .env or .git)",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, args []string) { fmt.Fprintln(a.io.Out, findRoot()) },
	}
}

// paintExpiry colours a JWT expiry: red once expired, green otherwise.
func paintExpiry(exp string, color bool) string {
	if strings.HasPrefix(exp, "EXPIRED") {
		return ui.Paint(exp, ui.Red, color)
	}
	return ui.Paint(exp, ui.Green, color)
}

func orUnset(v string) string {
	if v == "" {
		return "<not set>"
	}
	return v
}
