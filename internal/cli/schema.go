package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

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

func (a *app) ugqlCacheDir() string {
	if d := a.getenv("UGQL_CACHE_DIR"); d != "" {
		return d
	}
	home := a.getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".cache", "ugql")
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
	cmd := &cobra.Command{
		Use:               "type <endpoint> <Type>",
		Short:             "Show the fields (with args), input fields or enum values of a type",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: a.schemaEndpointArg,
		RunE: func(cmd *cobra.Command, args []string) error {
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
	return cmd
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

// syncResult is what one concurrent fetch reports back.
type syncResult struct {
	label string
	data  []byte
	count int
	err   string
}

func (a *app) schemaSyncCmd() *cobra.Command {
	o := &reqOpts{}
	var prefix string
	cmd := &cobra.Command{
		Use:   "sync [endpoint...]",
		Short: "Fetch every service's schema into the local cache the fzf / tv browsers read",
		Long: `Fetches each endpoint's schema (concurrently) into $UGQL_CACHE_DIR (default
~/.cache/ugql) and rebuilds what the browsers read:

  <endpoint>.json   the introspection result
  index.txt         "<endpoint> <kind> <name>" lines (the list)
  preview.jq        the detail program (jq -r --arg kind K --arg n N -f preview.jq FILE)
  .env              the API_ENV the cache was fetched with

With no endpoint, the env's "endpoints" from the config are used. Names become
file names, so only letters, digits, _ - . are accepted (a config file cannot
make this write outside the cache). Needs a login: this sends your token.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			names := args
			if len(names) == 0 {
				names = a.schemaProfile().Endpoints
			}
			if len(names) == 0 {
				return &ExitError{Code: exitUsage, Msg: "✗ no endpoints: name them, or set endpoints = [...] under [env.<name>] in your config"}
			}

			type job struct {
				label string
				spec  httpc.Spec
			}
			var jobs []job
			seen := map[string]bool{}
			for _, n := range names {
				label, ok := schemaLabel(n)
				if !ok {
					a.say(fmt.Sprintf("✗ %s (skipped: not a usable endpoint name)", n))
					continue
				}
				if seen[label] {
					continue
				}
				seen[label] = true
				body, err := gqlBody(schema.SchemaQuery, "{}")
				if err != nil {
					return err
				}
				// specs are built here, one at a time: building one reads config and
				// may print a warning; only the network calls run concurrently
				spec, err := a.newSpec("POST", gqlTarget(a.session().GraphQLURL, schemaEndpoint(a.effectivePrefix(prefix), n)), o)
				if err != nil {
					return err
				}
				setJSONBody(&spec, body)
				jobs = append(jobs, job{label, spec})
			}
			if o.dry {
				for _, j := range jobs {
					fmt.Fprintln(a.io.Out, a.curlLine(j.spec, nil))
				}
				return nil
			}

			cache := a.ugqlCacheDir()
			if err := os.MkdirAll(cache, 0o755); err != nil {
				return err
			}

			results := make(chan syncResult)
			sem := make(chan struct{}, 6)
			var wg sync.WaitGroup
			for _, j := range jobs {
				wg.Add(1)
				go func(j job) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					results <- fetchSchema(cmd.Context(), j.label, j.spec, o.retry)
				}(j)
			}
			go func() { wg.Wait(); close(results) }()

			fetched := 0
			for r := range results {
				if r.err != "" {
					a.say(fmt.Sprintf("✗ %s (skipped: %s)", r.label, r.err))
					continue
				}
				if err := writeFileAtomic(filepath.Join(cache, r.label+".json"), r.data, 0o644); err != nil {
					a.say(fmt.Sprintf("✗ %s (skipped: %v)", r.label, err))
					continue
				}
				fetched++
				a.say(fmt.Sprintf("✓ %s (%d types)", r.label, r.count))
			}

			if err := writeFileAtomic(filepath.Join(cache, "preview.jq"), []byte(schema.PreviewProgram), 0o644); err != nil {
				return err
			}
			var lines []string
			files, _ := filepath.Glob(filepath.Join(cache, "*.json"))
			for _, f := range files {
				data, err := os.ReadFile(f)
				if err != nil {
					continue
				}
				s, err := schema.Parse(data)
				if err != nil {
					continue
				}
				lines = append(lines, s.Index(strings.TrimSuffix(filepath.Base(f), ".json"))...)
			}
			schema.SortIndex(lines)
			if len(lines) == 0 {
				return &ExitError{Code: exitGeneric, Msg: "✗ nothing cached: are you logged in? (api login)"}
			}
			if err := writeFileAtomic(filepath.Join(cache, "index.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
				return err
			}
			envName := a.session().Name
			if envName == "" {
				envName = "?"
			}
			if err := writeFileAtomic(filepath.Join(cache, ".env"), []byte(envName+"\n"), 0o644); err != nil {
				return err
			}
			a.say(fmt.Sprintf("✓ %d items in %s", len(lines), cache))
			if fetched == 0 {
				return &ExitError{Code: exitGeneric, Msg: "✗ no schema could be fetched: this cache is stale"}
			}
			return nil
		},
	}
	a.bindSchemaFlags(o, cmd, &prefix)
	return cmd
}

// fetchSchema runs one introspection request. It touches no shared state.
func fetchSchema(ctx context.Context, label string, spec httpc.Spec, retries int) syncResult {
	resp, err := httpc.Do(ctx, spec, retries)
	if err != nil {
		return syncResult{label: label, err: shortErr(err)}
	}
	if resp.Status >= 400 {
		hint := ""
		if resp.Status == 401 || resp.Status == 403 {
			hint = " (token rejected? api login)"
		}
		return syncResult{label: label, err: fmt.Sprintf("HTTP %d%s", resp.Status, hint)}
	}
	var r struct {
		Data struct {
			Schema json.RawMessage `json:"__schema"`
		} `json:"data"`
	}
	if json.Unmarshal(resp.Body, &r) != nil || len(r.Data.Schema) == 0 {
		msg := "no __schema in the response"
		if gm := gqlError(resp.Body); gm != "" {
			msg = gm
		}
		return syncResult{label: label, err: msg}
	}
	s, err := schema.Parse(r.Data.Schema)
	if err != nil {
		return syncResult{label: label, err: err.Error()}
	}
	// keep the server's key order and numbers; only the layout is normalised
	var pretty []byte
	if pretty, err = indentJSON(r.Data.Schema); err != nil {
		return syncResult{label: label, err: err.Error()}
	}
	return syncResult{label: label, data: pretty, count: len(s.Types)}
}

func indentJSON(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// writeFileAtomic replaces path via a temp file in the same directory.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
