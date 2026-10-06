package ui

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Table prints rows as aligned columns under an optional header. Widths are
// measured on the plain text, so colour codes never break the alignment.
func Table(w io.Writer, header []string, rows [][]string, on bool) {
	all := rows
	if len(header) > 0 {
		all = append([][]string{header}, rows...)
	}
	var widths []int
	for _, r := range all {
		for i, cell := range r {
			if n := utf8.RuneCountInString(cell); i >= len(widths) {
				widths = append(widths, n)
			} else if n > widths[i] {
				widths[i] = n
			}
		}
	}
	line := func(r []string, code string) {
		parts := make([]string, len(r))
		for i, cell := range r {
			pad := ""
			if i < len(r)-1 { // no trailing spaces on the last column
				pad = strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell))
			}
			parts[i] = Paint(cell, code, on) + pad
		}
		fmt.Fprintln(w, strings.TrimRight(strings.Join(parts, "  "), " "))
	}
	if len(header) > 0 {
		line(header, Dim)
	}
	for _, r := range rows {
		line(r, "")
	}
}
