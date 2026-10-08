package cli

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"cybercat/api/internal/httpc"
	"cybercat/api/internal/jsonbody"
	"cybercat/api/internal/render"
	"cybercat/api/internal/ui"

	"github.com/spf13/cobra"
)

func (a *app) jsonCmd() *cobra.Command {
	var flat, compact bool
	cmd := &cobra.Command{
		Use:   "json key=value...",
		Short: "Build a JSON object from key=value / key:=json arguments",
		Long: `key=value is a string, key:=<json> is raw JSON (number, bool, null, array,
object). Dotted keys nest ({"a":{"b":1}} from a.b:=1) unless --flat is set.

A string value can be a reference, so a password never appears in the command
line: password=@keychain:NAME, @env:NAME or @prompt (@@x is the literal "@x").`,
		Example: `  api json name=cat age:=30 admin:=true tags:='["a","b"]'
  api json user.name=cat user.id:=1 | api post /users`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := jsonbody.BuildWith(args, flat, compact, a.resolveRef)
			if err != nil {
				return err
			}
			if a.colorOut() {
				out = string(render.Colorize([]byte(out), render.PaletteFromEnv()))
			}
			fmt.Fprintln(a.io.Out, out)
			return nil
		},
	}
	cmd.Flags().BoolVar(&flat, "flat", false, "keep dots in keys instead of nesting")
	cmd.Flags().BoolVarP(&compact, "compact", "c", false, "one-line output")
	return cmd
}

func parseInterval(s string) (time.Duration, error) {
	if n, err := strconv.ParseFloat(s, 64); err == nil && n > 0 {
		return time.Duration(n * float64(time.Second)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("bad interval %q: want seconds or a duration like 500ms", s)
	}
	return d, nil
}

func (a *app) watchCmd() *cobra.Command {
	o := &reqOpts{}
	cmd := &cobra.Command{
		Use:     "watch <interval> <path> [.filter]",
		Short:   "GET a path every interval until Ctrl+C",
		Example: "  api watch 3 /jobs/123 '.data.status'",
		Args:    cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			interval, err := parseInterval(args[0])
			if err != nil {
				return err
			}
			target, _, filter, err := splitPositionals(args[1:], false)
			if err != nil {
				return err
			}
			spec, err := a.newSpec("GET", target, o)
			if err != nil {
				return err
			}

			a.noPager = true // a pager would block the polling loop
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			a.say(fmt.Sprintf("⟳ watching %s every %s — Ctrl+C to stop", target, interval))
			for {
				fmt.Fprintf(a.io.Out, "\n── %s ──────────────────\n", time.Now().Format("15:04:05"))
				resp, err := a.send(ctx, spec, o)
				if err == nil {
					err = a.finish(resp, o, filter, nil)
				}
				if ee, ok := err.(*ExitError); ok && ee.Msg != "" {
					a.say(ee.Msg)
				}
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(interval):
				}
			}
		},
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			if len(args) == 1 {
				return a.pathCompletion(cmd, nil, toComplete)
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
	}
	a.bindFlags(o, cmd, false)
	return cmd
}

func (a *app) headersCmd() *cobra.Command {
	o := &reqOpts{}
	cmd := &cobra.Command{
		Use:   "headers <path>",
		Short: "GET a path and print only the response headers",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := a.newSpec("GET", args[0], o)
			if err != nil {
				return err
			}
			resp, err := a.send(cmd.Context(), spec, o)
			if err != nil {
				return err
			}
			color := a.colorOut()
			fmt.Fprintln(a.io.Out, ui.StatusLine(resp.Proto, resp.Status, resp.StatusText, color))
			writeHeaders(a.io.Out, "", resp.Header, color)
			return nil
		},
		ValidArgsFunction: a.pathCompletion,
	}
	a.bindFlags(o, cmd, false)
	return cmd
}

func (a *app) timeCmd() *cobra.Command {
	o := &reqOpts{}
	cmd := &cobra.Command{
		Use:   "time <path>",
		Short: "Request timing breakdown: dns / connect / tls / ttfb / total",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := a.newSpec("GET", args[0], o)
			if err != nil {
				return err
			}
			t, err := httpc.Trace(cmd.Context(), spec)
			if err != nil {
				return &ExitError{Code: httpc.ExitCode(err), Msg: "✗ " + shortErr(err)}
			}
			row := func(name string, d time.Duration) {
				fmt.Fprintf(a.io.Out, "%-8s%.3fs\n", name, d.Seconds())
			}
			row("dns", t.DNS)
			row("connect", t.Connect)
			row("tls", t.TLS)
			row("ttfb", t.TTFB)
			row("total", t.Total)
			fmt.Fprintf(a.io.Out, "%-8s%d\n", "status", t.Status)
			return nil
		},
		ValidArgsFunction: a.pathCompletion,
	}
	a.bindFlags(o, cmd, false)
	return cmd
}
