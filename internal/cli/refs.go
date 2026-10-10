package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"cybercat/api/internal/jsonbody"
	"cybercat/api/internal/secretref"
	"cybercat/api/internal/secretstore"

	"golang.org/x/term"
)

// refSource says who wrote a reference, which decides what it may do.
type refSource int

const (
	srcUser    refSource = iota // typed on the command line
	srcGlobal                   // your global config
	srcProject                  // a project .api.toml: may come from a cloned repo
)

// resolveRef is for values used locally (api json): no destination to check.
func (a *app) resolveRef(v string) (string, error) { return a.resolveRefFor(v, srcUser, nil) }

// resolverFor returns a resolver that checks every Keychain secret against the
// origins a request is about to reach (dests). Pass the destination even when it
// could not be worked out (""): a bound secret then fails closed.
func (a *app) resolverFor(source refSource, dests ...string) jsonbody.Resolver {
	return func(v string) (string, error) { return a.resolveRefFor(v, source, dests) }
}

// resolveRefFor expands a value reference (@keychain:NAME, @env:NAME, @prompt).
// Plain values pass through; "@@x" is the literal "@x".
//
// A Keychain secret bound to servers (api secret set NAME --for URL) is released
// only to those servers, whoever asks: a repo's config, an alias or a pasted
// command cannot redirect it. A project config may only use bound secrets, and
// never @env: (it could read any variable of yours).
func (a *app) resolveRefFor(v string, source refSource, dests []string) (string, error) {
	kind, arg := secretref.Parse(v)
	if a.auth.strictRefs && (kind == secretref.Env || kind == secretref.Prompt) {
		return "", fmt.Errorf("automatic login only uses @keychain: references (this login uses %s)", v)
	}
	switch kind {
	case secretref.Env:
		if source == srcProject {
			return "", fmt.Errorf("@env:%s is not allowed here: this login's server is not fixed by your own config, so it may only use Keychain secrets bound to that server (api secret set NAME --for <url>)", arg)
		}
		val, ok := a.lookup(arg)
		if !ok || val == "" {
			return "", fmt.Errorf("@env:%s is not set", arg)
		}
		return val, nil
	case secretref.Keychain:
		sec, err := a.store.Get(arg)
		switch {
		case errors.Is(err, secretstore.ErrNotFound):
			return "", fmt.Errorf("@keychain:%s: nothing stored: run `api secret set %s`", arg, arg)
		case err != nil:
			return "", fmt.Errorf("@keychain:%s: %w", arg, err)
		}
		for _, d := range dests {
			if !sec.AllowedFor(d) {
				return "", fmt.Errorf("@keychain:%s is bound to %s and will not be sent to %s (if that server is right: api secret bind %s <url>)",
					arg, strings.Join(sec.Origins, ", "), orUnknown(d), arg)
			}
		}
		if source == srcProject && len(sec.Origins) == 0 {
			hint := "<server url>"
			if len(dests) > 0 && dests[0] != "" {
				hint = dests[0]
			}
			return "", fmt.Errorf("@keychain:%s: this login's server is not fixed by your own config, so the secret must be bound to it: api secret bind %s %s", arg, arg, hint)
		}
		return sec.Value, nil
	case secretref.Prompt:
		label := arg
		if label == "" {
			label = "Secret"
		}
		return a.readSecret(label)
	}
	return secretref.Unescape(v), nil
}

func orUnknown(s string) string {
	if s == "" {
		return "an unknown server"
	}
	return s
}

// maskRef leaves references unresolved. --dry uses it so a dry run never reads
// your Keychain, asks for a password or prints a secret.
func maskRef(v string) (string, error) {
	if secretref.IsRef(v) {
		return "<" + v + ">", nil
	}
	return secretref.Unescape(v), nil
}

// readSecret asks for a value with no echo. It needs a terminal.
func (a *app) readSecret(label string) (string, error) {
	if a.io.ReadSecret != nil {
		return a.io.ReadSecret(label)
	}
	f, ok := a.io.In.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", fmt.Errorf("%s: no terminal to ask on: use @keychain:NAME or @env:NAME", label)
	}
	a.ask(label + ": ")
	b, err := term.ReadPassword(int(f.Fd()))
	a.trace("")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
