package cli

import (
	"cybercat/api/internal/gqlquery"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

// wsSrv is a GraphQL WebSocket server that records what the client sent.
type wsSrv struct {
	srv    *httptest.Server
	proto  string
	events []string
	end    string // "complete" (default) or "error"
	status int    // refuse the upgrade with this status

	mu     sync.Mutex
	paths  []string
	auths  []string
	frames []map[string]json.RawMessage
}

func newWSSrv(t *testing.T, proto string, events ...string) *wsSrv {
	w := &wsSrv{proto: proto, events: events}
	w.srv = httptest.NewServer(http.HandlerFunc(w.handle))
	t.Cleanup(w.srv.Close)
	return w
}

func (w *wsSrv) hits() int { w.mu.Lock(); defer w.mu.Unlock(); return len(w.paths) }

func (w *wsSrv) init() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, f := range w.frames {
		if string(f["type"]) == `"connection_init"` {
			return string(f["payload"])
		}
	}
	return ""
}

func (w *wsSrv) handle(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	w.paths = append(w.paths, r.URL.Path)
	w.auths = append(w.auths, r.Header.Get("Authorization"))
	w.mu.Unlock()
	if w.status != 0 {
		rw.WriteHeader(w.status)
		return
	}
	c, err := websocket.Accept(rw, r, &websocket.AcceptOptions{Subprotocols: []string{w.proto}})
	if err != nil {
		return
	}
	defer c.CloseNow()
	next, start := "next", "subscribe"
	if w.proto == "graphql-ws" {
		next, start = "data", "start"
	}
	send := func(v map[string]any) {
		b, _ := json.Marshal(v)
		_ = c.Write(r.Context(), websocket.MessageText, b)
	}
	for {
		_, data, err := c.Read(r.Context())
		if err != nil {
			return
		}
		var f map[string]json.RawMessage
		_ = json.Unmarshal(data, &f)
		w.mu.Lock()
		w.frames = append(w.frames, f)
		w.mu.Unlock()
		switch strings.Trim(string(f["type"]), `"`) {
		case "connection_init":
			send(map[string]any{"type": "connection_ack"})
		case start:
			for _, e := range w.events {
				send(map[string]any{"id": "1", "type": next, "payload": json.RawMessage(e)})
			}
			if w.end == "error" {
				send(map[string]any{"id": "1", "type": "error", "payload": []map[string]string{{"message": "boom"}}})
				return
			}
			send(map[string]any{"id": "1", "type": "complete"})
		}
	}
}

const subQuery = `subscription { orderUpdated { id status } }`

func TestSubscriptionStreamsOneLinePerEventWithTheFilterApplied(t *testing.T) {
	w := newWSSrv(t, "graphql-transport-ws",
		`{"data":{"orderUpdated":{"id":1,"status":"NEW"}}}`, `{"data":{"orderUpdated":{"id":1,"status":"PAID"}}}`)
	t.Setenv("API_BASE_URL", w.srv.URL)
	t.Setenv("API_TOKEN", "tok-123")
	r := run(t, "", "gql", subQuery, ".data.orderUpdated.status")
	if r.code != 0 || r.out != "\"NEW\"\n\"PAID\"\n" {
		t.Fatalf("%+v", r)
	}
	if w.auths[0] != "Bearer tok-123" || w.paths[0] != "/graphql" {
		t.Fatalf("auth %q path %q", w.auths[0], w.paths[0])
	}
	if w.init() != "" {
		t.Fatalf("the token must not be in connection_init unless asked: %s", w.init())
	}
}

func TestSubscriptionTokenInInitAndInitPayload(t *testing.T) {
	w := newWSSrv(t, "graphql-transport-ws", `{"data":{"x":1}}`)
	t.Setenv("API_BASE_URL", w.srv.URL)
	t.Setenv("API_TOKEN", "tok-123")
	r := run(t, "", "gql", subQuery, "--token-in-init", "--init", "tenant=acme", "--max", "1")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if got := w.init(); !strings.Contains(got, `"Authorization":"Bearer tok-123"`) || !strings.Contains(got, `"tenant":"acme"`) {
		t.Fatalf("connection_init %s", got)
	}
}

// The token goes to the active server only: an absolute endpoint elsewhere gets
// none, in the upgrade or in connection_init, exactly as for an HTTP request.
func TestSubscriptionNeverSendsTheTokenToAnotherServer(t *testing.T) {
	home := newWSSrv(t, "graphql-transport-ws")
	other := newWSSrv(t, "graphql-transport-ws", `{"data":{"x":1}}`)
	t.Setenv("API_BASE_URL", home.srv.URL)
	t.Setenv("API_TOKEN", "tok-123")
	r := run(t, "", "gql", subQuery, "-e", other.srv.URL+"/graphql", "--token-in-init")
	if r.code != 0 || other.hits() != 1 || other.auths[0] != "" || strings.Contains(other.init(), "tok-123") || strings.Contains(r.out+r.err, "tok-123") {
		t.Fatalf("%+v auths=%v init=%s", r, other.auths, other.init())
	}
	if home.hits() != 0 {
		t.Fatal("the home server must not be contacted")
	}
}

