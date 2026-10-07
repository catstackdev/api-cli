package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"cybercat/api/internal/config"
	"cybercat/api/internal/session"
	"cybercat/api/internal/ui"

	"github.com/spf13/cobra"
)

// scope picks which config file a command reads or writes.
type scope struct{ local, global bool }

func (sc *scope) bind(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&sc.local, "local", false, "the project file (.api.toml)")
	cmd.Flags().BoolVar(&sc.global, "global", false, "the global file (~/.config/api/config.toml)")
}

// path resolves the target file. Default: the local file if one exists, else
// the global one.
func (a *app) configPath(sc scope) (path string, isLocal bool, err error) {
	if sc.local && sc.global {
		return "", false, fmt.Errorf("use --local or --global, not both")
	}
	switch {
	case sc.global:
		return config.GlobalPathWith(a.getenv), false, nil
	case sc.local:
		if a.cfg.LocalPath != "" {
			return a.cfg.LocalPath, true, nil
		}
		return filepath.Join(findRoot(), ".api.toml"), true, nil
	case a.cfg.LocalPath != "":
		return a.cfg.LocalPath, true, nil
	}
	return config.GlobalPathWith(a.getenv), false, nil
}

func (a *app) configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show and edit the global / project config (envs, defaults, aliases)",
		Long: `Optional TOML config. Global: ~/.config/api/config.toml. Project: the nearest
.api.toml. They merge key by key (project wins).

  api --env dev get /users       use [env.dev]
  api me                         run the alias [alias] me = "..."

It never holds secrets: tokens and passwords are not part of the schema, and a
profile can only name a variable (token_env = "API_TOKEN_DEV") that holds one.
Treat a project .api.toml like code: it chooses the server your requests go to.`,
	}

	cmd.AddCommand(a.configPathCmd(), a.configShowCmd(), a.configGetCmd(), a.configSetCmd(), a.configInitCmd(), a.configEditCmd(), a.configValidateCmd(), a.configSchemaCmd())
	return cmd
}

func (a *app) configPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Show which config files are in use",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			g := config.GlobalPathWith(a.getenv)
			state := func(p string) string {
				if _, err := os.Stat(p); err == nil {
					return "exists"
				}
				return "missing"
			}
			rows := [][]string{{"global", g, state(g)}}
			if a.cfg.LocalPath != "" {
				rows = append(rows, []string{"local", a.cfg.LocalPath, "exists"})
			} else {
				rows = append(rows, []string{"local", filepath.Join(findRoot(), ".api.toml"), "missing (would be created here)"})
			}
			ui.Table(a.io.Out, []string{"SCOPE", "PATH", "STATE"}, rows, a.colorOut())
		},
	}
}

func (a *app) configShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the effective (merged) config",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			color := a.colorOut()
			c := a.cfg.Config
			out := a.io.Out
			fmt.Fprintf(out, "%s %s\n", ui.Paint("default_env", ui.Dim, color), orUnset(c.DefaultEnv))
			d := c.Defaults
			var parts []string
			add := func(k, v string) {
				if v != "" {
					parts = append(parts, k+"="+v)
				}
			}
			add("timeout", d.Timeout)
			add("connect_timeout", d.ConnectTimeout)
			add("color", d.Color)
			if d.Retry != nil {
				add("retry", fmt.Sprint(*d.Retry))
			}
			if d.Summary != nil {
				add("summary", fmt.Sprint(*d.Summary))
			}
			if d.TokenCache != nil {
				add("token_cache", fmt.Sprint(*d.TokenCache))
			}
			if d.AutoLogin != nil {
				add("auto_login", fmt.Sprint(*d.AutoLogin))
			}
			if d.Editor != "" {
				add("editor", fmt.Sprintf("%q", d.Editor))
			}
			fmt.Fprintf(out, "%s %s\n\n", ui.Paint("defaults   ", ui.Dim, color), orUnset(strings.Join(parts, " ")))

			var rows [][]string
			for _, n := range c.EnvNames() {
				e := c.Env[n]
				rows = append(rows, []string{n, e.URL, e.GraphQL, e.TokenEnv, e.EndpointPrefix, summarizeList(e.Endpoints, 4), strings.Join(sortedKeys(e.Headers), ",")})
			}
			if len(rows) > 0 {
				ui.Table(out, []string{"ENV", "URL", "GRAPHQL", "TOKEN_ENV", "PREFIX", "ENDPOINTS", "HEADERS"}, rows, color)
				fmt.Fprintln(out)
			}
			var arows [][]string
			for _, n := range sortedKeys(c.Alias) {
				arows = append(arows, []string{n, c.Alias[n]})
			}
			if len(arows) > 0 {
				ui.Table(out, []string{"ALIAS", "COMMAND"}, arows, color)
			}
			if len(rows) == 0 && len(arows) == 0 {
				a.say("  (no envs or aliases yet: `api config init`, then `api config set env.dev.url https://...`)")
			}
		},
	}
}

