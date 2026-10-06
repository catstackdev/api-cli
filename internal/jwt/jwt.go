// Package jwt reads the timestamps of a JSON Web Token WITHOUT verifying it. It
// exists only to tell the user when a token expires; nothing here is a trust
// decision, so a forged token can at worst show a wrong expiry.
package jwt

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// Claims are the timestamps found in a token. A zero time means "not present".
type Claims struct {
	Expires time.Time
	Issued  time.Time
}

// Parse reads exp / iat from a JWT. ok is false when the token is not a JWT
// (opaque tokens are common), in which case there is no expiry to show.
func Parse(token string) (c Claims, ok bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return Claims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return Claims{}, false
	}
	var raw struct {
		Exp json.Number `json:"exp"`
		Iat json.Number `json:"iat"`
	}
	dec := json.NewDecoder(strings.NewReader(string(payload)))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return Claims{}, false
	}
	toTime := func(n json.Number) time.Time {
		f, err := n.Float64()
		if err != nil || f <= 0 {
			return time.Time{}
		}
		return time.Unix(int64(f), 0)
	}
	c = Claims{Expires: toTime(raw.Exp), Issued: toTime(raw.Iat)}
	return c, !c.Expires.IsZero() || !c.Issued.IsZero()
}

// ExpiresIn is the time left (negative when expired). ok is false when the token
// carries no expiry.
func (c Claims) ExpiresIn(now time.Time) (d time.Duration, ok bool) {
	if c.Expires.IsZero() {
		return 0, false
	}
	return c.Expires.Sub(now), true
}

// Describe renders the expiry for humans: "expires in 2h13m" / "EXPIRED 5m ago".
func (c Claims) Describe(now time.Time) string {
	d, ok := c.ExpiresIn(now)
	if !ok {
		return ""
	}
	if d <= 0 {
		return "EXPIRED " + round(-d) + " ago"
	}
	return "expires in " + round(d)
}

func round(d time.Duration) string {
	switch {
	case d < time.Minute:
		return d.Round(time.Second).String()
	case d < 2*time.Hour:
		return d.Round(time.Minute).String()
	case d < 48*time.Hour:
		return d.Round(time.Minute).String()
	}
	return (d / (24 * time.Hour) * 24 * time.Hour).String()
}
