package schema

import (
	"fmt"
	"strings"
)

// MaxRelated caps how many related types an expansion lists: a type that points at half
// the schema must not bury the one you asked about.
const MaxRelated = 12

// memberRefs are the type references of a type's members: fields and input fields.
func (t Type) memberRefs() []Ref {
	var refs []Ref
	for _, f := range t.Fields {
		refs = append(refs, f.Type)
	}
	for _, f := range t.InputFields {
		refs = append(refs, f.Type)
	}
	return refs
}

// rootField is the query or mutation called name.
func (s Schema) rootField(kind, name string) (*Field, error) {
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
	for i := range rt.Fields {
		if rt.Fields[i].Name == name {
			return &rt.Fields[i], nil
		}
	}
	return nil, fmt.Errorf("no %s named %q", kind, name)
}

// related resolves refs to the types worth showing next to the one you asked about:
// named, in the schema, not a scalar, not already shown (skip), each once, in the order
// they appear. A type that points back at one already shown (a category that lists its
// tags) therefore cannot loop.
func (s Schema) related(refs []Ref, skip map[string]bool) []Type {
	seen := map[string]bool{}
	var out []Type
	for _, r := range refs {
		n := r.Base()
		if n == "" || strings.HasPrefix(n, "__") || skip[n] || seen[n] {
			continue
		}
		seen[n] = true
		if t, ok := s.FindType(n); ok && t.Kind != "SCALAR" {
			out = append(out, t)
		}
	}
	return out
}

// Related is the extra text `Detail` leaves out: the types that the shown members point
// at, one level deep ("category: TagCategory" -> what a TagCategory is). It is empty when
// there is nothing to add, and an error for the same unknown items Detail refuses.
func (s Schema) Related(kind, name string) ([]string, error) {
	var refs []Ref
	skip := map[string]bool{}
	if kind == "query" || kind == "mutation" {
		f, err := s.rootField(kind, name)
		if err != nil {
			return nil, err
		}
		// Detail already prints the returned type and the input types of the arguments
		skip[f.Type.Base()] = true
		if t, ok := s.FindType(f.Type.Base()); ok {
			refs = append(refs, t.memberRefs()...)
		}
		for _, a := range f.Args {
			if t, ok := s.FindType(a.Type.Base()); ok && t.Kind == "INPUT_OBJECT" {
				skip[t.Name] = true
				refs = append(refs, t.memberRefs()...)
			}
		}
	} else {
		t, ok := s.FindType(name)
		if !ok {
			return nil, fmt.Errorf("no type named %q", name)
		}
		skip[t.Name] = true
		refs = t.memberRefs()
	}
	rel := s.related(refs, skip)
	var out []string
	for i, t := range rel {
		if i == MaxRelated {
			out = append(out, "", fmt.Sprintf("... and %d more related types", len(rel)-MaxRelated))
			break
		}
		out = append(out, "", "related: "+Short(t.Kind)+" "+t.Name)
		out = append(out, t.body()...)
	}
	return out, nil
}

// Expanded is `api schema type --expand`: the type, then what its members point at.
type Expanded struct {
	Rendered
	Related     []Rendered `json:"related"`
	RelatedMore int        `json:"relatedMore,omitempty"`
}

// Expand renders t and the types its members point at (at most MaxRelated).
func (s Schema) Expand(t Type) Expanded {
	e := Expanded{Rendered: Render(t), Related: []Rendered{}}
	rel := s.related(t.memberRefs(), map[string]bool{t.Name: true})
	for i, r := range rel {
		if i == MaxRelated {
			e.RelatedMore = len(rel) - MaxRelated
			break
		}
		e.Related = append(e.Related, Render(r))
	}
	return e
}
