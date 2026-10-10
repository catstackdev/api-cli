package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

// SetInFile applies key=value to the config file at path WITHOUT rewriting it:
// the commented template, your own comments and the layout stay. An existing key
// is updated in place; a commented example line for the key ("# timeout = ...")
// is uncommented in place; otherwise the key is added to its table (created at
// the end of the file when it does not exist yet).
//
// The result is re-parsed and must equal the intended config exactly. If the
// text cannot be edited safely (unusual layouts such as a multi-line inline
// table) it falls back to rewriting the file, which loses comments, and says so
// by returning preserved=false.
func SetInFile(path, key, value string) (preserved bool, err error) {
	old, err := Read(path)
	if err != nil {
		return false, err
	}
	want, err := Read(path)
	if err != nil {
		return false, err
	}
	if err := Set(&want, key, value); err != nil {
		return false, err
	}

	var text []byte
	if data, rerr := os.ReadFile(path); rerr == nil {
		text = data
	}
	if edited, eerr := editText(string(text), key, old, want); eerr == nil {
		if got, perr := ParseText([]byte(edited), path); perr == nil && reflect.DeepEqual(got, want) {
			return true, writeAtomic(path, []byte(edited))
		}
	}
	return false, Write(path, want)
}

// writeAtomic replaces path via a RANDOM exclusive temp file in the same
// directory. A fixed name (path + ".tmp") could be a symlink planted by a repo,
// and writing through it would truncate a file of yours.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".api-*.tmp") // O_EXCL, mode 0600, unpredictable name
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Chmod(path, 0o600)
}

type edit struct{ table, key, lit string }

// inlineDrop removes a single-line inline table (headers = {...}) from a table
// and re-adds its entries as a sub-table, because a key cannot be both.
type inlineDrop struct {
	table, key string
	readd      []edit
}

var headerRe = regexp.MustCompile(`^\s*\[([^\[\]]+)\]\s*(?:#.*)?$`)

func normTable(s string) string {
	return strings.NewReplacer(" ", "", "\t", "", `"`, "", "'", "").Replace(s)
}

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func keyText(k string) string {
	if bareKey.MatchString(k) {
		return k
	}
	return tomlString(k)
}

// tomlString renders a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func tomlArray(items []string) string {
	q := make([]string, len(items))
	for i, it := range items {
		q[i] = tomlString(it)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func editText(text, key string, old, want Config) (string, error) {
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	edits, drop, err := planFor(key, old, want)
	if err != nil {
		return "", err
	}
	if drop != nil {
		var found bool
		if lines, found, err = removeInline(lines, drop.table, drop.key); err != nil {
			return "", err
		}
		if found {
			for _, e := range drop.readd {
				lines = upsert(lines, e)
			}
		}
	}
	for _, e := range edits {
		lines = upsert(lines, e)
	}
	return strings.Join(lines, eol) + eol, nil
}

// tableRange finds the ACTIVE table: the header index (-1 for the top level) and
// the end of its body (the next header). ok is false when it does not exist.
func tableRange(lines []string, table string) (start, end int, ok bool) {
	headers := []int{}
	for i, l := range lines {
		if headerRe.MatchString(l) {
			headers = append(headers, i)
		}
	}
	next := func(after int) int {
		for _, h := range headers {
			if h > after {
				return h
			}
		}
		return len(lines)
	}
	if table == "" {
		return -1, next(-1), true
	}
	for _, h := range headers {
		if normTable(headerRe.FindStringSubmatch(lines[h])[1]) == table {
			return h, next(h), true
		}
	}
	return 0, 0, false
}

func keyLineRe(key string, commented bool) *regexp.Regexp {
	k := regexp.QuoteMeta(key)
	pat := `^\s*(?:"` + k + `"|` + k + `)\s*=`
	if commented {
		pat = `^\s*#\s*(?:"` + k + `"|` + k + `)\s*=`
	}
	return regexp.MustCompile(pat)
}

func isActiveKeyLine(l string) bool {
	t := strings.TrimSpace(l)
	return t != "" && !strings.HasPrefix(t, "#") && !headerRe.MatchString(l)
}

// upsert sets table.key = lit: update in place, uncomment the example, or add.
func upsert(lines []string, e edit) []string {
	assign := keyText(e.key) + " = " + e.lit
	start, end, ok := tableRange(lines, e.table)
	if !ok {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		return append(lines, "["+e.table+"]", assign)
	}
	body := start + 1
	active, commented := keyLineRe(e.key, false), keyLineRe(e.key, true)
	for i := body; i < end; i++ {
		if active.MatchString(lines[i]) {
			lines[i] = assign
			return lines
		}
	}
	for i := body; i < end; i++ {
		if commented.MatchString(lines[i]) {
			lines[i] = assign
			return lines
		}
	}
	insert := body // right after the header
	for i := body; i < end; i++ {
		if isActiveKeyLine(lines[i]) {
			insert = i + 1
		}
	}
	add := []string{assign}
	if start == -1 && insert == end && end < len(lines) {
		add = append(add, "") // top level: keep a blank line before the first table
	}
	out := make([]string, 0, len(lines)+len(add))
	out = append(out, lines[:insert]...)
	out = append(out, add...)
	return append(out, lines[insert:]...)
}

var inlineOpen = func(key string) *regexp.Regexp {
	return regexp.MustCompile(`^\s*(?:"` + regexp.QuoteMeta(key) + `"|` + regexp.QuoteMeta(key) + `)\s*=\s*\{`)
}

// removeInline deletes a single-line inline table assigned to key in table.
func removeInline(lines []string, table, key string) ([]string, bool, error) {
	start, end, ok := tableRange(lines, table)
	if !ok {
		return lines, false, nil
	}
	re := inlineOpen(key)
	for i := start + 1; i < end; i++ {
		if !re.MatchString(lines[i]) {
			continue
		}
		t := strings.TrimSpace(lines[i])
		if !strings.Contains(t, "}") {
			return nil, false, fmt.Errorf("%s.%s is a multi-line inline table", table, key)
		}
		return append(lines[:i:i], lines[i+1:]...), true, nil
	}
	return lines, false, nil
}
