package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"cybercat/api/internal/gqlquery"
	"cybercat/api/internal/jsonbody"

	"github.com/spf13/cobra"
)

// gqlError returns the first GraphQL error message of a response, if any.
func gqlError(body []byte) string {
	var r struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &r) != nil || len(r.Errors) == 0 {
		return ""
	}
	msg := r.Errors[0].Message
	if n := len(r.Errors); n > 1 {
		msg += fmt.Sprintf(" (+%d more)", n-1)
	}
	return msg
}

// gqlPayload turns "<query|@file|-> [vars-json] [.filter]" plus -V pairs into
// the JSON request body and the filter. The query comes from stdin when the
// first argument is a filter (or "-"); @file reads a file.
func (a *app) gqlPayload(args, vars []string, resolve jsonbody.Resolver) (payload, filter string, err error) {
	query := ""
	rest := args
	if len(args) > 0 && !isFilter(args[0]) {
		query, rest = args[0], args[1:]
	}
	switch {
	case query == "" || query == "-":
		if isTerminal(a.io.In) && query == "" {
			return "", "", fmt.Errorf("missing <query> (or pipe it on stdin)")
		}
		q, err := readAll(a.io.In)
		if err != nil {
			return "", "", err
		}
		query = q
	case strings.HasPrefix(query, "@"):
		b, err := os.ReadFile(query[1:])
		if err != nil {
			return "", "", err
		}
		query = string(b)
	}
	if strings.TrimSpace(query) == "" {
		return "", "", fmt.Errorf("empty query")
	}

	filter, varsJSON := ".", ""
	for _, x := range rest {
		if isFilter(x) {
			filter = x
		} else if varsJSON == "" {
			varsJSON = x
		} else {
			return "", "", fmt.Errorf("unexpected argument %q", x)
		}
	}
	if len(vars) > 0 {
		if varsJSON != "" {
			return "", "", fmt.Errorf("use either a vars-json argument or -V, not both")
		}
		built, err := jsonbody.BuildWith(vars, false, true, resolve)
		if err != nil {
			return "", "", err
		}
		varsJSON = built
	}
	if varsJSON == "" {
		varsJSON = "{}"
	}
	if !json.Valid([]byte(varsJSON)) {
		return "", "", fmt.Errorf("vars is not valid JSON: %s", varsJSON)
	}

	payload, err = gqlquery.Body(query, varsJSON)
	return payload, filter, err
}

// gqlCall is what `api gql` was asked to send, shared by the one-shot request,
// the subscription and the interactive picker.
type gqlCall struct {
	endpoint string   // -e: the service under the GraphQL URL
	payload  string   // the JSON request body (empty for the picker, which builds its own)
	filter   string   // jq filter for the result
	pass     []string // curl arguments after --
}

