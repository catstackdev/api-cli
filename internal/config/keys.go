package config

import (
	"fmt"
	"strconv"
	"strings"

	"cybercat/api/internal/httpc"
	"cybercat/api/internal/secretref"
)

// Security-relevant: the fileScope of each key decides what a cloned repo's project
// file may set (see SECURITY.md and gotchas 10, 33 and 46).
//
// The settable keys, declared once. Set, Get, the in-place text edit (SetInFile)
// and SettableKeys all read this table, so a key cannot be settable but not
// readable, or readable but not editable in place.
//
// Adding a key still takes four steps elsewhere, on purpose: the struct field and
// its toml tag, the read-side validation in ParseText, the overlay in Merge, and
// schema.json. What a project file may override stays hand-written in LoadWith and
// Merge, where it can be read in one place; the scope below is checked against
// that behaviour by TestWhatAProjectFileMayOverride.

// fileScope says which config files may set a key.
type fileScope uint8

const (
	// globalFile is the zero value ON PURPOSE: a key nobody classified is honoured
	// from the global file only. A project file may come from a cloned repo, so
	// being project-settable has to be said, never assumed.
	globalFile fileScope = iota
	anyFile
)

// slot is one setting bound to a place in a Config.
type slot interface {
	assign(value string) error // validate, then store
	text() (string, bool)      // the value as `config get` shows it; false when unset
	literal() string           // the value as a TOML literal, for the in-place edit
}

type key struct {
	pattern string // as shown in SettableKeys; <x> matches one segment
	// rest makes the last <x> match everything after it, dots included (a login
	// variable like input.username is one name)
	rest  bool
	scope fileScope
	// bind attaches the key to c. args are what the pattern's <...> matched; full
	// is the key as typed. commit stores the change and is called only after assign
	// succeeded, so a refused value leaves c alone.
	bind func(c *Config, args []string, full string) (s slot, commit func())
	// table and name say where the key lives in the TOML text.
	table func(args []string) string
	name  func(args []string) string
	// inline, when set, finds an inline table (headers = {...}) that must become a
	// sub-table before the key can be written next to it.
	inline func(old Config, args []string) *inlineDrop
	// unreadable keys are set but not shown by Get.
	unreadable bool
}

// login reports whether the key belongs to a [env.<name>.login] block. Those are
// judged by their own rules (a secret-looking variable must be a reference), not
// refused by the secret-looking path.
func (k key) login() bool { return strings.Contains(k.pattern, ".login.") }

// --- slots ------------------------------------------------------------------

type textSlot struct {
	p     *string
	check func(string) error
}

func (s textSlot) assign(v string) error {
	if s.check != nil {
		if err := s.check(v); err != nil {
			return err
		}
	}
	*s.p = v
	return nil
}
func (s textSlot) text() (string, bool) { return *s.p, *s.p != "" }
func (s textSlot) literal() string      { return tomlString(*s.p) }

type flagSlot struct {
	p    **bool
	name string
}

func (s flagSlot) assign(v string) error {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("%s must be true or false", s.name)
	}
	*s.p = &b
	return nil
}
func (s flagSlot) text() (string, bool) {
	if *s.p == nil {
		return "", false
	}
	return strconv.FormatBool(**s.p), true
}
func (s flagSlot) literal() string {
	if *s.p == nil {
		return ""
	}
	return strconv.FormatBool(**s.p)
}

type countSlot struct{ p **int }

func (s countSlot) assign(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return fmt.Errorf("retry must be a non-negative integer")
	}
	*s.p = &n
	return nil
}
func (s countSlot) text() (string, bool) {
	if *s.p == nil {
		return "", false
	}
	return strconv.Itoa(**s.p), true
}
func (s countSlot) literal() string {
	if *s.p == nil {
		return ""
	}
	return strconv.Itoa(**s.p)
}

type listSlot struct{ p *[]string }

func (s listSlot) assign(v string) error {
	*s.p = nil
	for _, x := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
		*s.p = append(*s.p, x)
	}
	return nil
}
func (s listSlot) text() (string, bool) { return strings.Join(*s.p, ","), len(*s.p) > 0 }
func (s listSlot) literal() string      { return tomlArray(*s.p) }

// entrySlot is one entry of a string map (a header, an alias, a login variable).
type entrySlot struct {
	m     *map[string]string
	k     string
	check func(name, value string) error
}

