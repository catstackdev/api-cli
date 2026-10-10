// Package gqlws runs one GraphQL subscription over a WebSocket, in either of the
// two protocols in use: "graphql-transport-ws" (the graphql-ws library, Apollo
// Server 4+, Yoga, Hasura) and the legacy "graphql-ws" (Apollo's
// subscriptions-transport-ws). It knows nothing about tokens, config or output:
// the caller decides which URL and headers are safe and what to do with each
// event.
package gqlws

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// Protocol is the WebSocket subprotocol, which is also its name on the wire.
type Protocol string

const (
	TransportWS Protocol = "graphql-transport-ws"
	LegacyWS    Protocol = "graphql-ws"
)

// ParseProtocol accepts "" (the default, TransportWS) or one of the two names.
func ParseProtocol(s string) (Protocol, error) {
	switch Protocol(s) {
	case "", TransportWS:
		return TransportWS, nil
	case LegacyWS:
		return LegacyWS, nil
	}
	return "", fmt.Errorf("unknown subscription protocol %q (use %s or %s)", s, TransportWS, LegacyWS)
}

// Config is one subscription.
type Config struct {
	URL      string      // ws:// or wss://
	Header   http.Header // sent with the upgrade request
	Protocol Protocol

	// InitPayload is the payload of connection_init (nil: none). Servers that are
	// used from browsers often read the token from here, not from a header.
	InitPayload map[string]any

	Query         string
	Variables     json.RawMessage // a JSON object, or empty
	OperationName string

	Insecure       bool
	ConnectTimeout time.Duration // dial and connection_ack
	Max            int           // stop after this many events; 0 = until the end
}

// ServerError is a GraphQL error the server ended the subscription with. Its
// Payload is the list of errors.
type ServerError struct{ Payload json.RawMessage }

func (e *ServerError) Error() string { return "the server ended the subscription with an error" }

// maxMessage bounds one message: a hostile server must not make the CLI
// allocate without limit.
const maxMessage = 4 << 20

const subID = "1"

