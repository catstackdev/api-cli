package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"cybercat/api/internal/har"
	"cybercat/api/internal/history"
	"cybercat/api/internal/httpc"
	"cybercat/api/internal/render"
	"cybercat/api/internal/session"
	"cybercat/api/internal/ui"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// requestOpts is WHAT to send.
type requestOpts struct {
	headers    []string
	query      []string
	data       string
	noAuth     bool
	dry        bool
	trustHosts bool
}

// transportOpts is HOW to send it.
type transportOpts struct {
	timeout  time.Duration
	connect  time.Duration
	retry    int
	insecure bool
}

// outputOpts is how to SHOW the response.
type outputOpts struct {
	output     string
	include    bool
	verbose    bool
	summary    bool
	rawOutput  bool
	compact    bool
	rawBody    bool
	table      bool
	columns    []string
	har        string
	harSecrets bool
}

// reqOpts are the flags shared by every command that sends a request, in three
// groups. The groups are embedded, so o.timeout, o.table, ... keep working.
type reqOpts struct {
	requestOpts
	transportOpts
	outputOpts
}

func (a *app) envSeconds(name string, def time.Duration) time.Duration {
	if n, err := strconv.ParseFloat(a.getenv(name), 64); err == nil && n > 0 {
		return time.Duration(n * float64(time.Second))
	}
	return def
}

// bindFlags registers the shared request flags on cmd.
func (a *app) bindFlags(o *reqOpts, cmd *cobra.Command, withBody bool) {
	f := cmd.Flags()
	a.bindRequestFlags(o, f, withBody)
	a.bindTransportFlags(o, f)
	a.bindOutputFlags(o, f)
	_ = cmd.RegisterFlagCompletionFunc("header", headerCompletion)
}

func (a *app) bindRequestFlags(o *reqOpts, f *pflag.FlagSet, withBody bool) {
	f.StringArrayVarP(&o.headers, "header", "H", nil, "extra header, \"Name: value\" (repeatable)")
	f.StringArrayVarP(&o.query, "query", "Q", nil, "query parameter key=value (repeatable)")
	f.BoolVar(&o.trustHosts, "trust-hosts", false, "send your token to an absolute URL on another host (default: only to the active base URL)")
	f.BoolVar(&o.noAuth, "no-auth", false, "do not send the bearer token")
	f.BoolVar(&o.dry, "dry", false, "print the curl command instead of sending")
	if withBody {
		f.StringVarP(&o.data, "data", "d", "", "request body; @file reads a file, - reads stdin")
	}
}

func (a *app) bindTransportFlags(o *reqOpts, f *pflag.FlagSet) {
	f.DurationVar(&o.timeout, "timeout", a.envSeconds("CURL_MAX_TIME", a.cfgDuration(a.cfg.Defaults.Timeout, 300*time.Second)), "total time limit (env CURL_MAX_TIME, seconds)")
	f.DurationVar(&o.connect, "connect-timeout", a.envSeconds("CURL_CONNECT_TIMEOUT", a.cfgDuration(a.cfg.Defaults.ConnectTimeout, 10*time.Second)), "connect time limit (env CURL_CONNECT_TIMEOUT, seconds)")
	f.IntVar(&o.retry, "retry", a.cfgRetry(), "retry transport errors and 502/503/504 N times")
	f.BoolVarP(&o.insecure, "insecure", "k", false, "skip TLS certificate verification")
}

func (a *app) bindOutputFlags(o *reqOpts, f *pflag.FlagSet) {
	f.StringVarP(&o.output, "output", "o", "", "write the body to a file")
	f.BoolVarP(&o.include, "include", "i", false, "print the status line and response headers")
	f.BoolVarP(&o.verbose, "verbose", "v", false, "print request and response headers to stderr")
	f.BoolVar(&o.summary, "summary", a.getenv("API_SUMMARY") != "" || a.cfgSummary(), "print a status/type/time/size footer to stderr (env API_SUMMARY)")
	f.BoolVarP(&o.table, "table", "t", false, "print an array of objects (or one object) as a table")
	f.StringSliceVar(&o.columns, "columns", nil, "with --table: the columns to show, in order (a,b,c)")
	f.BoolVarP(&o.rawOutput, "raw-output", "r", false, "print string results without quotes (jq -r)")
	f.BoolVarP(&o.compact, "compact", "c", false, "compact JSON output")
	f.BoolVarP(&o.rawBody, "raw-body", "R", false, "print the body untouched: no filter, no pretty-print")
	f.StringVar(&o.har, "har", "", "also write the exchange to a HAR file (appends; secrets are masked)")
	f.BoolVar(&o.harSecrets, "har-secrets", false, "with --har: keep tokens, cookies and passwords in the file (dangerous to share)")
}

// isFilter reports whether an argument is a jq filter. ./file and ../file are
// paths, not filters.
func isFilter(s string) bool {
	return strings.HasPrefix(s, ".") && !strings.HasPrefix(s, "./") && !strings.HasPrefix(s, "../")
}

