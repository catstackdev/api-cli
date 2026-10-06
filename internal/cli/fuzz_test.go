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

func FuzzWalkBody(f *testing.F) {
	body := []byte(`{"data":{"items":[{"id":9}],"n":5,"t":true,"z":null}}`)
	for _, p := range []string{"data.items[0].id", "data.n", "data.t", "data.z", "data.items[7]", "data.items[0].id.x", "[0]", "data..n"} {
		f.Add(p)
	}
	f.Fuzz(func(t *testing.T, path string) {
		if !bodyPath.MatchString(path) {
			return
		}
		_, _ = walkBody(body, path)
		_, _ = walkBody([]byte(path), path)
	})
}
