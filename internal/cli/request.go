package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"cybercat/api/internal/har"
	"cybercat/api/internal/history"
	"cybercat/api/internal/httpc"
	"cybercat/api/internal/render"
	"cybercat/api/internal/ui"

	"github.com/spf13/cobra"
)

func (a *app) envOr(name, def string) string {
	if v := a.getenv(name); v != "" {
		return v
	}
	return def
}

func isTerminal(v any) bool { return ui.IsTerminal(v) }

func readAll(r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	return strings.TrimRight(string(b), "\r\n"), err
}

// resolveBody picks the body: -d wins over the positional; "-" and "@file"
// read stdin / a file; with neither, a piped stdin is the body.
func (a *app) resolveBody(flagVal, arg string) (string, error) {
	src := flagVal
	if src == "" {
		src = arg
	}
	switch {
	case src == "-":
		return readAll(a.io.In)
	case strings.HasPrefix(src, "@"):
		b, err := os.ReadFile(src[1:])
		return strings.TrimRight(string(b), "\r\n"), err
	case src != "":
		return src, nil
	case !isTerminal(a.io.In):
		return readAll(a.io.In)
	}
	return "", nil
}

// newSpec builds the common part of a request: URL, auth, headers, timeouts.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (a *app) newSpec(method, target string, o *reqOpts) (httpc.Spec, error) {
	return a.newSpecFor(a.envName, method, target, o)
}

