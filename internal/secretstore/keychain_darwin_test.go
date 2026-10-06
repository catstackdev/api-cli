//go:build darwin

package secretstore

import (
	"encoding/base64"
	"testing"
)

// The Keychain entry format, without touching the real Keychain.
func TestKeychainFormats(t *testing.T) {
	// unbound: the older, still-written format
	plain := encode(Secret{Value: "pässword 'q'"})
	if plain[:4] != "b64:" {
		t.Fatalf("%q", plain)
	}
	if got, err := decode(plain); err != nil || got.Value != "pässword 'q'" || len(got.Origins) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	// bound: carries the origins, still printable ASCII
	bound := encode(Secret{Value: "s3cret", Origins: []string{"https://api.dev.example.com", "http://localhost:3000"}})
	if bound[:3] != "v2:" {
		t.Fatalf("%q", bound)
	}
	for _, r := range bound {
		if r < 0x20 || r > 0x7e {
			t.Fatalf("not printable ASCII: %q", bound)
		}
	}
	got, err := decode(bound)
	if err != nil || got.Value != "s3cret" || len(got.Origins) != 2 || !got.AllowedFor("http://localhost:3000") || got.AllowedFor("https://evil.example") {
		t.Fatalf("%+v %v", got, err)
	}
	// entries written by hand (no marker) and by the very first builds still read
	if got, err := decode("plain-hand-written"); err != nil || got.Value != "plain-hand-written" {
		t.Fatalf("%+v %v", got, err)
	}
	old := "b64:" + base64.StdEncoding.EncodeToString([]byte("legacy"))
	if got, err := decode(old); err != nil || got.Value != "legacy" || len(got.Origins) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	// corrupt entries are an error, never a silently wrong secret
	for _, bad := range []string{"b64:***", "v2:***", "v2:" + base64.StdEncoding.EncodeToString([]byte("not json"))} {
		if _, err := decode(bad); err == nil {
			t.Errorf("%q decoded", bad)
		}
	}
}
