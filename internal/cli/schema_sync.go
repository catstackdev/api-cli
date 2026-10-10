package cli

import (
	"bytes"
	"context"
	"cybercat/api/internal/gqlquery"
	"cybercat/api/internal/httpc"
	"cybercat/api/internal/schema"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/cobra"
)

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
		Long: `Fetches each endpoint's schema (concurrently) into $UGQL_CACHE_DIR, or by
default into ~/.cache/ugql/<server>/ (one directory per server, so projects on
different servers do not overwrite each other; ~/.cache/ugql/current points at
the last one synced, for the fzf / tv browsers), and rebuilds what they read:

  <endpoint>.json   the introspection result
  index.txt         "<endpoint> <kind> <name>" lines (the list)
  preview.jq        the detail program (jq -r --arg kind K --arg n N -f preview.jq FILE)
  .env              the API_ENV the cache was fetched with

With no endpoint, the env's "endpoints" from the config are used. Names become
file names, so only letters, digits, _ - . are accepted (a config file cannot
make this write outside the cache). Needs a login: this sends your token.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.syncSchemas(cmd.Context(), args, o, prefix)
		},
	}
	a.bindSchemaFlags(o, cmd, &prefix)
	return cmd
}

// schemaJob is one introspection request, built before any of them is sent.
type schemaJob struct {
	label string
	spec  httpc.Spec
}

// syncSchemas fetches every endpoint's schema into the cache directory and
// rebuilds the files the fzf / tv browsers read.
func (a *app) syncSchemas(ctx context.Context, args []string, o *reqOpts, prefix string) error {
	names := args
	if len(names) == 0 {
		names = a.schemaProfile().Endpoints
	}
	if len(names) == 0 {
		return &ExitError{Code: exitUsage, Msg: "✗ no endpoints: name them, or set endpoints = [...] under [env.<name>] in your config"}
	}
	jobs, err := a.schemaJobs(names, o, prefix)
	if err != nil {
		return err
	}
	if o.dry {
		for _, j := range jobs {
			fmt.Fprintln(a.io.Out, a.curlLine(j.spec, nil))
		}
		return nil
	}

	cache := a.ugqlCacheDir()
	if cache == "" {
		return &ExitError{Code: exitGeneric, Msg: "✗ no server to key the cache by: `api env load <suffix>`, `api env use <name>` or --base/--env first"}
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return err
	}

	fetched := a.fetchSchemas(ctx, jobs, cache, o.retry)
	items, err := a.writeSchemaIndex(cache)
	if err != nil {
		return err
	}
	if a.ugqlNested() {
		// the fixed place the fzf / tv browsers read: the last server synced
		if key, ok := a.ugqlKey(); ok {
			if err := pointCurrent(a.ugqlRoot(), key); err != nil {
				a.say("⚠ could not update " + filepath.Join(a.ugqlRoot(), "current") + ": " + err.Error())
			}
		}
	}
	a.say(fmt.Sprintf("✓ %d items in %s", items, cache))
	if fetched == 0 {
		return &ExitError{Code: exitGeneric, Msg: "✗ no schema could be fetched: this cache is stale"}
	}
	return nil
}

// schemaJobs builds one request per distinct, usable endpoint name.
func (a *app) schemaJobs(names []string, o *reqOpts, prefix string) ([]schemaJob, error) {
	var jobs []schemaJob
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
		body, err := gqlquery.Body(schema.SchemaQuery, "{}")
		if err != nil {
			return nil, err
		}
		// specs are built here, one at a time: building one reads config and
		// may print a warning; only the network calls run concurrently
		spec, err := a.newSpec("POST", gqlquery.Target(a.session().GraphQLURL, schemaEndpoint(a.effectivePrefix(prefix), n)), o)
		if err != nil {
			return nil, err
		}
		setJSONBody(&spec, body)
		jobs = append(jobs, schemaJob{label, spec})
	}
	return jobs, nil
}

// fetchSchemas runs the jobs concurrently and stores each result as
// <label>.json in cache. It returns how many were stored.
func (a *app) fetchSchemas(ctx context.Context, jobs []schemaJob, cache string, retries int) int {
	results := make(chan syncResult)
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j schemaJob) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results <- fetchSchema(ctx, j.label, j.spec, retries)
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
	return fetched
}

// writeSchemaIndex rebuilds preview.jq, index.txt and .env from the schemas in
// cache and returns the number of index lines.
func (a *app) writeSchemaIndex(cache string) (int, error) {
	if err := writeFileAtomic(filepath.Join(cache, "preview.jq"), []byte(schema.PreviewProgram), 0o644); err != nil {
		return 0, err
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
		return 0, &ExitError{Code: exitGeneric, Msg: "✗ nothing cached: are you logged in? (api login)"}
	}
	if err := writeFileAtomic(filepath.Join(cache, "index.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return 0, err
	}
	envName := a.session().Name
	if envName == "" {
		envName = "?"
	}
	if err := writeFileAtomic(filepath.Join(cache, ".env"), []byte(envName+"\n"), 0o644); err != nil {
		return 0, err
	}
	return len(lines), nil
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

// pointCurrent makes <root>/current a symlink to the key's directory, replacing
// the old one in one step so a reader never sees it missing.
func pointCurrent(root, key string) error {
	tmp := filepath.Join(root, ".current.tmp")
	_ = os.Remove(tmp)
	if err := os.Symlink(key, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(root, "current"))
}
