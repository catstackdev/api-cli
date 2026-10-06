package jwt

import (
	"testing"
	"time"
)

func FuzzParse(f *testing.F) {
	f.Add("a.b.c")
	f.Add("eyJhbGciOiJub25lIn0.eyJleHAiOjF9.sig")
	f.Add("..")
	f.Add("x.eyJleHAiOjFlOTk5fQ.y")
	f.Fuzz(func(t *testing.T, token string) {
		c, ok := Parse(token)
		if !ok {
			return
		}
		_ = c.Describe(time.Now())
		_, _ = c.ExpiresIn(time.Now())
	})
}
