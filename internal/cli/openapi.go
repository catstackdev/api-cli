package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cybercat/api/internal/openapi"
	"cybercat/api/internal/ui"

	"github.com/spf13/cobra"
)

// defaultSpecPaths are tried in order when `openapi sync` gets no path: FastAPI
// and a custom jsonDocumentUrl, Swagger UI setups, Spring Boot (springdoc), a
// common alias, and NestJS's own <path>-json names (setup('api') and
// setup('docs')).
var defaultSpecPaths = []string{"/openapi.json", "/swagger.json", "/v3/api-docs", "/api-docs", "/api-json", "/docs-json"}

// openapiCacheDir is where the indexes live, one file per server.
func (a *app) openapiCacheDir() string {
	if d := a.getenv("API_OPENAPI_CACHE_DIR"); d != "" {
		return d
	}
	if d := a.getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "api", "openapi")
	}
	home := a.getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".cache", "api", "openapi")
}

// originKey turns an origin into a file name ("https://api.x.com" ->
// "https_api.x.com"); false when it cannot be one.
func originKey(o string) (string, bool) {
	key := strings.NewReplacer("://", "_", ":", "-", "[", "", "]", "").Replace(o)
	return key, o != "" && fileSafe.MatchString(key)
}

var fileSafe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// openapiCacheFile is the index of the ACTIVE SERVER. It is keyed by the base
// URL's origin, never by the env name: a name is attacker-controllable (a cloned
// repo's .env.dev) and a spec must only describe the server it came from.
func (a *app) openapiCacheFile() (string, error) {
	base := a.session().BaseURL
	if base == "" {
		return "", &ExitError{Code: exitGeneric, Msg: "✗ no base URL: `api env load <suffix>`, `api env use <name>`, --base/--env, or a default_env in a .api.toml (the index is kept per server)"}
	}
	o := origin(base)
	key, ok := originKey(o)
	if !ok {
		return "", &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ cannot key an index by %q", base)}
	}
	return filepath.Join(a.openapiCacheDir(), key+".json"), nil
}

// loadOpenAPI reads the cached index for the active server. It returns nil when
// there is none or it is unusable: completion calls it on every TAB, so it only
// reads one local file and never fails loudly.
func (a *app) loadOpenAPI() *openapi.Index {
	path, err := a.openapiCacheFile()
	if err != nil {
		return nil
	}
	if st, err := os.Stat(path); err != nil || st.Size() > openapi.MaxSpecBytes {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var idx openapi.Index
	if json.Unmarshal(data, &idx) != nil {
		return nil
	}
	return &idx
}

// requireOpenAPI is loadOpenAPI for commands that cannot work without an index. With
// no server selected it says so: "no index" would send you to `sync`, which cannot
// help (a tv channel or an editor starts api without your shell's session).
func (a *app) requireOpenAPI() (*openapi.Index, error) {
	if _, err := a.openapiCacheFile(); err != nil {
		return nil, err
	}
	idx := a.loadOpenAPI()
	if idx == nil {
		return nil, &ExitError{Code: exitGeneric, Msg: "✗ no OpenAPI index for this server: run `api openapi sync`"}
	}
	return idx, nil
}

// matchesFilter is the case-insensitive match of `list` and the picker; needle
// must already be lower-case.
func matchesFilter(op openapi.Operation, needle string) bool {
	return needle == "" || strings.Contains(strings.ToLower(op.Path+" "+op.Summary+" "+op.OperationID), needle)
}

// prettyBody pretty-prints a body skeleton; ok is false when it is not JSON.
func prettyBody(body string) (pretty string, ok bool) {
	var buf bytes.Buffer
	if json.Indent(&buf, []byte(body), "", "  ") != nil {
		return "", false
	}
	return buf.String(), true
}

func (a *app) openapiCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "openapi",
		Short: "Index an OpenAPI/Swagger JSON spec for completion (sync, list)",
		Long: `Reads an OpenAPI 3 or Swagger 2 document (JSON) from the server and keeps a small
index of its method + path pairs, so "api get <TAB>" completes the endpoints the API
really has. Completion stays offline: it reads that one local file.

  api openapi sync               # the env's openapi path, else tries /openapi.json,
                                 # /swagger.json, /v3/api-docs, /api-docs, /api-json, /docs-json
  api openapi sync /docs/spec.json
  api openapi sync --file ./openapi.json
  api openapi list users
  api openapi                    # pick an operation, edit the request, save and run it (like api gql)`,
	}
	o, pf := &reqOpts{}, &pickFlags{}
	cmd.Args = cobra.NoArgs
	cmd.RunE = func(cmd *cobra.Command, args []string) error { return a.openapiPick(cmd, "", *pf, o) }
	a.bindFlags(o, cmd, false)
	bindPickFlags(cmd, pf)
	cmd.AddCommand(a.openapiSyncCmd(), a.openapiListCmd(), a.openapiPickCmd(), a.openapiShowCmd())
	return cmd
}

