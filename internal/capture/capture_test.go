package capture

import (
	"net/http"
	"strings"
	"testing"
)

var login = &Response{
	Status: 201,
	Header: http.Header{"X-Request-Id": {"abc"}},
	Body:   []byte(`{"data":{"token":"t0k","items":[{"id":9},{"id":10}],"n":5,"big":12345678901234567890,"ok":true,"z":null}}`),
}

func value(t *testing.T, expr string) (string, error) {
	t.Helper()
	ref, ok := ParseRef(expr)
	if !ok {
		t.Fatalf("%q is not a reference", expr)
	}
	return login.Value(ref)
}

func TestValues(t *testing.T) {
	for expr, want := range map[string]string{
		"login.response.status":                "201",
		"login.response.headers.X-Request-Id":  "abc",
		"login.response.body.data.token":       "t0k",
		"login.response.body.$.data.token":     "t0k",
		"login.response.body.data.items[1].id": "10",
		"login.response.body.data.n":           "5",
		"login.response.body.data.big":         "12345678901234567890", // not rounded through float64
		"login.response.body.data.ok":          "true",
		"login.response.body.data.items[0]":    `{"id":9}`,
	} {
		if got, err := value(t, expr); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", expr, got, err, want)
		}
	}
	if got, _ := value(t, "login.response.body"); got != string(login.Body) {
		t.Errorf("the whole body: %q", got)
	}
}

func TestErrorsNameThePathNeverTheBody(t *testing.T) {
	for _, expr := range []string{
		"login.response.body.data.missing",
		"login.response.body.data.items[7]",
		"login.response.body.data.token.deeper",
		"login.response.body.data.z",
		"login.response.headers.X-Nope",
	} {
		if _, err := value(t, expr); err == nil {
			t.Errorf("%s: expected an error", expr)
		} else if strings.Contains(err.Error(), "t0k") {
			t.Errorf("%s: the error quotes response data: %v", expr, err)
		}
	}
}

// A path is keys and [n] indexes only: nothing that jq would evaluate gets here.
func TestOnlyKeysAndIndexesAreWalked(t *testing.T) {
	for _, path := range []string{`data|env`, `data.token | $ENV`, `data["token"]`, `.[]`, `data.(x)`} {
		_, err := value(t, "login.response.body."+path)
		if err == nil || !strings.Contains(err.Error(), "only keys and [n] indexes") {
			t.Errorf("%q was not refused as a path: %v", path, err)
		}
	}
	// a name that is only letters is a KEY, looked up as data, never evaluated
	for _, path := range []string{`$__loc__`, `env.HOME`} {
		_, err := value(t, "login.response.body."+path)
		if err == nil || !strings.Contains(err.Error(), "nothing at") {
			t.Errorf("%q should be a plain missing key: %v", path, err)
		}
	}
}

func TestANonJSONBodyIsAnError(t *testing.T) {
	r := &Response{Body: []byte("<html>")}
	ref, _ := ParseRef("x.response.body.a")
	if _, err := r.Value(ref); err == nil {
		t.Fatal("expected an error")
	}
}

func TestIsRefAndDependencies(t *testing.T) {
	for expr, want := range map[string]bool{
		"a.response.status": true, "a-b.response.body.x": true,
		"token": false, "$env API_X": false, "a.response": false, "a.response.other": false,
	} {
		if IsRef(expr) != want {
			t.Errorf("IsRef(%q) = %v, want %v", expr, !want, want)
		}
	}
	got := Dependencies("GET {{ login.response.body.t }}", "{{other.response.status}} {{login.response.status}}", "{{token}}")
	if strings.Join(got, ",") != "login,other" {
		t.Errorf("dependencies = %v; want login,other once each, in order", got)
	}
}

func FuzzWalkBody(f *testing.F) {
	body := []byte(`{"data":{"items":[{"id":9}],"n":5,"t":true,"z":null}}`)
	for _, p := range []string{"data.items[0].id", "data.n", "data.t", "data.z", "data.items[7]", "data.items[0].id.x", "[0]", "data..n"} {
		f.Add(p)
	}
	f.Fuzz(func(t *testing.T, path string) {
		if !bodyPath.MatchString(path) {
			return
		}
		_, _ = walkBody(body, path)
		_, _ = walkBody([]byte(path), path)
	})
}
