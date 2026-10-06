package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cybercat/api/internal/httpfile"
	"cybercat/api/internal/openapi"
	"cybercat/api/internal/session"
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

var fileSafe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// openapiCacheFile is the index of the ACTIVE SERVER. It is keyed by the base
// URL's origin, never by the env name: a name is attacker-controllable (a cloned
// repo's .env.dev) and a spec must only describe the server it came from.
func (a *app) openapiCacheFile() (string, error) {
	base := a.session().BaseURL
	if base == "" {
		return "", &ExitError{Code: exitGeneric, Msg: "✗ no base URL: `api env load <suffix>` or `api env use <name>` first (the index is kept per server)"}
	}
	o := origin(base)
	key := strings.NewReplacer("://", "_", ":", "-", "[", "", "]", "").Replace(o)
	if o == "" || !fileSafe.MatchString(key) {
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

// requireOpenAPI is loadOpenAPI for commands that cannot work without an index.
func (a *app) requireOpenAPI() (*openapi.Index, error) {
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

// pathParam finds the {id} placeholders of an OpenAPI path.
var pathParam = regexp.MustCompile(`\{([^{}]+)\}`)

// runnerVar is the .http runner's own {{variable}} syntax.
var runnerVar = regexp.MustCompile(`\{\{[^{}]*\}\}`)

// pickMarker starts the instruction lines this command adds to the file you edit;
// they are dropped again before the request is run or saved.
const pickMarker = "# openapi-pick:"

// pickCommand is the `api` command line for an operation, and the path
// parameters the user still has to fill in. Body verbs get a '{}' to start from.
func pickCommand(op openapi.Operation) (line string, params []string) {
	for _, m := range pathParam.FindAllStringSubmatch(op.Path, -1) {
		params = append(params, "{"+m[1]+"}")
	}
	p := shellWord(op.Path)
	switch op.Method {
	case "GET":
		line = "api get " + p
	case "DELETE":
		line = "api del " + p
	case "POST", "PUT", "PATCH":
		line = "api " + strings.ToLower(op.Method) + " " + p + " '{}'"
	case "HEAD":
		line = "api headers " + p
	default:
		line = "api get " + p + " -- -X " + op.Method
	}
	return line, params
}

var slugUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// pickSlug names the saved request: the operationId, else method + path words.
func pickSlug(op openapi.Operation) string {
	if op.OperationID != "" {
		return op.OperationID
	}
	s := strings.ToLower(op.Method) + "-" + strings.Trim(slugUnsafe.ReplaceAllString(op.Path, "-"), "-")
	return strings.Trim(s, "-")
}

// pickFile is the .http file the editor opens: one request, with a body
// skeleton from the spec for POST/PUT/PATCH. The summary is server data: it goes
// in as "# openapi: <text>" so it can never start an annotation (# @assert, # @filter)
// or a heading, and it was flattened to one line when the spec was indexed.
func pickFile(op openapi.Operation, slug string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n", slug)
	b.WriteString(pickMarker + " save to run, quit without saving to cancel. Fill in any {placeholder}.\n")
	b.WriteString(pickMarker + " Anything that changes data asks before it is sent.\n")
	if op.Summary != "" {
		fmt.Fprintf(&b, "# openapi: %s\n", op.Summary)
	}
	fmt.Fprintf(&b, "%s %s\nAccept: application/json\n", op.Method, op.Path)
	switch op.Method {
	case "POST", "PUT", "PATCH":
		body := op.Body
		if body == "" {
			body = "{}"
		}
		pretty, ok := prettyBody(body)
		if !ok {
			pretty = "{}"
		}
		b.WriteString("Content-Type: application/json\n\n" + pretty + "\n")
	}
	return b.String()
}

// stripPickMarkers drops the instruction lines pickFile added.
func stripPickMarkers(doc string) string {
	var out []string
	for _, l := range strings.Split(doc, "\n") {
		if !strings.HasPrefix(l, pickMarker) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func readOnlyMethod(m string) bool {
	switch strings.ToUpper(m) {
	case "GET", "HEAD", "OPTIONS":
		return true
	}
	return false
}

// pickFlags are the options of the picker.
type pickFlags struct{ yes, noSave, printOnly bool }

func bindPickFlags(cmd *cobra.Command, pf *pickFlags) {
	f := cmd.Flags()
	f.BoolVar(&pf.yes, "yes", false, "do not ask before sending a request that changes data")
	f.BoolVar(&pf.noSave, "no-save", false, "do not keep the request under "+requestsDir+"/")
	f.BoolVar(&pf.printOnly, "print", false, "print the api command for the picked operation: no editor, nothing sent")
}

const pickLong = `Pick an operation with fzf (the preview shows its summary and body skeleton), edit
the generated request in your editor, save to run it. It is the OpenAPI twin of
"api gql": quit the editor without saving to cancel, and anything that changes data
asks first ("✱ POST /users changes data on <server>: run it? [y/N]"). What you SAVED
decides whether to ask, not what you picked.

The request is a .http file: the body skeleton comes from the spec's request schema,
{placeholders} in the path must be filled in, and you may add # @assert lines. After
you confirm it is kept as ` + requestsDir + `/<name>.http (never overwriting an earlier one),
so "api run <name>" repeats it. --no-save keeps nothing; --print only prints the
api command for the pick. Needs a synced index (api openapi sync).`

func (a *app) openapiPickCmd() *cobra.Command {
	o := &reqOpts{}
	pf := &pickFlags{}
	cmd := &cobra.Command{
		Use:   "pick [text]",
		Short: "Pick an operation with fzf, edit the request, save and run it",
		Long:  pickLong,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := ""
			if len(args) == 1 {
				text = args[0]
			}
			return a.openapiPick(cmd, text, *pf, o)
		},
	}
	a.bindFlags(o, cmd, false)
	bindPickFlags(cmd, pf)
	return cmd
}

func (a *app) openapiShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <METHOD> <path>",
		Short: "Show one cached operation (summary and body skeleton): the picker's preview",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			idx, err := a.requireOpenAPI()
			if err != nil {
				return err
			}
			for _, op := range idx.Operations {
				if !strings.EqualFold(op.Method, args[0]) || op.Path != args[1] {
					continue
				}
				fmt.Fprintf(a.io.Out, "%s %s\n", op.Method, op.Path)
				if op.Summary != "" {
					fmt.Fprintln(a.io.Out, op.Summary)
				}
				if op.OperationID != "" {
					fmt.Fprintln(a.io.Out, "operationId: "+op.OperationID)
				}
				if pretty, ok := prettyBody(op.Body); op.Body != "" && ok {
					fmt.Fprintf(a.io.Out, "\nrequest body (skeleton):\n%s\n", pretty)
				}
				return nil
			}
			return &ExitError{Code: exitGeneric, Msg: "✗ no such operation in the index: " + args[0] + " " + args[1]}
		},
	}
}

// openapiPick lets the user choose one cached operation, then (unless --print)
// edits, saves and runs it.
func (a *app) openapiPick(cmd *cobra.Command, text string, pf pickFlags, o *reqOpts) error {
	idx, err := a.requireOpenAPI()
	if err != nil {
		return err
	}
	byLine := map[string]openapi.Operation{}
	var lines []string
	needle := strings.ToLower(text)
	for _, op := range idx.Operations {
		if !matchesFilter(op, needle) {
			continue
		}
		line := strings.TrimSpace(fmt.Sprintf("%-7s %s  %s", op.Method, op.Path, op.Summary))
		byLine[line] = op
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return &ExitError{Code: exitGeneric, Msg: "✗ nothing matches"}
	}
	if _, err := exec.LookPath("fzf"); err != nil {
		return &ExitError{Code: exitGeneric, Msg: "✗ picking needs fzf in PATH (brew install fzf); `api openapi list` works without it"}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	line, _, cancelled, err := a.pickWithFzf(lines, "--prompt=openapi> ",
		"--preview="+session.Quote(exe)+" openapi show {1} {2}", "--preview-window=right,60%,wrap")
	if err != nil {
		return err
	}
	if cancelled {
		return &ExitError{Code: exitGeneric}
	}
	op, ok := byLine[line]
	if !ok {
		return fmt.Errorf("could not read fzf's choice")
	}
	if pf.printOnly {
		cmdline, params := pickCommand(op)
		fmt.Fprintln(a.io.Out, cmdline)
		if len(params) > 0 {
			a.say("→ replace " + strings.Join(params, ", ") + " in the path before running it")
		}
		return nil
	}
	return a.openapiEditAndRun(cmd, op, pf, o)
}

func (a *app) openapiEditAndRun(cmd *cobra.Command, op openapi.Operation, pf pickFlags, o *reqOpts) error {
	slug := pickSlug(op)
	edited, changed, err := a.editTempExt(slug, ".http", pickFile(op, slug))
	if err != nil {
		return err
	}
	if !changed || strings.TrimSpace(edited) == "" {
		a.say("  (cancelled: nothing saved)")
		return &ExitError{Code: exitGeneric}
	}
	doc := stripPickMarkers(edited)
	file, err := httpfile.Parse([]byte(doc))
	if err != nil {
		return &ExitError{Code: exitUsage, Msg: "✗ the request is not valid: " + err.Error()}
	}
	if len(file.Requests) != 1 {
		return &ExitError{Code: exitUsage, Msg: fmt.Sprintf("✗ the file must hold exactly one request, found %d", len(file.Requests))}
	}
	req := &file.Requests[0]
	if m := pathParam.FindString(runnerVar.ReplaceAllString(req.URL, "")); m != "" {
		return &ExitError{Code: exitUsage, Msg: "✗ replace " + m + " in the path before running it (nothing was sent or saved)"}
	}

	// what was SAVED decides whether to ask, not what was picked: it may have been edited
	if !readOnlyMethod(req.Method) && !pf.yes && !o.dry {
		dest := orUnset(a.session().BaseURL)
		if strings.HasPrefix(req.URL, "http://") || strings.HasPrefix(req.URL, "https://") {
			dest = origin(req.URL)
		}
		if !a.confirm(fmt.Sprintf("✱ %s %s changes data on %s: run it?", strings.ToUpper(req.Method), req.URL, dest)) {
			a.say("  (not run, not saved)")
			return &ExitError{Code: exitGeneric}
		}
	}

	path := filepath.Join(findRoot(), "openapi-pick.http") // only where a "< file" body is resolved from
	if !pf.noSave && !o.dry {
		saved, err := savePicked(slug, doc)
		if err != nil {
			return err
		}
		path = saved
		a.say(fmt.Sprintf("✓ saved %s/%s (api run %s)", requestsDir, filepath.Base(saved), strings.TrimSuffix(filepath.Base(saved), ".http")))
	}
	if a.asserts, err = compileAsserts(path, []*httpfile.Request{req}); err != nil {
		return err
	}
	a.say("→ " + strings.ToUpper(req.Method) + " " + req.URL)
	return a.runRequest(cmd, path, file, req, map[string]string{}, o, "", o.trustHosts, false)
}

// savePicked writes the request under .api/requests/, never over an earlier file
// (get-users.http, get-users-2.http, ...).
func savePicked(slug, doc string) (string, error) {
	dir := filepath.Join(findRoot(), filepath.FromSlash(requestsDir))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for n := 1; n < 1000; n++ {
		name := slug + ".http"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.http", slug, n)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.WriteString(strings.TrimRight(doc, "\n") + "\n")
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		return path, werr
	}
	return "", fmt.Errorf("too many saved requests named %s", slug)
}

// verbPathCompletion completes the first argument of `api get|post|...`: the
// operations of the cached spec for this METHOD, then recently used paths.
// Offline and fast: one local file.
func (a *app) verbPathCompletion(method string) func(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		history, dir := pathCompletion(cmd, args, toComplete)
		if len(args) > 0 {
			return history, dir
		}
		const maxCandidates = 300
		var out []cobra.Completion
		seen := map[string]bool{}
		if idx := a.loadOpenAPI(); idx != nil {
			for _, op := range idx.Operations {
				if op.Method != method || !strings.HasPrefix(op.Path, toComplete) || seen[op.Path] {
					continue
				}
				seen[op.Path] = true
				if op.Summary != "" {
					out = append(out, cobra.CompletionWithDesc(op.Path, op.Summary))
				} else {
					out = append(out, op.Path)
				}
				if len(out) >= maxCandidates {
					break
				}
			}
		}
		for _, h := range history {
			if !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
		return out, dir
	}
}
