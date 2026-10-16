package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"cybercat/api/internal/gqlquery"
	"cybercat/api/internal/gqlws"
	"cybercat/api/internal/har"
	"cybercat/api/internal/httpc"
	"cybercat/api/internal/jsonbody"
	"cybercat/api/internal/render"
	"cybercat/api/internal/session"

	"github.com/spf13/cobra"
)

// subOpts are the flags of `api gql` that only apply to a subscription.
type subOpts struct {
	protocol    string
	max         int
	tokenInInit bool
	init        []string
	strict      bool
}

// errStopStream ends a subscription from inside the event callback; the reason
// is kept by the caller.
var errStopStream = errors.New("stop")

var upgradeStatus = regexp.MustCompile(`but got (\d{3})`)

func bindSubscribeFlags(cmd *cobra.Command, so *subOpts) {
	f := cmd.Flags()
	f.StringVar(&so.protocol, "protocol", "", "subscription protocol: graphql-transport-ws (default) or graphql-ws (legacy Apollo); env.<name>.ws_protocol")
	f.IntVar(&so.max, "max", 0, "subscriptions: stop after N events (0 = until Ctrl+C or the server completes)")
	f.BoolVar(&so.tokenInInit, "token-in-init", false, "subscriptions: also send the token in connection_init, for servers that read it there; env.<name>.ws_token_in_init")
	f.StringArrayVar(&so.init, "init", nil, "subscriptions: connection_init payload key=value or key:=json (repeatable; values may be @keychain:NAME)")
	_ = cmd.RegisterFlagCompletionFunc("protocol", func(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
		return []cobra.Completion{"graphql-transport-ws", "graphql-ws"}, cobra.ShellCompDirectiveNoFileComp
	})
}

// gqlSubscribe runs a subscription: the request is built like every other one
// (newSpec: URL, headers, and the token only if the destination rules allow it),
// then the same URL, headers and token are used for the WebSocket upgrade.
func (a *app) gqlSubscribe(ctx context.Context, cmd *cobra.Command, o *reqOpts, so *subOpts, call gqlCall) error {
	if err := checkSubscribeOptions(o, so, call); err != nil {
		return err
	}
	cfg, err := a.subscribeConfig(o, so, call)
	if err != nil {
		return err
	}
	if o.dry || o.verbose || a.debug || a.getenv("API_DEBUG") != "" {
		// the same masked description for --dry, --debug and -v: a stream has no
		// request/response pair to trace, so this is what -v can show of it
		desc := subscribeDescription(cfg)
		if o.dry {
			fmt.Fprintln(a.io.Out, desc)
			return nil
		}
		a.say("→ " + desc)
	}

	if cmd.Flags().Changed("timeout") {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
	}
	return a.streamSubscription(ctx, o, so, cfg, call.filter)
}

// checkSubscribeOptions refuses the options that make no sense on a stream.
func checkSubscribeOptions(o *reqOpts, so *subOpts, call gqlCall) error {
	switch {
	case len(call.pass) > 0:
		return fmt.Errorf("a subscription runs over a WebSocket: curl arguments (--) do not apply")
	case o.output != "" || o.har != "":
		return fmt.Errorf("a subscription is a stream: -o and --har do not apply")
	case so.max < 0:
		return fmt.Errorf("--max must be 0 or more")
	}
	return nil
}

