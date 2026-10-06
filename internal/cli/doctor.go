package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cybercat/api/internal/history"
	"cybercat/api/internal/httpc"
	"cybercat/api/internal/secretstore"
	"cybercat/api/internal/session"
	"cybercat/api/internal/ui"

	"github.com/spf13/cobra"
)

// shimContract is the number of the shell-shim contract this binary expects:
// which commands the shim must eval (the `mutating` case in src/api.sh). The
// shim exports it as API_SHIM. Raise it, and the value in src/api.sh, together
// whenever a command that changes the parent shell is added.
const shimContract = "1"

// shimNewer reports whether a shim marker is a higher contract than ours. A
// marker that is not a number counts as older: it predates the numbering.
func shimNewer(marker string) bool {
	m, err := strconv.Atoi(marker)
	if err != nil {
		return false
	}
	cur, _ := strconv.Atoi(shimContract)
	return m > cur
}

type level int

const (
	lvlOK level = iota
	lvlWarn
	lvlFail
)

type check struct {
	name   string
	lvl    level
	detail string
}

func (l level) cell(color bool) string {
	switch l {
	case lvlFail:
		return ui.Paint("✗ fail", ui.Red, color)
	case lvlWarn:
		return ui.Paint("⚠ warn", ui.Yellow, color)
	}
	return ui.Paint("✓ ok", ui.Green, color)
}