func (a *app) configGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Print one effective value (e.g. env.dev.url, defaults.timeout)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, ok := config.Get(a.cfg.Config, args[0])
			if !ok {
				return &ExitError{Code: exitGeneric, Msg: "✗ not set: " + args[0]}
			}
			fmt.Fprintln(a.io.Out, v)
			return nil
		},
		ValidArgsFunction: a.completeConfigKey,
	}
}

func (a *app) configSetCmd() *cobra.Command {
	var setScope scope
	set := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a value in the project file (if there is one) or the global file",
		Example: `  api config set env.dev.url https://api.dev.example.com
  api config set env.dev.token_env API_TOKEN_DEV
  api config set defaults.timeout 30s --global
  api config set alias.me "gql -e v2/user 'query { me { id } }' .data.me"`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, isLocal, err := a.configPath(setScope)
			if err != nil {
				return err
			}
			if isLocal && strings.HasSuffix(args[0], ".token_env") {
				return fmt.Errorf("token_env is only honoured in the global config (a project file could point it at your tokens): use --global")
			}
			if isLocal && (args[0] == "defaults.token_cache" || args[0] == "defaults.auto_login" || args[0] == "defaults.editor") {
				return fmt.Errorf("%s is only honoured in the global config (it decides whether a token outlives your shell): use --global", args[0])
			}
			preserved, err := config.SetInFile(path, args[0], args[1])
			if err != nil {
				return err
			}
			a.say(fmt.Sprintf("✓ %s = %s  (%s)", args[0], args[1], path))
			if !preserved {
				a.say("⚠ this file's layout could not be edited in place: it was rewritten and its comments were not kept")
			}
			// a default_env that names nothing finds no server (tv, nvim and CI start api
			// without your shell's session): say so now, not at the first failed request
			if _, defined := a.cfg.Env[args[1]]; args[0] == "default_env" && args[1] != "" && !defined {
				flag := "--global"
				if isLocal {
					flag = "--local"
				}
				a.say(fmt.Sprintf("⚠ env %q is not defined yet, so default_env finds no server: api config set env.%s.url <url> %s", args[1], args[1], flag))
			}
			return nil
		},
		ValidArgsFunction: a.completeConfigKey,
	}
	setScope.bind(set)
	return set
}

func (a *app) configInitCmd() *cobra.Command {
	var initScope scope
	var force bool
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Create a commented config file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, isLocal, err := a.configPath(initScope)
			if err != nil {
				return err
			}
			if _, err := os.Stat(path); err == nil && !force {
				msg := "✗ already exists: " + path + " (--force overwrites it; keep a copy first)"
				// say how to reach the OTHER file: init defaults to the project
				// file once one exists, which is not obvious
				other, flag := config.GlobalPathWith(a.getenv), "--global"
				if !isLocal {
					other, flag = filepath.Join(findRoot(), ".api.toml"), "--local"
				}
				state := "does not exist yet"
				if _, err := os.Stat(other); err == nil {
					state = "exists"
				}
				msg += fmt.Sprintf("\n  for the other file use %s: %s (%s)", flag, other, state)
				return &ExitError{Code: exitGeneric, Msg: msg}
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			text := config.Render(config.Global, "", "")
			if isLocal {
				text = config.Render(config.Project, "", "")
				// a project file: pre-fill [vars] from the names its .env files define
				var keys []string
				if sources, err := readEnvSources(findRoot()); err == nil {
					for _, src := range sources {
						keys = append(keys, src.Keys()...)
					}
				}
				if base, gql := detectURLVars(keys); base != "" || gql != "" {
					text = config.Render(config.Project, base, gql)
					if gql != "" {
						a.say("  [vars] graphql_url = " + gql + " (detected in your .env files)")
					}
					if base != "" {
						a.say("  [vars] base_url = " + base + " (detected in your .env files)")
					}
				}
			}
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				return err
			}
			a.say("✓ " + path)
			return nil
		},
	}
	initScope.bind(initCmd)
	initCmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	return initCmd
}

