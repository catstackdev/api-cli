// Package secretstore keeps secrets (a login password, a token) in the
// operating system's credential store, never in a file this tool writes.
//
// macOS uses the Keychain through the `security` command. Elsewhere there is no
// store: Default returns one whose methods fail with ErrUnavailable, and users
// fall back to @env:NAME or @prompt value references.
package secretstore

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

var (
	// ErrUnavailable means this system has no supported credential store.
	ErrUnavailable = errors.New("no credential store on this system (use @env:NAME or @prompt instead)")
	// ErrNotFound means nothing is stored under that name.
	ErrNotFound = errors.New("not found")
)

// Secret is a stored value plus the servers it may be sent to. A secret bound to
// no origin can be sent anywhere you point it; a bound one only to its origins,
// so a repo or a pasted command cannot redirect it.
type Secret struct {
	Value   string
	Origins []string // normalised "scheme://host[:port]"; empty = unbound
}

// AllowedFor reports whether the secret may be sent to origin.
func (s Secret) AllowedFor(origin string) bool {
	if len(s.Origins) == 0 {
		return true
	}
	for _, o := range s.Origins {
		if o == origin {
			return true
		}
	}
	return false
}

// Store is a minimal credential store.
type Store interface {
	Get(name string) (Secret, error)
	Set(name string, secret Secret) error
	Delete(name string) error
}

// NormalizeOrigin turns a URL into "scheme://host[:port]" (http or https only,
// lower case, the default port :80 / :443 dropped).
func NormalizeOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", fmt.Errorf("%q is not an http(s) URL", raw)
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") { // IPv6 literal
		host = "[" + host + "]"
	}
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	return strings.ToLower(u.Scheme) + "://" + host, nil
}

// NormalizeOrigins normalises and de-duplicates a list of URLs.
func NormalizeOrigins(raws []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, r := range raws {
		o, err := NormalizeOrigin(r)
		if err != nil {
			return nil, err
		}
		if !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	return out, nil
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidateName accepts short, plain names (they become part of a Keychain
// service name and of shell-free command arguments).
func ValidateName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("bad secret name %q: letters, digits, . _ - only (max 64)", name)
	}
	return nil
}

// Service is the store-level name for a secret: names are namespaced so this
// tool can never read or overwrite another application's entries.
func Service(name string) string { return "api:" + name }

// Memory is an in-memory Store for tests.
type Memory struct {
	mu sync.Mutex
	m  map[string]Secret
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory { return &Memory{m: map[string]Secret{}} }

func (s *Memory) Get(name string) (Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateName(name); err != nil {
		return Secret{}, err
	}
	v, ok := s.m[name]
	if !ok {
		return Secret{}, ErrNotFound
	}
	return v, nil
}

func (s *Memory) Set(name string, secret Secret) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateName(name); err != nil {
		return err
	}
	s.m[name] = secret
	return nil
}

func (s *Memory) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateName(name); err != nil {
		return err
	}
	if _, ok := s.m[name]; !ok {
		return ErrNotFound
	}
	delete(s.m, name)
	return nil
}