func (a *app) doctorCmd() *cobra.Command {
	var online, fix, asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the setup: binary, shell shim, completion, config, session, tools, .env permissions",
		Long: `Runs a set of offline checks and prints one line each. Exit 1 if anything
fails; warnings alone exit 0. --online also tries the active base URL.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if fix {
				a.fixEnvPerms()
			}
			checks := a.doctorChecks()
			if online {
				checks = append(checks, a.checkOnline(cmd.Context()))
			}

			if asJSON {
				return a.doctorJSON(checks)
			}

			a.printChecks(checks)
			fails, warns := tally(checks)

			switch {
			case fails > 0:
				return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ %d problem(s), %d warning(s)", fails, warns)}
			case warns > 0:
				a.say(fmt.Sprintf("✓ no problems, %d warning(s)", warns))
			default:
				a.say("✓ all good")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&online, "online", false, "also GET the active base URL (5s limit)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the checks as JSON (same exit code)")
	cmd.Flags().BoolVar(&fix, "fix", false, "chmod 600 the .env files that other users can read")
	return cmd
}

// tally counts the failing and warning checks.
func tally(checks []check) (fails, warns int) {
	for _, c := range checks {
		switch c.lvl {
		case lvlFail:
			fails++
		case lvlWarn:
			warns++
		}
	}
	return fails, warns
}

// printChecks prints the table; the status column is padded on its PLAIN width
// so colour codes never misalign it.
func (a *app) printChecks(checks []check) {
	color := a.colorOut()
	widest := 0
	for _, c := range checks {
		if n := len([]rune(c.lvl.cell(false))); n > widest {
			widest = n
		}
	}
	rows := make([][]string, 0, len(checks))
	for _, c := range checks {
		pad := strings.Repeat(" ", widest-len([]rune(c.lvl.cell(false))))
		rows = append(rows, []string{c.name, c.lvl.cell(color) + pad, c.detail})
	}
	ui.Table(a.io.Out, []string{"CHECK", "STATUS", "DETAIL"}, rows, color)
}

func (l level) word() string {
	switch l {
	case lvlFail:
		return "fail"
	case lvlWarn:
		return "warn"
	}
	return "ok"
}

// doctorJSON prints the checks for scripts and CI. Details are the same text the
// table shows (tokens already masked); the exit code is unchanged.
func (a *app) doctorJSON(checks []check) error {
	type item struct {
		Name   string `json:"name"`
		Level  string `json:"level"`
		Detail string `json:"detail"`
	}
	out := struct {
		OK       bool   `json:"ok"`
		Failures int    `json:"failures"`
		Warnings int    `json:"warnings"`
		Checks   []item `json:"checks"`
	}{Checks: []item{}}
	for _, c := range checks {
		out.Checks = append(out.Checks, item{c.name, c.lvl.word(), c.detail})
	}
	out.Failures, out.Warnings = tally(checks)
	out.OK = out.Failures == 0
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(a.io.Out, string(b))
	if out.Failures > 0 {
		return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ %d problem(s), %d warning(s)", out.Failures, out.Warnings)}
	}
	return nil
}

func newCheck(name string, lvl level, format string, args ...any) check {
	return check{name, lvl, fmt.Sprintf(format, args...)}
}

// doctorChecks runs the offline checks, in the order they are shown.
func (a *app) doctorChecks() []check {
	exe, _ := os.Executable()
	out := []check{
		newCheck("binary", lvlOK, "%s: %s", VersionString(), exe),
		a.checkShim(),
	}
	for _, group := range [][]check{
		checkCompletion(exe),
		{a.checkConfig()},
		a.checkSession(),
		{a.checkCredentialStore()},
		a.checkSecrets(),
		a.checkTools(),
		checkHistory(),
		checkEnvFiles(),
		checkCurlrc(),
	} {
		out = append(out, group...)
	}
	return out
}

func (a *app) checkShim() check {
	switch marker := a.getenv("API_SHIM"); {
	case marker == "":
		return newCheck("shell shim", lvlWarn, "not detected: run `exec zsh`, or those commands cannot change your shell")
	case marker == shimContract:
		return newCheck("shell shim", lvlOK, "loaded: login, env load/use/clear can change your shell")
	case shimNewer(marker):
		return newCheck("shell shim", lvlWarn, "contract %s is newer than this binary (%s): rebuild or reinstall the binary (make install)", marker, shimContract)
	default:
		return newCheck("shell shim", lvlWarn, "contract %q is older than this binary (%s): its list of shell-changing commands may be stale, run `exec zsh`", marker, shimContract)
	}
}

// checkCompletion compares the zsh completion cache with the binary.
func checkCompletion(exe string) []check {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	cache := filepath.Join(home, ".cache", "api-completion.zsh")
	st, err := os.Stat(cache)
	if err != nil {
		return []check{newCheck("completion", lvlWarn, "no cache at %s: open a new interactive zsh", cache)}
	}
	if exeStat, err := os.Stat(exe); err == nil && exeStat.ModTime().After(st.ModTime()) {
		return []check{newCheck("completion", lvlWarn, "cache is older than the binary: it refreshes on the next new shell")}
	}
	return []check{newCheck("completion", lvlOK, "%s", cache)}
}

func (a *app) checkConfig() check {
	switch {
	case a.cfgErr != nil:
		return newCheck("config", lvlFail, "%v", a.cfgErr)
	case len(a.cfg.Warnings) > 0:
		return newCheck("config", lvlWarn, "%s", strings.Join(a.cfg.Warnings, "; "))
	}
	return newCheck("config", lvlOK, "global: %s | local: %s | %d env(s), %d alias(es)",
		orNone(a.cfg.GlobalPath), orNone(a.cfg.LocalPath), len(a.cfg.Env), len(a.cfg.Alias))
}

// checkSession reports the session, plus a transport warning for plain http.
func (a *app) checkSession() []check {
	s, _ := a.resolve()
	var out []check
	switch {
	case s.BaseURL == "":
		out = append(out, newCheck("session", lvlWarn, "no base URL: `api env load <suffix>` or `api env use <name>`"))
	case s.Token == "":
		out = append(out, newCheck("session", lvlWarn, "env %s, base %s, no token (api login ...)", orUnset(s.Name), s.BaseURL))
	default:
		exp := tokenExpiry(s.Token, time.Now())
		detail := fmt.Sprintf("env %s, base %s, token %s", orUnset(s.Name), s.BaseURL, session.MaskToken(s.Token))
		if exp != "" {
			detail += " (" + exp + ")"
		}
		if strings.HasPrefix(exp, "EXPIRED") {
			out = append(out, newCheck("session", lvlWarn, "%s: run api login", detail))
		} else {
			out = append(out, newCheck("session", lvlOK, "%s", detail))
		}
	}
	if s.BaseURL != "" && !strings.HasPrefix(s.BaseURL, "https://") && !isLocalURL(s.BaseURL) {
		out = append(out, newCheck("transport", lvlWarn, "%s is not https: tokens would travel in clear text", s.BaseURL))
	}
	return out
}

// checkCredentialStore: can `@keychain:NAME` work here?
func (a *app) checkCredentialStore() check {
	_, err := a.store.Get("doctor-probe")
	switch {
	case err == nil || errors.Is(err, secretstore.ErrNotFound):
		return newCheck("credentials", lvlOK, "credential store available (api secret set NAME, then @keychain:NAME)")
	case errors.Is(err, secretstore.ErrUnavailable):
		return newCheck("credentials", lvlWarn, "no credential store on this system: use @env:NAME or @prompt for passwords (Linux: install secret-tool from libsecret-tools)")
	}
	return newCheck("credentials", lvlWarn, "credential store error: %v", err)
}

// checkSecrets: unbound secrets can be sent to any server you point them at.
func (a *app) checkSecrets() []check {
	names := a.knownSecretNames()
	if len(names) == 0 {
		return nil
	}
	var unbound, missing []string
	for _, n := range names {
		sec, err := a.store.Get(n)
		switch {
		case errors.Is(err, secretstore.ErrNotFound):
			missing = append(missing, n)
		case err == nil && len(sec.Origins) == 0:
			unbound = append(unbound, n)
		}
	}
	switch {
	case len(unbound) > 0:
		return []check{newCheck("secrets", lvlWarn, "%d of %d not bound to a server: %s (api secret bind NAME <url>)", len(unbound), len(names), strings.Join(unbound, ", "))}
	case len(missing) > 0:
		return []check{newCheck("secrets", lvlWarn, "known (index or config) but not in the Keychain: %s (api secret set NAME, or list --prune)", strings.Join(missing, ", "))}
	}
	return []check{newCheck("secrets", lvlOK, "%d stored, all bound to a server", len(names))}
}

func (a *app) checkTools() []check {
	var out []check
	if p, err := exec.LookPath("curl"); err == nil {
		out = append(out, newCheck("curl", lvlOK, "%s (used by `-- <curl args>`)", p))
	} else {
		out = append(out, newCheck("curl", lvlWarn, "not found: only the `-- <curl args>` escape hatch needs it"))
	}
	if cmd := ui.PagerCmdWith(a.lookup); cmd == "" {
		out = append(out, newCheck("pager", lvlOK, "disabled (API_PAGER is empty)"))
	} else if first := strings.Fields(cmd); len(first) > 0 {
		if _, err := exec.LookPath(first[0]); err != nil {
			out = append(out, newCheck("pager", lvlWarn, "%q not found: output is printed directly", first[0]))
		} else {
			out = append(out, newCheck("pager", lvlOK, "%s", cmd))
		}
	}
	return out
}

func checkHistory() []check {
	hp := history.Path()
	if hp == "" {
		return nil
	}
	dir := filepath.Dir(hp)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return []check{newCheck("history", lvlWarn, "cannot create %s: %v (path completion will be empty)", dir, err)}
	}
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return []check{newCheck("history", lvlWarn, "%s is not writable: %v", dir, err)}
	}
	f.Close()
	_ = os.Remove(f.Name())
	return []check{newCheck("history", lvlOK, "%s", hp)}
}

// checkEnvFiles: .env permissions, and whether git would commit them.
func checkEnvFiles() []check {
	root := findRoot()
	sources, err := readEnvSources(root)
	if err != nil || len(sources) == 0 {
		return nil
	}
	var loose []string
	for _, src := range sources {
		// Lstat: a symlink's own mode says nothing and chmod would follow it
		// out of the project, so symlinks are not part of this check.
		if st, err := os.Lstat(filepath.Join(root, src.Name)); err == nil && st.Mode()&os.ModeSymlink == 0 && st.Mode().Perm()&0o077 != 0 {
			loose = append(loose, fmt.Sprintf("%s (%04o)", src.Name, st.Mode().Perm()))
		}
	}
	var out []check
	if len(loose) > 0 {
		out = append(out, newCheck(".env files", lvlWarn, "readable by others: %s: chmod 600", strings.Join(loose, ", ")))
	} else {
		out = append(out, newCheck(".env files", lvlOK, "%d file(s) in %s, owner-only", len(sources), root))
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil && !gitignoresEnv(root) {
		out = append(out, newCheck(".gitignore", lvlWarn, "does not ignore .env files: secrets could be committed"))
	}
	return out
}

func checkCurlrc() []check {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(home, ".curlrc")); err != nil {
		return nil
	}
	return []check{newCheck("~/.curlrc", lvlOK, "present; api does not read it (timeouts, redirects and summary are api's own: see README)")}
}

func (a *app) checkOnline(ctx context.Context) check {
	s, _ := a.resolve()
	if s.BaseURL == "" {
		return check{"online", lvlWarn, "no base URL to try"}
	}
	spec := httpc.Spec{Method: "GET", URL: s.BaseURL, Timeout: 5 * time.Second, ConnectTimeout: 5 * time.Second}
	start := time.Now()
	resp, err := httpc.Do(ctx, spec, 0)
	if err != nil {
		return check{"online", lvlFail, shortErr(err)}
	}
	return check{"online", lvlOK, fmt.Sprintf("%s answered HTTP %d in %s", s.BaseURL, resp.Status, time.Since(start).Round(time.Millisecond))}
}

func gitignoresEnv(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch strings.TrimSpace(line) {
		case ".env", ".env*", ".env.*", "*.env", "/.env", "/.env*", "**/.env", ".env.local":
			return true
		}
	}
	return false
}

func isLocalURL(u string) bool {
	for _, p := range []string{"http://localhost", "http://127.", "http://[::1]"} {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	return false
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// fixEnvPerms makes the project's .env files owner-only. Symlinks are skipped:
// chmod would follow them to a file outside the project.
func (a *app) fixEnvPerms() {
	root := findRoot()
	sources, err := readEnvSources(root)
	if err != nil {
		return
	}
	for _, src := range sources {
		p := filepath.Join(root, src.Name)
		st, err := os.Lstat(p)
		if err != nil || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0o077 == 0 {
			if err == nil && st.Mode()&os.ModeSymlink != 0 {
				a.say("⚠ " + src.Name + " is a symlink: not changed")
			}
			continue
		}
		if err := os.Chmod(p, 0o600); err != nil {
			a.say("⚠ could not chmod " + src.Name + ": " + err.Error())
			continue
		}
		a.say(fmt.Sprintf("✓ %s: %04o → 0600", src.Name, st.Mode().Perm()))
	}
}
