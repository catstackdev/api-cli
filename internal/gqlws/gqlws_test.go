package gqlws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fake is a server speaking one protocol. It records what the client sent.
type fake struct {
	proto  string
	events []string // payloads sent as next/data
	then   string   // "complete" (default), "error", "hang"
	ping   bool

	mu       sync.Mutex
	header   http.Header
	got      []message
	srv      *httptest.Server
	noSubpro bool
}

func newFake(t *testing.T, proto string, events ...string) *fake {
	f := &fake{proto: proto, events: events}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) url() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") }

func (f *fake) sent() []message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]message(nil), f.got...)
}

func (f *fake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.header = r.Header.Clone()
	f.mu.Unlock()
	opts := &websocket.AcceptOptions{Subprotocols: []string{f.proto}}
	if f.noSubpro {
		opts = &websocket.AcceptOptions{}
	}
	c, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := r.Context()
	write := func(m message) {
		b, _ := json.Marshal(m)
		_ = c.Write(ctx, websocket.MessageText, b)
	}
	next := "next"
	if f.proto == string(LegacyWS) {
		next = "data"
	}
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m message
		_ = json.Unmarshal(data, &m)
		f.mu.Lock()
		f.got = append(f.got, m)
		f.mu.Unlock()
		switch m.Type {
		case "connection_init":
			write(message{Type: "connection_ack"})
		case "subscribe", "start":
			if f.ping {
				write(message{Type: "ping"})
			}
			for _, e := range f.events {
				write(message{ID: m.ID, Type: next, Payload: json.RawMessage(e)})
			}
			switch f.then {
			case "error":
				if f.proto == string(LegacyWS) {
					write(message{ID: m.ID, Type: "error", Payload: json.RawMessage(`{"message":"boom"}`)})
				} else {
					write(message{ID: m.ID, Type: "error", Payload: json.RawMessage(`[{"message":"boom"}]`)})
				}
				return
			case "hang":
			default:
				write(message{ID: m.ID, Type: "complete"})
			}
		}
	}
}

func collect(t *testing.T, c Config) ([]string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got []string
	err := Subscribe(ctx, c, func(p json.RawMessage) error { got = append(got, string(p)); return nil })
	return got, err
}

func types(ms []message) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Type)
	}
	return out
}

func TestTransportWSEventsPingAndComplete(t *testing.T) {
	f := newFake(t, "graphql-transport-ws", `{"data":{"n":1}}`, `{"data":{"n":2}}`)
	f.ping = true
	got, err := collect(t, Config{URL: f.url(), Query: "subscription { n }", Variables: json.RawMessage(`{"a":1}`),
		Header: http.Header{"Authorization": {"Bearer t0k"}}, InitPayload: map[string]any{"Authorization": "Bearer t0k"}})
	if err != nil || len(got) != 2 || got[0] != `{"data":{"n":1}}` {
		t.Fatalf("%v %v", got, err)
	}
	sent := f.sent()
	if got := strings.Join(types(sent)[:2], " "); got != "connection_init subscribe" {
		t.Fatalf("client sent %v", got)
	}
	for i := 0; !strings.Contains(strings.Join(types(f.sent()), " "), "pong"); i++ { // the reply to the server's ping
		if i > 50 {
			t.Fatalf("no pong: %v", types(f.sent()))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(string(sent[0].Payload), "Bearer t0k") || f.header.Get("Authorization") != "Bearer t0k" {
		t.Fatalf("init payload %s header %v", sent[0].Payload, f.header)
	}
	if !strings.Contains(string(sent[1].Payload), `"query":"subscription { n }"`) || !strings.Contains(string(sent[1].Payload), `"variables":{"a":1}`) {
		t.Fatalf("subscribe payload %s", sent[1].Payload)
	}
}

func TestLegacyProtocolUsesStartAndData(t *testing.T) {
	f := newFake(t, "graphql-ws", `{"data":{"n":1}}`)
	got, err := collect(t, Config{URL: f.url(), Protocol: LegacyWS, Query: "subscription { n }"})
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if ts := types(f.sent()); ts[0] != "connection_init" || ts[1] != "start" {
		t.Fatalf("%v", ts)
	}
}

func TestMaxStopsTheSubscriptionAndTellsTheServer(t *testing.T) {
	f := newFake(t, "graphql-transport-ws", `{"data":1}`, `{"data":2}`, `{"data":3}`)
	f.then = "hang"
	got, err := collect(t, Config{URL: f.url(), Query: "subscription { n }", Max: 2})
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	time.Sleep(100 * time.Millisecond)
	if ts := types(f.sent()); ts[len(ts)-1] != "complete" {
		t.Fatalf("the client must send complete, sent %v", ts)
	}
}

func TestServerErrorEndsWithServerError(t *testing.T) {
	for _, proto := range []Protocol{TransportWS, LegacyWS} {
		f := newFake(t, string(proto))
		f.then = "error"
		got, err := collect(t, Config{URL: f.url(), Protocol: proto, Query: "subscription { n }"})
		if !IsServerError(err) || len(got) != 1 || !strings.Contains(got[0], `"errors":[{"message":"boom"}]`) {
			t.Fatalf("%s: %v %v", proto, got, err)
		}
	}
}

func TestSubprotocolMustBeAccepted(t *testing.T) {
	f := newFake(t, "graphql-transport-ws")
	f.noSubpro = true
	_, err := collect(t, Config{URL: f.url(), Query: "subscription { n }"})
	if err == nil || !strings.Contains(err.Error(), "--protocol graphql-ws") {
		t.Fatalf("%v", err)
	}
}

func TestRedirectOnUpgradeIsNeverFollowed(t *testing.T) {
	hit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	_, err := collect(t, Config{URL: "ws" + strings.TrimPrefix(redir.URL, "http"), Query: "subscription { n }",
		Header: http.Header{"Authorization": {"Bearer t0k"}}})
	if err == nil || hit {
		t.Fatalf("err=%v followed=%v", err, hit)
	}
}

func TestNoAckIsATimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"graphql-transport-ws"}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		<-r.Context().Done()
	}))
	defer srv.Close()
	_, err := collect(t, Config{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Query: "subscription { n }", ConnectTimeout: 200 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "connection_ack") {
		t.Fatalf("%v", err)
	}
}

