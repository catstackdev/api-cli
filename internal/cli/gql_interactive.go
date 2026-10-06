package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"cybercat/api/internal/schema"
	"cybercat/api/internal/secret"
	"cybercat/api/internal/session"
)

const variablesMarker = "### variables"

// lastEndpointFile remembers the endpoint you picked last (a name, nothing secret).
func (a *app) lastEndpointFile() string { return filepath.Join(a.stateDir(), "last-endpoint") }

func (a *app) lastEndpoint() string {
	data, err := os.ReadFile(a.lastEndpointFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (a *app) rememberEndpoint(name string) {
	if dir := a.stateDir(); dir != "" && schemaLabelOK(name) {
		_ = os.MkdirAll(dir, 0o700)
		_ = os.WriteFile(a.lastEndpointFile(), []byte(name+"\n"), 0o600)
	}
}

func schemaLabelOK(name string) bool { _, ok := schemaLabel(name); return ok }

// editTemp opens text in your editor in a private temp file. changed is false when
// the file was not saved (the mtime is set to the past first, so any save moves
// it): quitting without saving is how you cancel. A non-zero editor exit (vim's
// :cq) is an error.
func (a *app) editTemp(name, text string) (edited string, changed bool, err error) {
	return a.editTempExt(name, ".graphql", text)
}

// editTempExt is editTemp with the file extension of your choice (editors pick
// their syntax highlighting from it).
func (a *app) editTempExt(name, ext, text string) (edited string, changed bool, err error) {
	argv, err := a.editorCommand()
	if err != nil {
		return "", false, err
	}
	dir, err := os.MkdirTemp("", "api-edit-*")
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, strings.NewReplacer("/", "-", " ", "-").Replace(name)+ext)
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		return "", false, err
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(file, past, past); err != nil {
		return "", false, err
	}
	cmd := exec.Command(argv[0], append(argv[1:], file)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", false, fmt.Errorf("editor exited with an error: %w", err)
	}
	st, err := os.Stat(file)
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", false, err
	}
	return string(data), !st.ModTime().Equal(past), nil
}

// splitEditorDoc separates the query from the variables block.
func splitEditorDoc(doc string) (query, vars string) {
	lines := strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == variablesMarker {
			return strings.Join(lines[:i], "\n"), strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
		}
	}
	return doc, ""
}

// stripCommentLines drops whole-line # comments (the header this file adds, and
// any you write): collapsed to one line, a comment would swallow the query.
func stripCommentLines(q string) string {
	var keep []string
	for _, l := range strings.Split(q, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

// hasSecretKey reports whether a JSON value has a secret-looking key anywhere.
func hasSecretKey(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if secret.Name(k) || hasSecretKey(val) {
				return true
			}
		}
	case []any:
		for _, val := range x {
			if hasSecretKey(val) {
				return true
			}
		}
	}
	return false
}

