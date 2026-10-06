package jwt

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func token(payload string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(payload)) + "." + enc([]byte("sig"))
}

func TestParse(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c, ok := Parse(token(`{"exp":1800007200,"iat":1800000000,"sub":"x"}`))
	if !ok || !c.Expires.Equal(time.Unix(1_800_007_200, 0)) || !c.Issued.Equal(now) {
		t.Fatalf("%+v %v", c, ok)
	}
	if d, ok := c.ExpiresIn(now); !ok || d != 2*time.Hour {
		t.Fatalf("%v %v", d, ok)
	}
	// float timestamps and standard (padded) base64 are tolerated
	padded := strings.Join([]string{"e30", base64.URLEncoding.EncodeToString([]byte(`{"exp":1800000060.5}`)), "c2ln"}, ".")
	if c, ok := Parse(padded); !ok || c.Expires.Unix() != 1_800_000_060 {
		t.Fatalf("%+v %v", c, ok)
	}
}

func TestParseRejectsWhatIsNotAJWT(t *testing.T) {
	for _, bad := range []string{"", "opaque-token-123", "a.b", "a.b.c.d", "x.!!!.y", token("not json"), token(`{}`), token(`{"exp":"soon"}`), token(`{"exp":-5}`)} {
		if c, ok := Parse(bad); ok {
			t.Errorf("Parse(%q) = %+v, want not-a-JWT", bad, c)
		}
	}
	// a token with only iat parses but has no expiry to show
	c, ok := Parse(token(`{"iat":1700000000}`))
	if !ok || c.Describe(time.Now()) != "" {
		t.Fatalf("%+v %v", c, ok)
	}
}

func TestDescribe(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	at := func(d time.Duration) Claims { return Claims{Expires: now.Add(d)} }
	for d, want := range map[time.Duration]string{
		30 * time.Second:                "expires in 30s",
		59*time.Minute + 20*time.Second: "expires in 59m0s",
		90 * time.Minute:                "expires in 1h30m0s",
		-5 * time.Minute:                "EXPIRED 5m0s ago",
		-1 * time.Second:                "EXPIRED 1s ago",
		72 * time.Hour:                  "expires in 72h0m0s",
	} {
		if got := at(d).Describe(now); got != want {
			t.Errorf("Describe(%v) = %q, want %q", d, got, want)
		}
	}
}
