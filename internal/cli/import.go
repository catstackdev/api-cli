package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"cybercat/api/internal/curlimport"
	"cybercat/api/internal/ui"

	"github.com/spf13/cobra"
)

// importCmd converts a pasted curl command into a .http request. It only
// PRINTS (or appends to a file you name): it sends nothing, resolves no
// credential and never changes the session, so it cannot conflict with
// `env load` / `env use`.
func (a *app) importCmd() *cobra.Command {
	var out string
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
  - browser noise (sec-*, user-agent, referer, ...) is dropped and listed

It refuses what would let pasted text change the file's structure (line breaks,
{{ ... }}), pipes and redirects, and flags it cannot translate (-F, @file).`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := a.importInput(args)
			if err != nil {
				return err
			}
			req, err := curlimport.Parse(input)
			if err != nil {
				return &ExitError{Code: exitUsage, Msg: "✗ " + err.Error()}
			}
			s := a.session()
			ctx := curlimport.Context{Base: s.BaseURL, GraphQL: s.GraphQLURL}
			dest := origin(req.URL)
			for name, p := range a.cfg.Env {
				if dest != "" && origin(p.URL) == dest {
					ctx.EnvNames = append(ctx.EnvNames, name)
				}
			}
			sort.Strings(ctx.EnvNames)
			res, err := curlimport.Render(req, ctx)
			if err != nil {
				return &ExitError{Code: exitUsage, Msg: "✗ " + err.Error()}
			}
			if out == "" {
				fmt.Fprint(a.io.Out, res.HTTP)
				return nil
			}
			if err := appendHTTP(out, res.HTTP); err != nil {
				return &ExitError{Code: exitGeneric, Msg: "✗ " + err.Error()}
			}
			a.say("✓ appended to " + out)
			for _, v := range res.Vars {
				a.say("  needs " + v + " in your shell when you run it")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "append the request to this .http file (created if missing, never overwritten)")
	return cmd
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

// appendHTTP adds a request block to a file, separated by a blank line.
func appendHTTP(path, block string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		block = "\n" + block
	}
	_, err = f.WriteString(block)
	return err
}
