package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"cybercat/api/internal/config"
	"cybercat/api/internal/schema"
	"cybercat/api/internal/session"

	"github.com/spf13/cobra"
)

// schemaProfile is the env whose `endpoints` / `endpoint_prefix` apply to api
// schema. With no env selected (no --env, no session, no default_env) and exactly
// one configured env that lists endpoints, that one is used: they are only NAMES,
// the requests still go to the server the shell points at.
func (a *app) schemaProfile() config.Env {
	_, prof := a.resolve()
	if len(prof.Endpoints) > 0 || prof.EndpointPrefix != "" || a.envName != "" || a.session().Name != "" {
		return prof
	}
	var only *config.Env
	for _, n := range a.cfg.EnvNames() {
		e := a.cfg.Env[n]
		if len(e.Endpoints) == 0 {
			continue
		}
		if only != nil {
			return prof // ambiguous: do not guess
		}
		only = &e
	}
	if only != nil {
		return *only
	}
	return prof
}

// editorCommand is what api schema opens items in: your global config, then
// $API_EDITOR, $VISUAL, $EDITOR, then nvim / vim / vi. The config value is global
// only: it is a program that gets run.
func (a *app) editorCommand() ([]string, error) {
	for _, v := range []string{a.cfg.Defaults.Editor, a.getenv("API_EDITOR"), a.getenv("VISUAL"), a.getenv("EDITOR")} {
		if w := splitWords(v); len(w) > 0 {
			return w, nil
		}
	}
	for _, name := range []string{"nvim", "vim", "vi"} {
		if p, err := exec.LookPath(name); err == nil {
			return []string{p}, nil
		}
	}
	return nil, fmt.Errorf("no editor found: api config set defaults.editor nvim --global (or set $EDITOR)")
}

// openInEditor writes text to a private temp file named like the item and opens it.
func (a *app) openInEditor(name, text string) error {
	argv, err := a.editorCommand()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "api-schema-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, strings.NewReplacer("/", "-", " ", "-").Replace(name)+".graphql")
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		return err
	}
	cmd := exec.Command(argv[0], append(argv[1:], file)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// pickWithFzf runs fzf over lines and returns the chosen line and the key that
// ended the selection ("" for Enter). cancelled is true when nothing was chosen.
func (a *app) pickWithFzf(lines []string, args ...string) (line, key string, cancelled bool, err error) {
	fzf, lerr := exec.LookPath("fzf")
	if lerr != nil {
		return "", "", false, fmt.Errorf("fzf is not installed (brew install fzf), or name what you want: api schema type <endpoint> <Type>")
	}
	cmd := exec.Command(fzf, args...)
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	cmd.Stderr = os.Stderr
	var out bytes.Buffer
	cmd.Stdout = &out
	if rerr := cmd.Run(); rerr != nil {
		if ee, ok := rerr.(*exec.ExitError); ok && (ee.ExitCode() == 1 || ee.ExitCode() == 130) {
			return "", "", true, nil
		}
		return "", "", false, rerr
	}
	parts := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	hasExpect := false
	for _, x := range args {
		if strings.HasPrefix(x, "--expect") {
			hasExpect = true
		}
	}
	if hasExpect && len(parts) >= 2 {
		return parts[1], parts[0], false, nil
	}
	if len(parts) == 0 || parts[0] == "" {
		return "", "", true, nil
	}
	return parts[0], "", false, nil
}

// schemaPick is `api schema [endpoint]` with no subcommand: choose an endpoint
// (when none is given), then browse its queries, mutations and types with a live
// preview. Enter prints the item, ctrl-e (or --edit) opens it in your editor.
func (a *app) schemaPick(ctx context.Context, o *reqOpts, endpoint, prefix string, edit bool) error {
	if endpoint == "" {
		eps := a.schemaProfile().Endpoints
		if len(eps) == 0 {
			return &ExitError{Code: exitUsage, Msg: "✗ name an endpoint (api schema <endpoint>), or list them: api config set env.<name>.endpoints user,auth --global"}
		}
		picked, _, cancelled, err := a.pickWithFzf(eps, "--prompt=endpoint> ", "--height=40%", "--reverse")
		if err != nil {
			return err
		}
		if cancelled {
			return &ExitError{Code: exitGeneric}
		}
		endpoint = picked
	}

	spec, resp, err := a.introspect(ctx, o, schemaEndpoint(a.effectivePrefix(prefix), endpoint), schema.SchemaQuery, "{}")
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

	dir, err := os.MkdirTemp("", "api-schema-pick-*")
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
	lines := s.Index(label)
	schema.SortIndex(lines)
	preview := fmt.Sprintf("%s schema show --file %s {2} {3}", session.Quote(exe), session.Quote(file))
	line, key, cancelled, err := a.pickWithFzf(lines,
		"--with-nth=2..", "--prompt="+label+"> ", "--expect=ctrl-e",
		"--preview="+preview, "--preview-window=right,60%,wrap",
		"--header=enter: show   ctrl-e: open in editor")
	if err != nil {
		return err
	}
	if cancelled {
		return &ExitError{Code: exitGeneric}
	}
	parts := strings.SplitN(line, " ", 3)
	if len(parts) != 3 {
		return fmt.Errorf("unexpected selection %q", line)
	}
	detail, err := s.Detail(parts[1], parts[2])
	if err != nil {
		return err
	}
	text := strings.Join(detail, "\n") + "\n"
	if edit || key == "ctrl-e" {
		return a.openInEditor(parts[0]+"-"+parts[2], text)
	}
	fmt.Fprint(a.io.Out, text)
	return nil
}

// schemaShowCmd prints the detail of one item from a saved schema file. It is the
// preview command the picker hands to fzf (hidden: not for typing).
func (a *app) schemaShowCmd() *cobra.Command {
	var file string
	var expand bool
	cmd := &cobra.Command{
		Use:    "show --file <schema.json> <kind> <name>",
		Short:  "Print the detail of one item of a saved schema (used by the picker preview)",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if !json.Valid(data) {
				return fmt.Errorf("%s is not JSON", file)
			}
			s, err := schema.Parse(data)
			if err != nil {
				return err
			}
			detail, err := s.Detail(args[0], args[1])
			if err != nil {
				return err
			}
			if expand {
				rel, err := s.Related(args[0], args[1])
				if err != nil {
					return err
				}
				detail = append(detail, rel...)
			}
			fmt.Fprintln(a.io.Out, strings.Join(detail, "\n"))
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "the saved schema (api schema dump > file)")
	cmd.Flags().BoolVar(&expand, "expand", false, "also show the types its fields point at, one level (what a television preview wants)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}
