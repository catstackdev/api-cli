package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
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

// plan maps a settable key to the edits that realise it in a file.
func plan(key string, old, want Config) (edits []edit, drop *inlineDrop, err error) {
	parts := strings.Split(key, ".")
	str := func(table, k, v string) []edit { return []edit{{table, k, tomlString(v)}} }
	switch {
	case key == "default_env":
		return str("", "default_env", want.DefaultEnv), nil, nil
	case parts[0] == "defaults" && len(parts) == 2:
		d := want.Defaults
		switch parts[1] {
		case "timeout":
			return str("defaults", "timeout", d.Timeout), nil, nil
		case "connect_timeout":
			return str("defaults", "connect_timeout", d.ConnectTimeout), nil, nil
		case "color":
			return str("defaults", "color", d.Color), nil, nil
		case "retry":
			return []edit{{"defaults", "retry", strconv.Itoa(*d.Retry)}}, nil, nil
		case "summary":
			return []edit{{"defaults", "summary", strconv.FormatBool(*d.Summary)}}, nil, nil
		case "editor":
			return str("defaults", "editor", d.Editor), nil, nil
		case "token_cache":
			return []edit{{"defaults", "token_cache", strconv.FormatBool(*d.TokenCache)}}, nil, nil
		case "auto_login":
			return []edit{{"defaults", "auto_login", strconv.FormatBool(*d.AutoLogin)}}, nil, nil
		}
	case parts[0] == "vars" && len(parts) == 2:
		v := want.Vars
		switch parts[1] {
		case "base_url":
			return str("vars", "base_url", v.BaseURL), nil, nil
		case "graphql_url":
			return str("vars", "graphql_url", v.GraphQLURL), nil, nil
		case "env_suffix":
			return str("vars", "env_suffix", v.EnvSuffix), nil, nil
		}
	case parts[0] == "alias" && len(parts) == 2:
		return str("alias", parts[1], want.Alias[parts[1]]), nil, nil
	case parts[0] == "env" && len(parts) >= 3:
		name := parts[1]
		table := "env." + name
		e := want.Env[name]
		field := strings.Join(parts[2:], ".")
		switch field {
		case "url":
			return str(table, "url", e.URL), nil, nil
		case "graphql":
			return str(table, "graphql", e.GraphQL), nil, nil
		case "token_env":
			return str(table, "token_env", e.TokenEnv), nil, nil
		case "endpoint_prefix":
			return str(table, "endpoint_prefix", e.EndpointPrefix), nil, nil
		case "openapi":
			return str(table, "openapi", e.OpenAPI), nil, nil
		case "endpoints":
			return []edit{{table, "endpoints", tomlArray(e.Endpoints)}}, nil, nil
		}
		if parts[2] == "headers" && len(parts) == 4 {
			sub := table + ".headers"
			d := &inlineDrop{table: table, key: "headers"}
			for h, v := range old.Env[name].Headers {
				d.readd = append(d.readd, edit{sub, h, tomlString(v)})
			}
			return []edit{{sub, parts[3], tomlString(e.Headers[parts[3]])}}, d, nil
		}
		if parts[2] == "login" && len(parts) >= 4 && e.Login != nil {
			ltable := table + ".login"
			lf := strings.Join(parts[3:], ".")
			switch lf {
			case "path":
				return str(ltable, "path", e.Login.Path), nil, nil
			case "gql":
				return str(ltable, "gql", e.Login.GQL), nil, nil
			case "endpoint":
				return str(ltable, "endpoint", e.Login.Endpoint), nil, nil
			case "token_path":
				return str(ltable, "token_path", e.Login.TokenPath), nil, nil
			}
			if strings.HasPrefix(lf, "vars.") {
				vk := strings.TrimPrefix(lf, "vars.")
				sub := ltable + ".vars"
				d := &inlineDrop{table: ltable, key: "vars"}
				if ol := old.Env[name].Login; ol != nil {
					for k, v := range ol.Vars {
						d.readd = append(d.readd, edit{sub, k, tomlString(v)})
					}
				}
				return []edit{{sub, vk, tomlString(e.Login.Vars[vk])}}, d, nil
			}
		}
	}
	return nil, nil, fmt.Errorf("no in-place edit for %q", key)
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
	edits, drop, err := plan(key, old, want)
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
