package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cybercat/api/internal/httpc"
	"cybercat/api/internal/httpfile"
	"cybercat/api/internal/ui"

	"github.com/spf13/cobra"
)

// requestsDir is where `api run <name>` looks for saved requests.
const requestsDir = ".api/requests"

// resolveHTTPFile maps "users" or "users.http" to <root>/.api/requests/users.http,
// and any existing path to itself.
func resolveHTTPFile(arg string) (string, error) {
	if st, err := os.Stat(arg); err == nil && !st.IsDir() {
		return arg, nil
	}
	dir := filepath.Join(findRoot(), filepath.FromSlash(requestsDir))
	for _, cand := range []string{arg, arg + ".http", arg + ".rest"} {
		p := filepath.Join(dir, cand)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("no such request file %q (looked in the current directory and %s)", arg, filepath.Join(dir))
}

func listHTTPFiles() []string {
	dir := filepath.Join(findRoot(), filepath.FromSlash(requestsDir))
	var out []string
	for _, pat := range []string{"*.http", "*.rest"} {
		m, _ := filepath.Glob(filepath.Join(dir, pat))
		for _, f := range m {
			out = append(out, strings.TrimSuffix(strings.TrimSuffix(filepath.Base(f), ".http"), ".rest"))
		}
	}
	return out
}

func (a *app) printSavedRequests() {
	names := listHTTPFiles()
	if len(names) == 0 {
		a.say("  (no saved requests: put name.http files in " + requestsDir + "/)")
		return
	}
	rows := make([][]string, len(names))
	for i, n := range names {
		rows[i] = []string{n}
	}
	ui.Table(a.io.Out, []string{"SAVED REQUESTS"}, rows, a.colorOut())
}

func (a *app) printRequests(file *httpfile.File) {
	rows := make([][]string, len(file.Requests))
	for i, r := range file.Requests {
		rows[i] = []string{fmt.Sprint(i + 1), r.Name, r.Method, r.URL}
	}
	ui.Table(a.io.Out, []string{"#", "NAME", "METHOD", "URL"}, rows, a.colorOut())
}

// splitRunArgs separates the request selector from the jq filter.
func splitRunArgs(rest []string) (sel, filter string, err error) {
	for _, x := range rest {
		switch {
		case isFilter(x):
			filter = x
		case sel == "":
			sel = x
		default:
			return "", "", fmt.Errorf("unexpected argument %q", x)
		}
	}
	return sel, filter, nil
}

func parseVars(vars []string) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range vars {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("bad --var %q: want key=value", kv)
		}
		out[k] = v
	}
	return out, nil
}

func selectRequests(file *httpfile.File, sel string, all bool) ([]*httpfile.Request, error) {
	if !all {
		r, err := file.Find(sel)
		if err != nil {
			return nil, err
		}
		return []*httpfile.Request{r}, nil
	}
	if sel != "" {
		return nil, fmt.Errorf("--all runs every request: drop the selector %q", sel)
	}
	reqs := make([]*httpfile.Request, len(file.Requests))
	for i := range file.Requests {
		reqs[i] = &file.Requests[i]
	}
	return reqs, nil
}

// printedError is err's exit code with no message: sayRunError already printed
// it, and the root handler would print it a second time.
func printedError(err error) error {
	if ee, ok := err.(*ExitError); ok {
		return &ExitError{Code: ee.Code}
	}
	return &ExitError{Code: exitUsage}
}

// sayRunError prints a failed request's message once (an ExitError may have none).
func (a *app) sayRunError(err error) {
	if ee, ok := err.(*ExitError); ok {
		if ee.Msg != "" {
			a.say(ee.Msg)
		}
		return
	}
	a.say("✗ " + err.Error())
}

// runFlags are the options of `api run` that only it uses.
type runFlags struct {
	vars      []string
	list      bool
	all       bool
	keepGoing bool
}

