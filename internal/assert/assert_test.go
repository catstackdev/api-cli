package assert

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func env(status int, body string, h http.Header) Envelope {
	return Envelope{Status: status, Header: h, Body: []byte(body), Duration: 120 * time.Millisecond}
}

func TestEval(t *testing.T) {
	h := http.Header{"Content-Type": {"application/json"}, "Set-Cookie": {"a=1", "b=2"}}
	e := env(200, `{"data":{"id":12345678901234567890,"items":[1,2,3],"name":"x"}}`, h)
	for _, c := range []struct {
		expr   string
		pass   bool
		reason string
	}{
		{".status == 200", true, ""},
		{".status == 404", false, "false"},
		{".status >= 200 and .status < 300", true, ""},
		{".body.data.items | length == 3", true, ""},
		{`.body.data.id == 12345678901234567890`, true, ""}, // big ids survive
		{`.headers["content-type"] | startswith("application/json")`, true, ""},
		{`.headers["set-cookie"] | contains("b=2")`, true, ""},
		{".ms < 1000", true, ""},
		{".body.missing", false, "null"},
		{".body.data.name", false, "not a boolean (string)"},
		{".body.data.items", false, "not a boolean (array)"},
		{".body.data", false, "not a boolean (object)"},
		{".status", false, "not a boolean (number)"},
		{"empty", false, "no result"},
		{".body.data.name + 1", false, "evaluation error"},
	} {
		a, err := Compile(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		r := a.Eval(e)
		if r.Pass != c.pass || !strings.HasPrefix(r.Reason, c.reason) {
			t.Errorf("%s: got %+v, want pass=%v reason %q", c.expr, r, c.pass, c.reason)
		}
	}
}

func TestEvalNonJSONAndEmptyBody(t *testing.T) {
	a, _ := Compile(`.body == "plain text"`)
	if r := a.Eval(env(200, "plain text", nil)); !r.Pass {
		t.Fatalf("%+v", r)
	}
	b, _ := Compile(`.body == null`)
	if r := b.Eval(env(204, "", nil)); !r.Pass {
		t.Fatalf("%+v", r)
	}
}

func TestCompileRejectsBadExpressions(t *testing.T) {
	for _, expr := range []string{"", "   ", ".a ==", "nosuchfunction(1)", ".a | |"} {
		if _, err := Compile(expr); err == nil {
			t.Errorf("%q must not compile", expr)
		}
	}
}

// A hostile .http file can carry any jq. It must not be able to read the
// process environment (gojq's CLI does; the library does only when asked).
func TestAssertCannotReadEnvironment(t *testing.T) {
	t.Setenv("PROBE_SECRET", "hunter2-from-env")
	for _, expr := range []string{
		`env.PROBE_SECRET == "hunter2-from-env"`,
		`$ENV.PROBE_SECRET == "hunter2-from-env"`,
		`(env | length) > 0`,
		`($ENV | has("PROBE_SECRET"))`,
	} {
		a, err := Compile(expr)
		if err != nil {
			continue // refusing to compile is fine too
		}
		if r := a.Eval(env(200, `{}`, nil)); r.Pass {
			t.Errorf("%s read the environment", expr)
		}
	}
}

// What is printed on failure must never carry response data: bodies and
// headers hold tokens, and jq's own error text quotes values.
func TestAssertFailureNeverPrintsResponseData(t *testing.T) {
	const secret = "tok-SECRET-0123456789"
	h := http.Header{"Authorization": {"Bearer " + secret}}
	e := env(200, `{"token":"`+secret+`","n":1}`, h)
	for _, expr := range []string{
		`.body.token + 1`,                // error quoting the value
		`.body.token`,                    // non-boolean string
		`.body.token == "other"`,         // plain false
		`.headers.authorization | error`, // error() with the value as message
		`.body | keys | .[0] | tonumber`, // conversion error
		`.body.token | tonumber`,
	} {
		a, err := Compile(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		r := a.Eval(e)
		if r.Pass {
			t.Fatalf("%s unexpectedly passed", expr)
		}
		if strings.Contains(r.Reason, secret) || strings.Contains(r.Reason, "0123456789") {
			t.Errorf("%s leaked response data: %q", expr, r.Reason)
		}
	}
}

func TestEvalHasATimeout(t *testing.T) {
	old := Timeout
	Timeout = 50 * time.Millisecond
	defer func() { Timeout = old }()
	a, err := Compile(`last(range(infinite)) == 1`)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r := a.Eval(env(200, `{}`, nil))
	if r.Pass || !strings.Contains(r.Reason, "timed out") || time.Since(start) > 3*time.Second {
		t.Fatalf("%+v after %v", r, time.Since(start))
	}
}
