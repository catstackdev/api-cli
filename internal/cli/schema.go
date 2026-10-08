package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"cybercat/api/internal/httpc"
	"cybercat/api/internal/schema"

	"github.com/spf13/cobra"
)

// schemaEndpoint turns "user" into "<prefix>user"; a name with a "/" or a full URL
// passes through (the Upscale shell helper used the prefix "v2/").
func schemaEndpoint(prefix, name string) string {
	if strings.Contains(name, "/") || strings.HasPrefix(name, "http://") || strings.HasPrefix(name, "https://") {
		return name
	}
	return prefix + name
}

// schemaLabel is the cache file name of an endpoint: "v2/user" -> "user".
// URLs have no sensible label and are refused.
func schemaLabel(name string) (string, bool) {
	if strings.HasPrefix(name, "http://") || strings.HasPrefix(name, "https://") {
		return "", false
	}
	label := path.Base(strings.TrimRight(name, "/"))
	return label, schema.ValidLabel(label)
}

// ugqlRoot is the base of the schema cache ($UGQL_CACHE_DIR, default ~/.cache/ugql).
func (a *app) ugqlRoot() string {
	if d := a.getenv("UGQL_CACHE_DIR"); d != "" {
		return d
	}
	home := a.getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".cache", "ugql")
}

// ugqlNested: with the default location every server gets its own directory
// (<root>/<origin key>/), so two projects on different servers never overwrite
// each other's schemas. An explicit $UGQL_CACHE_DIR is the caller's own
// directory (a tv channel's per-project one, a test's) and stays flat.
func (a *app) ugqlNested() bool { return a.getenv("UGQL_CACHE_DIR") == "" }

// ugqlKey names the server the GraphQL requests of this shell go to.
func (a *app) ugqlKey() (string, bool) {
	s := a.session()
	full, err := httpc.ResolveURL(s.BaseURL, gqlTarget(s.GraphQLURL, ""), nil)
	if err != nil {
		return "", false
	}
	return originKey(origin(full))
}

// ugqlCacheDir is where the schemas of the ACTIVE SERVER live ("" when the
// server is not known, so nothing is read or written at the wrong place).
func (a *app) ugqlCacheDir() string {
	if !a.ugqlNested() {
		return a.ugqlRoot()
	}
	key, ok := a.ugqlKey()
	if !ok {
		return ""
	}
	return filepath.Join(a.ugqlRoot(), key)
}

// introspect POSTs a query to the endpoint and returns the response, recovering
// from a rejected token like any other request.
func (a *app) introspect(ctx context.Context, o *reqOpts, endpoint, query, varsJSON string) (httpc.Spec, *httpc.Response, error) {
	body, err := gqlBody(query, varsJSON)
	if err != nil {
		return httpc.Spec{}, nil, err
	}
	spec, err := a.newSpec("POST", gqlTarget(a.session().GraphQLURL, endpoint), o)
	if err != nil {
		return spec, nil, err
	}
	setJSONBody(&spec, body)
	if o.dry {
		return spec, nil, nil
	}
	resp, err := a.sendRecovering(ctx, spec, o)
	return spec, resp, err
}

// dataObject returns .data.<key> of a GraphQL response, or a readable error.
func dataObject(resp *httpc.Response, key string) ([]byte, error) {
	if resp.Status >= 400 {
		return nil, &ExitError{Code: exitHTTP, Msg: fmt.Sprintf("✗ HTTP %d %s", resp.Status, resp.StatusText)}
	}
	var r struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if json.Unmarshal(resp.Body, &r) != nil || len(r.Data[key]) == 0 || string(r.Data[key]) == "null" {
		msg := "no data." + key + " in the response"
		if gm := gqlError(resp.Body); gm != "" {
			msg = gm
		}
		return nil, &ExitError{Code: exitGraphQL, Msg: "✗ introspection failed: " + msg}
	}
	return r.Data[key], nil
}

// printJSON writes ordered JSON through the normal output path (colour, pager).
func (a *app) printJSON(o *reqOpts, data []byte) error {
	return a.finish(&httpc.Response{Status: 200, Body: data}, o, ".", nil)
}

// effectivePrefix is --prefix, or the env's endpoint_prefix when the flag is not given.
func (a *app) effectivePrefix(flag string) string {
	if flag != "" {
		return flag
	}
	return a.schemaProfile().EndpointPrefix
}

