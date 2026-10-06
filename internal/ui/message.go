package ui

import (
	"fmt"
	"net/http"
	"strings"
)

// glyphStyle says how a message that starts with a glyph is painted.
type glyphStyle struct {
	code      string
	wholeLine bool
}

var glyphs = map[string]glyphStyle{
	"✗": {Red, true},
	"⚠": {Yellow, true},
	"✓": {Green, false},
	"⟳": {Cyan, false},
	"→": {Dim, true},
}

// Message styles a status line by its leading glyph: errors (✗) and warnings
// (⚠) are painted whole, ✓ and ⟳ only on the glyph, → (a trace line) dimmed.
// Text without a known glyph is returned unchanged.
func Message(msg string, on bool) string {
	if !on {
		return msg
	}
	for g, st := range glyphs {
		if rest, ok := strings.CutPrefix(msg, g); ok {
			if st.wholeLine {
				return Paint(msg, st.code, true)
			}
			return Paint(g, st.code, true) + rest
		}
	}
	return msg
}

// StatusLine formats "HTTP/1.1 200 OK" with the code coloured by class.
func StatusLine(proto string, code int, text string, on bool) string {
	var c string
	switch {
	case code >= 500:
		c = Red + ";" + Bold
	case code >= 400:
		c = Red
	case code >= 300:
		c = Yellow
	case code >= 200:
		c = Green
	default:
		c = Cyan
	}
	if text == "" {
		text = http.StatusText(code)
	}
	return fmt.Sprintf("%s %s %s", Paint(proto, Dim, on), Paint(fmt.Sprint(code), c, on), text)
}

// HeaderLine formats "Name: value" with the name in cyan.
func HeaderLine(prefix, name, value string, on bool) string {
	return prefix + Paint(name, Cyan, on) + ": " + value
}
