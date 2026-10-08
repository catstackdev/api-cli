package cli

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cybercat/api/internal/httpfile"
	"cybercat/api/internal/secret"
)

// checkRequestFiles: saved requests in .api/requests/ are meant to be kept and
// shared, so they are not ignored; what must not happen is a literal credential
// in one of them. Only names are reported, never values.
func checkRequestFiles() []check {
	root := findRoot()
	var files []string
	for _, pat := range []string{"*.http", "*.rest"} {
		m, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(requestsDir), pat))
		files = append(files, m...)
	}
	if len(files) == 0 {
		return nil
	}
	sort.Strings(files)
	var bad []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		parsed, err := httpfile.Parse(data)
		if err != nil {
			continue
		}
		var names []string
		for _, r := range parsed.Requests {
			names = append(names, literalSecretNames(r)...)
		}
		if len(names) > 0 {
			bad = append(bad, filepath.Base(f)+" ("+strings.Join(dedupe(names), ", ")+")")
		}
	}
	if len(bad) > 0 {
		return []check{newCheck(requestsDir, lvlWarn, "literal secret values in %s: use {{$env API_NAME}} or {{token}} before committing", strings.Join(bad, "; "))}
	}
	return []check{newCheck(requestsDir, lvlOK, "%d saved request file(s), no literal secrets", len(files))}
}

// literalSecretNames lists secret-looking headers, query parameters and JSON
// fields of r whose value is typed in rather than a {{...}} reference.
func literalSecretNames(r httpfile.Request) []string {
	var out []string
	for _, h := range r.Headers {
		if secret.Name(h.Name) && isLiteral(h.Value) {
			out = append(out, h.Name)
		}
	}
	if _, q, ok := strings.Cut(r.URL, "?"); ok {
		for _, kv := range strings.Split(q, "&") {
			k, v, _ := strings.Cut(kv, "=")
			if k, err := url.QueryUnescape(k); err == nil && secret.Param(k) && isLiteral(v) {
				out = append(out, k)
			}
		}
	}
	var body any
	if json.Unmarshal([]byte(r.Body), &body) == nil {
		out = append(out, jsonLiteralSecrets(body)...)
	}
	return out
}

// isLiteral: a non-empty value with no {{reference}} in it.
func isLiteral(v string) bool {
	v = strings.TrimSpace(v)
	return v != "" && !strings.Contains(v, "{{")
}

func jsonLiteralSecrets(v any) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && secret.Name(k) && isLiteral(s) {
				out = append(out, k)
			}
			out = append(out, jsonLiteralSecrets(val)...)
		}
	case []any:
		for _, val := range x {
			out = append(out, jsonLiteralSecrets(val)...)
		}
	}
	return out
}

func dedupe(in []string) []string {
	sort.Strings(in)
	out := in[:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}
