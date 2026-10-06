package envfile

import (
	"regexp"
	"strings"
	"testing"
)

var exampleLine = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=$`)

// An .env file is the most secret-laden file in a project. Whatever it contains,
// the generated example must hold key names only (no values) by default.
func FuzzExampleNeverContainsValues(f *testing.F) {
	f.Add("A=1\nSECRET=hunter2\nURL=https://u:p@host\n# c\nB=\"two words\"\n")
	f.Add("export X=y\r\nZ=\r\n")
	f.Add("A=#\nB='\n")
	f.Fuzz(func(t *testing.T, text string) {
		src := Source{Name: ".env", Entries: Parse([]byte(text))}
		for _, line := range strings.Split(Example([]Source{src}, false), "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if !exampleLine.MatchString(line) {
				t.Fatalf("example line %q carries more than a key name (input %q)", line, text)
			}
		}
		// --keep-values may keep harmless values, never secret-looking ones: judge
		// what is actually printed (a repeated key prints one merged value)
		for _, line := range strings.Split(Example([]Source{src}, true), "\n") {
			key, val, ok := strings.Cut(line, "=")
			if !ok || strings.HasPrefix(line, "#") || val == "" {
				continue
			}
			if IsSecretKey(key) || HasCredentials(val) {
				t.Fatalf("secret-looking %s kept its value: %q (input %q)", key, line, text)
			}
		}
	})
}
