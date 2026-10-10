package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cybercat/api/internal/gqlquery"
	"cybercat/api/internal/httpc"
	"cybercat/api/internal/jsondiff"
	"cybercat/api/internal/render"
	"cybercat/api/internal/ui"

	"github.com/spf13/cobra"
)

// writeOp matches a GraphQL mutation/subscription operation keyword.
// side is the outcome of the request against one env.
type side struct {
	env  string
	url  string
	resp *httpc.Response
}

func (a *app) diffCmd() *cobra.Command {
	o := &reqOpts{}
	var (
		envs     []string
		ignore   []string
		gql      bool
		endpoint string
		vars     []string
		arrayKey string
		asJSON   bool
	)
	cmd := &cobra.Command{
		Use:   "diff --envs a,b <path> [.filter]   |   diff --envs a,b --gql [-e endpoint] <query> [-V k=v] [.filter]",
		Short: "Send the same request to two envs and show how the JSON differs",
		Long: `Sends one read-only request to two configured envs ([env.<name>] in
the api config) and prints a structural diff, with jq-style paths. Each env
uses its own token (token_env, or the shell session's token for the env you are
logged in to): one env's token is never sent to the other.

Only GET and GraphQL queries are supported. Mutations and subscriptions are
refused, because diff would run them against both envs.

Exit status: 0 identical, 1 different (like diff(1)), other codes for errors.`,
		Example: `  api diff --envs dev,thien /rest/v2/smart-content/<id> .data
  api diff --envs dev,thien /rest/v2/smart-content '.data | length'
  api diff --envs dev,thien --ignore .data.updatedAt --ignore .meta /rest/v2/x
  api diff --envs dev,thien --gql -e v2/user 'query { me { id name } }' .data`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(envs) != 2 || envs[0] == "" || envs[1] == "" || envs[0] == envs[1] {
				return fmt.Errorf("--envs needs two different envs, e.g. --envs dev,thien")
			}
			for _, e := range envs {
				if _, ok := a.cfg.Env[e]; !ok {
					return fmt.Errorf("unknown env %q (known: %s): see `api config show`", e, strings.Join(a.cfg.EnvNames(), ", "))
				}
			}

			var (
				method, target string
				body, filter   string
			)
			if gql {
				payload, f, err := a.gqlPayload(args, vars, a.resolverFor(srcUser,
					a.destOrigin(envs[0], gqlquery.Target(a.resolveForGraphQL(envs[0]), endpoint), o),
					a.destOrigin(envs[1], gqlquery.Target(a.resolveForGraphQL(envs[1]), endpoint), o)))
				if err != nil {
					return err
				}
				var req struct {
					Query string `json:"query"`
				}
				_ = json.Unmarshal([]byte(payload), &req)
				if !gqlquery.IsReadOnly(req.Query) {
					return fmt.Errorf("refusing to diff a mutation or subscription: it would run against both envs")
				}
				method, body, filter = "POST", payload, f
			} else {
				t, _, f, err := splitPositionals(args, false)
				if err != nil {
					return err
				}
				method, target, filter = "GET", t, f
			}

			// Build both specs first (each resolves its own env, URL, token and
			// headers), then send.
			// Build both specs first (each resolves its own env, URL, token and
			// headers), then send.
			var specs [2]httpc.Spec
			for i, e := range envs {
				t := target
				if gql {
					rs, _ := a.resolveFor(e)
					t = gqlquery.Target(rs.GraphQLURL, endpoint)
				}
				spec, err := a.newSpecFor(e, method, t, o)
				if err != nil {
					return fmt.Errorf("%s: %w", e, err)
				}
				if gql {
					setJSONBody(&spec, body)
				}
				specs[i] = spec
			}
			var sides [2]side
			for i, e := range envs {
				resp, err := a.send(cmd.Context(), specs[i], o)
				if err != nil {
					if ee, ok := err.(*ExitError); ok {
						ee.Msg = "✗ " + e + ": " + strings.TrimPrefix(ee.Msg, "✗ ")
					}
					return err
				}
				sides[i] = side{e, specs[i].URL, resp}
			}
			return a.reportDiff(sides, filter, jsondiff.Options{Ignore: ignore, ArrayKey: arrayKey}, asJSON)
		},
		ValidArgsFunction: a.pathCompletion,
	}
	a.bindFlags(o, cmd, false)
	f := cmd.Flags()
	f.StringSliceVar(&envs, "envs", nil, "the two envs to compare, e.g. dev,thien (from `api config show`)")
	f.StringArrayVar(&ignore, "ignore", nil, "ignore a path and everything below it (repeatable), e.g. .data.updatedAt")
	f.StringVar(&arrayKey, "array-key", "", "match array elements by this field (e.g. id) instead of by position: a reordered list is not a difference")
	f.BoolVar(&asJSON, "json", false, "print the result as JSON (same exit status)")
	f.BoolVar(&gql, "gql", false, "compare a GraphQL query instead of a GET")
	f.StringVarP(&endpoint, "endpoint", "e", "", "(--gql) path appended to the GraphQL base, or a full URL")
	f.StringArrayVarP(&vars, "var", "V", nil, "(--gql) variable key=value or key:=json (repeatable)")
	_ = cmd.MarkFlagRequired("envs")
	_ = cmd.RegisterFlagCompletionFunc("envs", func(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
		var out []cobra.Completion
		for _, n := range a.cfg.EnvNames() {
			out = append(out, n)
		}
		return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	})
	_ = cmd.RegisterFlagCompletionFunc("endpoint", a.endpointCompletion)
	return cmd
}

