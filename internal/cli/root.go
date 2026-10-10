// Package cli wires the api subcommands. Every command builds an httpc.Spec
// and goes through the same send/finish pipeline.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"cybercat/api/internal/config"
	"cybercat/api/internal/httpfile"
	"cybercat/api/internal/secretstore"
	"cybercat/api/internal/ui"

	"github.com/spf13/cobra"
)

// Version and BuildTime are set at build time with -ldflags (see the Makefile).
var (
	Version   = "dev"
	BuildTime = ""
)

// VersionString identifies THIS build: the commit-based version, when it was
// built, and the Go version. Two builds of the same commit differ by time.
func VersionString() string {
	out := Version
	if BuildTime != "" {
		out += " (built " + BuildTime
	} else {
		out += " (build time unknown"
	}
	return out + ", " + runtime.Version() + ")"
}

// IO is the set of streams a run reads and writes; tests swap it out.
type IO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer

	// Both are optional and exist for tests: the credential store (default: the
	// system one) and a no-echo prompt (default: the terminal).
	Store      secretstore.Store
	ReadSecret func(label string) (string, error)
	// Getenv reads the environment; nil means the process environment. Tests
	// inject one to run the CLI without touching (or depending on) the real one.
	Getenv func(name string) (string, bool)
}

// StdIO returns the process streams.
func StdIO() IO { return IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr} }

// ExitError ends a run with a specific exit code. A non-empty Msg is printed
// to stderr first.
type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string { return e.Msg }

// Exit codes beyond curl's own (22 HTTP, 28 timeout, 7 connect, 6 DNS).
const (
	exitGeneric = 1
	exitUsage   = 2
	exitHTTP    = 22
	exitFilter  = 5
	exitGraphQL = 4
	// exitAssert (3) is declared in run_assert.go.
)

type app struct {
	io    IO
	store secretstore.Store
	base  string
	token string
	debug bool

	cfg     config.Loaded // global + local config, merged
	cfgErr  error         // load error, if any (shown by `api doctor`)
	envName string        // --env: use the [env.<name>] profile

	color               string // --color / API_COLOR: auto, always, never
	forceColor, noColor bool   // -C / -M
	noPager             bool

	strictRefs   bool // automatic login: only @keychain: references
	reloginTried bool // at most one automatic login per invocation

	captured map[string]*captured // `api run`: responses of requests that already ran

	asserts      map[*httpfile.Request][]compiledAssert // `api run`: compiled @assert lines of the requests about to run
	assertTotal  int                                    // assertions evaluated in this run
	assertFailed int                                    // ... of which failed
	running      map[string]bool                        // `api run`: requests being run for their values (cycle guard)
}

// mode is the colour preference: -M beats -C beats --color / API_COLOR.
func (a *app) mode() ui.Mode {
	switch {
	case a.noColor:
		return ui.Never
	case a.forceColor:
		return ui.Always
	}
	return ui.ParseMode(a.color)
}

func (a *app) colorOut() bool { return ui.EnabledWith(a.io.Out, a.mode(), a.getenv) }
func (a *app) colorErr() bool { return ui.EnabledWith(a.io.Err, a.mode(), a.getenv) }

// lookup / getenv read the environment through IO.Getenv when set.
func (a *app) lookup(name string) (string, bool) {
	if a.io.Getenv != nil {
		return a.io.Getenv(name)
	}
	return os.LookupEnv(name)
}

func (a *app) getenv(name string) string {
	v, _ := a.lookup(name)
	return v
}

// say prints a status line to stderr, styled by its leading glyph.
func (a *app) say(msg string) {
	fmt.Fprintln(a.io.Err, ui.Message(msg, a.colorErr()))
}

// ask writes a prompt that is answered on the terminal (no newline, no styling).
func (a *app) ask(prompt string) { fmt.Fprint(a.io.Err, prompt) }

// trace writes one plain line of --verbose output (curl's "> " / "< " lines, the
// --summary footer). Unlike say it is never styled by a glyph: it is meant to be
// read as the request itself, and what it prints must already be masked.
func (a *app) trace(format string, args ...any) { fmt.Fprintf(a.io.Err, format+"\n", args...) }

func (a *app) cfgDuration(v string, def time.Duration) time.Duration {
	if v == "" {
		return def
	}
	if d, err := config.Duration(v); err == nil {
		return d
	}
	return def
}

func (a *app) cfgRetry() int {
	if r := a.cfg.Defaults.Retry; r != nil && *r > 0 {
		return *r
	}
	return 0
}

func (a *app) cfgSummary() bool {
	s := a.cfg.Defaults.Summary
	return s != nil && *s
}

// expandAlias rewrites `api <alias> args...` using [alias] from config. A real
// command always wins over an alias of the same name; expansion happens once.
func (a *app) expandAlias(root *cobra.Command, args []string) []string {
	if len(args) == 0 {
		return args
	}
	cmdline, ok := a.cfg.Alias[args[0]]
	if !ok {
		return args
	}
	if c, _, err := root.Find(args[:1]); err == nil && c != root {
		return args
	}
	return append(splitWords(cmdline), args[1:]...)
}

// splitWords splits a command line on spaces, honouring '...' and "..." quotes.
func splitWords(s string) []string {
	var words []string
	var cur strings.Builder
	var quote rune
	inWord := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}

