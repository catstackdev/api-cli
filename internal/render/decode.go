package render

import (
	"bytes"
	"encoding/json"

	"github.com/itchyny/gojq"
)

// Decode evaluates filter on a JSON body and returns the result as a value for
// comparison. The identity filter and plain paths keep numbers as json.Number
// (exact); any other filter goes through gojq, and several results become a
// []any.
func Decode(body []byte, filter string) (any, error) {
	trimmed := bytes.TrimSpace(body)
	filter = trimSpace(filter)

	decode := func(raw []byte) (any, error) {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		return v, dec.Decode(&v)
	}
	if filter == "" || filter == "." {
		return decode(trimmed)
	}
	if steps, ok := parsePath(filter); ok {
		if val, ok := evalPath(trimmed, steps); ok {
			return decode(val)
		}
	}

	query, err := gojq.Parse(filter)
	if err != nil {
		return nil, err
	}
	var input any
	if input, err = decode(trimmed); err != nil {
		return nil, err
	}
	var results []any
	iter := query.Run(normalize(input))
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if e, isErr := v.(error); isErr {
			return nil, e
		}
		results = append(results, v)
	}
	if len(results) == 1 {
		return results[0], nil
	}
	return results, nil
}

func trimSpace(s string) string { return string(bytes.TrimSpace([]byte(s))) }