func (s entrySlot) assign(v string) error {
	if *s.m == nil {
		*s.m = map[string]string{}
	}
	if s.check != nil {
		if err := s.check(s.k, v); err != nil {
			return err
		}
	}
	(*s.m)[s.k] = v
	return nil
}
func (s entrySlot) text() (string, bool) { v, ok := (*s.m)[s.k]; return v, ok }
func (s entrySlot) literal() string      { return tomlString((*s.m)[s.k]) }

// --- declaring keys -----------------------------------------------------------

func fixed(s string) func([]string) string { return func([]string) string { return s } }

// topKey is a key of the top level of the file (default_env).
func topKey(name string, scope fileScope, ref func(*Config) slot) key {
	return key{
		pattern: name, scope: scope, table: fixed(""), name: fixed(name),
		bind: func(c *Config, _ []string, _ string) (slot, func()) { return ref(c), func() {} },
	}
}

// sectionKey is a key of [defaults] or [vars].
func sectionKey[T any](section, name string, scope fileScope, part func(*Config) *T, ref func(*T) slot) key {
	return key{
		pattern: section + "." + name, scope: scope, table: fixed(section), name: fixed(name),
		bind: func(c *Config, _ []string, _ string) (slot, func()) { return ref(part(c)), func() {} },
	}
}

// envKey is a field of [env.<name>].
func envKey(name string, scope fileScope, ref func(*Env) slot) key {
	return key{
		pattern: "env.<name>." + name, scope: scope,
		table: func(a []string) string { return "env." + a[0] },
		name:  fixed(name),
		bind: func(c *Config, a []string, _ string) (slot, func()) {
			e := c.Env[a[0]]
			return ref(&e), func() { storeEnv(c, a[0], e) }
		},
	}
}

func storeEnv(c *Config, name string, e Env) {
	if c.Env == nil {
		c.Env = map[string]Env{}
	}
	c.Env[name] = e
}

// loginKey is a field of [env.<name>.login]. Login blocks are honoured from a
// project file too, under the extra rules LoadWith applies, hence anyFile.
func loginKey(name string, ref func(*Login) slot) key {
	return key{
		pattern: "env.<name>.login." + name, scope: anyFile, unreadable: true,
		table: func(a []string) string { return "env." + a[0] + ".login" },
		name:  fixed(name),
		bind:  bindLogin(func(l *Login, _ []string, _ string) slot { return ref(l) }),
	}
}

func bindLogin(ref func(l *Login, a []string, full string) slot) func(*Config, []string, string) (slot, func()) {
	return func(c *Config, a []string, full string) (slot, func()) {
		e := c.Env[a[0]]
		if e.Login == nil {
			e.Login = &Login{}
		}
		// the rest of the env is kept as it is: a new Env field must not be lost here
		return ref(e.Login, a, full), func() { storeEnv(c, a[0], e) }
	}
}

func text(p *string, check func(string) error) slot { return textSlot{p, check} }
func flag(p **bool, name string) slot               { return flagSlot{p, name} }

