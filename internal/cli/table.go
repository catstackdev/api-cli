package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"unicode/utf8"

	"cybercat/api/internal/render"
	"cybercat/api/internal/ui"
)

const maxCell = 60

// cell renders one value for a table: scalars as text, null as "-", nested
// values as compact JSON, long text shortened.
func cell(v any) string {
	var s string
	switch t := v.(type) {
	case nil:
		s = "-"
	case string:
		s = strings.Join(strings.Fields(t), " ")
	case json.Number:
		s = t.String()
	case *big.Int:
		s = t.String()
	case bool, int, int64, float64:
		s = fmt.Sprint(t)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			s = fmt.Sprint(t)
		} else {
			s = string(b)
		}
	}
	if utf8.RuneCountInString(s) > maxCell {
		r := []rune(s)
		s = string(r[:maxCell-1]) + "…"
	}
	return s
}

// columnsOf returns the union of the rows' keys: id and name first, the rest
// alphabetical (JSON objects have no order once decoded).
func columnsOf(rows []map[string]any) []string {
	seen := map[string]bool{}
	for _, r := range rows {
		for k := range r {
			seen[k] = true
		}
	}
	var rest []string
	for k := range seen {
		if k != "id" && k != "name" {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	var cols []string
	for _, k := range []string{"id", "name"} {
		if seen[k] {
			cols = append(cols, k)
		}
	}
	return append(cols, rest...)
}

// writeTable prints the filtered result as a table when it has a table shape:
// an array of objects (columns), or a single object (key / value). It reports
// false, writing nothing, for anything else so the caller prints it normally.
func writeTable(buf *bytes.Buffer, body []byte, filter string, columns []string, color bool) bool {
	v, err := render.Decode(body, filter)
	if err != nil {
		return false
	}
	switch t := v.(type) {
	case []any:
		rows := make([]map[string]any, 0, len(t))
		for _, x := range t {
			m, ok := x.(map[string]any)
			if !ok {
				return false
			}
			rows = append(rows, m)
		}
		if len(rows) == 0 {
			return false
		}
		cols := columns
		if len(cols) == 0 {
			cols = columnsOf(rows)
		}
		data := make([][]string, len(rows))
		for i, r := range rows {
			line := make([]string, len(cols))
			for j, c := range cols {
				line[j] = cell(r[c])
			}
			data[i] = line
		}
		ui.Table(buf, cols, data, color)
		return true
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			if len(columns) == 0 || contains(columns, k) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			return false
		}
		data := make([][]string, len(keys))
		for i, k := range keys {
			data[i] = []string{k, cell(t[k])}
		}
		ui.Table(buf, []string{"KEY", "VALUE"}, data, color)
		return true
	}
	return false
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
