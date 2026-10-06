// Package schema reads GraphQL introspection results and shapes them for people
// and for the fzf / television browsers: signatures, a one-line-per-item index
// and the detail program the browsers run with jq.
//
// Output is built with explicit key order (jq's object construction order),
// never through a map: the files it writes are consumed by jq programs and by
// people diffing them.
package schema

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// PreviewProgram is the jq program the fzf / television previews run
// (`jq -r --arg kind K --arg n N -f preview.jq <schema>.json`). It stays jq on
// purpose: the browsers are subprocesses that have jq and nothing else. The text
// is byte-for-byte what the old shell helper wrote.
//
//go:embed preview.jq
var PreviewProgram string

// Introspection queries. REF is the type-reference selection, three wrappers deep
// (NON_NULL / LIST nesting beyond that is not shown).
const refSelection = `kind name ofType { kind name ofType { kind name ofType { kind name } } }`

const (
	// SchemaQuery fetches every type of a service.
	SchemaQuery = `{ __schema { queryType { name } mutationType { name } types { name kind fields { name args { name type { ` + refSelection + ` } } type { ` + refSelection + ` } } inputFields { name type { ` + refSelection + ` } } enumValues { name } } } }`
	// TypeQuery fetches one type; the variable is $n.
	TypeQuery = `query($n: String!) { __type(name: $n) { name kind fields { name args { name type { ` + refSelection + ` } } type { ` + refSelection + ` } } inputFields { name type { ` + refSelection + ` } } enumValues { name } } }`
	// NamesQuery fetches only the root field names.
	NamesQuery = `{ __schema { queryType { fields { name } } mutationType { fields { name } } } }`
)

// Ref is a (possibly wrapped) type reference.
type Ref struct {
	Kind   string  `json:"kind"`
	Name   *string `json:"name"`
	OfType *Ref    `json:"ofType"`
}

// String renders it like GraphQL SDL: [User!]!
func (r Ref) String() string {
	switch r.Kind {
	case "NON_NULL":
		if r.OfType != nil {
			return r.OfType.String() + "!"
		}
	case "LIST":
		if r.OfType != nil {
			return "[" + r.OfType.String() + "]"
		}
	}
	if r.Name == nil {
		return "null"
	}
	return *r.Name
}

// Base is the named type underneath any wrappers.
func (r Ref) Base() string {
	if r.OfType != nil {
		return r.OfType.Base()
	}
	if r.Name == nil {
		return ""
	}
	return *r.Name
}

type Arg struct {
	Name string `json:"name"`
	Type Ref    `json:"type"`
}

type Field struct {
	Name string `json:"name"`
	Args []Arg  `json:"args"`
	Type Ref    `json:"type"`
}

type InputField struct {
	Name string `json:"name"`
	Type Ref    `json:"type"`
}

type EnumValue struct {
	Name string `json:"name"`
}

type Type struct {
	Name        string       `json:"name"`
	Kind        string       `json:"kind"`
	Fields      []Field      `json:"fields"`
	InputFields []InputField `json:"inputFields"`
	EnumValues  []EnumValue  `json:"enumValues"`
}

type root struct {
	Name string `json:"name"`
}

// Schema is the `__schema` object of an introspection result.
type Schema struct {
	QueryType    *root  `json:"queryType"`
	MutationType *root  `json:"mutationType"`
	Types        []Type `json:"types"`
}

// Parse reads a `__schema` object and insists it has types: a service with
// introspection disabled answers with errors and no data.
func Parse(data []byte) (Schema, error) {
	var s Schema
	if err := json.Unmarshal(data, &s); err != nil {
		return Schema{}, fmt.Errorf("not an introspection result: %w", err)
	}
	if len(s.Types) == 0 {
		return Schema{}, fmt.Errorf("no types in the result (introspection disabled on the server?)")
	}
	return s, nil
}

// Signature is `name(arg: Type, ...): Type`.
func (f Field) Signature() string {
	var b strings.Builder
	b.WriteString(f.Name)
	if len(f.Args) > 0 {
		parts := make([]string, len(f.Args))
		for i, a := range f.Args {
			parts[i] = a.Name + ": " + a.Type.String()
		}
		b.WriteString("(" + strings.Join(parts, ", ") + ")")
	}
	b.WriteString(": " + f.Type.String())
	return b.String()
}

// Short is the kind word the browsers show: OBJECT -> type, INPUT_OBJECT -> input.
func Short(kind string) string {
	switch kind {
	case "OBJECT":
		return "type"
	case "INPUT_OBJECT":
		return "input"
	case "ENUM":
		return "enum"
	case "INTERFACE":
		return "interface"
	case "UNION":
		return "union"
	}
	return kind
}

// Rendered is the readable form of one type, keys in this order.
type Rendered struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Fields      []string `json:"fields"`
	InputFields []string `json:"inputFields"`
	EnumValues  []string `json:"enumValues"`
}

// Render shapes a type for `api schema type`.
func Render(t Type) Rendered {
	r := Rendered{Name: t.Name, Kind: t.Kind, Fields: []string{}, InputFields: []string{}, EnumValues: []string{}}
	for _, f := range t.Fields {
		r.Fields = append(r.Fields, f.Signature())
	}
	for _, f := range t.InputFields {
		r.InputFields = append(r.InputFields, f.Name+": "+f.Type.String())
	}
	for _, v := range t.EnumValues {
		r.EnumValues = append(r.EnumValues, v.Name)
	}
	return r
}

// Names are the root field names, queries then mutations.
type Names struct {
	Queries   []string `json:"queries"`
	Mutations []string `json:"mutations"`
}

