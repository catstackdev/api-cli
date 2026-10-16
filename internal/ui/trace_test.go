package ui

import (
	"strings"
	"testing"
	"time"
)

func TestTraceLinesArePlainWhenColourIsOff(t *testing.T) {
	got := RequestLine("POST", "http://h/x?a=1", false)
	if got != "POST http://h/x?a=1" {
		t.Errorf("request line = %q", got)
	}
	if got := ResponseLine("HTTP/1.1", 200, "OK", 1500*time.Microsecond, false); got != "HTTP/1.1 200 OK  2ms" {
		t.Errorf("response line = %q", got)
	}
	lines := TimingLines(TimingPhases{DNS: time.Millisecond, Connect: 2 * time.Millisecond, TTFB: 20 * time.Millisecond, Total: 30 * time.Millisecond}, false)
	if len(lines) != 1 || strings.Contains(lines[0], "█") {
		t.Errorf("no bar without colour: %q", lines)
	}
}

func TestTraceColourOnlyAddsEscapeCodes(t *testing.T) {
	ph := TimingPhases{DNS: time.Millisecond, Connect: 2 * time.Millisecond, TLS: 8 * time.Millisecond, TTFB: 120 * time.Millisecond, Total: 150 * time.Millisecond}
	plain, coloured := TimingLines(ph, false), TimingLines(ph, true)
	if sgr.ReplaceAllString(coloured[0], "") != plain[0] {
		t.Errorf("colour changed the text:\n%q\n%q", coloured[0], plain[0])
	}
	if got, want := sgr.ReplaceAllString(RequestLine("GET", "http://h/x?a=1", true), ""), RequestLine("GET", "http://h/x?a=1", false); got != want {
		t.Errorf("request line: %q != %q", got, want)
	}
}

func TestTimingBarFitsAndKeepsEveryPhaseVisible(t *testing.T) {
	// a 1ms connect in a 2s request must still get a cell, and the bar never overflows
	bar := timingBar(time.Millisecond, 1900*time.Millisecond, 99*time.Millisecond, 2*time.Second)
	cells := strings.Count(sgr.ReplaceAllString(bar, ""), "█")
	if cells < 3 || cells > barWidth {
		t.Errorf("bar has %d cells (want 3..%d)", cells, barWidth)
	}
	if len(sgr.FindAllString(bar, -1)) != 6 { // three painted blocks, each open + reset
		t.Errorf("each phase should be its own block: %q", bar)
	}
}

func TestTimingOnAReusedConnectionHasNoConnectionPhases(t *testing.T) {
	line := TimingLines(TimingPhases{TTFB: 10 * time.Millisecond, Total: 12 * time.Millisecond, Reused: true}, false)[0]
	if !strings.Contains(line, "connection reused") || strings.Contains(line, "dns") || strings.Contains(line, "connect ") {
		t.Errorf("reused connection: %q", line)
	}
}
