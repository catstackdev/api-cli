// Package ui owns everything about how output looks in a terminal: whether to
// colour, styled status messages, HTTP status/header formatting, the pager and
// aligned tables. Commands decide WHAT to print; this package decides how.
//
// JSON colouring itself stays in render (it needs the jq palette); this
// package only answers "should colour be on for this stream".
package ui

import (
	"os"
	"strings"
)

// Mode is the user's colour preference.
type Mode int

const (
	Auto Mode = iota
	Always
	Never
)

// ParseMode maps "always" / "never" (anything else is auto).
func ParseMode(s string) Mode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "always", "yes", "true", "force":
		return Always
	case "never", "no", "false", "off":
		return Never
	}
	return Auto
}

// IsTerminal reports whether w is a character device (a TTY).
func IsTerminal(w any) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// Enabled decides colour for one stream: an explicit mode wins, otherwise
// NO_COLOR and TERM=dumb turn it off, otherwise it follows the TTY.
func Enabled(w any, m Mode) bool { return EnabledWith(w, m, os.Getenv) }

// EnabledWith is Enabled with an injectable environment.
func EnabledWith(w any, m Mode, getenv func(string) string) bool {
	switch m {
	case Always:
		return true
	case Never:
		return false
	}
	if getenv("NO_COLOR") != "" || getenv("TERM") == "dumb" {
		return false
	}
	return IsTerminal(w)
}

// SGR codes used by the helpers below.
const (
	Bold   = "1"
	Dim    = "2"
	Red    = "31"
	Green  = "32"
	Yellow = "33"
	Blue   = "34"
	Cyan   = "36"
)

// Paint wraps s in an SGR code when on is true.
func Paint(s, code string, on bool) string {
	if !on || code == "" || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