// ParseNames reads the answer to NamesQuery (the `__schema` object).
func ParseNames(data []byte) (Names, error) {
	var raw struct {
		QueryType *struct {
			Fields []EnumValue `json:"fields"`
		} `json:"queryType"`
		MutationType *struct {
			Fields []EnumValue `json:"fields"`
		} `json:"mutationType"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Names{}, fmt.Errorf("not an introspection result: %w", err)
	}
	n := Names{Queries: []string{}, Mutations: []string{}}
	if raw.QueryType != nil {
		for _, f := range raw.QueryType.Fields {
			n.Queries = append(n.Queries, f.Name)
		}
	}
	if raw.MutationType != nil {
		for _, f := range raw.MutationType.Fields {
			n.Mutations = append(n.Mutations, f.Name)
		}
	}
	return n, nil
}

// Index returns one "<endpoint> <kind> <name>" line per browsable item: every
// root field (kind query / mutation) and every non-scalar, non-introspection type.
func (s Schema) Index(endpoint string) []string {
	var q, m string
	if s.QueryType != nil {
		q = s.QueryType.Name
	}
	if s.MutationType != nil {
		m = s.MutationType.Name
	}
	var out []string
	for _, t := range s.Types {
		if t.Name == "" || (t.Name != q && t.Name != m) {
			continue
		}
		kind := "mutation"
		if t.Name == q {
			kind = "query"
		}
		for _, f := range t.Fields {
			out = append(out, endpoint+" "+kind+" "+f.Name)
		}
	}
	for _, t := range s.Types {
		if strings.HasPrefix(t.Name, "__") || t.Kind == "SCALAR" || t.Name == q || t.Name == m {
			continue
		}
		out = append(out, endpoint+" "+Short(t.Kind)+" "+t.Name)
	}
	return out
}

// SortIndex orders lines like `sort -k1,1 -k2,2 -k3,3` in the C locale.
func SortIndex(lines []string) {
	key := func(l string) [3]string {
		p := strings.SplitN(l, " ", 3)
		var k [3]string
		copy(k[:], p)
		return k
	}
	sort.SliceStable(lines, func(i, j int) bool {
		a, b := key(lines[i]), key(lines[j])
		for n := 0; n < 3; n++ {
			if a[n] != b[n] {
				return a[n] < b[n]
			}
		}
		return false
	})
}

// Pretty marshals v with jq's layout: two-space indent, no HTML escaping.
func Pretty(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// FindType returns the named type.
func (s Schema) FindType(name string) (Type, bool) {
	for _, t := range s.Types {
		if t.Name == name {
			return t, true
		}
	}
	return Type{}, false
}

// ValidLabel reports whether name may become a cache file name (<label>.json).
// Endpoint names can come from a project's config, so a path must never get through.
func ValidLabel(name string) bool {
	if name == "" || len(name) > 64 || strings.HasPrefix(name, ".") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// TypeList is every non-introspection, non-scalar type name: the root query and
// mutation types first (they are the ones people cannot guess), then the rest
// sorted. With kinds each line is "<kind> <name>".
func (s Schema) TypeList(kinds bool) []string {
	var q, m string
	if s.QueryType != nil {
		q = s.QueryType.Name
	}
	if s.MutationType != nil {
		m = s.MutationType.Name
	}
	line := func(kind, name string) string {
		if kinds {
			return kind + " " + name
		}
		return name
	}
	var roots []string
	var rest []Type
	for _, t := range s.Types {
		switch {
		case t.Name == q && q != "":
			roots = append([]string{line("query", t.Name)}, roots...)
		case t.Name == m && m != "":
			roots = append(roots, line("mutation", t.Name))
		case strings.HasPrefix(t.Name, "__") || t.Kind == "SCALAR":
		default:
			rest = append(rest, t)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].Name < rest[j].Name })
	for _, t := range rest {
		roots = append(roots, line(Short(t.Kind), t.Name))
	}
	return roots
}

// body lists the members of a type: fields (as signatures), input fields, enum values.
func (t Type) body() []string {
	var out []string
	for _, f := range t.Fields {
		out = append(out, "  "+f.Signature())
	}
	for _, f := range t.InputFields {
		out = append(out, "  "+f.Name+": "+f.Type.String())
	}
	for _, v := range t.EnumValues {
		out = append(out, "  "+v.Name)
	}
	return out
}

// Detail is the text the browsers show for one index item (kind query, mutation,
// type, input, enum...). It is the Go twin of preview.jq, which the fzf and
// television previews still run with jq: a test compares the two.
func (s Schema) Detail(kind, name string) ([]string, error) {
	if kind == "query" || kind == "mutation" {
		root := ""
		if kind == "query" && s.QueryType != nil {
			root = s.QueryType.Name
		} else if kind == "mutation" && s.MutationType != nil {
			root = s.MutationType.Name
		}
		rt, ok := s.FindType(root)
		if !ok {
			return nil, fmt.Errorf("no %s type in this schema", kind)
		}
		var f *Field
		for i := range rt.Fields {
			if rt.Fields[i].Name == name {
				f = &rt.Fields[i]
				break
			}
		}
		if f == nil {
			return nil, fmt.Errorf("no %s named %q", kind, name)
		}
		out := []string{f.Signature(), "", "returns " + f.Type.Base()}
		if t, ok := s.FindType(f.Type.Base()); ok {
			out = append(out, t.body()...)
		}
		for _, a := range f.Args {
			if t, ok := s.FindType(a.Type.Base()); ok && t.Kind == "INPUT_OBJECT" {
				out = append(out, "", "arg type "+t.Name)
				out = append(out, t.body()...)
			}
		}
		return out, nil
	}
	t, ok := s.FindType(name)
	if !ok {
		return nil, fmt.Errorf("no type named %q", name)
	}
	return append([]string{Short(t.Kind) + " " + t.Name}, t.body()...), nil
}