type message struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Subscribe connects, subscribes and calls emit with the payload of every event
// ({"data":...} / {"errors":...}, the same shape as an HTTP response) until the
// server completes it, ctx is cancelled, c.Max events were seen or emit fails.
// A clean end returns nil. A redirect on the upgrade is never followed: the
// request carries the caller's credentials.
func Subscribe(ctx context.Context, c Config, emit func(json.RawMessage) error) error {
	if c.Protocol == "" {
		c.Protocol = TransportWS
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if c.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // --insecure, asked for by the user
	}
	client := &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	dctx, cancel := withTimeout(ctx, c.ConnectTimeout)
	conn, _, err := websocket.Dial(dctx, c.URL, &websocket.DialOptions{
		HTTPClient:   client,
		HTTPHeader:   c.Header,
		Subprotocols: []string{string(c.Protocol)},
	})
	cancel()
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != string(c.Protocol) {
		return fmt.Errorf("the server did not accept the %s subprotocol (try --protocol %s)", c.Protocol, other(c.Protocol))
	}
	conn.SetReadLimit(maxMessage)

	send := func(m message) error {
		b, err := json.Marshal(m)
		if err != nil {
			return err
		}
		return conn.Write(ctx, websocket.MessageText, b)
	}

	var init json.RawMessage
	if c.InitPayload != nil {
		if init, err = json.Marshal(c.InitPayload); err != nil {
			return err
		}
	}
	if err := send(message{Type: "connection_init", Payload: init}); err != nil {
		return err
	}
	actx, acancel := withTimeout(ctx, c.ConnectTimeout)
	err = awaitAck(actx, conn, send)
	acancel()
	if err != nil {
		return err
	}

	op := struct {
		Query         string          `json:"query"`
		Variables     json.RawMessage `json:"variables,omitempty"`
		OperationName string          `json:"operationName,omitempty"`
	}{c.Query, c.Variables, c.OperationName}
	payload, err := json.Marshal(op)
	if err != nil {
		return err
	}
	start, stop, event := "subscribe", "complete", "next"
	if c.Protocol == LegacyWS {
		start, stop, event = "start", "stop", "data"
	}
	if err := send(message{ID: subID, Type: start, Payload: payload}); err != nil {
		return err
	}

	seen := 0
	finish := func() error {
		// best effort: tell the server we are done
		wctx, wcancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer wcancel()
		if b, err := json.Marshal(message{ID: subID, Type: stop}); err == nil {
			_ = conn.Write(wctx, websocket.MessageText, b)
		}
		if c.Protocol == LegacyWS {
			if b, err := json.Marshal(message{Type: "connection_terminate"}); err == nil {
				_ = conn.Write(wctx, websocket.MessageText, b)
			}
		}
		// the stop message above is the goodbye; a close handshake would wait for a
		// server that may never answer, and an intentional end is never an error
		conn.CloseNow()
		return nil
	}

	for {
		m, err := read(ctx, conn)
		if err != nil {
			if ctx.Err() != nil {
				_ = finish()
				return nil
			}
			return err
		}
		switch m.Type {
		case "ping":
			if err := send(message{Type: "pong"}); err != nil {
				return err
			}
		case "pong", "ka", "connection_ack":
		case event:
			if m.ID != "" && m.ID != subID {
				continue
			}
			if err := emit(m.Payload); err != nil {
				_ = finish()
				return err
			}
			if seen++; c.Max > 0 && seen >= c.Max {
				return finish()
			}
		case "error":
			if err := emit(wrapErrors(m.Payload)); err != nil {
				return err
			}
			return &ServerError{Payload: m.Payload}
		case "complete":
			// the server is done: nothing to negotiate, and a clean end is never an error
			conn.CloseNow()
			return nil
		case "connection_error":
			return fmt.Errorf("the server refused the connection: %s", clip(m.Payload))
		}
	}
}

func awaitAck(ctx context.Context, conn *websocket.Conn, send func(message) error) error {
	for {
		m, err := read(ctx, conn)
		if err != nil {
			return fmt.Errorf("waiting for connection_ack: %w", err)
		}
		switch m.Type {
		case "connection_ack":
			return nil
		case "ping":
			if err := send(message{Type: "pong"}); err != nil {
				return err
			}
		case "connection_error":
			return fmt.Errorf("the server refused the connection: %s", clip(m.Payload))
		}
	}
}

func read(ctx context.Context, conn *websocket.Conn) (message, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		// a normal close by the server is the end, not an error
		if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
			return message{Type: "complete"}, nil
		}
		if st := websocket.CloseStatus(err); st != -1 {
			return message{}, fmt.Errorf("the server closed the connection (code %d)", st)
		}
		return message{}, err
	}
	var m message
	if err := json.Unmarshal(data, &m); err != nil {
		return message{}, fmt.Errorf("the server sent a message that is not JSON")
	}
	return m, nil
}

// wrapErrors puts a protocol-level error into the shape of a GraphQL response:
// graphql-transport-ws sends a list of errors, the legacy protocol one error.
func wrapErrors(p json.RawMessage) json.RawMessage {
	var list []json.RawMessage
	if json.Unmarshal(p, &list) == nil {
		b, _ := json.Marshal(map[string]any{"errors": list})
		return b
	}
	b, _ := json.Marshal(map[string]any{"errors": []json.RawMessage{p}})
	return b
}

// clip bounds server text in an error message (it is data from a server: a
// connection_error payload may be any size).
func clip(p json.RawMessage) string {
	s := string(p)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return fmt.Sprintf("%q", s)
}

func other(p Protocol) Protocol {
	if p == TransportWS {
		return LegacyWS
	}
	return TransportWS
}

func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

// IsServerError reports whether err is a ServerError.
func IsServerError(err error) bool {
	var se *ServerError
	return errors.As(err, &se)
}
