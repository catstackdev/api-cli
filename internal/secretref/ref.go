// Package secretref parses value references: a way to say "this value comes
// from somewhere else" so a secret never has to be typed on a command line or
// written in a config file.
//
//	@keychain:NAME   the credential store entry "api:NAME" (see `api secret`)
//	@env:NAME        the environment variable NAME
//	@prompt          ask, with no echo (@prompt:Label shows a label)
//	@@text           the literal text "@text"
package secretref

import (
	"regexp"
	"strings"
)

// Kind of a reference.
type Kind int

const (
	None Kind = iota // a plain value
	Env
	Keychain
	Prompt
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Parse classifies v. For Env, Keychain and Prompt, arg is the NAME / label.
// A reference with a malformed NAME is not a reference (it stays a plain value
// and so cannot smuggle anything through).
func Parse(v string) (kind Kind, arg string) {
	switch {
	case strings.HasPrefix(v, "@@"):
		return None, ""
	case strings.HasPrefix(v, "@env:"):
		if n := v[len("@env:"):]; envName.MatchString(n) {
			return Env, n
		}
	case strings.HasPrefix(v, "@keychain:"):
		if n := v[len("@keychain:"):]; n != "" {
			return Keychain, n
		}
	case v == "@prompt":
		return Prompt, ""
	case strings.HasPrefix(v, "@prompt:"):
		return Prompt, v[len("@prompt:"):]
	}
	return None, ""
}

// IsRef reports whether v is a reference.
func IsRef(v string) bool { k, _ := Parse(v); return k != None }

// Unescape turns "@@x" into "@x"; other values are returned unchanged.
func Unescape(v string) string {
	if strings.HasPrefix(v, "@@") {
		return v[1:]
	}
	return v
}
