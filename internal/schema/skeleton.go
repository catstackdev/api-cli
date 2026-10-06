package schema

import (
	"encoding/json"
	"fmt"
	"strings"
)

// innermost walks NON_NULL / LIST wrappers to the named type reference.
func (r Ref) innermost() Ref {
	for r.OfType != nil {
		r = *r.OfType
	}
	return r
}

// isList reports whether any wrapper is a list.
func (r Ref) isList() bool {
	for x := &r; x != nil; x = x.OfType {
		if x.Kind == "LIST" {
			return true
		}
	}
	return false
}

// maxSelected bounds how many fields a generated selection lists.
const maxSelected = 8

// Skeleton builds a ready-to-edit operation for one root field: the variables its
// arguments need, the call, and a selection of the return type's plain fields.
// It also returns a JSON object with a zero value for each variable.
func (s Schema) Skeleton(kind, name string) (query, variables string, err error) {
	root := ""
	if kind == "query" && s.QueryType != nil {
		root = s.QueryType.Name
	} else if kind == "mutation" && s.MutationType != nil {
		root = s.MutationType.Name
	}
	rt, ok := s.FindType(root)
	if !ok {
		return "", "", fmt.Errorf("no %s type in this schema", kind)
	}
	var f *Field
	for i := range rt.Fields {
		if rt.Fields[i].Name == name {
			f = &rt.Fields[i]
			break
		}
	}
	if f == nil {
		return "", "", fmt.Errorf("no %s named %q", kind, name)
	}

	opName := strings.ToUpper(name[:1]) + name[1:]
	var decl, call, vars []string
	for _, a := range f.Args {
		decl = append(decl, "$"+a.Name+": "+a.Type.String())
		call = append(call, a.Name+": $"+a.Name)
		q, _ := json.Marshal(a.Name)
		vars = append(vars, "  "+string(q)+": "+s.zeroValue(a.Type, 0))
	}
	head := kind + " " + opName
	if len(decl) > 0 {
		head += "(" + strings.Join(decl, ", ") + ")"
	}
	field := name
	if len(call) > 0 {
		field += "(" + strings.Join(call, ", ") + ")"
	}

	var b strings.Builder
	b.WriteString(head + " {\n")
	if sel := s.selection(f.Type.innermost()); len(sel) > 0 {
		b.WriteString("  " + field + " {\n")
		for _, x := range sel {
			b.WriteString("    " + x + "\n")
		}
		b.WriteString("  }\n")
	} else {
		b.WriteString("  " + field + "\n")
	}
	b.WriteString("}\n")

	variables = "{}"
	if len(vars) > 0 {
		variables = "{\n" + strings.Join(vars, ",\n") + "\n}"
	}
	return b.String(), variables, nil
}

// selection lists what to ask for on a return type: its scalar and enum fields
// (without arguments), or __typename when there is nothing plain to list.
func (s Schema) selection(ref Ref) []string {
	switch ref.Kind {
	case "SCALAR", "ENUM":
		return nil
	}
	if ref.Name == nil {
		return []string{"__typename"}
	}
	t, ok := s.FindType(*ref.Name)
	if !ok || t.Kind == "UNION" || t.Kind == "INTERFACE" && len(t.Fields) == 0 {
		return []string{"__typename"}
	}
	var out []string
	for _, f := range t.Fields {
		if len(f.Args) > 0 {
			continue
		}
		switch f.Type.innermost().Kind {
		case "SCALAR", "ENUM":
			out = append(out, f.Name)
		}
		if len(out) == maxSelected {
			break
		}
	}
	if len(out) == 0 {
		return []string{"__typename"}
	}
	return out
}

// zeroValue is a JSON placeholder for a variable of type ref.
func (s Schema) zeroValue(ref Ref, depth int) string {
	if ref.isList() {
		return "[]"
	}
	in := ref.innermost()
	switch in.Kind {
	case "SCALAR":
		if in.Name != nil {
			switch *in.Name {
			case "Int", "Float":
				return "0"
			case "Boolean":
				return "false"
			}
		}
		return `""`
	case "ENUM":
		if in.Name != nil {
			if t, ok := s.FindType(*in.Name); ok && len(t.EnumValues) > 0 {
				q, _ := json.Marshal(t.EnumValues[0].Name)
				return string(q)
			}
		}
		return `""`
	case "INPUT_OBJECT":
		if in.Name == nil || depth >= 2 {
			return "{}"
		}
		t, ok := s.FindType(*in.Name)
		if !ok || len(t.InputFields) == 0 {
			return "{}"
		}
		var parts []string
		for _, f := range t.InputFields {
			q, _ := json.Marshal(f.Name)
			parts = append(parts, string(q)+": "+s.zeroValue(f.Type, depth+1))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return "null"
}
