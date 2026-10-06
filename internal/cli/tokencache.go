package cli

// The token cache lets a login outlive the shell (`defaults.token_cache`) and
// lets a rejected token be replaced without typing anything (`defaults.auto_login`).
// A cached token is a credential at rest, so every rule here errs towards NOT
// using it; see SECURITY.md ("Token cache").

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cybercat/api/internal/httpc"
	"cybercat/api/internal/jwt"
	"cybercat/api/internal/render"
	"cybercat/api/internal/secretstore"
	"cybercat/api/internal/session"
)

const (
	tokenCachePrefix = "token."
	tokenExpirySkew  = 60 * time.Second // a token about to expire is as good as expired
)

func (a *app) tokenCacheOn() bool {
	b := a.cfg.Defaults.TokenCache
	return b != nil && *b
}

func (a *app) autoLoginOn() bool {
	b := a.cfg.Defaults.AutoLogin
	return b != nil && *b
}

// tokenCacheName is the credential-store name for an env's cached token.
func tokenCacheName(env string) (string, bool) {
	name := tokenCachePrefix + env
	return name, env != "" && secretstore.ValidateName(name) == nil
}

// readCachedToken returns the cached token of env if it may go to dest. A cached
// token is only ever released to the origin it was stored for: an entry without a
// binding (someone edited the Keychain) is never used.
func (a *app) readCachedToken(env, dest string) string {
	if !a.tokenCacheOn() || dest == "" {
		return ""
	}
	name, ok := tokenCacheName(env)
	if !ok {
		return ""
	}
	sec, err := a.store.Get(name)
	if err != nil || sec.Value == "" || len(sec.Origins) == 0 || !sec.AllowedFor(dest) {
		return ""
	}
	return sec.Value
}

// cachedToken is readCachedToken minus tokens that are expired or about to be.
func (a *app) cachedToken(env, dest string) string {
	tok := a.readCachedToken(env, dest)
	if tok == "" {
		return ""
	}
	if c, ok := jwt.Parse(tok); ok {
		if left, has := c.ExpiresIn(time.Now()); has && left < tokenExpirySkew {
			return ""
		}
	}
	return tok
}

// saveCachedToken stores a token bound to the origin it was obtained from, and
// only when that origin is the env's base URL (the one place it will be sent).
func (a *app) saveCachedToken(env, baseURL, loginURL, token string) {
	if !a.tokenCacheOn() || token == "" {
		return
	}
	name, ok := tokenCacheName(env)
	if !ok || baseURL == "" {
		return
	}
	base, err := secretstore.NormalizeOrigin(baseURL)
	if err != nil {
		return
	}
	if from, err := secretstore.NormalizeOrigin(loginURL); err != nil || from != base {
		a.say("⚠ token not cached: it came from " + orUnknown(origin(loginURL)) + ", not the env's base URL " + base)
		return
	}
	if err := a.store.Set(name, secretstore.Secret{Value: token, Origins: []string{base}}); err != nil {
		a.say("⚠ could not cache the token: " + err.Error())
		return
	}
	a.say("  (token cached for env " + env + ", bound to " + base + ")")
}

// forgetCachedToken deletes env's cached token and reports whether there was one.
func (a *app) forgetCachedToken(env string) bool {
	name, ok := tokenCacheName(env)
	if !ok {
		return false
	}
	err := a.store.Delete(name)
	return err == nil
}

var errNotRecovered = errors.New("not recovered")

// retryOn401 runs once after a request was answered 401. It only acts on a token
// WE attached (the shell's session token or the cached one) to the active base
// URL, never on --token, -H Authorization, a login or --no-auth.
func (a *app) retryOn401(ctx context.Context, spec httpc.Spec, o *reqOpts) (*httpc.Response, bool) {
	// o.noAuth is redundant with the `sent` check below (no header was attached);
	// kept as defence in depth, which is why no test targets it alone.
	if o.noAuth || a.token != "" || (!a.tokenCacheOn() && !a.autoLoginOn()) {
		return nil, false
	}
	rs, _ := a.resolve()
	dest := origin(spec.URL)
	if rs.Name == "" || dest == "" || dest != origin(rs.BaseURL) {
		return nil, false
	}
	sent := strings.TrimPrefix(spec.Header("Authorization"), "Bearer ")
	if sent == "" || (sent != rs.Token && sent != a.readCachedToken(rs.Name, dest)) {
		return nil, false
	}

	// 1. another terminal may have logged in since: try the cached token once
	if tok := a.cachedToken(rs.Name, dest); tok != "" && tok != sent {
		if resp, err := a.resend(ctx, spec, o, tok); err == nil && resp.Status != 401 {
			a.say("⟳ this shell's token was rejected; the cached one worked (run `api login` to refresh the shell)")
			return resp, true
		}
	}
	// 2. log in again from the configured login
	if !a.autoLoginOn() || a.reloginTried {
		return nil, false
	}
	a.reloginTried = true
	tok, err := a.autoLogin(ctx, o)
	if err != nil {
		a.say("⚠ automatic login failed: " + err.Error())
		return nil, false
	}
	resp, err := a.resend(ctx, spec, o, tok)
	if err != nil {
		return nil, false
	}
	a.say("⟳ logged in again automatically (this shell still holds the old token until you run `api login`)")
	return resp, true
}

func (a *app) resend(ctx context.Context, spec httpc.Spec, o *reqOpts, token string) (*httpc.Response, error) {
	s2 := spec
	s2.Headers = append([]httpc.Header(nil), spec.Headers...)
	s2.SetHeader("Authorization", "Bearer "+token)
	return a.send(ctx, s2, o)
}

// autoLogin performs the env's configured login without any prompt: only
// @keychain: references are accepted (a surprise password prompt in the middle of
// a script, or an @env: read, is not what "automatic" should mean).
func (a *app) autoLogin(ctx context.Context, o *reqOpts) (string, error) {
	lo := *o
	lo.noAuth, lo.dry, lo.har, lo.verbose, lo.summary = true, false, "", false, false
	a.strictRefs = true
	defer func() { a.strictRefs = false }()
	spec, tokenPath, err := a.loginFromConfig(&lo, nil, false)
	if err != nil {
		return "", err
	}
	resp, err := a.send(ctx, spec, &lo)
	if err != nil {
		return "", err
	}
	if resp.Status >= 400 {
		return "", fmt.Errorf("the server answered HTTP %d", resp.Status)
	}
	token, err := render.First(resp.Body, tokenPath)
	if err != nil || token == "" {
		return "", fmt.Errorf("token not found at %s", tokenPath)
	}
	rs, _ := a.resolve()
	a.saveCachedToken(rs.Name, rs.BaseURL, spec.URL, token)
	return token, nil
}

// cacheStatus describes the cached token of env for `api env status`.
func (a *app) cacheStatus(env, base string) string {
	if !a.tokenCacheOn() {
		return ""
	}
	dest := origin(base)
	tok := a.readCachedToken(env, dest)
	switch {
	case tok == "":
		return "none"
	case tokenExpiry(tok, time.Now()) != "":
		return session.MaskToken(tok) + "  (" + tokenExpiry(tok, time.Now()) + ")"
	}
	return session.MaskToken(tok)
}