// keys is the table. Its order is the order of SettableKeys.
var keys = []key{
	topKey("default_env", anyFile, func(c *Config) slot { return text(&c.DefaultEnv, nil) }),

	sectionKey("defaults", "timeout", anyFile, func(c *Config) *Defaults { return &c.Defaults }, func(d *Defaults) slot {
		return text(&d.Timeout, func(v string) error { _, err := Duration(v); return err })
	}),
	sectionKey("defaults", "connect_timeout", anyFile, func(c *Config) *Defaults { return &c.Defaults }, func(d *Defaults) slot {
		return text(&d.ConnectTimeout, func(v string) error { _, err := Duration(v); return err })
	}),
	sectionKey("defaults", "retry", anyFile, func(c *Config) *Defaults { return &c.Defaults }, func(d *Defaults) slot { return countSlot{&d.Retry} }),
	sectionKey("defaults", "color", anyFile, func(c *Config) *Defaults { return &c.Defaults }, func(d *Defaults) slot {
		return text(&d.Color, func(v string) error {
			switch v {
			case "auto", "always", "never":
				return nil
			}
			return fmt.Errorf("color must be auto, always or never")
		})
	}),
	sectionKey("defaults", "summary", anyFile, func(c *Config) *Defaults { return &c.Defaults }, func(d *Defaults) slot { return flag(&d.Summary, "summary") }),
	// token_cache, auto_login and editor decide whether a token outlives the shell
	// and which program is run: a cloned repo must not choose either.
	sectionKey("defaults", "token_cache", globalFile, func(c *Config) *Defaults { return &c.Defaults }, func(d *Defaults) slot { return flag(&d.TokenCache, "token_cache") }),
	sectionKey("defaults", "auto_login", globalFile, func(c *Config) *Defaults { return &c.Defaults }, func(d *Defaults) slot { return flag(&d.AutoLogin, "auto_login") }),
	sectionKey("defaults", "editor", globalFile, func(c *Config) *Defaults { return &c.Defaults }, func(d *Defaults) slot {
		return text(&d.Editor, func(v string) error {
			if strings.ContainsAny(v, "\n\r") {
				return fmt.Errorf("editor must be one line")
			}
			return nil
		})
	}),

	envKey("url", anyFile, func(e *Env) slot { return text(&e.URL, nil) }),
	envKey("graphql", anyFile, func(e *Env) slot { return text(&e.GraphQL, nil) }),
	envKey("endpoints", anyFile, func(e *Env) slot { return listSlot{&e.Endpoints} }),
	envKey("endpoint_prefix", anyFile, func(e *Env) slot { return text(&e.EndpointPrefix, ValidateEndpointPrefix) }),
	envKey("openapi", anyFile, func(e *Env) slot { return text(&e.OpenAPI, ValidateOpenAPIPath) }),
	envKey("subscriptions", anyFile, func(e *Env) slot { return text(&e.Subscriptions, ValidateSubscriptionsPath) }),
	envKey("ws_protocol", anyFile, func(e *Env) slot { return text(&e.WSProtocol, ValidateWSProtocol) }),
	envKey("ws_token_in_init", anyFile, func(e *Env) slot { return flag(&e.WSTokenInInit, "ws_token_in_init") }),
	// token_env names which of YOUR variables is sent to the env's url: a project
	// file must never choose it.
	envKey("token_env", globalFile, func(e *Env) slot { return text(&e.TokenEnv, ValidateTokenEnv) }),
	{
		pattern: "env.<name>.headers.<Header>", scope: anyFile,
		table: func(a []string) string { return "env." + a[0] + ".headers" },
		name:  func(a []string) string { return a[1] },
		bind: func(c *Config, a []string, _ string) (slot, func()) {
			e := c.Env[a[0]]
			return entrySlot{&e.Headers, a[1], httpc.CheckHeader}, func() { storeEnv(c, a[0], e) }
		},
		inline: func(old Config, a []string) *inlineDrop {
			sub := "env." + a[0] + ".headers"
			d := &inlineDrop{table: "env." + a[0], key: "headers"}
			for h, v := range old.Env[a[0]].Headers {
				d.readd = append(d.readd, edit{sub, h, tomlString(v)})
			}
			return d
		},
	},

	sectionKey("vars", "base_url", anyFile, func(c *Config) *Vars { return &c.Vars }, func(v *Vars) slot { return text(&v.BaseURL, ValidateVarName) }),
	sectionKey("vars", "graphql_url", anyFile, func(c *Config) *Vars { return &c.Vars }, func(v *Vars) slot { return text(&v.GraphQLURL, ValidateVarName) }),
	sectionKey("vars", "env_suffix", anyFile, func(c *Config) *Vars { return &c.Vars }, func(v *Vars) slot { return text(&v.EnvSuffix, ValidateEnvSuffix) }),

	loginKey("path", func(l *Login) slot { return text(&l.Path, nil) }),
	loginKey("gql", func(l *Login) slot { return text(&l.GQL, nil) }),
	loginKey("endpoint", func(l *Login) slot { return text(&l.Endpoint, nil) }),
	loginKey("token_path", func(l *Login) slot {
		return text(&l.TokenPath, func(v string) error {
			if !strings.HasPrefix(v, ".") {
				return fmt.Errorf("token_path must be a jq path like .data.token")
			}
			return nil
		})
	}),
	{
		pattern: "env.<name>.login.vars.<name>", rest: true, scope: anyFile, unreadable: true,
		table: func(a []string) string { return "env." + a[0] + ".login.vars" },
		name:  func(a []string) string { return a[1] },
		bind: bindLogin(func(l *Login, a []string, full string) slot {
			return entrySlot{&l.Vars, a[1], func(name, value string) error {
				// a literal password has no place in a config file
				if IsSecretKey(name) && !secretref.IsRef(value) {
					return fmt.Errorf("%s looks like a secret: store it with `api secret set NAME` and set the value to @keychain:NAME (or @env:NAME / @prompt)", full)
				}
				return nil
			}}
		}),
		inline: func(old Config, a []string) *inlineDrop {
			sub := "env." + a[0] + ".login.vars"
			d := &inlineDrop{table: "env." + a[0] + ".login", key: "vars"}
			if ol := old.Env[a[0]].Login; ol != nil {
				for k, v := range ol.Vars {
					d.readd = append(d.readd, edit{sub, k, tomlString(v)})
				}
			}
			return d
		},
	},

	{
		pattern: "alias.<name>", scope: anyFile,
		table: fixed("alias"),
		name:  func(a []string) string { return a[0] },
		bind: func(c *Config, a []string, _ string) (slot, func()) {
			return entrySlot{&c.Alias, a[0], nil}, func() {}
		},
	},
}

