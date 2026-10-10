package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"cybercat/api/internal/curlimport"
	"cybercat/api/internal/render"
	"cybercat/api/internal/ui"

	"github.com/spf13/cobra"
)

// importCmd converts a pasted curl command into a .http request. It only
// PRINTS (or appends to a file you name): it sends nothing, resolves no
// credential and never changes the session, so it cannot conflict with
// `env load` / `env use`.
func (a *app) importCmd() *cobra.Command {
	f := &importFlags{}
	cmd := &cobra.Command{
		Use:   "import [curl command | -]",
		Short: "Convert a copied curl command into a .http request (prints; sends nothing)",
		Long: `Reads a curl command (browser DevTools "Copy as cURL") from stdin, or from the
argument, and prints one .http request for "api run".

  pbpaste | api import
  pbpaste | api import --out login.http      append to a file (never overwrites)

Prefer stdin: a DevTools command contains single quotes, tokens and cookies, and
an argument would end up in your shell history.

What it does:
  - the server becomes {{graphql}} / {{base}} when it is the active one, so the
    file follows "api env use"; the matching envs are listed in a comment
  - a GraphQL body ({"query": ...}) is detected and named after the operation
  - Authorization: Bearer <token> becomes {{token}} (your session token); an
    empty one ("Bearer null") is dropped
  - anything secret-looking (headers, cookies, JSON fields, query parameters)
    becomes {{$env API_NAME}}: read from your shell when the file runs, never
    written to the file
  - what is left out is listed WITH ITS REASON (browser, cache, tracing, the
    browser's cookie, already sent by api, already in your env profile); a
    header that is not recognised is kept but flagged ("# check:") so you can
    delete it if the request works without it. --minimal drops those too, and
    --keep NAME keeps one that was dropped

It refuses what would let pasted text change the file's structure (line breaks,
{{ ... }}), pipes and redirects, and flags it cannot translate (-F, @file).`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return a.importCurl(args, f) },
	}
	cmd.Flags().BoolVar(&f.minimal, "minimal", false, "also drop headers that are not recognised (keep only what is known to matter)")
	cmd.Flags().StringSliceVar(&f.keep, "keep", nil, "keep these headers even if they would be dropped (comma separated)")
	cmd.Flags().StringVar(&f.out, "out", "", "append the request to this .http file (created if missing, never overwritten)")
	return cmd
}

// importFlags are the options of `api import`.
type importFlags struct {
	out     string
	minimal bool
	keep    []string
}

// importCurl converts the pasted curl command and prints it or appends it to a file.
func (a *app) importCurl(args []string, f *importFlags) error {
	input, err := a.importInput(args)
	if err != nil {
		return err
	}
	req, err := curlimport.Parse(input)
	if err != nil {
		return &ExitError{Code: exitUsage, Msg: "✗ " + err.Error()}
	}
	ctx := a.importContext(req, f)
	res, err := curlimport.Render(req, ctx)
	if err != nil {
		return &ExitError{Code: exitUsage, Msg: "✗ " + err.Error()}
	}
	if f.out == "" {
		fmt.Fprint(a.io.Out, ui.HTTP(res.HTTP, a.colorOut(), func(b string) string {
			return string(render.Colorize([]byte(b), render.PaletteFromEnv()))
		}))
		return nil
	}
	name, added, err := appendHTTP(f.out, res.HTTP)
	if err != nil {
		return &ExitError{Code: exitGeneric, Msg: "✗ " + err.Error()}
	}
	if added {
		a.say("✓ appended to " + f.out + " as \"" + name + "\"")
	} else {
		a.say("✓ already in " + f.out + " as \"" + name + "\" (nothing added)")
	}
	for _, v := range res.Vars {
		a.say("  needs " + v + " in your shell when you run it")
	}
	a.say("  run it: api run " + shellWord(f.out) + " " + shellWord(name))
	return nil
}

// importContext tells the converter what the session and the config already
// provide, so the request does not repeat it.
func (a *app) importContext(req curlimport.Request, f *importFlags) curlimport.Context {
	s := a.session()
	ctx := curlimport.Context{Base: s.BaseURL, GraphQL: s.GraphQLURL}
	ctx.Minimal = f.minimal
	ctx.Keep = map[string]bool{}
	for _, h := range f.keep {
		ctx.Keep[strings.ToLower(strings.TrimSpace(h))] = true
	}
	// a header the matching env profile already sends is not needed here
	ctx.Redundant = map[string]string{}
	dest := origin(req.URL)
	for name, p := range a.cfg.Env {
		if dest != "" && origin(p.URL) == dest {
			ctx.EnvNames = append(ctx.EnvNames, name)
			for k, v := range p.Headers {
				ctx.Redundant[strings.ToLower(k)] = v
			}
		}
	}
	sort.Strings(ctx.EnvNames)
	return ctx
}

// importInput is the curl text: the arguments joined, or stdin for no argument
// or "-". A terminal with nothing piped is a usage error, not a hang.
func (a *app) importInput(args []string) (string, error) {
	if len(args) > 0 && !(len(args) == 1 && args[0] == "-") {
		return strings.Join(args, " "), nil
	}
	if ui.IsTerminal(a.io.In) {
		return "", &ExitError{Code: exitUsage, Msg: "✗ nothing to import: pipe the command in, e.g. pbpaste | api import"}
	}
	data, err := io.ReadAll(io.LimitReader(a.io.In, 1<<20))
	if err != nil {
		return "", &ExitError{Code: exitGeneric, Msg: "✗ " + err.Error()}
	}
	return string(data), nil
}

// appendHTTP adds a request block to a file, separated by a blank line. A block
// that is already there is not added again, and a different request with the
// same name gets a number ("GET /captures 2"), so `api run file "name"` stays
// unambiguous. It returns the name the block has in the file.
func appendHTTP(path, block string) (name string, added bool, err error) {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", false, err
	}
	name = strings.TrimSpace(strings.TrimPrefix(strings.SplitN(block, "\n", 2)[0], "###"))
	if strings.Contains(string(existing), block) {
		return name, false, nil
	}
	taken := map[string]bool{}
	for _, l := range strings.Split(string(existing), "\n") {
		if rest, ok := strings.CutPrefix(l, "###"); ok {
			taken[strings.TrimSpace(rest)] = true
		}
	}
	if taken[name] {
		base := name
		for n := 2; taken[name]; n++ {
			name = fmt.Sprintf("%s %d", base, n)
		}
		block = "### " + name + strings.TrimPrefix(block, "### "+base)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	if len(existing) > 0 {
		block = "\n" + block
	}
	_, err = f.WriteString(block)
	return name, err == nil, err
}
