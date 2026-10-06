package cli

import (
	"bytes"
	"context"
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
