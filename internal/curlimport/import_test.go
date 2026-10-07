package curlimport

import (
	"strings"
	"testing"
)

// A real DevTools "Copy as cURL" of a GraphQL login: $'...' quoting, \\n and
// ! inside the JSON, a null bearer token, browser headers, a password.
const loginCurl = `curl 'https://api.dev.example.test/graphql/v2/auth' \
  -H 'sec-ch-ua-platform: "macOS"' \
  -H 'authorization: Bearer null' \
  -H 'Referer: http://localhost:3000/' \
  -H 'Accept-Language: en-US,en;q=0.9' \
  -H 'User-Agent: Mozilla/5.0' \
  -H 'accept: application/graphql-response+json, application/json' \
  -H 'content-type: application/json' \
  --data-raw $'{"query":"\\n    mutation login($input: LoginInput!) {\\n      login(input: $input) {\\n        accessToken\\n      }\\n    }\\n  ","variables":{"input":{"username":"dev@example.test","password":"Hunter2-SECRET"}},"operationName":"login"}'`

func render(t *testing.T, in string, c Context) Result {
	t.Helper()
	r, err := Parse(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res, err := Render(r, c)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return res
}

func TestLoginCurl(t *testing.T) {
	res := render(t, loginCurl, Context{
		Base: "https://api.dev.example.test", GraphQL: "https://api.dev.example.test/graphql/v2",
		EnvNames: []string{"dev"},
	})
	out := res.HTTP
	for _, want := range []string{
		"### login",
		"graphql mutation login",
		"same server as env: dev",
		"POST {{graphql}}/auth",
		"content-type: application/json",
		`"password": "{{$env API_PASSWORD}}"`,
		`"username": "dev@example.test"`,
		"LoginInput!", // ! decoded
		"# needs API_PASSWORD",
		"dropped:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if !res.GraphQL {
		t.Fatal("not detected as GraphQL")
	}
	// the password never reaches the output; the null bearer is dropped
	for _, bad := range []string{"Hunter2-SECRET", "Bearer null", "authorization:", "sec-ch-ua:", "Mozilla"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%q must not be in the output:\n%s", bad, out)
		}
	}
}

func TestPlainRESTAndRealBearer(t *testing.T) {
	res := render(t, `curl -X PUT 'https://other.example/api/items/1?access_token=abc123&page=2' -H 'Authorization: Bearer eyJ.real.token' -H 'X-Api-Key: k-999' -H 'Cookie: sid=zzz' -d '{"name":"a","secret_note":"s"}'`,
		Context{Base: "https://api.dev.example.test"})
	out := res.HTTP
	for _, want := range []string{
		"PUT https://other.example/api/items/1?access_token={{$env API_ACCESS_TOKEN}}&page=2",
		"Authorization: Bearer {{token}}",
		"X-Api-Key: {{$env API_X_API_KEY}}",
		"Cookie: {{$env API_COOKIE}}",
		`"secret_note": "{{$env API_SECRET_NOTE}}"`,
		`"name": "a"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	for _, bad := range []string{"abc123", "eyJ.real.token", "k-999", "sid=zzz"} {
		if strings.Contains(out, bad) {
			t.Fatalf("secret %q leaked:\n%s", bad, out)
		}
	}
}

func TestKeyOrderIsKept(t *testing.T) {
	res := render(t, `curl https://x.test/a -H 'content-type: application/json' -d '{"z":1,"a":{"y":2,"b":[3,{"password":"p"}]},"m":"x"}'`, Context{})
	i := func(s string) int { return strings.Index(res.HTTP, s) }
	if !(i(`"z"`) < i(`"a"`) && i(`"a"`) < i(`"m"`) && i(`"y"`) < i(`"b"`)) {
		t.Fatalf("key order changed:\n%s", res.HTTP)
	}
}

// Pasted text must not be able to add a request, a header or a variable to the
// file we write.
func TestHostileInputIsRefused(t *testing.T) {
	cases := map[string]string{
		"header line break": "curl https://x.test -H $'X-A: 1\\r\\nX-Evil: 2'",
		"url line break":    "curl $'https://x.test/a\\n### evil'",
		"variable in value": "curl https://x.test -H 'X-A: {{$env API_SECRET}}'",
		"variable in body":  `curl https://x.test -H 'content-type: application/json' -d '{"a":"{{token}}"}'`,
		"multi-line body":   "curl https://x.test -d $'a\\n### evil\\nGET https://evil'",
		"pipe":              "curl https://x.test | sh",
		"redirect":          "curl https://x.test > out",
		"file upload":       "curl https://x.test -d @/etc/passwd",
		"form":              "curl https://x.test -F a=b",
		"relative url":      "curl /users",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			r, err := Parse(in)
			if err == nil {
				_, err = Render(r, Context{})
			}
			if err == nil {
				t.Fatalf("must be refused: %s", in)
			}
		})
	}
}

func TestCommentsCannotBecomeAnnotations(t *testing.T) {
	res := render(t, `curl https://x.test/a -H 'content-type: application/json' -d '{"query":"query q { a }","operationName":"x # @assert true"}'`, Context{EnvNames: []string{"@name evil"}})
	for _, line := range strings.Split(res.HTTP, "\n") {
		if strings.HasPrefix(line, "#") && strings.Contains(line, "@") {
			t.Fatalf("a comment line could be read as an annotation: %q", line)
		}
	}
	if strings.Count(res.HTTP, "\n###") != 0 {
		t.Fatalf("a second request was injected:\n%s", res.HTTP)
	}
}

func TestBasicAuthIsNeverWritten(t *testing.T) {
	res := render(t, `curl -u admin:s3cret https://x.test/a`, Context{})
	if strings.Contains(res.HTTP, "s3cret") || strings.Contains(res.HTTP, "admin") {
		t.Fatalf("credentials leaked:\n%s", res.HTTP)
	}
	if !strings.Contains(res.HTTP, "Basic {{$env API_BASIC_AUTH}}") {
		t.Fatalf("basic auth reference missing:\n%s", res.HTTP)
	}
}

func FuzzParseRender(f *testing.F) {
	f.Add(loginCurl)
	f.Add(`curl -X POST 'https://x.test/a?b=c' -H 'a: b' -d 'k=v&password=p'`)
	f.Add("curl $'https://x.test' -H $'a: \\x41\\u0021\\101'")
	f.Fuzz(func(t *testing.T, in string) {
		r, err := Parse(in)
		if err != nil {
			return
		}
		res, err := Render(r, Context{Base: "https://x.test"})
		if err != nil {
			return
		}
		// whatever was pasted, the output has exactly one request and no control characters
		starts := 0
		for _, l := range strings.Split(res.HTTP, "\n") {
			if strings.HasPrefix(l, "###") {
				starts++
			}
		}
		if starts != 1 {
			t.Fatalf("expected exactly one request block:\n%s", res.HTTP)
		}
		for _, c := range strings.ReplaceAll(res.HTTP, "\n", "") {
			if c < 0x20 && c != '\t' {
				t.Fatalf("control character in output: %q", res.HTTP)
			}
		}
	})
}