// SettableKeys are the keys Set understands, for help text and completion.
var SettableKeys = func() []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.pattern
	}
	return out
}()

// lookupKey finds the key a typed name refers to and what its <...> matched.
func lookupKey(typed string) (k *key, args []string, ok bool) {
	parts := strings.Split(typed, ".")
	for i := range keys {
		if a, match := matchPattern(strings.Split(keys[i].pattern, "."), parts, keys[i].rest); match {
			return &keys[i], a, true
		}
	}
	return nil, nil, false
}

func matchPattern(pattern, parts []string, rest bool) ([]string, bool) {
	var args []string
	for i, p := range pattern {
		switch {
		case i >= len(parts):
			return nil, false
		case !strings.HasPrefix(p, "<"):
			if p != parts[i] {
				return nil, false
			}
		case rest && i == len(pattern)-1:
			tail := strings.Join(parts[i:], ".")
			return append(args, tail), tail != ""
		default:
			args = append(args, parts[i])
		}
	}
	return args, len(parts) == len(pattern)
}

// Set applies key=value to c. Secret-looking keys are refused.
func Set(c *Config, typed, value string) error {
	k, args, found := lookupKey(typed)
	inLogin := isLoginKey(typed)
	if !inLogin && IsSecretKey(typed) {
		return fmt.Errorf("refusing to store %q: config never holds secrets (use token_env to name a variable)", typed)
	}
	if !found {
		return unknownKey(typed)
	}
	s, commit := k.bind(c, args, typed)
	if err := s.assign(value); err != nil {
		return err
	}
	commit()
	return nil
}

// Get returns the value of a key as text ("" and false when unset).
func Get(c Config, typed string) (string, bool) {
	k, args, found := lookupKey(typed)
	if !found || k.unreadable {
		return "", false
	}
	s, _ := k.bind(&c, args, typed)
	return s.text()
}

// planFor maps a settable key to the edits that realise it in a file.
func planFor(typed string, old, want Config) (edits []edit, drop *inlineDrop, err error) {
	k, args, found := lookupKey(typed)
	if !found {
		return nil, nil, fmt.Errorf("no in-place edit for %q", typed)
	}
	s, _ := k.bind(&want, args, typed)
	if k.inline != nil {
		drop = k.inline(old, args)
	}
	return []edit{{k.table(args), k.name(args), s.literal()}}, drop, nil
}

// isLoginKey is true for env.<name>.login.<field...>.
func isLoginKey(typed string) bool {
	parts := strings.Split(typed, ".")
	return len(parts) >= 4 && parts[0] == "env" && parts[2] == "login"
}

// unknownKey says a key does not exist, listing what would, by the key's shape.
func unknownKey(typed string) error {
	parts := strings.Split(typed, ".")
	switch {
	case isLoginKey(typed):
		return fmt.Errorf("unknown key %q (known: login.path, login.gql, login.endpoint, login.token_path, login.vars.<name>)", typed)
	case parts[0] == "vars" && len(parts) == 2:
		return fmt.Errorf("unknown key %q (known: vars.base_url, vars.graphql_url, vars.env_suffix)", typed)
	case parts[0] == "defaults" && len(parts) == 2:
		return fmt.Errorf("unknown key defaults.%s", parts[1])
	case parts[0] == "env" && len(parts) >= 3:
		return fmt.Errorf("unknown key %q", typed)
	}
	return fmt.Errorf("unknown key %q (known: %s)", typed, strings.Join(SettableKeys, ", "))
}