func TestParseProtocol(t *testing.T) {
	if p, err := ParseProtocol(""); err != nil || p != TransportWS {
		t.Fatal(p, err)
	}
	if _, err := ParseProtocol("nope"); err == nil {
		t.Fatal("accepted")
	}
}

// A clean end is never an error, whatever the server does with our goodbye.
func TestACleanEndIsNeverAnError(t *testing.T) {
	t.Run("server closes 1000 without complete", func(t *testing.T) {
		srv := rawServer(t, func(c *websocket.Conn, ctx context.Context) {
			handshake(ctx, c)
			writeMsg(ctx, c, message{ID: "1", Type: "next", Payload: json.RawMessage(`{"data":1}`)})
			_ = c.Close(websocket.StatusNormalClosure, "bye")
		})
		got, err := collect(t, Config{URL: srv, Query: "subscription { n }"})
		if err != nil || len(got) != 1 {
			t.Fatalf("%v %v", got, err)
		}
	})
	t.Run("--max against a server that ignores the close", func(t *testing.T) {
		srv := rawServer(t, func(c *websocket.Conn, ctx context.Context) {
			handshake(ctx, c)
			writeMsg(ctx, c, message{ID: "1", Type: "next", Payload: json.RawMessage(`{"data":1}`)})
			time.Sleep(4 * time.Second) // never reads again
		})
		start := time.Now()
		got, err := collect(t, Config{URL: srv, Query: "subscription { n }", Max: 1})
		if err != nil || len(got) != 1 || time.Since(start) > 2*time.Second {
			t.Fatalf("%v %v after %v", got, err, time.Since(start))
		}
	})
}

// What a server says is data: bounded in our errors, and one message cannot make
// us allocate without limit, while an ordinary large event still works.
func TestServerTextInErrorsAndMessagesAreBounded(t *testing.T) {
	hostile := "\x1b[2J" + strings.Repeat("A", 400)
	srv := rawServer(t, func(c *websocket.Conn, ctx context.Context) {
		_, _, _ = c.Read(ctx) // connection_init
		b, _ := json.Marshal(map[string]any{"type": "connection_error", "payload": hostile})
		_ = c.Write(ctx, websocket.MessageText, b)
	})
	_, err := collect(t, Config{URL: srv, Query: "subscription { n }"})
	if err == nil || len(err.Error()) > 300 || !strings.Contains(err.Error(), "refused the connection") {
		t.Fatalf("%q", err)
	}

	big := func(n int) string { return `{"data":"` + strings.Repeat("x", n) + `"}` }
	for n, wantErr := range map[int]bool{100 << 10: false, 5 << 20: true} {
		payload := big(n)
		srv := rawServer(t, func(c *websocket.Conn, ctx context.Context) {
			handshake(ctx, c)
			c.SetReadLimit(-1)
			writeMsg(ctx, c, message{ID: "1", Type: "next", Payload: json.RawMessage(payload)})
			writeMsg(ctx, c, message{ID: "1", Type: "complete"})
		})
		got, err := collect(t, Config{URL: srv, Query: "subscription { n }"})
		if wantErr != (err != nil) || (!wantErr && len(got) != 1) {
			t.Fatalf("%d bytes: wantErr=%v got %d events, err %v", n, wantErr, len(got), err)
		}
	}
}

// rawServer runs h on each upgraded connection (graphql-transport-ws).
func rawServer(t *testing.T, h func(*websocket.Conn, context.Context)) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"graphql-transport-ws"}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		h(c, r.Context())
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func writeMsg(ctx context.Context, c *websocket.Conn, m message) {
	b, _ := json.Marshal(m)
	_ = c.Write(ctx, websocket.MessageText, b)
}

// handshake answers connection_init and reads the subscribe message.
func handshake(ctx context.Context, c *websocket.Conn) {
	_, _, _ = c.Read(ctx)
	writeMsg(ctx, c, message{Type: "connection_ack"})
	_, _, _ = c.Read(ctx)
}
