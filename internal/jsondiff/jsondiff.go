// Package jsondiff compares two decoded JSON values structurally and reports
// each difference as a jq-style path.
package jsondiff

import (
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Kind of a difference.
type Kind int

const (
	Changed Kind = iota
	Added        // only in B
	Removed      // only in A
)

// Change is one difference. A and B hold the values (nil on the missing side).
type Change struct {
	Path string
	Kind Kind
	A, B any
}

var plainKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func joinKey(path, key string) string {
	if plainKey.MatchString(key) {
		return path + "." + key
	}
	q, _ := json.Marshal(key)
	return path + "[" + string(q) + "]"
}

// Options tune a comparison.
type Options struct {
	// Ignore lists paths (and everything below them) to skip.
	Ignore []string
	// ArrayKey, when set (e.g. "id"), matches the elements of an array of objects
	// by that field instead of by position, so a reordered list is not a
	// difference and a change reads `.data[id=42].name`. An array that cannot be
	// matched (an element without the key, a duplicate key) falls back to
	// position.
	ArrayKey string
}

// Diff returns the differences between a and b. Paths in ignore (and anything
// below them) are skipped; the root path is ".".
func Diff(a, b any, ignore []string) []Change { return DiffWith(a, b, Options{Ignore: ignore}) }

// DiffWith is Diff with options.
func DiffWith(a, b any, o Options) []Change {
	var out []Change
	w := walker{o: o, out: &out}
	w.walk(a, b, "")
	return out
}

type walker struct {
	o   Options
	out *[]Change
}

func ignored(path string, ignore []string) bool {
	for _, ig := range ignore {
		ig = strings.TrimSpace(ig)
		if ig == "" || ig == "." {
			continue
		}
		if path == ig || strings.HasPrefix(path, ig+".") || strings.HasPrefix(path, ig+"[") {
			return true
		}
	}
	return false
}

func (w walker) walk(a, b any, path string) {
	ignore, out := w.o.Ignore, w.out
	if ignored(path, ignore) {
		return
	}
	label := path
	if label == "" {
		label = "."
	}

	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, Change{label, Changed, a, b})
			return
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			p := joinKey(path, k)
			x, inA := av[k]
			y, inB := bv[k]
			switch {
			case inA && !inB:
				if !ignored(p, ignore) {
					*out = append(*out, Change{p, Removed, x, nil})
				}
			case !inA && inB:
				if !ignored(p, ignore) {
					*out = append(*out, Change{p, Added, nil, y})
				}
			default:
				w.walk(x, y, p)
			}
		}
	case []any:
		bv, ok := b.([]any)
		if !ok {
			*out = append(*out, Change{label, Changed, a, b})
			return
		}
		if w.o.ArrayKey != "" {
			if ka, kb, ok := keyed(av, bv, w.o.ArrayKey); ok {
				w.walkKeyed(av, bv, ka, kb, path)
				return
			}
		}
		n := len(av)
		if len(bv) > n {
			n = len(bv)
		}
		for i := 0; i < n; i++ {
			p := fmt.Sprintf("%s[%d]", path, i)
			switch {
			case i >= len(bv):
				if !ignored(p, ignore) {
					*out = append(*out, Change{p, Removed, av[i], nil})
				}
			case i >= len(av):
				if !ignored(p, ignore) {
					*out = append(*out, Change{p, Added, nil, bv[i]})
				}
			default:
				w.walk(av[i], bv[i], p)
			}
		}
	default:
		if !scalarEqual(a, b) {
			*out = append(*out, Change{label, Changed, a, b})
		}
	}
}

func numString(v any) (string, bool) {
	switch n := v.(type) {
	case json.Number:
		return n.String(), true
	case int:
		return strconv.Itoa(n), true
	case int64:
		return strconv.FormatInt(n, 10), true
	case float64:
		return strconv.FormatFloat(n, 'g', -1, 64), true
	case *big.Int:
		return n.String(), true
	}
	return "", false
}

func scalarEqual(a, b any) bool {
	if as, ok := numString(a); ok {
		bs, ok := numString(b)
		if !ok {
			return false
		}
		if as == bs {
			return true
		}
		ra, ok1 := new(big.Rat).SetString(as)
		rb, ok2 := new(big.Rat).SetString(bs)
		return ok1 && ok2 && ra.Cmp(rb) == 0 // 1 == 1.0 == 1e0
	}
	return reflect.DeepEqual(a, b)
}

// Format renders a value compactly for display, shortened to max runes.
func Format(v any, max int) string {
	var s string
	switch t := v.(type) {
	case json.Number:
		s = t.String()
	case *big.Int:
		s = t.String()
	default:
		b, err := json.Marshal(v)
		if err != nil {
			s = fmt.Sprint(v)
		} else {
			s = string(b)
		}
	}
	if r := []rune(s); max > 0 && len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// keyOf returns the identity of an array element: the scalar under key.
func keyOf(v any, key string) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	id, ok := m[key]
	if !ok {
		return "", false
	}
	switch x := id.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case float64, int, int64, bool:
		return fmt.Sprint(x), true
	}
	return "", false
}

// keyed returns the identity of every element of both arrays, or ok=false when
// they cannot be matched by key (a non-object, a missing key, a duplicate).
func keyed(a, b []any, key string) (ka, kb []string, ok bool) {
	collect := func(arr []any) ([]string, bool) {
		seen := map[string]bool{}
		keys := make([]string, 0, len(arr))
		for _, el := range arr {
			k, has := keyOf(el, key)
			if !has || seen[k] {
				return nil, false
			}
			seen[k] = true
			keys = append(keys, k)
		}
		return keys, true
	}
	var okA, okB bool
	if ka, okA = collect(a); !okA {
		return nil, nil, false
	}
	if kb, okB = collect(b); !okB {
		return nil, nil, false
	}
	return ka, kb, true
}

func (w walker) walkKeyed(a, b []any, ka, kb []string, path string) {
	label := func(k string) string { return fmt.Sprintf("%s[%s=%s]", path, w.o.ArrayKey, k) }
	inB := map[string]int{}
	for i, k := range kb {
		inB[k] = i
	}
	inA := map[string]bool{}
	for i, k := range ka {
		inA[k] = true
		p := label(k)
		if j, ok := inB[k]; ok {
			w.walk(a[i], b[j], p)
		} else if !ignored(p, w.o.Ignore) {
			*w.out = append(*w.out, Change{p, Removed, a[i], nil})
		}
	}
	for j, k := range kb {
		if !inA[k] {
			if p := label(k); !ignored(p, w.o.Ignore) {
				*w.out = append(*w.out, Change{p, Added, nil, b[j]})
			}
		}
	}
}

// String names a kind for machine-readable output.
func (k Kind) String() string {
	switch k {
	case Added:
		return "added"
	case Removed:
		return "removed"
	}
	return "changed"
}