func (a *app) schemaEndpointsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "endpoints",
		Short: "List the endpoints configured for the env (endpoints = [...])",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			prof := a.schemaProfile()
			if len(prof.Endpoints) == 0 {
				return &ExitError{Code: exitGeneric, Msg: "✗ no endpoints for this env: api config set env.<name>.endpoints user,auth --global"}
			}
			for _, e := range prof.Endpoints {
				fmt.Fprintln(a.io.Out, e)
			}
			return nil
		},
	}
}

func (a *app) schemaCmd() *cobra.Command {
	o := &reqOpts{}
	var prefix string
	var edit bool
	cmd := &cobra.Command{
		Use:   "schema [endpoint]",
		Short: "Explore a GraphQL service by introspection (names, types, local cache)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			endpoint := ""
			if len(args) == 1 {
				endpoint = args[0]
			}
			return a.schemaPick(cmd.Context(), o, endpoint, prefix, edit)
		},
		ValidArgsFunction: a.schemaEndpointArg,
		Long: `With no subcommand it is a browser: pick an endpoint (from your config's
endpoints), then browse its queries, mutations and types in fzf with a live
preview. Enter prints the item; ctrl-e (or --edit) opens it in your editor
($API_EDITOR, $VISUAL, $EDITOR, or defaults.editor in the global config; nvim
when none is set).

Introspection helpers. <endpoint> is appended to the GraphQL base like
"api gql -e"; with --prefix v2/ a bare name such as "user" means v2/user.

  api schema names user --prefix v2/        # query and mutation names
  api schema type user Query --prefix v2/   # fields (with args), input fields or enum values
  api schema dump user --prefix v2/         # the whole introspection result
  api schema sync --prefix v2/ user auth    # cache schemas for fzf / television (~/.cache/ugql)`,
	}
	cmd.AddCommand(a.schemaNamesCmd(), a.schemaTypeCmd(), a.schemaDumpCmd(), a.schemaSyncCmd(), a.schemaEndpointsCmd(), a.schemaTypesCmd(), a.schemaShowCmd())
	a.bindSchemaFlags(o, cmd, &prefix)
	cmd.Flags().BoolVar(&edit, "edit", false, "open the picked item in your editor instead of printing it")
	return cmd
}

func (a *app) bindSchemaFlags(o *reqOpts, cmd *cobra.Command, prefix *string) {
	f := cmd.Flags()
	a.bindRequestFlags(o, f, false)
	a.bindTransportFlags(o, f)
	f.StringVar(prefix, "prefix", "", `prepended to a bare endpoint name (e.g. "v2/")`)
	_ = cmd.RegisterFlagCompletionFunc("prefix", func(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	})
}

func (a *app) schemaNamesCmd() *cobra.Command {
	o := &reqOpts{}
	var prefix string
	cmd := &cobra.Command{
		Use:               "names <endpoint>",
		Short:             "List the query and mutation names of an endpoint",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.schemaEndpointArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, resp, err := a.introspect(cmd.Context(), o, schemaEndpoint(a.effectivePrefix(prefix), args[0]), schema.NamesQuery, "{}")
			if err != nil {
				return err
			}
			if o.dry {
				fmt.Fprintln(a.io.Out, a.curlLine(spec, nil))
				return nil
			}
			raw, err := dataObject(resp, "__schema")
			if err != nil {
				return err
			}
			names, err := schema.ParseNames(raw)
			if err != nil {
				return &ExitError{Code: exitGraphQL, Msg: "✗ " + err.Error()}
			}
			out, err := schema.Pretty(names)
			if err != nil {
				return err
			}
			return a.printJSON(o, out)
		},
	}
	a.bindSchemaFlags(o, cmd, &prefix)
	return cmd
}

