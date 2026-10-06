package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"cybercat/api/internal/jsonbody"

	"github.com/spf13/cobra"
)

// gqlTarget resolves -e: "" is the base, a full URL is used as-is, anything
// else is appended to $API_GRAPHQL_URL (default "/graphql" on the base URL).
func gqlTarget(graphqlURL, endpoint string) string {
	base := graphqlURL
	if base == "" {
		base = "/graphql"
	}
	switch {
	case endpoint == "":
		return base
	case strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://"):
		return endpoint
	default:
		return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(endpoint, "/")
	}
}

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

	payload, err = gqlBody(query, varsJSON)
	return payload, filter, err
}

// gqlBody is the JSON request body of a GraphQL call.
func gqlBody(query, varsJSON string) (string, error) {
	b, err := json.Marshal(struct {
		Query     string          `json:"query"`
		Variables json.RawMessage `json:"variables"`
	}{query, json.RawMessage(varsJSON)})
	return string(b), err
}

func (a *app) gqlCmd() *cobra.Command {
	o := &reqOpts{}
	var (
		endpoint string
		vars     []string
		strict   bool
		pick     bool
		yes      bool
	)
	cmd := &cobra.Command{
		Use:   "gql [query|@file|-] [vars-json] [.filter] [-- curl args]",
		Short: "GraphQL request against $API_GRAPHQL_URL (or $API_BASE_URL/graphql)",
		Example: `  api gql 'query { me { id } }' '.data.me'
  api gql -e v2/user 'query { me { id } }' '.data.me'
  api gql 'query($id: ID!) { user(id: $id) { name } }' -V id=1 '.data'
  api gql @queries/me.graphql
  echo 'query { me { id } }' | api gql
  api gql                   # on a terminal: pick the endpoint and the query with fzf`,
		RunE: func(cmd *cobra.Command, args []string) error {
			before, pass := splitDash(cmd, args)

			// no query on a terminal (or --pick): choose it with fzf
			noQuery := len(before) == 0 || (len(before) == 1 && isFilter(before[0]))
			if pick || (noQuery && len(pass) == 0 && isTerminal(a.io.In)) {
				filter := ""
				if len(before) == 1 && isFilter(before[0]) {
					filter = before[0]
				}
				return a.gqlInteractive(cmd.Context(), o, endpoint, filter, yes)
			}

			resolver := a.resolverFor(srcUser, a.destOrigin(a.envName, gqlTarget(a.session().GraphQLURL, endpoint), o))
			if o.dry {
				resolver = maskRef
			}
			payload, filter, err := a.gqlPayload(before, vars, resolver)
			if err != nil {
				return err
			}

			target := gqlTarget(a.session().GraphQLURL, endpoint)
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
	f := cmd.Flags()
	f.StringVarP(&endpoint, "endpoint", "e", "", "path appended to the GraphQL base, or a full URL")
	f.StringArrayVarP(&vars, "var", "V", nil, "variable key=value or key:=json (repeatable)")
	f.BoolVar(&pick, "pick", false, "choose the endpoint and the query with fzf, edit the generated query, then run it (default with no query on a terminal)")
	f.BoolVar(&yes, "yes", false, "with --pick: do not ask before running a mutation")
	f.BoolVar(&strict, "strict", false, "exit 4 when the response has GraphQL errors")
	_ = cmd.RegisterFlagCompletionFunc("endpoint", a.endpointCompletion)
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
		ValidArgsFunction: pathCompletion,
	}
	a.bindFlags(o, cmd, false)
	cmd.Flags().StringArrayVarP(&fields, "form", "F", nil, "multipart field key=value or key=@file (repeatable)")
	cmd.Flags().StringArrayVarP(&data, "data", "d", nil, "urlencoded field key=value (repeatable)")
	return cmd
}