func TestSubscriptionDryRunConnectsToNothingAndMasksTheToken(t *testing.T) {
	w := newWSSrv(t, "graphql-transport-ws")
	t.Setenv("API_BASE_URL", w.srv.URL)
	t.Setenv("API_TOKEN", "abcdefghijklmnopqrstuvwxyz")
	r := run(t, "", "gql", subQuery, "--dry", "--token-in-init", "--init", "password=hunter2")
	if r.code != 0 || w.hits() != 0 {
		t.Fatalf("%+v hits=%d", r, w.hits())
	}
	for _, secret := range []string{"abcdefghijklmnopqrstuvwxyz", "hunter2"} {
		if strings.Contains(r.out+r.err, secret) {
			t.Fatalf("%q leaked:\n%s", secret, r.out)
		}
	}
	if !strings.HasPrefix(r.out, "WebSocket ws://") || !strings.Contains(r.out, "graphql-transport-ws") || !strings.Contains(r.out, "subscribe ") {
		t.Fatalf("%s", r.out)
	}
}

func TestSubscriptionConfigPathAndLegacyProtocol(t *testing.T) {
	w := newWSSrv(t, "graphql-ws", `{"data":{"x":1}}`)
	projectWith(t, map[string]string{".api.toml": "default_env = \"dev\"\n[env.dev]\nurl = \"" + w.srv.URL + "\"\nsubscriptions = \"/realtime\"\nws_protocol = \"graphql-ws\"\nws_token_in_init = true\n"})
	t.Setenv("API_TOKEN_DEV", "")
	r := run(t, "", "gql", subQuery, "--token", "tok-9")
	if r.code != 0 || w.paths[0] != "/realtime" || r.out == "" {
		t.Fatalf("%+v paths=%v", r, w.paths)
	}
	if !strings.Contains(w.init(), "Bearer tok-9") {
		t.Fatalf("ws_token_in_init from config not honoured: %s", w.init())
	}
	// the flag beats the config
	if r := run(t, "", "gql", subQuery, "--protocol", "graphql-transport-ws"); r.code == 0 {
		t.Fatalf("a server speaking only graphql-ws must fail: %+v", r)
	}
}

func TestSubscriptionExitCodes(t *testing.T) {
	t.Run("server error is 4", func(t *testing.T) {
		w := newWSSrv(t, "graphql-transport-ws")
		w.end = "error"
		t.Setenv("API_BASE_URL", w.srv.URL)
		if r := run(t, "", "gql", subQuery); r.code != 4 || !strings.Contains(r.err, "boom") {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("--strict stops at the first GraphQL error in an event", func(t *testing.T) {
		w := newWSSrv(t, "graphql-transport-ws", `{"errors":[{"message":"nope"}]}`, `{"data":{"x":1}}`)
		t.Setenv("API_BASE_URL", w.srv.URL)
		if r := run(t, "", "gql", subQuery, "--strict"); r.code != 4 || !strings.Contains(r.err, "nope") || strings.Contains(r.out, `"x"`) {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("a refused upgrade is 22", func(t *testing.T) {
		w := newWSSrv(t, "graphql-transport-ws")
		w.status = http.StatusUnauthorized
		t.Setenv("API_BASE_URL", w.srv.URL)
		if r := run(t, "", "gql", subQuery); r.code != 22 || !strings.Contains(r.err, "401") {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("a bad filter is 5", func(t *testing.T) {
		w := newWSSrv(t, "graphql-transport-ws", `{"data":1}`)
		t.Setenv("API_BASE_URL", w.srv.URL)
		if r := run(t, "", "gql", subQuery, ".data | nope(("); r.code != 5 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("a comment mentioning subscription is still a query", func(t *testing.T) {
		if gqlquery.IsSubscription("# subscription\nquery { me { id } }") || !gqlquery.IsSubscription("# c\n  subscription S { x }") {
			t.Fatal("detection")
		}
	})
	t.Run("curl args and -o do not apply", func(t *testing.T) {
		t.Setenv("API_BASE_URL", "http://127.0.0.1:1")
		if r := run(t, "", "gql", subQuery, "-o", "x"); r.code != 2 {
			t.Fatalf("%+v", r)
		}
	})
}

func TestSubscriptionDebugMasksTheTokenOnStderr(t *testing.T) {
	w := newWSSrv(t, "graphql-transport-ws", `{"data":{"x":1}}`)
	t.Setenv("API_BASE_URL", w.srv.URL)
	t.Setenv("API_TOKEN", "abcdefghijklmnopqrstuvwxyz")
	r := run(t, "", "gql", subQuery, "--debug", "--token-in-init", "--init", "password=hunter2")
	if r.code != 0 || !strings.Contains(r.err, "WebSocket ws://") {
		t.Fatalf("%+v", r)
	}
	for _, secret := range []string{"abcdefghijklmnopqrstuvwxyz", "hunter2"} {
		if strings.Contains(r.out+r.err, secret) {
			t.Fatalf("%q leaked:\n%s", secret, r.err)
		}
	}
}

func TestSubscriptionTimeoutIsExit28AndMaxIsAnOrdinaryEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(rw, r, &websocket.AcceptOptions{Subprotocols: []string{"graphql-transport-ws"}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		for { // acknowledge, then stay silent
			_, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if strings.Contains(string(data), "connection_init") {
				_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"connection_ack"}`))
			}
		}
	}))
	defer srv.Close()
	t.Setenv("API_BASE_URL", srv.URL)
	if r := run(t, "", "gql", subQuery, "--timeout", "300ms"); r.code != 28 {
		t.Fatalf("%+v", r)
	}
}
