package schema

import (
	"path/filepath"
	"strings"
	"testing"
)

// Endpoint names can come from a project's config and become file names in the
// cache directory: whatever passes ValidLabel must stay inside it.
func FuzzValidLabelStaysInsideTheCache(f *testing.F) {
	for _, s := range []string{"user", "..", "../x", "a/b", ".hidden", "a b", "x\\y", "\x00", "v2.user", "é"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if !ValidLabel(name) {
			return
		}
		dir := filepath.Join("cache", "ugql")
		p := filepath.Join(dir, name+".json")
		if filepath.Dir(p) != dir || strings.HasPrefix(filepath.Base(p), ".") || strings.ContainsAny(name, "/\\\x00") {
			t.Fatalf("label %q escapes or hides: %s", name, p)
		}
	})
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(`{"types":[{"name":"A","kind":"OBJECT"}]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`[`))
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := Parse(data)
		if err != nil {
			return
		}
		lines := s.Index("ep")
		SortIndex(lines)
		for _, ty := range s.Types {
			_, _ = Pretty(Render(ty))
		}
	})
}
