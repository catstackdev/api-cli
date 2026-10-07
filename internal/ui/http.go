package ui

import (
	"regexp"
	"strings"
)

var httpVar = regexp.MustCompile(`\{\{[^{}]*\}\}`)

// paintVars colours the {{...}} references of a .http line: your session's own
// values (token, base, graphql, env) green, and {{$env ...}} yellow, because
// those are the ones you have to provide before the request can run.
func paintVars(s string, on bool) string {
	return httpVar.ReplaceAllStringFunc(s, func(v string) string {
		if strings.HasPrefix(v, "{{$env") {
			return Paint(v, Yellow, on)
		}
		return Paint(v, Green, on)
	})
}

// HTTP highlights .http text (what `api import` prints and `api run` reads):
// "###" names in bold cyan, comments dim, the method in bold, header names in
// cyan, {{variables}} by kind. A request body goes through body, when given
// (the caller passes its JSON colourer: this package knows nothing about jq
// palettes). With on false the text is returned unchanged, and with it on,
// removing the escape codes gives back the exact input.
func HTTP(text string, on bool, body func(string) string) string {
	if !on {
		return text
	}
	lines := strings.Split(text, "\n")
	var out []string
	inBody, seenRequest := false, false
	var bodyLines []string
	flush := func() {
		if len(bodyLines) == 0 {
			return
		}
		raw := strings.Join(bodyLines, "\n")
		if body != nil {
			out = append(out, body(raw))
		} else {
			out = append(out, paintVars(raw, on))
		}
		bodyLines = nil
	}
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "###"):
			flush()
			inBody, seenRequest = false, false
			out = append(out, Paint(l, Bold+";"+Cyan, on))
		case inBody:
			bodyLines = append(bodyLines, l)
		case strings.HasPrefix(l, "#") || strings.HasPrefix(l, "//"):
			out = append(out, Paint(l, Dim, on))
		case l == "":
			if seenRequest {
				inBody = true
			}
			out = append(out, l)
		case !seenRequest:
			seenRequest = true
			method, rest, _ := strings.Cut(l, " ")
			if rest == "" {
				out = append(out, paintVars(l, on))
				continue
			}
			out = append(out, Paint(method, Bold, on)+" "+paintVars(rest, on))
		default:
			if name, value, ok := strings.Cut(l, ":"); ok && !strings.ContainsAny(name, " \t") {
				out = append(out, Paint(name, Cyan, on)+":"+paintVars(value, on))
			} else {
				out = append(out, paintVars(l, on))
			}
		}
	}
	flush()
	return strings.Join(out, "\n")
}