// confirm asks a yes/no question on the terminal; the default is no.
func (a *app) confirm(question string) bool {
	fmt.Fprintf(a.io.Err, "%s [y/N] ", question)
	line, _ := bufio.NewReader(a.io.In).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// gqlInteractive is `api gql` with no query on a terminal (or --pick): choose the
// endpoint, choose a query or mutation (fzf, with a preview), edit the generated
// skeleton in your editor, run it. Mutations ask for confirmation first.
func (a *app) gqlInteractive(ctx context.Context, o *reqOpts, endpoint, filter string, yes bool) error {
	if endpoint == "" {
		eps := a.schemaProfile().Endpoints
		if len(eps) == 0 {
			return &ExitError{Code: exitUsage, Msg: "✗ name an endpoint (api gql -e <endpoint>), or list them: api config set env.<name>.endpoints user,auth --global"}
		}
		// the one you used last comes first
		if last := a.lastEndpoint(); last != "" {
			ordered := []string{}
			for _, e := range eps {
				if e == last {
					ordered = append([]string{e}, ordered...)
				} else {
					ordered = append(ordered, e)
				}
			}
			eps = ordered
		}
		picked, _, cancelled, err := a.pickWithFzf(eps, "--prompt=endpoint> ", "--height=40%", "--reverse")
		if err != nil {
			return err
		}
		if cancelled {
			return &ExitError{Code: exitGeneric}
		}
		endpoint = picked
		defer func() { a.rememberEndpoint(picked) }()
	}
	path := schemaEndpoint(a.effectivePrefix(""), endpoint)

	spec, resp, err := a.introspect(ctx, o, path, schema.SchemaQuery, "{}")
	if err != nil {
		return err
	}
	if o.dry {
		fmt.Fprintln(a.io.Out, a.curlLine(spec, nil))
		return nil
	}
	raw, err := dataObject(resp, "__schema")
	if err != nil {
		return err
	}
	s, err := schema.Parse(raw)
	if err != nil {
		return &ExitError{Code: exitGraphQL, Msg: "✗ introspection failed for " + endpoint + ": " + err.Error()}
	}

	dir, err := os.MkdirTemp("", "api-gql-pick-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	label := endpoint
	if l, ok := schemaLabel(endpoint); ok {
		label = l
	}
	var lines []string
	for _, l := range s.Index(label) {
		if p := strings.SplitN(l, " ", 3); len(p) == 3 && (p[1] == "query" || p[1] == "mutation") {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return &ExitError{Code: exitGeneric, Msg: "✗ " + endpoint + " has no queries or mutations"}
	}
	schema.SortIndex(lines)
	preview := fmt.Sprintf("%s schema show --file %s {2} {3}", session.Quote(exe), session.Quote(file))
	line, _, cancelled, err := a.pickWithFzf(lines,
		"--with-nth=2..", "--prompt="+label+"> ", "--preview="+preview, "--preview-window=right,60%,wrap")
	if err != nil {
		return err
	}
	if cancelled {
		return &ExitError{Code: exitGeneric}
	}
	parts := strings.SplitN(line, " ", 3)
	kind, field := parts[1], parts[2]

	query, vars, err := s.Skeleton(kind, field)
	if err != nil {
		return err
	}
	header := "# " + label + ": " + kind + " " + field + " — save to run, quit without saving to cancel\n" +
		"# the JSON below the marker line are the variables; delete it if there are none\n"
	edited, changed, err := a.editTemp(label+"-"+field, header+query+"\n"+variablesMarker+"\n"+vars+"\n")
	if err != nil {
		return err
	}
	if !changed || strings.TrimSpace(edited) == "" {
		a.say("  (cancelled: nothing saved)")
		return &ExitError{Code: exitGeneric}
	}
	q, v := splitEditorDoc(edited)
	q = stripCommentLines(q)
	if strings.TrimSpace(strings.ReplaceAll(q, "\n", "")) == "" || !strings.Contains(q, "{") {
		return &ExitError{Code: exitUsage, Msg: "✗ no query in the file"}
	}
	varsJSON := "{}"
	var parsed any
	if v != "" {
		if err := json.Unmarshal([]byte(v), &parsed); err != nil {
			return &ExitError{Code: exitUsage, Msg: "✗ the variables are not valid JSON: " + err.Error()}
		}
		varsJSON = v
	}

	// what runs decides whether to ask, not what you picked: you may have edited it
	if !isReadOnlyQuery(q) && !yes {
		if !a.confirm(fmt.Sprintf("✱ %s %s changes data on %s: run it?", kind, field, orUnset(a.session().BaseURL))) {
			a.say("  (not run)")
			return &ExitError{Code: exitGeneric}
		}
	}

	body, err := gqlBody(q, compactJSON(varsJSON))
	if err != nil {
		return err
	}
	spec, err = a.newSpec("POST", gqlTarget(a.session().GraphQLURL, path), o)
	if err != nil {
		return err
	}
	setJSONBody(&spec, body)

	// the one-liner, so a good query can become an alias. Variables are left out
	// when any looks like a secret: this line ends up in scrollback.
	oneline := strings.Join(strings.Fields(strings.NewReplacer("\n", " ").Replace(q)), " ")
	cmdline := fmt.Sprintf("api gql -e %s %s", shellWord(path), shellWord(oneline))
	if parsed != nil && varsJSON != "{}" {
		if hasSecretKey(parsed) {
			cmdline += " -V <variables left out: a name looks like a secret>"
		} else {
			cmdline += " " + shellWord(compactJSON(varsJSON))
		}
	}
	a.say("→ " + cmdline)

	return a.dispatch(ctx, spec, o, orDot(filter), "", nil)
}

func orDot(s string) string {
	if s == "" {
		return "."
	}
	return s
}

func compactJSON(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}
