// Package history remembers requests for tab completion and `api again`.
// It is best-effort (a request never fails over it) and deliberately stores
// only method, path and filter: never a body, a header or a token. A path whose
// query string carries a secret-looking parameter is not recorded at all.
//
// File format: one entry per line, newest last. A line is either a legacy bare
// path or "METHOD<TAB>path<TAB>filter".
package history

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"cybercat/api/internal/secret"
)

const maxLines = 500

// RunMethod marks an entry that points at a saved request file instead of a
// path (see the cli package); it is never offered as a path.
const RunMethod = "RUN"

// Entry is one remembered request.
type Entry struct {
	Method string
	Path   string
	Filter string
	// Origin is the server the request went to (scheme://host[:port], public,
	// never a token). Lines written before it existed have none: they cannot be
	// told apart by server, so they are only listed on request and never replayed.
	Origin string
	// Root is the project directory a RunMethod entry belongs to: its Path is the
	// name of a file under <Root>/.api/requests, which means nothing elsewhere.
	Root string
}

// Path is the history file location (it may not exist yet).
func Path() string { return file() }

func file() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "api", "history")
}

// Safe reports whether a path can be stored: no control characters, and no
// secret-looking query parameter.
func Safe(path string) bool {
	if path == "" || strings.ContainsAny(path, "\n\r\t") {
		return false
	}
	if i := strings.IndexByte(path, '?'); i >= 0 {
		q, err := url.ParseQuery(path[i+1:])
		if err != nil {
			return false
		}
		for name := range q {
			if secret.Param(name) {
				return false
			}
		}
	}
	// credentials in the authority: https://user:pass@host/...
	if strings.Contains(path, "://") {
		if u, err := url.Parse(path); err != nil || u.User != nil {
			return false
		}
	}
	return true
}

func (e Entry) line() string {
	l := e.Method + "\t" + e.Path + "\t" + e.Filter
	if e.Origin != "" || e.Root != "" {
		l += "\t" + e.Origin
	}
	if e.Root != "" {
		l += "\t" + e.Root
	}
	return l
}

func parse(line string) (Entry, bool) {
	if !strings.Contains(line, "\t") {
		return Entry{Method: "GET", Path: line}, line != "" // legacy: bare path
	}
	f := strings.SplitN(line, "\t", 5)
	for len(f) < 5 {
		f = append(f, "")
	}
	return Entry{Method: f[0], Path: f[1], Filter: f[2], Origin: f[3], Root: f[4]}, f[1] != ""
}

// Record appends an entry. Unsafe entries are dropped silently.
func Record(e Entry) {
	f := file()
	if f == "" || !Safe(e.Path) || strings.ContainsAny(e.Filter, "\n\r\t") || strings.ContainsAny(e.Method, "\n\r\t ") || strings.ContainsAny(e.Origin, "\n\r\t ") || strings.ContainsAny(e.Root, "\n\r\t") {
		return
	}
	if e.Filter == "." {
		e.Filter = ""
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return
	}
	lines := read(f)
	lines = append(lines, e.line())
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	_ = os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// Entries returns unique entries, newest first (at most limit when > 0).
func Entries(limit int) []Entry {
	lines := read(file())
	seen := map[string]bool{}
	var out []Entry
	for i := len(lines) - 1; i >= 0; i-- {
		e, ok := parse(lines[i])
		if !ok || seen[e.line()] {
			continue
		}
		seen[e.line()] = true
		out = append(out, e)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// ForOrigin returns the entries recorded for one server, newest first (at most
// limit when > 0). Untagged legacy entries are not part of any server.
func ForOrigin(origin string, limit int) []Entry {
	var out []Entry
	for _, e := range Entries(0) {
		if origin != "" && e.Origin == origin {
			out = append(out, e)
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out
}

// Paths returns unique recorded request paths for one server starting with
// prefix, newest first. It feeds completion, so entries that are not plain
// requests (RunMethod) never show up as paths.
func Paths(prefix, origin string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range ForOrigin(origin, 0) {
		if e.Method == RunMethod || seen[e.Path] || !strings.HasPrefix(e.Path, prefix) {
			continue
		}
		seen[e.Path] = true
		out = append(out, e.Path)
	}
	return out
}

func read(f string) []string {
	if f == "" {
		return nil
	}
	data, err := os.ReadFile(f)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