func (a *app) configEditCmd() *cobra.Command {
	var editScope scope
	edit := &cobra.Command{
		Use:   "edit",
		Short: "Open the config file in $EDITOR (created from the template if missing)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, isLocal, err := a.configPath(editScope)
			if err != nil {
				return err
			}
			if _, err := os.Stat(path); err != nil {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					return err
				}
				tpl := config.Render(config.Global, "", "")
				if isLocal {
					tpl = config.Render(config.Project, "", "")
				}
				if err := os.WriteFile(path, []byte(tpl), 0o600); err != nil {
					return err
				}
			}
			editor := a.getenv("VISUAL")
			if editor == "" {
				editor = a.getenv("EDITOR")
			}
			if editor == "" {
				editor = "vi"
			}
			c := exec.Command("sh", "-c", editor+` "$1"`, "sh", path)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			return c.Run()
		},
	}
	editScope.bind(edit)
	return edit
}

// completeConfigKey completes the first argument of `config get|set`.
func (a *app) completeConfigKey(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return a.configKeys(toComplete), cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

// configKeys completes the settable key paths, expanding <name> with the
// envs/aliases that already exist.
func (a *app) configKeys(prefix string) []cobra.Completion {
	var out []cobra.Completion
	add := func(k string) {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	for _, k := range config.SettableKeys {
		switch {
		case strings.HasPrefix(k, "env.<name>"):
			for _, n := range a.cfg.EnvNames() {
				add(strings.Replace(k, "<name>", n, 1))
			}
			add(k)
		case strings.HasPrefix(k, "alias.<name>"):
			for n := range a.cfg.Alias {
				add("alias." + n)
			}
			add(k)
		default:
			add(k)
		}
	}
	return out
}

// useEnv emits the assignments that switch the shell to [env.<name>].
// They are PLAIN assignments, not `export`: session variables stay unexported
// (see up-scale.sh). The old token is dropped so it can never be sent to the
// new env; token_env, when set, is picked up from the named variable.
func (a *app) useEnvCmd() *cobra.Command {
	var emit bool
	cmd := &cobra.Command{
		Use:   "use [name]",
		Short: "Switch the shell to a configured [env.<name>] (base URL, GraphQL URL, token_env); no name = pick with fzf",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) == 1 {
				name = args[0]
			} else {
				names := a.cfg.EnvNames()
				if len(names) == 0 {
					return &ExitError{Code: exitGeneric, Msg: "✗ no envs configured: see `api config init`"}
				}
				lines := make([]string, len(names))
				for i, n := range names {
					lines[i] = n + "  " + a.cfg.Env[n].URL
				}
				idx, err := a.fzfPick("env", lines)
				if err != nil {
					return err
				}
				name = names[idx-1]
			}
			p, ok := a.cfg.Env[name]
			if !ok {
				known := strings.Join(a.cfg.EnvNames(), ", ")
				if known == "" {
					known = "none configured: see `api config init`"
				}
				return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ unknown env %q (known: %s)", name, known)}
			}
			if p.URL == "" {
				return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ env %q has no url", name)}
			}
			if !emit {
				return &ExitError{Code: exitUsage, Msg: "✗ a child process cannot set your shell's variables: use the zsh `api env use` function, or eval \"$(api env use --emit " + name + ")\""}
			}
			token := "''"
			if p.TokenEnv != "" { // validated to API_TOKEN_[A-Z0-9_]+ on read
				token = `"$` + p.TokenEnv + `"`
			}
			a.say("✓ env " + name + "  " + p.URL)
			fmt.Fprintf(a.io.Out, "%s=%s; %s=%s; %s=%s; %s=%s; %s=%s\n",
				session.EnvBaseURL, session.Quote(p.URL),
				session.EnvGraphQLURL, session.Quote(p.GraphQL),
				session.EnvName, session.Quote(name),
				session.EnvFrom, "toml",
				session.EnvToken, token)
			return nil
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			var out []cobra.Completion
			for _, n := range a.cfg.EnvNames() {
				out = append(out, cobra.CompletionWithDesc(n, a.cfg.Env[n].URL))
			}
			return out, cobra.ShellCompDirectiveNoFileComp
		},
	}
	cmd.Flags().BoolVar(&emit, "emit", false, "print shell assignments for eval")
	markShellEmit(cmd)
	return cmd
}

