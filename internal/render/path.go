package render

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

// A step of a plain jq path: .key or [index].
type step struct {
	key   string
	idx   int
	isIdx bool
}

func isIdentChar(c byte, first bool) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (!first && c >= '0' && c <= '9')
}

// parsePath accepts only plain paths: .a.b, .a[0].b, .[2]. Anything else
// (pipes, functions, iteration, quoted keys) returns ok=false and is left to
// gojq. A bare "." is the identity and never reaches here.
func parsePath(f string) (steps []step, ok bool) {
	if len(f) < 2 || f[0] != '.' {
		return nil, false
	}
	for i := 0; i < len(f); {
		switch f[i] {
		case '.':
			i++
			j := i
			for j < len(f) && isIdentChar(f[j], j == i) {
				j++
			}
			if j > i {
				steps = append(steps, step{key: f[i:j]})
				i = j
			} else if i >= len(f) || f[i] != '[' {
				return nil, false
			}
		case '[':
			end := strings.IndexByte(f[i:], ']')
			if end < 0 {
				return nil, false
			}
			n, err := strconv.Atoi(f[i+1 : i+end])
			if err != nil {
				return nil, false
			}
			steps = append(steps, step{idx: n, isIdx: true})
			i += end + 1
		default:
			return nil, false
		}
	}
	return steps, len(steps) > 0
}

var jsonNull = json.RawMessage("null")

// evalPath walks raw JSON without decoding values, so nested key order and
// number literals are untouched. Missing keys and null parents give null, like
// jq. A type mismatch (key on an array...) returns ok=false so gojq can report
// the proper error.
func evalPath(raw []byte, steps []step) (json.RawMessage, bool) {
	cur := json.RawMessage(raw)
	for _, s := range steps {
		t := bytes.TrimSpace(cur)
		if bytes.Equal(t, jsonNull) {
			continue
		}
		if s.isIdx {
			var arr []json.RawMessage
			if json.Unmarshal(t, &arr) != nil {
				return nil, false
			}
			n := s.idx
			if n < 0 {
				n += len(arr)
			}
			if n < 0 || n >= len(arr) {
				cur = jsonNull
			} else {
				cur = arr[n]
			}
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(t, &obj) != nil {
			return nil, false
		}
		v, found := obj[s.key]
		if !found {
			cur = jsonNull
		} else {
			cur = v
		}
	}
	return cur, true
}

func writeRawValue(w io.Writer, val json.RawMessage, o Options) error {
	val = bytes.TrimSpace(val)
	if o.RawOutput && len(val) > 0 && val[0] == '"' {
		var s string
		if json.Unmarshal(val, &s) == nil {
			_, err := io.WriteString(w, s+"\n")
			return err
		}
	}
	var out bytes.Buffer
	var err error
	if o.Compact {
		err = json.Compact(&out, val)
	} else {
		err = json.Indent(&out, val, "", "  ")
	}
	if err != nil {
		return err
	}
	out.WriteByte('\n')
	return emit(w, out.Bytes(), o)
}