func (a *app) schemaTypeCmd() *cobra.Command {
	o := &reqOpts{}
	var prefix string
	var expand bool
	cmd := &cobra.Command{
		Use:               "type <endpoint> <Type>",
		Short:             "Show the fields (with args), input fields or enum values of a type",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: a.schemaEndpointArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			if expand {
				return a.schemaTypeExpanded(cmd.Context(), o, prefix, args[0], args[1])
			}
			varsJSON, err := json.Marshal(map[string]string{"n": args[1]})
			if err != nil {
				return err
			}
			spec, resp, err := a.introspect(cmd.Context(), o, schemaEndpoint(a.effectivePrefix(prefix), args[0]), schema.TypeQuery, string(varsJSON))
			if err != nil {
				return err
			}
			if o.dry {
				fmt.Fprintln(a.io.Out, a.curlLine(spec, nil))
				return nil
			}
			raw, err := dataObject(resp, "__type")
			if err != nil {
				if ee, ok := err.(*ExitError); ok && strings.Contains(ee.Msg, "no data.__type") {
					return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ no type %q on %s", args[1], args[0])}
				}
				return err
			}
			var t schema.Type
			if err := json.Unmarshal(raw, &t); err != nil {
				return &ExitError{Code: exitGraphQL, Msg: "✗ unexpected __type result: " + err.Error()}
			}
			out, err := schema.Pretty(schema.Render(t))
			if err != nil {
				return err
			}
			return a.printJSON(o, out)
		},
	}
	a.bindSchemaFlags(o, cmd, &prefix)
	cmd.Flags().BoolVar(&expand, "expand", false, `also show the types its fields point at, one level ("category: TagCategory" -> what a TagCategory is)`)
	return cmd
}

// schemaTypeExpanded is `schema type --expand`: it needs the whole schema (the types
// the fields point at are not known until it is read), so it asks for that once.
func (a *app) schemaTypeExpanded(ctx context.Context, o *reqOpts, prefix, endpoint, name string) error {
	spec, resp, err := a.introspect(ctx, o, schemaEndpoint(a.effectivePrefix(prefix), endpoint), schema.SchemaQuery, "{}")
	if err != nil {
		return err
	}
	if o.dry {
		fmt.Fprintln(a.io.Out, a.curlLine(spec, nil))
		return nil
	}
	raw, err := dataObject(resp, "__schema")
	if err != nil {
		return err
	}
	s, err := schema.Parse(raw)
	if err != nil {
		return &ExitError{Code: exitGraphQL, Msg: "✗ introspection failed for " + endpoint + ": " + err.Error()}
	}
	t, ok := s.FindType(name)
	if !ok {
		return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ no type %q on %s", name, endpoint)}
	}
	out, err := schema.Pretty(s.Expand(t))
	if err != nil {
		return err
	}
	return a.printJSON(o, out)
}

func (a *app) schemaTypesCmd() *cobra.Command {
	o := &reqOpts{}
	var prefix string
	var kinds bool
	cmd := &cobra.Command{
		Use:               "types <endpoint>",
		Short:             "List the type names of an endpoint, root query/mutation types first",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.schemaEndpointArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, resp, err := a.introspect(cmd.Context(), o, schemaEndpoint(a.effectivePrefix(prefix), args[0]), schema.SchemaQuery, "{}")
			if err != nil {
				return err
			}
			if o.dry {
				fmt.Fprintln(a.io.Out, a.curlLine(spec, nil))
				return nil
			}
			raw, err := dataObject(resp, "__schema")
			if err != nil {
				return err
			}
			s, err := schema.Parse(raw)
			if err != nil {
				return &ExitError{Code: exitGraphQL, Msg: "✗ introspection failed for " + args[0] + ": " + err.Error()}
			}
			for _, line := range s.TypeList(kinds) {
				fmt.Fprintln(a.io.Out, line)
			}
			return nil
		},
	}
	a.bindSchemaFlags(o, cmd, &prefix)
	cmd.Flags().BoolVar(&kinds, "kinds", false, "prefix each name with its kind (query, mutation, type, input, enum...)")
	return cmd
}

func (a *app) schemaDumpCmd() *cobra.Command {
	o := &reqOpts{}
	var prefix string
	cmd := &cobra.Command{
		Use:               "dump <endpoint>",
		Short:             "Print the whole introspection result (the __schema object)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.schemaEndpointArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, resp, err := a.introspect(cmd.Context(), o, schemaEndpoint(a.effectivePrefix(prefix), args[0]), schema.SchemaQuery, "{}")
			if err != nil {
				return err
			}
			if o.dry {
				fmt.Fprintln(a.io.Out, a.curlLine(spec, nil))
				return nil
			}
			raw, err := dataObject(resp, "__schema")
			if err != nil {
				return err
			}
			if _, err := schema.Parse(raw); err != nil {
				return &ExitError{Code: exitGraphQL, Msg: "✗ introspection failed for " + args[0] + ": " + err.Error()}
			}
			return a.finish(&httpc.Response{Status: 200, Body: raw}, o, ".", nil)
		},
	}
	a.bindSchemaFlags(o, cmd, &prefix)
	return cmd
}

func (a *app) schemaEndpointArg(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return a.endpointCompletion(cmd, args, toComplete)
}