func (a *app) openapiSyncCmd() *cobra.Command {
	o := &reqOpts{}
	var file string
	cmd := &cobra.Command{
		Use:   "sync [path|url]",
		Short: "Fetch the spec and cache its index for the active server",
		Long: `Fetches the spec with the normal request pipeline (the session token goes to the
active base URL only, never to another host) and stores an index of method, path
and summary under ~/.cache/api/openapi/, owner-only. The raw document is not
kept. Only JSON is read; for a YAML spec fetch its JSON form or convert it with
yq -o=json and use --file.

Where the spec lives: the argument, else the selected env's "openapi" (a path in
.api.toml, e.g. api config set env.local.openapi /docs/spec.json), else the
usual locations are tried in turn.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cacheFile, err := a.openapiCacheFile()
			if err != nil {
				return err
			}
			var data []byte
			source := file
			if file != "" {
				data, err = readSpecFile(file)
			} else {
				var dry bool
				if data, source, dry, err = a.fetchSpec(cmd.Context(), args, o); dry {
					return err
				}
			}
			if err != nil {
				return err
			}
			return a.storeIndex(cacheFile, data, source)
		},
	}
	a.bindRequestFlags(o, cmd.Flags(), false)
	a.bindTransportFlags(o, cmd.Flags())
	cmd.Flags().StringVar(&file, "file", "", "read the spec from a local JSON file instead of the server")
	return cmd
}

func readSpecFile(file string) ([]byte, error) {
	st, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if st.Size() > openapi.MaxSpecBytes {
		return nil, &ExitError{Code: exitGeneric, Msg: "✗ " + file + ": spec too large"}
	}
	return os.ReadFile(file)
}

// fetchSpec gets the spec through the normal request pipeline. dry reports that
// --dry printed the request instead of sending it (err is then dispatch's result).
func (a *app) fetchSpec(ctx context.Context, args []string, o *reqOpts) (data []byte, source string, dry bool, err error) {
	// An argument wins, then the selected env's `openapi` path; only with
	// neither do we probe the usual locations (a 404 then means "next").
	candidates, probing := defaultSpecPaths, true
	if _, prof := a.resolve(); len(args) == 0 && prof.OpenAPI != "" {
		candidates, probing = []string{prof.OpenAPI}, false
	}
	if len(args) == 1 {
		candidates, probing = args, false
	}
	for _, c := range candidates {
		spec, err := a.newSpec("GET", c, o)
		if err != nil {
			return nil, "", false, err
		}
		if o.dry {
			return nil, "", true, a.dispatch(ctx, spec, o, ".", "", nil)
		}
		resp, err := a.sendRecovering(ctx, spec, o)
		if err != nil {
			return nil, "", false, err
		}
		if resp.Status == 404 && probing {
			continue // probing: try the next common location
		}
		if resp.Status >= 400 {
			return nil, "", false, &ExitError{Code: exitHTTP, Msg: fmt.Sprintf("✗ GET %s: HTTP %d %s", c, resp.Status, resp.StatusText)}
		}
		return resp.Body, c, false, nil
	}
	return nil, "", false, &ExitError{Code: exitGeneric, Msg: "✗ no OpenAPI document found (tried " + strings.Join(candidates, ", ") + "): pass its path, or --file"}
}

// storeIndex parses the document and writes only its index, never the raw spec.
func (a *app) storeIndex(cacheFile string, data []byte, source string) error {
	idx, err := openapi.Parse(data)
	if err != nil {
		return &ExitError{Code: exitGeneric, Msg: "✗ " + source + ": " + err.Error()}
	}
	idx.Source = source
	idx.Fetched = time.Now().UTC().Format(time.RFC3339)
	out, err := json.MarshalIndent(idx, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cacheFile), 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(cacheFile, out, 0o600); err != nil {
		return err
	}
	a.say(fmt.Sprintf("✓ %d operations from %s cached: %s", len(idx.Operations), source, cacheFile))
	if idx.Skipped > 0 {
		a.say(fmt.Sprintf("⚠ %d path(s) with control characters, spaces or an unusual shape were skipped", idx.Skipped))
	}
	if idx.Truncated {
		a.say(fmt.Sprintf("⚠ the spec has more than %d operations: the index stops there", openapi.MaxOperations))
	}
	return nil
}

func (a *app) openapiListCmd() *cobra.Command {
	var method string
	cmd := &cobra.Command{
		Use:   "list [filter]",
		Short: "List the cached operations (filter matches path, summary or operationId)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			idx, err := a.requireOpenAPI()
			if err != nil {
				return err
			}
			filter := ""
			if len(args) == 1 {
				filter = strings.ToLower(args[0])
			}
			var rows [][]string
			for _, op := range idx.Operations {
				if method != "" && !strings.EqualFold(method, op.Method) {
					continue
				}
				if !matchesFilter(op, filter) {
					continue
				}
				rows = append(rows, []string{op.Method, op.Path, op.Summary})
			}
			if len(rows) == 0 {
				a.say("  (nothing matches)")
				return nil
			}
			ui.Table(a.io.Out, []string{"METHOD", "PATH", "SUMMARY"}, rows, a.colorOut())
			return nil
		},
	}
	cmd.Flags().StringVar(&method, "method", "", "only this HTTP method")
	return cmd
}