func (a *app) gqlCmd() *cobra.Command {
	o := &reqOpts{}
	var (
		endpoint string
		vars     []string
		strict   bool
		pick     bool
		pf       pickFlags
		item     string
		so       subOpts
	)
	cmd := &cobra.Command{
		Use:   "gql [query|@file|-] [vars-json] [.filter] [-- curl args]",
		Short: "GraphQL request against $API_GRAPHQL_URL (or $API_BASE_URL/graphql)",
		Example: `  api gql 'query { me { id } }' '.data.me'
  api gql -e v2/user 'query { me { id } }' '.data.me'
  api gql 'query($id: ID!) { user(id: $id) { name } }' -V id=1 '.data'
  api gql @queries/me.graphql
  echo 'query { me { id } }' | api gql
  api gql                   # on a terminal: pick the endpoint and the query with fzf
  api gql 'subscription { orderUpdated { id status } }' .data.orderUpdated   # WebSocket: one JSON line per event`,
		RunE: func(cmd *cobra.Command, args []string) error {
			before, pass := splitDash(cmd, args)

			// no query on a terminal (or --pick): choose it with fzf
			noQuery := len(before) == 0 || (len(before) == 1 && isFilter(before[0]))
			if pick || item != "" || (noQuery && len(pass) == 0 && isTerminal(a.io.In)) {
				filter := ""
				if len(before) == 1 && isFilter(before[0]) {
					filter = before[0]
				}
				return a.gqlInteractive(cmd.Context(), o, pf, gqlCall{endpoint: endpoint, filter: filter}, item)
			}

			resolver := a.resolverFor(srcUser, a.destOrigin(a.envName, gqlquery.Target(a.session().GraphQLURL, endpoint), o))
			if o.dry {
				resolver = maskRef
			}
			payload, filter, err := a.gqlPayload(before, vars, resolver)
			if err != nil {
				return err
			}

			if q := gqlquery.Text(payload); gqlquery.IsSubscription(q) {
				if o.viewer != "" {
					return &ExitError{Code: exitUsage, Msg: "✗ --viewer does not work with a subscription (events stream as JSON lines: pipe them to a viewer)"}
				}
				so.strict = strict
				return a.gqlSubscribe(cmd.Context(), cmd, o, &so, gqlCall{endpoint: endpoint, payload: payload, filter: filter, pass: pass})
			}

			target := gqlquery.Target(a.session().GraphQLURL, endpoint)
			spec, err := a.newSpec("POST", target, o)
			if err != nil {
				return err
			}
			setJSONBody(&spec, payload)

			if o.dry || len(pass) > 0 {
				return a.dispatch(cmd.Context(), spec, o, filter, "", pass)
			}
			resp, err := a.send(cmd.Context(), spec, o)
			if err != nil {
				return err
			}
			if err := a.finish(resp, o, filter, nil); err != nil {
				return err
			}
			if msg := gqlError(resp.Body); strict && msg != "" {
				return &ExitError{Code: exitGraphQL, Msg: "✗ GraphQL: " + msg}
			}
			return nil
		},
	}
	a.bindFlags(o, cmd, false)
	a.bindViewerFlag(cmd, o)
	f := cmd.Flags()
	f.StringVarP(&endpoint, "endpoint", "e", "", "path appended to the GraphQL base, or a full URL")
	f.StringArrayVarP(&vars, "var", "V", nil, "variable key=value or key:=json (repeatable)")
	f.BoolVar(&pick, "pick", false, "choose the endpoint and the query with fzf, edit the generated query, then run it (default with no query on a terminal)")
	f.BoolVar(&pf.yes, "yes", false, "with --pick: do not ask before running a mutation")
	f.BoolVar(&pf.noSave, "no-save", false, "with --pick: do not keep the request under "+requestsDir+"/ (so `api again` cannot repeat it)")
	f.StringVar(&item, "item", "", `implies --pick: use this "<query|mutation> <name>" instead of choosing it with fzf (for tv, editors)`)
	f.BoolVar(&strict, "strict", false, "exit 4 when the response has GraphQL errors")
	_ = cmd.RegisterFlagCompletionFunc("endpoint", a.endpointCompletion)
	bindSubscribeFlags(cmd, &so)
	return cmd
}

// formCmd is multipart / urlencoded POST: no JSON headers.
func (a *app) formCmd() *cobra.Command {
	o := &reqOpts{}
	var fields, data []string
	cmd := &cobra.Command{
		Use:   "form <path> [.filter] [-- curl args]",
		Short: "POST multipart (-F) or urlencoded (-d) form data",
		Example: `  api form /upload -F file=@photo.jpg -F type=avatar
  api form /login -d email=x@x.com -d password=secret`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			before, pass := splitDash(cmd, args)
			target, _, filter, err := splitPositionals(before, false)
			if err != nil {
				return err
			}
			if len(fields) == 0 && len(data) == 0 {
				return fmt.Errorf("nothing to send: pass -F key=value / key=@file or -d key=value")
			}
			spec, err := a.newSpec("POST", target, o)
			if err != nil {
				return err
			}
			spec.Fields, spec.Data = fields, data
			return a.dispatch(cmd.Context(), spec, o, filter, target, pass)
		},
		ValidArgsFunction: a.pathCompletion,
	}
	a.bindFlags(o, cmd, false)
	a.bindViewerFlag(cmd, o)
	cmd.Flags().StringArrayVarP(&fields, "form", "F", nil, "multipart field key=value or key=@file (repeatable)")
	cmd.Flags().StringArrayVarP(&data, "data", "d", nil, "urlencoded field key=value (repeatable)")
	return cmd
}
