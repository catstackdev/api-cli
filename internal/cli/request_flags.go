package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

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
	viewer     string // --viewer: set only on the commands that bind it (bindViewerFlag)
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
	// every command that sends a request can show it (see send): -v lives here, not
	// with the output flags, so the sync commands that print no body have it too
	f.BoolVarP(&o.verbose, "verbose", "v", false, "print the masked request, response headers and timings to stderr")
}

func (a *app) bindOutputFlags(o *reqOpts, f *pflag.FlagSet) {
	f.StringVarP(&o.output, "output", "o", "", "write the body to a file")
	f.BoolVarP(&o.include, "include", "i", false, "print the status line and response headers")
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