// subscribeConfig builds the WebSocket connection from the same Spec every
// other request uses, so the token's destination rules apply unchanged.
func (a *app) subscribeConfig(o *reqOpts, so *subOpts, call gqlCall) (gqlws.Config, error) {
	endpoint, payload := call.endpoint, call.payload
	var req struct {
		Query     string          `json:"query"`
		Variables json.RawMessage `json:"variables"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return gqlws.Config{}, err
	}

	s, prof := a.resolve()
	target := gqlquery.Target(s.GraphQLURL, endpoint)
	spec, err := a.newSpec("GET", target, o)
	if err != nil {
		return gqlws.Config{}, err
	}
	u, err := url.Parse(spec.URL)
	if err != nil {
		return gqlws.Config{}, err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return gqlws.Config{}, fmt.Errorf("cannot open a WebSocket to %q", spec.URL)
	}
	// env.<name>.subscriptions is a PATH on the env's own server: the origin, and so
	// where the token goes, is the one the GraphQL URL already had.
	if endpoint == "" && prof.Subscriptions != "" {
		pu, perr := url.Parse(prof.Subscriptions)
		if perr != nil {
			return gqlws.Config{}, perr
		}
		u.Path, u.RawPath = pu.Path, ""
		if pu.RawQuery != "" {
			u.RawQuery = pu.RawQuery
		}
	}

	protoName := so.protocol
	if protoName == "" {
		protoName = prof.WSProtocol
	}
	proto, err := gqlws.ParseProtocol(protoName)
	if err != nil {
		return gqlws.Config{}, err
	}

	header := http.Header{}
	for _, h := range spec.Headers {
		if strings.EqualFold(h.Name, "Accept") || strings.EqualFold(h.Name, "Content-Type") {
			continue
		}
		header.Add(h.Name, h.Value)
	}
	auth := header.Get("Authorization")

	resolver := a.resolverFor(srcUser, a.destOrigin(a.envName, target, o))
	if o.dry {
		resolver = jsonbody.Resolver(maskRef)
	}
	var initPayload map[string]any
	if len(so.init) > 0 {
		built, berr := jsonbody.BuildWith(so.init, false, true, resolver)
		if berr != nil {
			return gqlws.Config{}, berr
		}
		if err := json.Unmarshal([]byte(built), &initPayload); err != nil {
			return gqlws.Config{}, err
		}
	}
	if so.tokenInInit || (prof.WSTokenInInit != nil && *prof.WSTokenInInit) {
		if auth == "" {
			a.say("⚠ no token to put in connection_init (log in, or pass --token)")
		} else {
			if initPayload == nil {
				initPayload = map[string]any{}
			}
			initPayload["Authorization"] = auth
		}
	}
	if u.Scheme == "ws" && auth != "" && !isLoopback(u.Hostname()) {
		a.say("⚠ sending your token over plain ws:// to " + u.Host + ": use a wss:// URL")
	}

	cfg := gqlws.Config{
		URL:            u.String(),
		Header:         header,
		Protocol:       proto,
		InitPayload:    initPayload,
		Query:          req.Query,
		Variables:      req.Variables,
		Insecure:       o.insecure,
		ConnectTimeout: o.connect,
		Max:            so.max,
	}
	return cfg, nil
}

// streamSubscription prints each event through the filter until the stream ends
// and maps how it ended to an exit code.
func (a *app) streamSubscription(ctx context.Context, o *reqOpts, so *subOpts, cfg gqlws.Config, filter string) error {
	color := a.colorOut()
	var filterErr error
	gqlMsg := ""
	emit := func(p json.RawMessage) error {
		var buf bytes.Buffer
		if o.table && !o.rawBody && writeTable(&buf, p, filter, o.columns, color) {
			// printed as a table
		} else if err := render.Write(&buf, p, render.Options{
			Filter: filter, RawBody: o.rawBody, Compact: true, RawOutput: o.rawOutput, Color: color,
		}); err != nil {
			filterErr = err
			return errStopStream
		}
		if _, err := a.io.Out.Write(buf.Bytes()); err != nil {
			return err
		}
		if msg := gqlError(p); so.strict && msg != "" {
			gqlMsg = msg
			return errStopStream
		}
		return nil
	}

	err := gqlws.Subscribe(ctx, cfg, emit)
	return subscribeExit(ctx, o, err, filterErr, gqlMsg)
}

// subscribeExit turns the way a subscription ended into the command's result.
func subscribeExit(ctx context.Context, o *reqOpts, err, filterErr error, gqlMsg string) error {
	switch {
	case err == nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		// --timeout is curl's --max-time: reaching it is exit 28 (use --max N for a clean stop)
		return &ExitError{Code: 28, Msg: "✗ timed out after " + o.timeout.String()}
	case err == nil:
		return nil
	case errors.Is(err, errStopStream) && filterErr != nil:
		return &ExitError{Code: exitFilter, Msg: "✗ " + filterErr.Error()}
	case errors.Is(err, errStopStream) && gqlMsg != "":
		return &ExitError{Code: exitGraphQL, Msg: "✗ GraphQL: " + gqlMsg}
	case gqlws.IsServerError(err):
		var se *gqlws.ServerError
		_ = errors.As(err, &se)
		msg := "the server ended the subscription with an error"
		if m := gqlError(wrapForMessage(se.Payload)); m != "" {
			msg = m
		}
		return &ExitError{Code: exitGraphQL, Msg: "✗ GraphQL: " + msg}
	}
	if m := upgradeStatus.FindStringSubmatch(err.Error()); m != nil {
		return &ExitError{Code: exitHTTP, Msg: fmt.Sprintf("✗ HTTP %s on the WebSocket upgrade (token missing or rejected? a redirect is never followed)", m[1])}
	}
	return &ExitError{Code: httpc.ExitCode(err), Msg: "✗ " + shortErr(err)}
}

// wrapForMessage makes the payload of an error message readable by gqlError.
func wrapForMessage(p json.RawMessage) []byte {
	var list []json.RawMessage
	if json.Unmarshal(p, &list) == nil {
		b, _ := json.Marshal(map[string]any{"errors": list})
		return b
	}
	b, _ := json.Marshal(map[string]any{"errors": []json.RawMessage{p}})
	return b
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// subscribeDescription is what --dry and --debug print: the same fields a curl
// line would show, masked the same way (credential headers, secret-looking
// payload fields), because the token may be in both the upgrade and connection_init.
func subscribeDescription(c gqlws.Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, "WebSocket %s (%s)", har.MaskURL(c.URL), c.Protocol)
	names := make([]string, 0, len(c.Header))
	for n := range c.Header {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		for _, v := range c.Header[n] {
			shown := har.MaskHeader(n, v)
			if tok, ok := strings.CutPrefix(v, "Bearer "); ok && strings.EqualFold(n, "Authorization") {
				shown = "Bearer " + session.MaskToken(tok)
			}
			fmt.Fprintf(&b, "\n  > %s: %s", n, shown)
		}
	}
	if c.InitPayload != nil {
		raw, _ := json.Marshal(c.InitPayload)
		fmt.Fprintf(&b, "\n  connection_init %s", har.MaskBody(string(raw), "application/json"))
	}
	op, _ := json.Marshal(map[string]any{"query": c.Query, "variables": c.Variables})
	fmt.Fprintf(&b, "\n  subscribe %s", har.MaskBody(string(op), "application/json"))
	return b.String()
}
