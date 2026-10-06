package secretref

import (
	"strings"
	"testing"
)

// A reference with a malformed NAME must stay a plain value: nothing a config
// file says can turn into a reference by accident.
func FuzzParse(f *testing.F) {
	for _, s := range []string{"@keychain:x", "@env:A_B", "@env:a b", "@prompt", "@prompt:Label", "@@x", "plain", "", "@env:", "@keychain:"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		kind, arg := Parse(v)
		_ = Unescape(v)
		if kind == Env && !envName.MatchString(arg) {
			t.Fatalf("@env with a bad name accepted: %q", v)
		}
		if kind == None && arg != "" {
			t.Fatalf("a plain value has no argument: %q -> %q", v, arg)
		}
		if strings.HasPrefix(v, "@@") && kind != None {
			t.Fatalf("an escaped value became a reference: %q", v)
		}
		if IsRef(v) != (kind != None) {
			t.Fatalf("IsRef disagrees with Parse for %q", v)
		}
	})
}
