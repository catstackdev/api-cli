// Package session reads the API_* variables the shell helpers share and
// builds shell code that is safe to eval.
package session

import (
	"os"
	"strings"
)

// Environment variable names, shared with the zsh helpers.
const (
	EnvBaseURL    = "API_BASE_URL"
	EnvGraphQLURL = "API_GRAPHQL_URL"
	EnvToken      = "API_TOKEN"
	EnvName       = "API_ENV"
	// EnvFrom records HOW the session was set ("toml" for `env use`, "file:.env.x"
	// for `env load`). Display only: nothing is decided from it.
	EnvFrom = "API_ENV_FROM"
)

// Session is the request context of the current shell.
type Session struct {
	BaseURL    string
	GraphQLURL string
	Token      string
	Name       string
	From       string
}

// FromEnv reads the session from the process environment.
func FromEnv() Session { return FromLookup(os.LookupEnv) }

// FromLookup reads the session through lookup (tests inject one).
func FromLookup(lookup func(string) (string, bool)) Session {
	get := func(k string) string { v, _ := lookup(k); return v }
	return Session{
		BaseURL:    get(EnvBaseURL),
		GraphQLURL: get(EnvGraphQLURL),
		Token:      get(EnvToken),
		Name:       get(EnvName),
		From:       get(EnvFrom),
	}
}

// MaskToken shortens a token to its first 12 characters for display.
func MaskToken(token string) string {
	if token == "" {
		return "<not set>"
	}
	runes := []rune(token)
	if len(runes) > 12 {
		runes = runes[:12]
	}
	return string(runes) + "…"
}

// Quote single-quotes a value for POSIX shells. Every value that ends up in
// eval'd output must go through it: tokens come from server responses.
func Quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// Export returns an `export KEY='value'` statement.
func Export(key, value string) string {
	return "export " + key + "=" + Quote(value)
}

// Unset returns an `unset KEY...` statement.
func Unset(keys ...string) string {
	return "unset " + strings.Join(keys, " ")
}
