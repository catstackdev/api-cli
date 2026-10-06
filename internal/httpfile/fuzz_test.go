package httpfile

import (
	"strings"
	"testing"
)

// .http files come from repos: nothing in one may crash the parser, and a request
// that parses must have a method and a URL.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		sample,
		"",
		"GET /x\n",
		"### a\n# @name n\nPOST /y\nA: b\n\n{\"x\":1}\n",
		"@v = 1\nGET {{v}}\n",
		"< ./file\n",
		"GET\n",
		"### \n\n\n###\n",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		file, err := Parse(data)
		if err != nil {
			return
		}
		for _, r := range file.Requests {
			if r.Method == "" || r.URL == "" {
				t.Fatalf("request without method or url: %+v", r)
			}
			if strings.ContainsAny(r.Method, " \t\r\n") {
				t.Fatalf("method %q", r.Method)
			}
		}
		// and interpolation of anything in it must not panic
		lookup := func(name string) (string, bool) { v, ok := file.Vars[name]; return v, ok }
		for _, r := range file.Requests {
			_, _, _ = Interpolate(r.URL+r.Body, lookup, nil, func(string) (string, bool) { return "", false })
		}
	})
}

func FuzzInterpolateNeverLoopsForever(f *testing.F) {
	f.Add("{{a}}", "{{a}}")
	f.Add("{{a}} {{b}}", "{{b}}")
	f.Fuzz(func(t *testing.T, a, b string) {
		lookup := func(name string) (string, bool) {
			switch name {
			case "a":
				return a, true
			case "b":
				return b, true
			}
			return "", false
		}
		_, _, _ = Interpolate(a+b, lookup, nil, func(string) (string, bool) { return "", false })
	})
}
