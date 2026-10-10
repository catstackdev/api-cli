package httpc

import (
	"fmt"
	"strings"
)

// ParseHeader splits a "Name: value" argument (-H) and checks it with CheckHeader.
// Errors name the header, never its value: a -H value is often a key.
func ParseHeader(raw string) (name, value string, err error) {
	name, value, ok := strings.Cut(raw, ":")
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return "", "", fmt.Errorf("bad header: want \"Name: value\"")
	}
	value = strings.TrimSpace(value)
	if err := CheckHeader(name, value); err != nil {
		return "", "", err
	}
	return name, value, nil
}

// CheckHeader is the one rule for a header that will be written onto the wire,
// whatever its source (-H, an env profile in config, a .http file): the name is
// an HTTP token and nothing in the line is a control character. A CR or LF in a
// value would start a second header (or the body), and the name or value may
// come from a file in a cloned repo.
func CheckHeader(name, value string) error {
	if name == "" {
		return fmt.Errorf("bad header: empty name")
	}
	for _, r := range name {
		if !isTokenRune(r) {
			return fmt.Errorf("bad header name %q: only letters, digits and !#$%%&'*+-.^_`|~ are allowed", name)
		}
	}
	for _, r := range value {
		if r < 0x20 && r != '\t' || r == 0x7f {
			return fmt.Errorf("bad header %q: the value has a control character (line break?)", name)
		}
	}
	return nil
}

func isTokenRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", r)
}
