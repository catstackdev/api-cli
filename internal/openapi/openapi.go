// Package openapi reduces an OpenAPI 3 / Swagger 2 document to the one thing
// `api` needs offline: which method + path pairs exist (and a one-line summary),
// for tab completion and `api openapi list`.
//
// The spec comes from a server, so it is data, never trusted: the result is a
// small index of sanitised strings (no control characters, bounded sizes and
// counts), and the raw document is not kept.
//
// Formats are pluggable. Today only JSON is decoded; a YAML decoder is one more
// entry in decoders (see Decoder) and nothing else changes.
package openapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	// MaxSpecBytes bounds what Parse reads: a hostile server cannot make a sync
	// allocate without limit.
	MaxSpecBytes = 32 << 20
	// MaxOperations bounds the index so completion stays fast.
	MaxOperations = 20000
	// MaxPath and MaxSummary bound the strings kept per operation.
	MaxPath    = 512
	MaxSummary = 120
)

// ErrUnsupportedFormat means the document is not in a format a decoder reads.
var ErrUnsupportedFormat = errors.New("unsupported spec format")

// Operation is one method + path of the API.
type Operation struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Summary     string `json:"summary,omitempty"`
	OperationID string `json:"operationId,omitempty"`
	// Body is a compact JSON skeleton of the request body (see body.go), or "".
	Body string `json:"body,omitempty"`
}

// Index is what gets cached. Skipped counts paths dropped as unsafe; Truncated
// says the operation limit was hit.
type Index struct {
	Source     string      `json:"source,omitempty"`
	Fetched    string      `json:"fetched,omitempty"`
	Title      string      `json:"title,omitempty"`
	Version    string      `json:"version,omitempty"`
	Skipped    int         `json:"skipped,omitempty"`
	Truncated  bool        `json:"truncated,omitempty"`
	Operations []Operation `json:"operations"`
}

// Decoder turns the bytes of one format into the generic document Build reads.
type Decoder struct {
	Name   string
	Match  func(data []byte) bool
	Decode func(data []byte) (map[string]any, error)
}

// decoders is tried in order. To support YAML, append a Decoder whose Match
// recognises it and whose Decode returns the same map[string]any shape.
var decoders = []Decoder{{
	Name:  "json",
	Match: func(d []byte) bool { return bytes.HasPrefix(bytes.TrimSpace(d), []byte("{")) },
	Decode: func(d []byte) (map[string]any, error) {
		var doc map[string]any
		if err := json.Unmarshal(d, &doc); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		return doc, nil
	},
}}

// Parse decodes data and builds the index.
func Parse(data []byte) (*Index, error) {
	if len(data) > MaxSpecBytes {
		return nil, fmt.Errorf("spec too large (%d MB limit)", MaxSpecBytes>>20)
	}
	for _, d := range decoders {
		if d.Match(data) {
			doc, err := d.Decode(data)
			if err != nil {
				return nil, err
			}
			return build(doc)
		}
	}
	return nil, fmt.Errorf("%w: only JSON is read for now. Fetch the JSON form (often /openapi.json or /v3/api-docs) or convert it, e.g. yq -o=json", ErrUnsupportedFormat)
}

var methodOrder = []string{"get", "post", "put", "patch", "delete", "head", "options"}

func build(doc map[string]any) (*Index, error) {
	_, v3 := doc["openapi"].(string)
	_, v2 := doc["swagger"].(string)
	paths, ok := doc["paths"].(map[string]any)
	if (!v3 && !v2) || !ok {
		return nil, errors.New("not an OpenAPI or Swagger document (needs \"openapi\"/\"swagger\" and \"paths\")")
	}
	idx := &Index{Operations: []Operation{}}
	bodies, budget := bodyBuilder{doc: doc}, MaxBodyBudget
	if info, ok := doc["info"].(map[string]any); ok {
		idx.Title = clean(str(info["title"]), MaxSummary)
		idx.Version = clean(str(info["version"]), 40)
	}
	names := make([]string, 0, len(paths))
	for p := range paths {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		if strings.HasPrefix(p, "x-") { // a vendor extension, not a path
			continue
		}
		item, ok := paths[p].(map[string]any)
		if !ok { // a $ref or something else we do not follow
			continue
		}
		if !safePath(p) {
			idx.Skipped++
			continue
		}
		for _, m := range methodOrder {
			op, ok := item[m].(map[string]any)
			if !ok {
				continue
			}
			if len(idx.Operations) >= MaxOperations {
				idx.Truncated = true
				return idx, nil
			}
			o := Operation{
				Method:      strings.ToUpper(m),
				Path:        p,
				Summary:     clean(str(op["summary"]), MaxSummary),
				OperationID: safeID(str(op["operationId"])),
			}
			if m == "post" || m == "put" || m == "patch" {
				if body := bodies.forOperation(op); len(body) <= budget {
					o.Body, budget = body, budget-len(body)
				}
			}
			idx.Operations = append(idx.Operations, o)
		}
	}
	return idx, nil
}

func str(v any) string { s, _ := v.(string); return s }

// safePath accepts what a request path can be: it starts with "/", is bounded,
// and has no control characters or spaces (they would reach a terminal or break
// shell completion).
func safePath(p string) bool {
	if !strings.HasPrefix(p, "/") || len(p) > MaxPath {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// safeID keeps an operationId only when it is a plain identifier.
func safeID(s string) string {
	if s == "" || len(s) > 128 {
		return ""
	}
	for _, r := range s {
		if !(r == '_' || r == '.' || r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return ""
		}
	}
	return s
}

// clean flattens whitespace, drops control characters and truncates to max runes.
func clean(s string, max int) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r), unicode.IsControl(r):
			space = b.Len() > 0
		default:
			if space {
				b.WriteByte(' ')
				space = false
			}
			b.WriteRune(r)
		}
	}
	out := []rune(b.String())
	if len(out) > max {
		out = append(out[:max-1], '…')
	}
	return string(out)
}