func (a *app) configSchemaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schema",
		Short: "Print the JSON Schema of the config files (for editors and CI)",
		Long: `Prints a JSON Schema for config.toml / .api.toml. With taplo or "Even Better
TOML", save it and point a file at it with a first line such as:

  api config schema > ~/.config/api/schema.json
  #:schema file:///Users/you/.config/api/schema.json`,
		Args: cobra.NoArgs,
		Run:  func(cmd *cobra.Command, args []string) { fmt.Fprint(a.io.Out, config.Schema) },
	}
}

func (a *app) configValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [file...]",
		Short: "Check config files for errors and for settings that will be ignored",
		Long: `Reads the global and project config (or the files you name) and reports, per
file: parse errors with the key and line (never the value), half-built logins, urls
that are not http(s), a default_env that names nothing, and settings a project
file may not use (they are valid TOML but ignored: token_env, @env: logins,
token_cache, unsafe aliases). Exits 1 when there is an error; warnings alone exit 0.
Offline: it never reads the Keychain.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			files := args
			if len(files) == 0 {
				if g := config.GlobalPathWith(a.getenv); g != "" {
					if _, err := os.Stat(g); err == nil {
						files = append(files, g)
					}
				}
				if a.cfg.LocalPath != "" {
					files = append(files, a.cfg.LocalPath)
				}
			}
			if len(files) == 0 {
				a.say("✓ no config files (nothing to validate): api config init")
				return nil
			}
			global, _ := config.Read(config.GlobalPathWith(a.getenv))
			known := map[string]bool{}
			for n := range global.Env {
				known[n] = true
			}

			errs, warns := 0, 0
			for _, f := range files {
				data, err := os.ReadFile(f)
				if err != nil {
					a.say("✗ " + err.Error())
					errs++
					continue
				}
				c, err := config.ParseText(data, f)
				if err != nil {
					a.say("✗ " + err.Error())
					errs++
					continue
				}
				issues := config.Lint(c, filepath.Base(f) == ".api.toml", known)
				if len(issues) == 0 {
					a.say("✓ " + f)
					continue
				}
				for _, i := range issues {
					if i.Error {
						errs++
						a.say("✗ " + f + ": " + i.Message)
					} else {
						warns++
						a.say("⚠ " + f + ": " + i.Message)
					}
				}
			}
			if errs > 0 {
				return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ %d error(s), %d warning(s)", errs, warns)}
			}
			if warns > 0 {
				a.say(fmt.Sprintf("✓ no errors, %d warning(s)", warns))
			}
			return nil
		},
	}
}

// summarizeList shows the first few items and the total: a long list would make
// the table wider than the terminal. `api config get env.<name>.endpoints` has all.
func summarizeList(items []string, show int) string {
	if len(items) <= show {
		return strings.Join(items, ",")
	}
	return fmt.Sprintf("%s,… (%d)", strings.Join(items[:show], ","), len(items))
}