// Run executes the CLI and returns the process exit code.
func Run(ctx context.Context, args []string, stdio IO) int {
	a := &app{io: stdio, store: stdio.Store}
	if a.store == nil {
		a.store = secretstore.Default()
	}
	if cwd, err := os.Getwd(); err == nil {
		loaded, err := config.LoadWith(cwd, a.getenv)
		a.cfg, a.cfgErr = loaded, err
		if err != nil {
			a.say("⚠ config ignored: " + err.Error())
		}
		for _, w := range loaded.Warnings {
			a.say("⚠ " + w)
		}
	}
	root := a.rootCmd()
	root.SetArgs(a.expandAlias(root, args))
	root.SetIn(stdio.In)
	root.SetOut(stdio.Out)
	root.SetErr(stdio.Err)

	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		if ee.Msg != "" {
			a.say(ee.Msg)
		}
		return ee.Code
	}
	a.say("✗ " + err.Error())
	return exitUsage
}

func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "api",
		Short:         "REST + GraphQL client for the terminal",
		Version:       VersionString(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&a.base, "base", "", "base URL (default $API_BASE_URL)")
	root.PersistentFlags().StringVar(&a.token, "token", "", "bearer token (default $API_TOKEN)")
	root.PersistentFlags().BoolVar(&a.debug, "debug", false, "print the equivalent curl command to stderr (or set API_DEBUG=1)")
	pf := root.PersistentFlags()
	pf.StringVar(&a.color, "color", a.envOr("API_COLOR", orDefault(a.cfg.Defaults.Color, "auto")), "colour output: auto, always or never (env API_COLOR, NO_COLOR, JQ_COLORS)")
	pf.BoolVarP(&a.forceColor, "color-output", "C", false, "always colour the output (jq -C)")
	pf.BoolVarP(&a.noColor, "monochrome-output", "M", false, "never colour the output (jq -M)")
	pf.BoolVar(&a.noPager, "no-pager", false, "never page long output (env API_PAGER sets the pager, empty disables)")
	pf.StringVar(&a.envName, "env", "", "use the [env.<name>] profile from config (see: api config show)")
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if a.envName == "" {
			return nil
		}
		if _, ok := a.cfg.Env[a.envName]; !ok {
			known := strings.Join(a.cfg.EnvNames(), ", ")
			if known == "" {
				known = "none configured: see `api config init`"
			}
			return fmt.Errorf("unknown env %q (known: %s)", a.envName, known)
		}
		return nil
	}
	_ = root.RegisterFlagCompletionFunc("env", func(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
		var out []cobra.Completion
		for _, n := range a.cfg.EnvNames() {
			out = append(out, cobra.CompletionWithDesc(n, a.cfg.Env[n].URL))
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	})
	_ = root.RegisterFlagCompletionFunc("color", func(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
		return []cobra.Completion{"auto", "always", "never"}, cobra.ShellCompDirectiveNoFileComp
	})

	// Help lists "get, GET" so the uppercase spelling is discoverable; padding is
	// cobra's rule (widest sibling, at least 11) applied to the longer names.
	cobra.AddTemplateFunc("cmdNames", verbNames)
	cobra.AddTemplateFunc("cmdPad", func(c *cobra.Command) int {
		pad := 11
		if p := c.Parent(); p != nil {
			for _, sib := range p.Commands() {
				if n := len(verbNames(sib)); n > pad {
					pad = n
				}
			}
		}
		return pad
	})
	root.SetUsageTemplate(strings.ReplaceAll(root.UsageTemplate(), "{{rpad .Name .NamePadding }}", "{{rpad (cmdNames .) (cmdPad .)}}"))

	// Cobra completes command names but not aliases, so GET/POST/... would never
	// be offered. Not after a lowercase letter, which cannot match them anyway.
	root.ValidArgsFunction = func(_ *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) > 0 || (toComplete != "" && (toComplete[0] < 'A' || toComplete[0] > 'Z')) {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var out []cobra.Completion
		for _, c := range root.Commands() {
			for _, al := range c.Aliases {
				if al == strings.ToUpper(al) && strings.HasPrefix(al, toComplete) {
					out = append(out, cobra.CompletionWithDesc(al, c.Short))
				}
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}

	root.AddCommand(
		a.verbCmd("get", "GET", false, "GET a path", "GET"),
		a.verbCmd("del", "DELETE", false, "DELETE a path", "delete", "DELETE"),
		a.verbCmd("post", "POST", true, "POST a JSON body", "POST"),
		a.verbCmd("put", "PUT", true, "PUT a JSON body", "PUT"),
		a.verbCmd("patch", "PATCH", true, "PATCH with a JSON body", "PATCH"),
		a.formCmd(),
		a.gqlCmd(),
		a.loginCmd(),
		a.logoutCmd(),
		a.schemaCmd(),
		a.openapiCmd(),
		a.envCmd(),
		a.configCmd(),
		a.secretCmd(),
		a.doctorCmd(),
		a.shellInitCmd(),
		a.diffCmd(),
		a.runCmd(),
		a.importCmd(),
		a.historyCmd(),
		a.requestsCmd(),
		a.cacheCmd(),
		a.againCmd(),
		a.jsonCmd(),
		a.watchCmd(),
		a.headersCmd(),
		a.timeCmd(),
	)
	return root
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// verbNames is the name shown in help: "get, GET" for a verb command (the
// uppercase aliases), the plain name for everything else.
func verbNames(c *cobra.Command) string {
	for _, al := range c.Aliases {
		if al == strings.ToUpper(al) {
			return c.Name() + ", " + al
		}
	}
	return c.Name()
}
