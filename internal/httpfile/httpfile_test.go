package httpfile

import (
	"regexp"
	"strings"
	"testing"
)

const sample = `# a comment before everything
@host = https://api.dev.example.com
@user = me@x.com

### List users
# @filter .data | length
GET {{host}}/users
    ?limit=5
    &sort=asc
Accept: application/json

### login
# @name login-request
POST {{host}}/auth/login HTTP/1.1
Content-Type: application/json
X-Trace: {{$uuid}}

{
  "email": "{{user}}",
  "password": "{{$env API_PASSWORD}}"
}


### From a file
PUT /things/1
Content-Type: application/json

< ./body.json

### bare url
/ping
`

func TestParse(t *testing.T) {
	f, err := Parse([]byte(strings.ReplaceAll(sample, "\n", "\r\n"))) // CRLF too
	if err != nil {
		t.Fatal(err)
	}
	if f.Vars["host"] != "https://api.dev.example.com" || f.Vars["user"] != "me@x.com" {
		t.Fatalf("vars %v", f.Vars)
	}
	if len(f.Requests) != 4 {
		t.Fatalf("%d requests: %+v", len(f.Requests), f.Requests)
	}
	list := f.Requests[0]
	if list.Name != "List users" || list.Method != "GET" || list.URL != "{{host}}/users?limit=5&sort=asc" ||
		list.Filter != ".data | length" || len(list.Headers) != 1 || list.Headers[0] != (Header{"Accept", "application/json"}) || list.Body != "" {
		t.Fatalf("list: %+v", list)
	}
	login := f.Requests[1]
	if login.Name != "login-request" || login.Method != "POST" || login.URL != "{{host}}/auth/login" || len(login.Headers) != 2 {
		t.Fatalf("login (@name overrides, HTTP/1.1 dropped): %+v", login)
	}
	if !strings.HasPrefix(login.Body, "{\n  \"email\"") || strings.HasSuffix(login.Body, "\n") {
		t.Fatalf("login body %q", login.Body)
	}
	if file := f.Requests[2]; file.BodyFile != "./body.json" || file.Body != "" || file.Method != "PUT" {
		t.Fatalf("body file: %+v", file)
	}
	if bare := f.Requests[3]; bare.Method != "GET" || bare.URL != "/ping" {
		t.Fatalf("bare url: %+v", bare)
	}
}

func TestParseErrorsAndEdgeCases(t *testing.T) {
	for in, want := range map[string]string{
		"### x\nFOO /bar\n":                   "bad request line",
		"### x\nGET /a\nnot a header\n\nbody": "expected \"Name: value\"",
		"### only a name\n":                   "has no request line",
	} {
		if _, err := Parse([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) = %v, want error containing %q", in, err, want)
		}
	}
	f, err := Parse([]byte("GET /one\n\n###\n###\nPOST /two\n\n{\"a\":1}\n"))
	if err != nil || len(f.Requests) != 2 || f.Requests[1].Body != `{"a":1}` {
		t.Fatalf("%+v %v", f, err)
	}
	// an HTML body starting with < is a body, not a file reference
	f, _ = Parse([]byte("POST /x\n\n<html></html>\n"))
	if f.Requests[0].BodyFile != "" || f.Requests[0].Body != "<html></html>" {
		t.Fatalf("%+v", f.Requests[0])
	}
}

func TestFind(t *testing.T) {
	f, _ := Parse([]byte(sample))
	for sel, want := range map[string]string{"login-request": "login-request", "LOGIN-REQUEST": "login-request", "2": "login-request", "#1": "List users"} {
		if r, err := f.Find(sel); err != nil || r.Name != want {
			t.Errorf("Find(%q) = %v %v", sel, r, err)
		}
	}
	if _, err := f.Find(""); err == nil || !strings.Contains(err.Error(), "4 requests") {
		t.Fatalf("ambiguous: %v", err)
	}
	if _, err := f.Find("nope"); err == nil || !strings.Contains(err.Error(), "have:") {
		t.Fatalf("%v", err)
	}
	one, _ := Parse([]byte("GET /x\n"))
	if r, err := one.Find(""); err != nil || r.URL != "/x" {
		t.Fatal("a single request needs no selector")
	}
}

func lookupOf(m map[string]string) Lookup {
	return func(n string) (string, bool) { v, ok := m[n]; return v, ok }
}

func TestInterpolate(t *testing.T) {
	vars := map[string]string{"host": "https://h", "url": "{{host}}/x", "token": "SECRET-T", "loop": "{{loop}}"}
	isSecret := func(n string) bool { return n == "token" }
	env := func(n string) (string, bool) {
		if strings.HasPrefix(n, "API_") && n == "API_PASSWORD" {
			return "pw", true
		}
		return "", false
	}

	got, secret, err := Interpolate("{{url}}?a={{ host }}", lookupOf(vars), isSecret, env)
	if err != nil || got != "https://h/x?a=https://h" || secret {
		t.Fatalf("%q %v %v", got, secret, err)
	}
	if _, secret, _ := Interpolate("Bearer {{token}}", lookupOf(vars), isSecret, env); !secret {
		t.Fatal("{{token}} must be flagged secret")
	}
	if got, secret, err := Interpolate("{{$env API_PASSWORD}}", lookupOf(vars), isSecret, env); err != nil || got != "pw" || !secret {
		t.Fatalf("$env: %q %v %v", got, secret, err)
	}
	for _, bad := range []string{"{{$env HOME}}", "{{$env AWS_SECRET_ACCESS_KEY}}", "{{missing}}", "{{$nope}}", "{{loop}}"} {
		if _, _, err := Interpolate(bad, lookupOf(vars), isSecret, env); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	id, _, _ := Interpolate("{{$uuid}}", lookupOf(vars), isSecret, env)
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("uuid %q", id)
	}
	if ts, _, _ := Interpolate("{{$timestamp}}", lookupOf(vars), isSecret, env); len(ts) < 10 {
		t.Fatalf("timestamp %q", ts)
	}
}

func TestLiteralVariablesAreNeverReExpanded(t *testing.T) {
	// a value that came from a server must not be able to pull {{token}} or
	// {{$env ...}} in when it is inserted
	lookup := func(n string) (string, bool) {
		switch n {
		case "login.response.body.x":
			return "{{token}}-{{$env API_SECRET}}", true
		case "plain":
			return "{{token}}", true
		case "token":
			return "REAL-TOKEN", true
		}
		return "", false
	}
	getenv := func(string) (string, bool) { return "ENV-SECRET", true }
	isLiteral := func(n string) bool { return strings.Contains(n, ".response.") }

	out, _, err := InterpolateWith("a={{login.response.body.x}}", Options{Lookup: lookup, Getenv: getenv, IsLiteral: isLiteral})
	if err != nil || out != "a={{token}}-{{$env API_SECRET}}" {
		t.Fatalf("literal value was expanded: %q %v", out, err)
	}
	// ordinary variables still nest (the documented behaviour)
	out, _, _ = InterpolateWith("{{plain}}", Options{Lookup: lookup, Getenv: getenv, IsLiteral: isLiteral})
	if out != "REAL-TOKEN" {
		t.Fatalf("plain nesting broke: %q", out)
	}
}
