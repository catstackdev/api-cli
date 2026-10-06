package openapi

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// The body skeleton is what `api openapi pick` puts in your editor: the shape of
// the request body with one placeholder per type. It is built from the spec's
// request schema, which is server data, so it is strictly bounded and every key
// and value that reaches the file is checked.
const (
	// MaxBody is the largest skeleton kept for one operation (bytes).
	MaxBody = 2048
	// MaxBodyProps caps the properties listed per object.
	MaxBodyProps = 40
	// MaxBodyBudget caps all skeletons of one index (bytes), so a huge spec
	// cannot make the cache file, or every TAB that reads it, expensive.
	MaxBodyBudget = 2 << 20

	maxBodyDepth = 3
)

var bodyKey = regexp.MustCompile(`^[A-Za-z0-9_.$@-]{1,64}$`)

// bodyBuilder resolves $refs against the document being parsed.
type bodyBuilder struct{ doc map[string]any }

// forOperation returns the compact JSON skeleton of op's JSON request body, or
// "" when it has none (or none we can show safely).
func (b bodyBuilder) forOperation(op map[string]any) string {
	schema := b.requestSchema(op)
	if schema == nil {
		return ""
	}
	for depth := maxBodyDepth; depth >= 1; depth-- {
		if out := b.node(schema, depth, map[string]bool{}); len(out) <= MaxBody {
			return out
		}
	}
	return ""
}

// requestSchema finds the JSON body schema: requestBody.content for OpenAPI 3,
// the `in: body` parameter for Swagger 2.
func (b bodyBuilder) requestSchema(op map[string]any) map[string]any {
	if rb, ok := b.deref(op["requestBody"]); ok {
		content, _ := rb["content"].(map[string]any)
		types := make([]string, 0, len(content))
		for ct := range content {
			if strings.Contains(strings.ToLower(ct), "json") {
				types = append(types, ct)
			}
		}
		sort.Strings(types)
		for _, want := range append([]string{"application/json"}, types...) {
			if entry, ok := content[want].(map[string]any); ok {
				if sc, ok := entry["schema"].(map[string]any); ok {
					return sc
				}
			}
		}
		return nil
	}
	if params, ok := op["parameters"].([]any); ok {
		for _, p := range params {
			if pm, ok := b.deref(p); ok && pm["in"] == "body" {
				if sc, ok := pm["schema"].(map[string]any); ok {
					return sc
				}
			}
		}
	}
	return nil
}

// deref follows one "#/..." reference inside the document.
func (b bodyBuilder) deref(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	ref, isRef := m["$ref"].(string)
	if !isRef {
		return m, true
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, false
	}
	var cur any = b.doc
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
		next, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next[part]
	}
	out, ok := cur.(map[string]any)
	return out, ok
}

// flat merges a schema with its allOf parts (and the first oneOf/anyOf branch)
// into properties + required + the scalar facts. A $ref already being expanded
// on this path counts as an empty object: cycles end.
func (b bodyBuilder) flat(v any, active map[string]bool) (props map[string]any, required []string, kind string, enum []any) {
	props = map[string]any{}
	var visit func(v any, depth int)
	visit = func(v any, depth int) {
		m, ok := v.(map[string]any)
		if !ok || depth > 6 {
			return
		}
		if ref, isRef := m["$ref"].(string); isRef {
			if active[ref] {
				return
			}
			active[ref] = true
			defer delete(active, ref)
			target, ok := b.deref(m)
			if !ok {
				return
			}
			visit(target, depth+1)
			return
		}
		if t, ok := m["type"].(string); ok && kind == "" {
			kind = t
		}
		if e, ok := m["enum"].([]any); ok && enum == nil {
			enum = e
		}
		if p, ok := m["properties"].(map[string]any); ok {
			for k, pv := range p {
				if _, seen := props[k]; !seen {
					props[k] = pv
				}
			}
		}
		if r, ok := m["required"].([]any); ok {
			for _, x := range r {
				if s, ok := x.(string); ok {
					required = append(required, s)
				}
			}
		}
		if parts, ok := m["allOf"].([]any); ok {
			for _, part := range parts {
				visit(part, depth+1)
			}
		}
		for _, k := range []string{"oneOf", "anyOf"} {
			if parts, ok := m[k].([]any); ok && len(parts) > 0 {
				visit(parts[0], depth+1)
			}
		}
	}
	visit(v, 0)
	return props, required, kind, enum
}

// node renders one schema as compact JSON.
func (b bodyBuilder) node(v any, depth int, active map[string]bool) string {
	props, required, kind, enum := b.flat(v, active)
	if len(props) > 0 || kind == "object" {
		if depth <= 0 {
			return "{}"
		}
		keys := make([]string, 0, len(props))
		seen := map[string]bool{}
		for _, r := range required {
			if _, ok := props[r]; ok && !seen[r] {
				keys, seen[r] = append(keys, r), true
			}
		}
		rest := make([]string, 0, len(props))
		for k := range props {
			if !seen[k] {
				rest = append(rest, k)
			}
		}
		sort.Strings(rest)
		keys = append(keys, rest...)
		var sb strings.Builder
		sb.WriteByte('{')
		n := 0
		for _, k := range keys {
			if !bodyKey.MatchString(k) {
				continue
			}
			if n >= MaxBodyProps {
				break
			}
			if n > 0 {
				sb.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			sb.Write(kb)
			sb.WriteByte(':')
			sb.WriteString(b.node(props[k], depth-1, active))
			n++
		}
		sb.WriteByte('}')
		return sb.String()
	}
	switch kind {
	case "array":
		return "[]"
	case "integer", "number":
		if len(enum) > 0 {
			if f, ok := enum[0].(float64); ok {
				return strconv.FormatFloat(f, 'f', -1, 64)
			}
		}
		return "0"
	case "boolean":
		return "false"
	case "string", "":
		if len(enum) > 0 {
			if s, ok := enum[0].(string); ok && plainEnum(s) {
				sb, _ := json.Marshal(s)
				return string(sb)
			}
		}
		if kind == "" {
			return "null"
		}
		return `""`
	}
	return "null"
}

// plainEnum accepts a short, printable enum value.
func plainEnum(s string) bool {
	if s == "" || len([]rune(s)) > 40 {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}