// runFile resolves a .http file, picks the requests to run and runs them.
func (a *app) runFile(cmd *cobra.Command, args []string, o *reqOpts, rf *runFlags) error {
	if len(args) == 0 {
		if !rf.list {
			return fmt.Errorf("missing <file|name> (try: api run --list)")
		}
		a.printSavedRequests()
		return nil
	}

	path, err := resolveHTTPFile(args[0])
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	file, err := httpfile.Parse(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	if rf.list {
		a.printRequests(file)
		return nil
	}

	sel, filter, err := splitRunArgs(args[1:])
	if err != nil {
		return err
	}
	uservars, err := parseVars(rf.vars)
	if err != nil {
		return err
	}
	reqs, err := selectRequests(file, sel, rf.all)
	if err != nil {
		return err
	}
	if a.run.asserts, err = compileAsserts(path, reqs); err != nil {
		return err
	}

	var firstErr error
	for _, r := range reqs {
		if rf.all {
			a.say(fmt.Sprintf("→ %s: %s %s", r.Name, r.Method, r.URL))
		}
		job := runJob{path: path, file: file, req: r, uservars: uservars, filter: filter}
		if err := a.runRequest(cmd, job, o); err != nil {
			a.sayRunError(err)
			if firstErr == nil {
				firstErr = printedError(err)
			}
			if !rf.keepGoing {
				break
			}
		}
	}
	if rf.all && a.run.assertTotal > 0 {
		a.say(fmt.Sprintf("%d assertion(s): %d passed, %d failed", a.run.assertTotal, a.run.assertTotal-a.run.assertFailed, a.run.assertFailed))
	}
	return firstErr
}

func completeRunArgs(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	var out []cobra.Completion
	switch len(args) {
	case 0:
		for _, n := range listHTTPFiles() {
			out = append(out, n)
		}
		return out, cobra.ShellCompDirectiveDefault
	case 1:
		if p, err := resolveHTTPFile(args[0]); err == nil {
			if data, err := os.ReadFile(p); err == nil {
				if f, err := httpfile.Parse(data); err == nil {
					for _, r := range f.Requests {
						out = append(out, cobra.CompletionWithDesc(r.Name, r.Method+" "+r.URL))
					}
				}
			}
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func (a *app) runCmd() *cobra.Command {
	o := &reqOpts{}
	rf := &runFlags{}
	cmd := &cobra.Command{
		Use:   "run [file|name] [request] [.filter]",
		Short: "Run saved requests from a .http file (JetBrains / VS Code REST Client format)",
		Long: `Runs a request from a .http file. <name> is looked up in ` + requestsDir + `/ of the
project (name.http), or give a path. Pick a request by name or number; a file with
one request needs no selector. Variables: @var = value in the file, {{var}},
--var k=v, {{base}} {{graphql}} {{token}} {{env}} from the session,
{{$env API_NAME}} (API_* variables only), {{$uuid}}, {{$timestamp}}.

Safety: a .http file can come from a repository, so {{token}} and the session
bearer token are NOT sent to a host other than the active base URL, and a
request that would send {{token}} / {{$env ...}} there is refused, unless you
pass --trust-hosts.`,
		Example: `  api run --list                    # saved files in ` + requestsDir + `
  api run users --list              # requests in users.http
  api run users "List users"
  api run users 2 .data.id --var id=42
  api run users --all --dry`,
		RunE:              func(cmd *cobra.Command, args []string) error { return a.runFile(cmd, args, o, rf) },
		ValidArgsFunction: completeRunArgs,
	}
	a.bindFlags(o, cmd, false)
	f := cmd.Flags()
	f.StringArrayVar(&rf.vars, "var", nil, "set a {{variable}}: key=value (repeatable; beats @var in the file)")
	f.BoolVar(&rf.list, "list", false, "list saved files, or the requests inside a file")
	f.BoolVar(&rf.all, "all", false, "run every request in the file, in order (stops at the first failure)")
	f.BoolVar(&rf.keepGoing, "keep-going", false, "with --all: continue after a failure")
	return cmd
}

// runJob is one request of a .http file on its way out: where the file lives
// (a "< file" body is resolved from there), what the file holds, and what the
// user supplied for it.
type runJob struct {
	path     string
	file     *httpfile.File
	req      *httpfile.Request
	uservars map[string]string // --var k=v; nil when there are none
	filter   string            // replaces the request's own @filter when set
	silent   bool              // run only for the values later requests take from its response: nothing is printed
}

// runRequest interpolates one request and sends it through the normal pipeline.
func (a *app) runRequest(cmd *cobra.Command, job runJob, o *reqOpts) error {
	r := job.req
	if err := a.runDependencies(cmd, job, o); err != nil {
		return err
	}
	s, _ := a.resolve()
	builtin := map[string]string{"base": s.BaseURL, "graphql": s.GraphQLURL, "token": s.Token, "env": s.Name}
	rr, err := a.renderRequest(job, builtin, o.dry)
	if err != nil {
		return err
	}
	target, headers, body, secret := rr.target, rr.headers, rr.body, rr.secret

	// A file's host is the file author's choice, not yours: never hand it your
	// session token by default.
	opts := *o
	if oc := origin(target); oc != "" && oc != origin(s.BaseURL) && !o.trustHosts {
		if secret {
			return fmt.Errorf("request %q would send {{token}} / {{$env ...}} to %s, which is not the active base URL: pass --trust-hosts to allow it", r.Name, oc)
		}
		opts.noAuth = true
	}

	spec, err := a.newSpec(r.Method, target, &opts)
	if err != nil {
		return err
	}
	for _, h := range headers {
		spec.SetHeader(h.name, h.value)
	}
	if body != "" {
		spec.Body = body
		if t := strings.TrimSpace(body); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
			spec.DefaultHeader("Content-Type", "application/json")
		}
	}

	filter := job.filter
	if filter == "" {
		filter = r.Filter
	}
	if filter == "" {
		filter = "."
	}
	if opts.dry {
		if n := len(a.run.asserts[r]); n > 0 {
			a.say(fmt.Sprintf("→ %d assertion(s) not evaluated with --dry", n))
		}
		return a.dispatch(cmd.Context(), spec, &opts, filter, "", nil)
	}
	resp, err := a.sendRecovering(cmd.Context(), spec, &opts)
	if err != nil {
		return err
	}
	a.remember(r.Name, resp)
	if job.silent {
		if resp.Status >= 400 {
			return &ExitError{Code: exitHTTP, Msg: fmt.Sprintf("✗ %s: HTTP %d %s (its response is needed by a later request)", r.Name, resp.Status, resp.StatusText)}
		}
		return nil
	}
	if list := a.run.asserts[r]; len(list) > 0 {
		return a.finishAsserted(r, resp, &opts, filter, list)
	}
	return a.finish(resp, &opts, filter, nil)
}

// headerKV is one header with its variables expanded.
type headerKV struct{ name, value string }

// renderedRequest is a request with every {{...}} expanded. secret reports that
// an expanded value was a token or a {{$env}} value: the caller must not send it
// to a host the user did not choose.
type renderedRequest struct {
	target  string
	headers []headerKV
	body    string
	secret  bool
}

// renderRequest expands the variables of r. dry only matters for response
// captures, which stand in as placeholders.
func (a *app) renderRequest(job runJob, builtin map[string]string, dry bool) (renderedRequest, error) {
	r, file, uservars := job.req, job.file, job.uservars
	var captureErr error
	lookup := func(name string) (string, bool) {
		if isCaptureRef(name) {
			v, err := a.captureLookup(name, dry)
			if err != nil {
				captureErr = err
				return "", false
			}
			return v, true
		}
		if v, ok := uservars[name]; ok {
			return v, true
		}
		if v, ok := file.Vars[name]; ok {
			return v, true
		}
		if v, ok := builtin[name]; ok && v != "" {
			return v, true
		}
		return "", false
	}
	getenv := func(name string) (string, bool) {
		if !strings.HasPrefix(name, "API_") {
			return "", false
		}
		v := a.getenv(name)
		return v, v != ""
	}

	secret := false
	interp := func(in string) (string, error) {
		out, used, err := httpfile.InterpolateWith(in, interpolateOptions(lookup, getenv))
		secret = secret || used
		if err != nil {
			if captureErr != nil {
				return "", fmt.Errorf("%w (request %q, line %d)", captureErr, r.Name, r.Line)
			}
			return "", fmt.Errorf("%s (request %q, line %d)", err, r.Name, r.Line)
		}
		if strings.ContainsAny(out, "\r\n") {
			return "", fmt.Errorf("request %q: a variable put a line break into %q (header injection?)", r.Name, strings.SplitN(in, "\n", 2)[0])
		}
		return out, nil
	}

	target, err := interp(r.URL)
	if err != nil {
		return renderedRequest{}, err
	}
	var headers []headerKV
	for _, h := range r.Headers {
		name, err := interp(h.Name)
		if err != nil {
			return renderedRequest{}, err
		}
		value, err := interp(h.Value)
		if err != nil {
			return renderedRequest{}, err
		}
		if err := httpc.CheckHeader(name, value); err != nil {
			return renderedRequest{}, fmt.Errorf("request %q: %w", r.Name, err)
		}
		headers = append(headers, headerKV{name, value})
	}
	body := r.Body
	if body != "" {
		// bodies may legitimately span lines: interpolate, but allow breaks
		b, used, err := httpfile.InterpolateWith(body, interpolateOptions(lookup, getenv))
		if err != nil {
			if captureErr != nil {
				return renderedRequest{}, fmt.Errorf("%w (request %q)", captureErr, r.Name)
			}
			return renderedRequest{}, fmt.Errorf("%s (request %q)", err, r.Name)
		}
		secret = secret || used
		body = b
	}
	if r.BodyFile != "" {
		p := r.BodyFile
		if !filepath.IsAbs(p) {
			p = filepath.Join(filepath.Dir(job.path), p)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return renderedRequest{}, fmt.Errorf("request %q: body file: %w", r.Name, err)
		}
		body = strings.TrimRight(string(data), "\r\n")
	}
	return renderedRequest{target: target, headers: headers, body: body, secret: secret}, nil
}

// runDependencies runs, once and silently, every request whose response r uses
// and that has not run yet.
func (a *app) runDependencies(cmd *cobra.Command, job runJob, o *reqOpts) error {
	r, file := job.req, job.file
	if o.dry {
		return nil // placeholders stand in for the values
	}
	texts := []string{r.URL, r.Body}
	for _, h := range r.Headers {
		texts = append(texts, h.Name, h.Value)
	}
	for _, name := range dependencies(texts...) {
		if _, done := a.run.captured[name]; done {
			continue
		}
		if name == r.Name || a.run.running[name] {
			return fmt.Errorf("request %q takes a value from %q, which depends on it (a cycle)", r.Name, name)
		}
		var dep *httpfile.Request
		for i := range file.Requests {
			if file.Requests[i].Name == name {
				dep = &file.Requests[i]
				break
			}
		}
		if dep == nil {
			return fmt.Errorf("request %q uses {{%s.response...}} but this file has no request named %q (name one with `# @name %s`)", r.Name, name, name, name)
		}
		if a.run.running == nil {
			a.run.running = map[string]bool{}
		}
		a.run.running[r.Name] = true
		a.say(fmt.Sprintf("  ↳ running %q first: %q uses its response", name, r.Name))
		err := a.runRequest(cmd, runJob{path: job.path, file: file, req: dep, uservars: job.uservars, silent: true}, o)
		delete(a.run.running, r.Name)
		if err != nil {
			return err
		}
	}
	return nil
}
