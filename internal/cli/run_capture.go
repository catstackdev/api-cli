package cli

// Response capture for `api run`: a later request can use a value from an earlier
// one, `{{login.response.body.data.token}}`, the way REST Client / httpyac files
// do. The response is SERVER data in a FILE that may come from a repo, so:
//
//   - a captured value is data, never a template (httpfile.Options.IsLiteral): the
//     server cannot make `{{token}}` or `{{$env ...}}` expand inside it;
//   - it counts as a secret like {{token}}: a request to another host is refused
//     without --trust-hosts;
//   - the path into the body is a plain key/index path walked in Go, never jq
//     (jq could read your environment);
//   - it lives in memory for this invocation only: never in history, never written.

import (
	"fmt"

	"cybercat/api/internal/capture"
	"cybercat/api/internal/httpc"
	"cybercat/api/internal/httpfile"
)

// captureLookup resolves a {{name.response...}} reference. In a dry run nothing has
// been sent, so it yields a visible placeholder instead.
func (a *app) captureLookup(expr string, dry bool) (string, error) {
	if dry {
		return "<" + expr + ">", nil
	}
	ref, _ := capture.ParseRef(expr) // callers check capture.IsRef first
	c, ok := a.run.captured[ref.Name]
	if !ok {
		return "", fmt.Errorf("{{%s}}: request %q has not run yet", expr, ref.Name)
	}
	v, err := c.Value(ref)
	if err != nil {
		return "", fmt.Errorf("{{%s}}: %w", expr, err)
	}
	return v, nil
}

func (a *app) remember(name string, resp *httpc.Response) {
	if name == "" {
		return
	}
	if a.run.captured == nil {
		a.run.captured = map[string]*capture.Response{}
	}
	a.run.captured[name] = &capture.Response{Status: resp.Status, Header: resp.Header, Body: resp.Body}
}

// interpolateOptions wires captured values into httpfile: data, secret.
func interpolateOptions(lookup httpfile.Lookup, getenv func(string) (string, bool)) httpfile.Options {
	return httpfile.Options{
		Lookup:    lookup,
		Getenv:    getenv,
		IsSecret:  func(name string) bool { return name == "token" || capture.IsRef(name) },
		IsLiteral: capture.IsRef,
	}
}