func (a *app) reportDiff(sides [2]side, filter string, opts jsondiff.Options, asJSON bool) error {
	color := a.colorOut()
	out := a.io.Out

	var changes []jsondiff.Change
	if sides[0].resp.Status != sides[1].resp.Status {
		changes = append(changes, jsondiff.Change{Path: "HTTP status", Kind: jsondiff.Changed, A: sides[0].resp.Status, B: sides[1].resp.Status})
	}

	va, errA := render.Decode(sides[0].resp.Body, filter)
	vb, errB := render.Decode(sides[1].resp.Body, filter)
	switch {
	case errA == nil && errB == nil:
		changes = append(changes, jsondiff.DiffWith(va, vb, opts)...)
	case strings.TrimSpace(string(sides[0].resp.Body)) != strings.TrimSpace(string(sides[1].resp.Body)):
		changes = append(changes, jsondiff.Change{Path: "body (not JSON)", Kind: jsondiff.Changed,
			A: firstLine(sides[0].resp.Body), B: firstLine(sides[1].resp.Body)})
	}

	if asJSON {
		return a.diffJSON(sides, changes)
	}
	nameW := 0
	for _, s := range sides {
		if len(s.env) > nameW {
			nameW = len(s.env)
		}
	}
	for i, s := range sides {
		mark := "---"
		if i == 1 {
			mark = "+++"
		}
		fmt.Fprintf(out, "%s %-*s  %s  %s  %s\n", mark, nameW, s.env, s.url,
			ui.StatusLine("HTTP", s.resp.Status, "", color), ui.Paint(s.resp.Duration.Round(time.Millisecond).String(), ui.Dim, color))
	}

	if len(changes) == 0 {
		a.say("✓ identical")
		return nil
	}
	pathW := 0
	for _, c := range changes {
		if len(c.Path) > pathW {
			pathW = len(c.Path)
		}
	}
	for _, c := range changes {
		var glyph, code, detail string
		switch c.Kind {
		case jsondiff.Added:
			glyph, code, detail = "+", ui.Green, jsondiff.Format(c.B, 100)
		case jsondiff.Removed:
			glyph, code, detail = "-", ui.Red, jsondiff.Format(c.A, 100)
		default:
			glyph, code = "~", ui.Yellow
			detail = jsondiff.Format(c.A, 60) + " → " + jsondiff.Format(c.B, 60)
		}
		fmt.Fprintf(out, "%s %s  %s\n", ui.Paint(glyph, code, color), ui.Paint(fmt.Sprintf("%-*s", pathW, c.Path), code, color), detail)
	}
	return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ %d difference(s)", len(changes))}
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// resolveForGraphQL is the GraphQL base URL of an env ("" falls back to /graphql).
func (a *app) resolveForGraphQL(envName string) string {
	s, _ := a.resolveFor(envName)
	return s.GraphQLURL
}

// diffJSON is the machine-readable report. The exit status matches the human one.
func (a *app) diffJSON(sides [2]side, changes []jsondiff.Change) error {
	type end struct {
		Env    string `json:"env"`
		URL    string `json:"url"`
		Status int    `json:"status"`
	}
	type change struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
		A    any    `json:"a"`
		B    any    `json:"b"`
	}
	rep := struct {
		Identical bool     `json:"identical"`
		Left      end      `json:"left"`
		Right     end      `json:"right"`
		Changes   []change `json:"changes"`
	}{
		Identical: len(changes) == 0,
		Left:      end{sides[0].env, sides[0].url, sides[0].resp.Status},
		Right:     end{sides[1].env, sides[1].url, sides[1].resp.Status},
		Changes:   []change{},
	}
	for _, c := range changes {
		rep.Changes = append(rep.Changes, change{c.Path, c.Kind.String(), c.A, c.B})
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(a.io.Out, string(b))
	if len(changes) > 0 {
		return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ %d difference(s)", len(changes))}
	}
	return nil
}
