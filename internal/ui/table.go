package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// Table prints rows as aligned columns under an optional header. Widths are
// measured on the plain text, so colour codes never break the alignment.
func Table(w io.Writer, header []string, rows [][]string, on bool) {
	TableWrap(w, header, rows, on, -1, 0)
}

// TermWidth is the width of the terminal w writes to, or 0 when it is not one
// (a pipe, a file, a test): output meant for scripts is never wrapped.
func TermWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok {
		return 0
	}
	n, _, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0
	}
	return n
}

// minWrapCol keeps a wrapped column readable on a very narrow terminal.
const minWrapCol = 20

// TableWrap is Table, but when a row is wider than maxWidth (> 0) the cells of
// column wrapCol continue on the following lines instead of running off the
// screen. A long URL is broken before a "&" when there is one nearby.
func TableWrap(w io.Writer, header []string, rows [][]string, on bool, wrapCol, maxWidth int) {
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
	if maxWidth > 0 && wrapCol >= 0 && wrapCol < len(widths) {
		total := 2 * (len(widths) - 1)
		for _, n := range widths {
			total += n
		}
		if over := total - maxWidth; over > 0 {
			widths[wrapCol] = max(minWrapCol, widths[wrapCol]-over)
		}
	}
	wrapping := wrapCol >= 0 && wrapCol < len(widths) && maxWidth > 0
	line := func(r []string, code string) {
		cells := r
		var rest []string // what the wrapped column still has to print
		if wrapping && wrapCol < len(r) {
			chunks := wrapAt(r[wrapCol], widths[wrapCol])
			cells = append([]string(nil), r...)
			cells[wrapCol], rest = chunks[0], chunks[1:]
		}
		emit := func(cells []string) {
			parts := make([]string, len(cells))
			for i, cell := range cells {
				pad := ""
				if i < len(cells)-1 { // no trailing spaces on the last column
					pad = strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell))
				}
				parts[i] = Paint(cell, code, on) + pad
			}
			fmt.Fprintln(w, strings.TrimRight(strings.Join(parts, "  "), " "))
		}
		emit(cells)
		// continuation: the other columns stay empty, so the text lines up
		for _, chunk := range rest {
			next := make([]string, len(cells))
			next[wrapCol] = chunk
			emit(next)
		}
	}
	if len(header) > 0 {
		line(header, Dim)
	}
	for _, r := range rows {
		line(r, "")
	}
}

// wrapAt cuts s into pieces of at most n runes, preferring to start a piece at
// the last "&" that fits.
func wrapAt(s string, n int) []string {
	r := []rune(s)
	var out []string
	for len(r) > n {
		cut := n
		for i := n; i > 0; i-- {
			if r[i] == '&' {
				cut = i
				break
			}
		}
		out = append(out, string(r[:cut]))
		r = r[cut:]
	}
	return append(out, string(r))
}
