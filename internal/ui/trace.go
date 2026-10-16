package ui

import (
	"fmt"
	"strings"
	"time"
)

// This file is how `-v` looks: the request going out (">") and the response
// coming back ("<") on stderr. With on false every function returns the plain
// text, and with it on, removing the escape codes gives back that same text.

// Arrow is the margin marker of a traced line, dimmed so the content stands out.
func Arrow(out bool, on bool) string {
	if out {
		return Paint(">", Dim, on) + " "
	}
	return Paint("<", Dim, on) + " "
}

// verbColour gives each HTTP verb its own colour: reads green, writes yellow,
// replacements blue, deletions red.
func verbColour(method string) string {
	switch strings.ToUpper(method) {
	case "GET", "HEAD", "OPTIONS":
		return Green + ";" + Bold
	case "POST":
		return Yellow + ";" + Bold
	case "PUT", "PATCH":
		return Blue + ";" + Bold
	case "DELETE":
		return Red + ";" + Bold
	}
	return Cyan + ";" + Bold
}

// RequestLine formats "POST http://host/path?x=1": the verb coloured, the URL
// with its query string dimmed.
func RequestLine(method, url string, on bool) string {
	base, query, hasQuery := strings.Cut(url, "?")
	if hasQuery {
		query = "?" + query
	}
	return Paint(method, verbColour(method), on) + " " + base + Paint(query, Dim, on)
}

// ResponseLine is the status line plus how long the request took, e.g.
// "HTTP/1.1 200 OK  10ms".
func ResponseLine(proto string, code int, text string, took time.Duration, on bool) string {
	return StatusLine(proto, code, text, on) + "  " + Paint(took.Round(time.Millisecond).String(), Dim, on)
}

// ms prints a duration in milliseconds, the unit a request is thought about in.
func ms(d time.Duration) string {
	switch {
	case d >= 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d >= time.Second:
		return fmt.Sprintf("%.2fs", d.Seconds())
	case d >= 10*time.Millisecond:
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000)
}

// speed paints a total by how long it is: under 200ms green, under 1s yellow,
// slower red.
func speed(d time.Duration) string {
	switch {
	case d < 200*time.Millisecond:
		return Green
	case d < time.Second:
		return Yellow
	}
	return Red
}

// TimingPhases are the cumulative marks of one request (see httpc.Timing).
// Reused means there was no dns/connect/tls: the connection was already open.
type TimingPhases struct {
	DNS, Connect, TLS, TTFB, Total time.Duration
	Reused                         bool
}

// barWidth is how many cells the timing bar has.
const barWidth = 32

// TimingLines shows where the time went: one line of numbers and, when colour
// is on, a bar split into connect (cyan), waiting for the server (yellow) and
// download (green), each as wide as its share of the total.
func TimingLines(t TimingPhases, on bool) []string {
	label := func(s string) string { return Paint(s, Dim, on) }
	var parts []string
	connect := time.Duration(0)
	if t.Reused {
		parts = append(parts, label("connection reused"))
	} else {
		connect = t.Connect
		if t.TLS > t.Connect {
			connect = t.TLS
		}
		parts = append(parts, label("dns ")+ms(t.DNS), label("connect ")+ms(t.Connect))
		if t.TLS > 0 {
			parts = append(parts, label("tls ")+ms(t.TLS))
		}
	}
	wait, download := max(t.TTFB-connect, 0), max(t.Total-t.TTFB, 0)
	parts = append(parts, label("wait ")+ms(wait), label("download ")+ms(download),
		label("total ")+Paint(ms(t.Total), speed(t.Total), on))
	lines := []string{"  " + strings.Join(parts, label(" · "))}
	if on && t.Total > 0 {
		lines = append(lines, "  "+timingBar(connect, wait, download, t.Total))
	}
	return lines
}

// timingBar draws the three phases as blocks, at least one cell for a phase that
// took any time, so a fast connect is still visible.
func timingBar(connect, wait, download, total time.Duration) string {
	cells := func(d time.Duration) int {
		if d <= 0 {
			return 0
		}
		n := int(float64(d) / float64(total) * barWidth)
		if n < 1 {
			n = 1
		}
		return n
	}
	c, w, d := cells(connect), cells(wait), cells(download)
	// rounding up the small phases can overshoot the bar: take it from the widest
	for c+w+d > barWidth {
		switch {
		case w >= c && w >= d:
			w--
		case d >= c:
			d--
		default:
			c--
		}
	}
	block := func(n int, code string) string { return Paint(strings.Repeat("█", n), code, true) }
	return block(c, Cyan) + block(w, Yellow) + block(d, Green)
}
