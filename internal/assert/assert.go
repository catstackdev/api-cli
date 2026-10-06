// Package assert evaluates the `# @assert <jq>` lines of a .http request
// against its response.
//
// An expression runs on one object, {status, headers, body, ms}, and passes
// only when its first result is the boolean true.
//
// The .http file may come from a cloned repo, so evaluation is deliberately
// narrow: the program is compiled without an environment loader (jq's `env` and
// `$ENV` are empty), it has a time limit, and a Result never carries response
// data. Bodies and headers hold tokens, and jq's own error text quotes values,
// so a failure says only what KIND of thing went wrong.
package assert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cybercat/api/internal/render"

	"github.com/itchyny/gojq"
)

// Timeout bounds one evaluation: `last(range(infinite))` must not hang a CI job.
var Timeout = 5 * time.Second

// Envelope is the response an assertion sees.
type Envelope struct {
	Status   int
	Header   http.Header
	Body     []byte
	Duration time.Duration
}

// Result is the outcome of one assertion. Reason is empty on a pass.
type Result struct {
	Pass   bool
	Reason string
}

// Assertion is a compiled expression.
type Assertion struct {
	Expr string
	code *gojq.Code
}

// Compile parses expr. Nothing here passes gojq.WithEnvironLoader, so the
// program cannot read the process environment.
func Compile(expr string) (*Assertion, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, errors.New("empty expression")
	}
	q, err := gojq.Parse(expr)
	if err != nil {
		return nil, err
	}
	code, err := gojq.Compile(q)
	if err != nil {
		return nil, err
	}
	return &Assertion{Expr: expr, code: code}, nil
}

// Eval runs the assertion against one response.
func (a *Assertion) Eval(e Envelope) Result {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	v, ok := a.code.RunWithContext(ctx, e.input()).Next()
	switch {
	case !ok:
		return Result{Reason: "no result"}
	case v == nil:
		return Result{Reason: "null"}
	}
	if err, isErr := v.(error); isErr {
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return Result{Reason: "timed out"}
		}
		return Result{Reason: "evaluation error (value hidden)"}
	}
	b, isBool := v.(bool)
	switch {
	case isBool && b:
		return Result{Pass: true}
	case isBool:
		return Result{Reason: "false"}
	default:
		return Result{Reason: "not a boolean (" + kind(v) + ")"}
	}
}

// input builds the value jq sees. Header names are lower-cased and repeated
// headers joined with ", ". A body that is not JSON is a string, an empty one null.
func (e Envelope) input() any {
	headers := map[string]any{}
	for name, vals := range e.Header {
		headers[strings.ToLower(name)] = strings.Join(vals, ", ")
	}
	return map[string]any{
		"status":  e.Status,
		"headers": headers,
		"body":    decodeBody(e.Body),
		"ms":      int(e.Duration.Milliseconds()),
	}
}

func decodeBody(body []byte) any {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil || dec.More() {
		return string(body)
	}
	return render.Normalize(v)
}

func kind(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case int, float64:
		return "number"
	}
	if _, ok := v.(fmt.Stringer); ok {
		return "number" // *big.Int
	}
	return "value"
}
