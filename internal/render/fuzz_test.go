package render

import "testing"

func FuzzPathEvaluation(f *testing.F) {
	body := []byte(`{"a":{"b":[1,{"c":"x"}]},"n":null,"s":"str"}`)
	for _, p := range []string{".a.b[1].c", ".a[0]", ".[2]", ".n.x", ".s.x", ".a.b[-1]", ".a.b[99999999999999999999]", ".", ".a..b", ".[", ".a[]", ".a.b[1].c.d"} {
		f.Add(p)
	}
	f.Fuzz(func(t *testing.T, filter string) {
		steps, ok := parsePath(filter)
		if !ok {
			return
		}
		_, _ = evalPath(body, steps)
		_, _ = evalPath([]byte(filter), steps) // garbage as the document too
	})
}
