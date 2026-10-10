package cli

import "testing"

func FuzzSplitWords(f *testing.F) {
	for _, s := range []string{"get /x", `gql 'a b' "c d"`, `a"b`, "", "  ", `'`, "x\ty"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, w := range splitWords(s) {
			_ = w
		}
	})
}
