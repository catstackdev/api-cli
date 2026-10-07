package curlimport

import (
	"fmt"
	"sort"
	"strings"

	"cybercat/api/internal/secret"
)

// What to do with one header of a pasted request. The decision is made in one
// place (classify) with a REASON, so the output can say why something was left
// out and the user can overrule it (--keep) or ask for less (--minimal).
type verdict int

const (
	keep    verdict = iota // needed: sent as is
	drop                   // noise or redundant: left out, reported with its group
	auth                   // Authorization: turned into {{token}} or a reference
	secretH                // secret-looking: replaced by a {{$env API_X}} reference
	check                  // not recognised: kept, but flagged so the user can prune it
)

type decision struct {
	v     verdict
	group string // why it was dropped
}

// dropGroups are headers that a browser adds and an API call does not need. A
// group is also how the dropped line is worded, so it reads as a reason.
var dropGroups = []struct {
	group    string
	exact    []string
	prefixes []string
}{
	{"browser", []string{"user-agent", "referer", "origin", "accept-language", "accept-encoding", "dnt", "priority", "upgrade-insecure-requests"}, []string{"sec-"}},
	{"transport", []string{"connection", "host", "content-length", "te", "keep-alive", "transfer-encoding"}, nil},
	// sent from here, a cache validator turns a 200 into an empty 304
	{"cache", []string{"cache-control", "pragma", "if-none-match", "if-modified-since", "if-match", "if-unmodified-since", "if-range"}, nil},
	{"tracing", []string{"traceparent", "tracestate", "baggage", "sentry-trace", "x-request-id", "x-correlation-id", "x-amzn-trace-id"}, []string{"x-datadog-", "x-b3-", "x-sentry-"}},
	// the browser's session, not your API credential (that is {{token}})
	{"browser session", []string{"cookie"}, nil},
}

// groupOrder is the order the dropped line lists its groups in.
var groupOrder = []string{"browser", "transport", "cache", "tracing", "browser session",
	"already sent by api", "already in your env", "not needed (--minimal)", "invalid name", "auth"}

func dropGroup(lower string) (string, bool) {
	for _, g := range dropGroups {
		for _, e := range g.exact {
			if lower == e {
				return g.group, true
			}
		}
		for _, p := range g.prefixes {
			if strings.HasPrefix(lower, p) {
				return g.group, true
			}
		}
	}
	return "", false
}

// redundantAccept: api sends Accept: application/json itself, and a browser's
// "anything" says nothing the server needs.
func redundantAccept(v string) bool {
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(v)), " ", "") {
	case "application/json", "*/*", "application/json,text/plain,*/*":
		return true
	}
	return false
}

// classify decides one header. lower is its lower-cased name. c.Keep overrules a
// drop (the header still goes through the secret check), c.Redundant holds what
// the matching env profile already sends, c.Minimal drops what is not recognised.
func classify(lower, value string, c Context) decision {
	if lower == "authorization" {
		return decision{v: auth}
	}
	if !c.Keep[lower] {
		if g, ok := dropGroup(lower); ok {
			return decision{drop, g}
		}
		if lower == "accept" && redundantAccept(value) {
			return decision{drop, "already sent by api"}
		}
		if v, ok := c.Redundant[lower]; ok && v == value {
			return decision{drop, "already in your env"}
		}
	}
	switch {
	case lower == "content-type" || lower == "accept":
		return decision{v: keep}
	case secret.Name(lower):
		return decision{v: secretH}
	case c.Minimal && !c.Keep[lower]:
		return decision{drop, "not needed (--minimal)"}
	}
	return decision{v: check}
}

// dropLog collects what was left out, by reason.
type dropLog struct{ by map[string][]string }

func (d *dropLog) add(group, name string) {
	if d.by == nil {
		d.by = map[string][]string{}
	}
	d.by[group] = append(d.by[group], name)
}

func (d *dropLog) empty() bool { return len(d.by) == 0 }

// String words the log as "browser (sec-* ×6, origin) · cache (if-none-match)".
func (d *dropLog) String() string {
	var parts []string
	for _, g := range groupOrder {
		names := d.by[g]
		if len(names) == 0 {
			continue
		}
		sec, rest := 0, []string{}
		for _, n := range unique(names) {
			if strings.HasPrefix(n, "sec-") {
				sec++
			} else {
				rest = append(rest, n)
			}
		}
		sort.Strings(rest)
		if sec > 0 {
			rest = append([]string{fmt.Sprintf("sec-* ×%d", sec)}, rest...)
		}
		parts = append(parts, g+" ("+strings.Join(rest, ", ")+")")
	}
	return strings.Join(parts, " · ")
}