// newSpecFor is newSpec for a given env name (see resolveFor).
func (a *app) newSpecFor(envName, method, target string, o *reqOpts) (httpc.Spec, error) {
	s, prof := a.resolveFor(envName)
	// A named env without a url cannot say where to send a relative path. Say so,
	// instead of the generic "API_BASE_URL not set": the fix is in the config.
	if envName != "" && s.BaseURL == "" && strings.HasPrefix(target, "/") {
		if p, ok := a.cfg.Env[envName]; ok && p.URL == "" {
			return httpc.Spec{}, fmt.Errorf("env %q has no url in your config: set it with `api config set env.%s.url https://… --global` (an env's login and token belong to that url, so a project .env cannot supply it)", envName, envName)
		}
	}
	full, err := httpc.ResolveURL(s.BaseURL, target, o.query)
	if err != nil {
		return httpc.Spec{}, err
	}
	spec := httpc.Spec{
		Method:         method,
		URL:            full,
		Timeout:        o.timeout,
		ConnectTimeout: o.connect,
		Insecure:       o.insecure,
		// --trust-hosts also allows following a redirect to another host
		FollowCrossOrigin: o.trustHosts,
	}
	for _, name := range sortedKeys(prof.Headers) {
		spec.SetHeader(name, prof.Headers[name])
	}
	for _, h := range o.headers {
		name, value, ok := strings.Cut(h, ":")
		if !ok || strings.TrimSpace(name) == "" {
			return httpc.Spec{}, fmt.Errorf("bad header %q: want \"Name: value\"", h)
		}
		spec.SetHeader(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	if s.Token != "" && !o.noAuth && !spec.HasHeader("Authorization") {
		// Your token goes to the active base URL only. An absolute URL on another
		// host (typed, pasted, or from a project alias) does not get it unless you
		// say so: --token (explicit) or --trust-hosts.
		if oc := origin(full); oc != "" && oc != origin(s.BaseURL) && !o.trustHosts && a.token == "" {
			a.say(fmt.Sprintf("⚠ not sending your token to %s (not the active base URL): pass --token or --trust-hosts", oc))
		} else {
			spec.SetHeader("Authorization", "Bearer "+s.Token)
		}
	}
	if s.Token == "" && !o.noAuth && !o.dry && !spec.HasHeader("Authorization") {
		// A cached token goes to the env's base URL only, even with --trust-hosts:
		// it is a credential at rest, not something you typed.
		if oc := origin(full); oc != "" && oc == origin(s.BaseURL) {
			if tok := a.cachedToken(s.Name, oc); tok != "" {
				spec.SetHeader("Authorization", "Bearer "+tok)
			}
		}
	}
	spec.DefaultHeader("Accept", "application/json")
	return spec, nil
}

func setJSONBody(spec *httpc.Spec, body string) {
	if body != "" {
		spec.Body = body
		spec.DefaultHeader("Content-Type", "application/json")
	}
}

// verbCmd is get / del / post / put / patch.
func (a *app) verbCmd(name, method string, withBody bool, short string, aliases ...string) *cobra.Command {
	o := &reqOpts{}
	use := name + " <path> "
	if withBody {
		use += "[body] "
	}
	use += "[.filter] [-- curl args]"
	cmd := &cobra.Command{
		Use:     use,
		Short:   short,
		Aliases: aliases,
		Args:    cobra.MinimumNArgs(1),
		Example: fmt.Sprintf("  api %s /users '.data[0].id'", name),
		RunE: func(cmd *cobra.Command, args []string) error {
			before, pass := splitDash(cmd, args)
			target, bodyArg, filter, err := splitPositionals(before, withBody)
			if err != nil {
				return err
			}
			spec, err := a.newSpec(method, target, o)
			if err != nil {
				return err
			}
			if withBody {
				body, err := a.resolveBody(o.data, bodyArg)
				if err != nil {
					return err
				}
				setJSONBody(&spec, body)
			}
			return a.dispatch(cmd.Context(), spec, o, filter, target, pass)
		},
		ValidArgsFunction: a.verbPathCompletion(method),
	}
	a.bindFlags(o, cmd, withBody)
	return cmd
}

// dispatch is the shared tail of every request command.
func (a *app) dispatch(ctx context.Context, spec httpc.Spec, o *reqOpts, filter, histPath string, pass []string) error {
	if o.dry {
		fmt.Fprintln(a.io.Out, a.curlLine(spec, pass))
		return nil
	}
	if len(pass) > 0 {
		return a.execCurl(spec, pass)
	}
	resp, err := a.sendRecovering(ctx, spec, o)
	if err != nil {
		return err
	}
	var rec *history.Entry
	if histPath != "" {
		// the server the request really went to (histPath may be an absolute URL);
		// one that cannot be named is not remembered
		if oc := origin(spec.URL); oc != "" {
			rec = &history.Entry{Method: spec.Method, Path: histPath, Filter: filter, Origin: oc}
		}
	}
	return a.finish(resp, o, filter, rec)
}

// sendRecovering is send plus the 401 recovery (cached token, automatic login).
func (a *app) sendRecovering(ctx context.Context, spec httpc.Spec, o *reqOpts) (*httpc.Response, error) {
	resp, err := a.send(ctx, spec, o)
	if err != nil {
		return nil, err
	}
	if resp.Status == 401 {
		if again, ok := a.retryOn401(ctx, spec, o); ok {
			resp = again
		}
	}
	return resp, nil
}

// send runs the request, with --debug / --verbose tracing on stderr.
func (a *app) send(ctx context.Context, spec httpc.Spec, o *reqOpts) (*httpc.Response, error) {
	if a.debug || a.getenv("API_DEBUG") != "" {
		a.say("→ " + a.curlLine(spec, nil))
	}
	if o.verbose {
		a.trace("> %s %s", spec.Method, spec.URL)
		for _, h := range spec.Headers {
			a.trace("> %s: %s", h.Name, maskHeader(h.Name, h.Value))
		}
		a.trace(">")
	}
	resp, err := httpc.Do(ctx, spec, o.retry)
	if err != nil {
		return nil, &ExitError{Code: httpc.ExitCode(err), Msg: "✗ " + shortErr(err)}
	}
	if o.verbose {
		a.trace("< %s %d %s (%s)", resp.Proto, resp.Status, resp.StatusText, resp.Duration.Round(time.Millisecond))
		printHeaders(a.io.Err, "< ", resp.Header)
		a.trace("<")
	}
	if o.har != "" {
		entry := har.Build(spec, resp, time.Now().Add(-resp.Duration), har.Options{IncludeSecrets: o.harSecrets})
		if err := har.Append(o.har, entry, Version); err != nil {
			a.say("⚠ could not write HAR: " + err.Error())
		} else if o.harSecrets {
			a.say("⚠ " + o.har + " contains secrets: do not share it")
		}
	}
	if o.summary {
		footer := fmt.Sprintf("\n─── api ───────────────────────────────\n  status : %d\n  type   : %s\n  time   : %.3fs\n  size   : %d bytes\n  url    : %s\n───────────────────────────────────────",
			resp.Status, resp.Header.Get("Content-Type"), resp.Duration.Seconds(), len(resp.Body), spec.URL)
		a.trace("%s", ui.Paint(footer, ui.Dim, a.colorErr()))
	}
	return resp, nil
}

// finish prints the response and turns HTTP >= 400 / filter errors into exit
// codes. The body is always printed first, like curl --fail-with-body.
func (a *app) finish(resp *httpc.Response, o *reqOpts, filter string, rec *history.Entry) error {
	var buf bytes.Buffer
	color := a.colorOut()
	if o.include {
		buf.WriteString(ui.StatusLine(resp.Proto, resp.Status, resp.StatusText, color) + "\n")
		writeHeaders(&buf, "", resp.Header, color)
		buf.WriteByte('\n')
	}

	var filterErr error
	if o.output != "" {
		if err := os.WriteFile(o.output, resp.Body, 0o644); err != nil {
			return err
		}
		a.say(fmt.Sprintf("✓ saved %d bytes → %s", len(resp.Body), o.output))
	} else if o.table && !o.rawBody && writeTable(&buf, resp.Body, filter, o.columns, color) {
		// printed as a table
	} else {
		filterErr = render.Write(&buf, resp.Body, render.Options{
			Filter:    filter,
			RawBody:   o.rawBody,
			Compact:   o.compact,
			RawOutput: o.rawOutput,
			Color:     color,
		})
	}
	// Long output on a terminal goes through the pager (less -FRX by default:
	// it exits by itself when everything fits one screen).
	if err := ui.Page(a.io.Out, buf.Bytes(), ui.PagerOptions{
		Cmd:     ui.PagerCmdWith(a.lookup),
		Enabled: ui.ShouldPageWith(a.io.Out, a.noPager, a.lookup),
	}); err != nil {
		return err
	}

	if resp.Status >= 400 {
		return &ExitError{Code: exitHTTP, Msg: fmt.Sprintf("✗ HTTP %d %s", resp.Status, resp.StatusText)}
	}
	if rec != nil {
		history.Record(*rec)
	}
	if filterErr != nil {
		return &ExitError{Code: exitFilter, Msg: "✗ " + filterErr.Error()}
	}
	return nil
}