// splitPositionals sorts <path> [body] [.filter]: the last filter wins, the
// first non-filter after the path is the body.
func splitPositionals(args []string, withBody bool) (target, body, filter string, err error) {
	if len(args) == 0 {
		return "", "", "", fmt.Errorf("missing <path>")
	}
	target, filter = args[0], "."
	for _, x := range args[1:] {
		switch {
		case isFilter(x):
			filter = x
		case withBody && body == "":
			body = x
		default:
			return "", "", "", fmt.Errorf("unexpected argument %q", x)
		}
	}
	return target, body, filter, nil
}

func splitDash(cmd *cobra.Command, args []string) (before, pass []string) {
	if i := cmd.ArgsLenAtDash(); i >= 0 {
		return args[:i], args[i:]
	}
	return args, nil
}

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
		ValidArgsFunction: pathCompletion,
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
		rec = &history.Entry{Method: spec.Method, Path: histPath, Filter: filter}
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
		fmt.Fprintf(a.io.Err, "> %s %s\n", spec.Method, spec.URL)
		for _, h := range spec.Headers {
			fmt.Fprintf(a.io.Err, "> %s: %s\n", h.Name, maskHeader(h.Name, h.Value))
		}
		fmt.Fprintln(a.io.Err, ">")
	}
	resp, err := httpc.Do(ctx, spec, o.retry)
	if err != nil {
		return nil, &ExitError{Code: httpc.ExitCode(err), Msg: "✗ " + shortErr(err)}
	}
	if o.verbose {
		fmt.Fprintf(a.io.Err, "< %s %d %s (%s)\n", resp.Proto, resp.Status, resp.StatusText, resp.Duration.Round(time.Millisecond))
		printHeaders(a.io.Err, "< ", resp.Header)
		fmt.Fprintln(a.io.Err, "<")
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
		fmt.Fprintln(a.io.Err, ui.Paint(footer, ui.Dim, a.colorErr()))
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

// ── curl bridge ────────────────────────────────────────────────────────────

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellWord(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return session.Quote(s)
}

func maskHeader(name, value string) string {
	if strings.EqualFold(name, "Authorization") {
		if tok, ok := strings.CutPrefix(value, "Bearer "); ok {
			return "Bearer " + session.MaskToken(tok)
		}
	}
	return value
}

// curlLine is the curl command for --dry and --debug, with secrets masked: the
// bearer token (first characters only), credential headers, secret-looking JSON
// fields / form pairs / query parameters. So it is safe to paste into a bug
// report, and a resolved @keychain password never appears in it.
func (a *app) curlLine(spec httpc.Spec, pass []string) string {
	masked := spec
	masked.URL = har.MaskURL(spec.URL)
	masked.Headers = make([]httpc.Header, len(spec.Headers))
	ctype := ""
	for i, h := range spec.Headers {
		v := har.MaskHeader(h.Name, h.Value)
		if strings.EqualFold(h.Name, "Authorization") {
			if tok, ok := strings.CutPrefix(h.Value, "Bearer "); ok {
				v = "Bearer " + session.MaskToken(tok)
			}
		}
		if strings.EqualFold(h.Name, "Content-Type") {
			ctype = h.Value
		}
		masked.Headers[i] = httpc.Header{Name: h.Name, Value: v}
	}
	masked.Body = har.MaskBody(spec.Body, ctype)
	masked.Data = make([]string, len(spec.Data))
	for i, d := range spec.Data {
		masked.Data[i] = har.MaskPair(d)
	}
	masked.Fields = make([]string, len(spec.Fields))
	for i, f := range spec.Fields {
		masked.Fields[i] = har.MaskPair(f)
	}

	words := []string{"curl"}
	for _, arg := range append(masked.CurlArgs(), pass...) {
		words = append(words, shellWord(arg))
	}
	return strings.Join(words, " ")
}

// execCurl hands the request to the real curl, with extra flags after `--`.
func (a *app) execCurl(spec httpc.Spec, pass []string) error {
	path, err := exec.LookPath("curl")
	if err != nil {
		return &ExitError{Code: exitGeneric, Msg: "✗ curl not found in PATH"}
	}
	c := exec.Command(path, append(spec.CurlArgs(), pass...)...)
	c.Stdin, c.Stdout, c.Stderr = a.io.In, a.io.Out, a.io.Err
	if err := c.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return &ExitError{Code: ee.ExitCode()}
		}
		return err
	}
	return nil
}

// writeHeaders prints sorted "Name: value" lines, names coloured when asked.
func writeHeaders(w io.Writer, prefix string, h http.Header, color bool) {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range h[k] {
			fmt.Fprintln(w, ui.HeaderLine(prefix, k, v, color))
		}
	}
}

func printHeaders(w io.Writer, prefix string, h http.Header) {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range h[k] {
			fmt.Fprintf(w, "%s%s: %s\n", prefix, k, v)
		}
	}
}

func shortErr(err error) string {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err.Error()
	}
	return err.Error()
}
