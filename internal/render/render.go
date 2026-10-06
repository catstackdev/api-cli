// Package render prints a response body, optionally through a jq filter.
package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strings"

	"github.com/itchyny/gojq"
)

// Options controls how a body is printed.
type Options struct {
	Filter    string // jq filter; "" or "." prints the body as-is
	RawBody   bool   // print the body untouched (no filter, no pretty-print)
	Compact   bool   // one-line JSON
	RawOutput bool   // like jq -r: print string results without quotes
	Color     bool   // colour JSON output (palette from JQ_COLORS)
}

// ErrFilter is returned for a filter that does not parse or run; the raw
// body is still printed so nothing is lost.
type ErrFilter struct{ Err error }

func (e ErrFilter) Error() string { return "jq: " + e.Err.Error() }
func (e ErrFilter) Unwrap() error { return e.Err }

// Write prints body to w. A body that is not JSON (HTML error page, plain
// text) is always printed as-is.
func Write(w io.Writer, body []byte, o Options) error {
	trimmed := bytes.TrimSpace(body)
	filter := strings.TrimSpace(o.Filter)

	if o.RawBody || len(trimmed) == 0 || !json.Valid(trimmed) {
		return writeRaw(w, body)
	}

	// Identity filter: re-indent the original bytes so key order is kept
	// (gojq's maps are unordered).
	if (filter == "" || filter == ".") && !o.RawOutput {
		var out bytes.Buffer
		var err error
		if o.Compact {
			err = json.Compact(&out, trimmed)
		} else {
			err = json.Indent(&out, trimmed, "", "  ")
		}
		if err != nil {
			return writeRaw(w, body)
		}
		out.WriteByte('\n')
		return emit(w, out.Bytes(), o)
	}

	// Plain paths (.data.me[0].id) are resolved on the raw bytes too, so the
	// server's key order survives; anything fancier goes through gojq.
	if steps, ok := parsePath(filter); ok {
		if val, ok := evalPath(trimmed, steps); ok {
			return writeRawValue(w, val, o)
		}
	}

	query, err := gojq.Parse(filter)
	if err != nil {
		_ = writeRaw(w, body)
		return ErrFilter{err}
	}
	var input any
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&input); err != nil {
		return writeRaw(w, body)
	}

	iter := query.Run(normalize(input))
	for {
		v, ok := iter.Next()
		if !ok {
			return nil
		}
		if err, isErr := v.(error); isErr {
			return ErrFilter{err}
		}
		if err := writeValue(w, v, o); err != nil {
			return err
		}
	}
}

func writeRaw(w io.Writer, body []byte) error {
	if _, err := w.Write(body); err != nil {
		return err
	}
	if len(body) > 0 && body[len(body)-1] != '\n' {
		_, err := w.Write([]byte{'\n'})
		return err
	}
	return nil
}

func writeValue(w io.Writer, v any, o Options) error {
	if s, ok := v.(string); ok && o.RawOutput {
		_, err := fmt.Fprintln(w, s)
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if !o.Compact {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		compact, merr := gojq.Marshal(v)
		if merr != nil {
			return err
		}
		buf.Reset()
		buf.Write(compact)
		buf.WriteByte('\n')
	}
	return emit(w, buf.Bytes(), o)
}

// Normalize is normalize for other packages that hand decoded JSON to gojq.
func Normalize(v any) any { return normalize(v) }

// normalize converts json.Number into the int / float64 / *big.Int values
// gojq expects, so large ids survive the round trip.
func normalize(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return int(i)
		}
		if !strings.ContainsAny(t.String(), ".eE") {
			if b, ok := new(big.Int).SetString(t.String(), 10); ok {
				return b
			}
		}
		f, _ := t.Float64()
		return f
	case map[string]any:
		for k, x := range t {
			t[k] = normalize(x)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = normalize(x)
		}
		return t
	default:
		return v
	}
}

// First runs filter on body and returns the first result as a string
// ("" when absent or null). Used by login to pull the token out.
func First(body []byte, filter string) (string, error) {
	query, err := gojq.Parse(filter)
	if err != nil {
		return "", err
	}
	var input any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&input); err != nil {
		return "", err
	}
	v, ok := query.Run(normalize(input)).Next()
	if !ok || v == nil {
		return "", nil
	}
	if err, isErr := v.(error); isErr {
		return "", err
	}
	if s, isStr := v.(string); isStr {
		return s, nil
	}
	return fmt.Sprint(v), nil
}
