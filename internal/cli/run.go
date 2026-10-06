package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

func (a *app) runCmd() *cobra.Command {
	o := &reqOpts{}
	var (
		vars      []string
		list      bool
		all       bool
		keepGoing bool
	)
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
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				if !list {
					return fmt.Errorf("missing <file|name> (try: api run --list)")
				}
				names := listHTTPFiles()
				if len(names) == 0 {
					a.say("  (no saved requests: put name.http files in " + requestsDir + "/)")
					return nil
				}
				rows := make([][]string, len(names))
				for i, n := range names {
					rows[i] = []string{n}
				}
				ui.Table(a.io.Out, []string{"SAVED REQUESTS"}, rows, a.colorOut())
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

			if list {
				rows := make([][]string, len(file.Requests))
				for i, r := range file.Requests {
					rows[i] = []string{fmt.Sprint(i + 1), r.Name, r.Method, r.URL}
				}
				ui.Table(a.io.Out, []string{"#", "NAME", "METHOD", "URL"}, rows, a.colorOut())
				return nil
			}

			sel, filter := "", ""
			for _, x := range args[1:] {
				if isFilter(x) {
					filter = x
				} else if sel == "" {
					sel = x
				} else {
					return fmt.Errorf("unexpected argument %q", x)
				}
			}
			uservars := map[string]string{}
			for _, kv := range vars {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return fmt.Errorf("bad --var %q: want key=value", kv)
				}
				uservars[k] = v
			}

			var reqs []*httpfile.Request
			if all {
				if sel != "" {
					return fmt.Errorf("--all runs every request: drop the selector %q", sel)
				}
				for i := range file.Requests {
					reqs = append(reqs, &file.Requests[i])
				}
			} else {
				r, err := file.Find(sel)
				if err != nil {
					return err
				}
				reqs = []*httpfile.Request{r}
			}

			if a.asserts, err = compileAsserts(path, reqs); err != nil {
				return err
			}

			var firstErr error
			for _, r := range reqs {
				if all {
					a.say(fmt.Sprintf("→ %s: %s %s", r.Name, r.Method, r.URL))
				}
				err := a.runRequest(cmd, path, file, r, uservars, o, filter, o.trustHosts, false)
				if err != nil {
					if ee, ok := err.(*ExitError); ok && ee.Msg != "" {
						a.say(ee.Msg)
					} else if !ok {
						a.say("✗ " + err.Error())
					}
					if firstErr == nil {
						firstErr = err
					}
					if !keepGoing {
						break
					}
				}
			}
			if all && a.assertTotal > 0 {
				a.say(fmt.Sprintf("%d assertion(s): %d passed, %d failed", a.assertTotal, a.assertTotal-a.assertFailed, a.assertFailed))
			}
			return firstErr
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
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
		},
	}
	a.bindFlags(o, cmd, false)
	f := cmd.Flags()
	f.StringArrayVar(&vars, "var", nil, "set a {{variable}}: key=value (repeatable; beats @var in the file)")
	f.BoolVar(&list, "list", false, "list saved files, or the requests inside a file")
	f.BoolVar(&all, "all", false, "run every request in the file, in order (stops at the first failure)")
	f.BoolVar(&keepGoing, "keep-going", false, "with --all: continue after a failure")
	return cmd
}

// runRequest interpolates one request and sends it through the normal pipeline.
//
// silent runs the request only for the values later requests take from its
// response: nothing is printed.
func (a *app) runRequest(cmd *cobra.Command, path string, file *httpfile.File, r *httpfile.Request, uservars map[string]string, o *reqOpts, filterOverride string, trust, silent bool) error {
	if err := a.runDependencies(cmd, path, file, r, uservars, o, trust); err != nil {
		return err
	}
	s, _ := a.resolve()
	builtin := map[string]string{"base": s.BaseURL, "graphql": s.GraphQLURL, "token": s.Token, "env": s.Name}
	var captureErr error
	lookup := func(name string) (string, bool) {
		if isCaptureRef(name) {
			v, err := a.captureLookup(name, o.dry)
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
		return err
	}
	type kv struct{ name, value string }
	var headers []kv
	for _, h := range r.Headers {
		name, err := interp(h.Name)
		if err != nil {
			return err
		}
		value, err := interp(h.Value)
		if err != nil {
			return err
		}
		headers = append(headers, kv{name, value})
	}
	body := r.Body
	if body != "" {
		// bodies may legitimately span lines: interpolate, but allow breaks
		b, used, err := httpfile.InterpolateWith(body, interpolateOptions(lookup, getenv))
		if err != nil {
			if captureErr != nil {
				return fmt.Errorf("%w (request %q)", captureErr, r.Name)
			}
			return fmt.Errorf("%s (request %q)", err, r.Name)
		}
		secret = secret || used
		body = b
	}
	if r.BodyFile != "" {
		p := r.BodyFile
		if !filepath.IsAbs(p) {
			p = filepath.Join(filepath.Dir(path), p)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("request %q: body file: %w", r.Name, err)
		}
		body = strings.TrimRight(string(data), "\r\n")
	}

	// A file's host is the file author's choice, not yours: never hand it your
	// session token by default.
	opts := *o
	if oc := origin(target); oc != "" && oc != origin(s.BaseURL) && !trust {
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

	filter := filterOverride
	if filter == "" {
		filter = r.Filter
	}
	if filter == "" {
		filter = "."
	}
	if opts.dry {
		if n := len(a.asserts[r]); n > 0 {
			a.say(fmt.Sprintf("→ %d assertion(s) not evaluated with --dry", n))
		}
		return a.dispatch(cmd.Context(), spec, &opts, filter, "", nil)
	}
	resp, err := a.sendRecovering(cmd.Context(), spec, &opts)
	if err != nil {
		return err
	}
	a.remember(r.Name, resp)
	if silent {
		if resp.Status >= 400 {
			return &ExitError{Code: exitHTTP, Msg: fmt.Sprintf("✗ %s: HTTP %d %s (its response is needed by a later request)", r.Name, resp.Status, resp.StatusText)}
		}
		return nil
	}
	if list := a.asserts[r]; len(list) > 0 {
		return a.finishAsserted(r, resp, &opts, filter, list)
	}
	return a.finish(resp, &opts, filter, nil)
}

// runDependencies runs, once and silently, every request whose response r uses
// and that has not run yet.
func (a *app) runDependencies(cmd *cobra.Command, path string, file *httpfile.File, r *httpfile.Request, uservars map[string]string, o *reqOpts, trust bool) error {
	if o.dry {
		return nil // placeholders stand in for the values
	}
	texts := []string{r.URL, r.Body}
	for _, h := range r.Headers {
		texts = append(texts, h.Name, h.Value)
	}
	for _, name := range dependencies(texts...) {
		if _, done := a.captured[name]; done {
			continue
		}
		if name == r.Name || a.running[name] {
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
		if a.running == nil {
			a.running = map[string]bool{}
		}
		a.running[r.Name] = true
		a.say(fmt.Sprintf("  ↳ running %q first: %q uses its response", name, r.Name))
		err := a.runRequest(cmd, path, file, dep, uservars, o, "", trust, true)
		delete(a.running, r.Name)
		if err != nil {
			return err
		}
	}
	return nil
}
