package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cybercat/api/internal/envfile"
	"cybercat/api/internal/ui"

	"github.com/spf13/cobra"
)

// templateSuffixes are .env.<suffix> files that document variables rather than
// configure an environment.
var templateSuffixes = map[string]bool{"example": true, "sample": true, "template": true, "dist": true}

// readEnvSources parses .env and every real .env.<suffix> in the project root
// (the example/sample files themselves excluded), .env first.
func readEnvSources(root string) ([]envfile.Source, error) {
	matches, _ := filepath.Glob(filepath.Join(root, ".env*"))
	sort.Strings(matches)
	var sources []envfile.Source
	add := func(path string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sources = append(sources, envfile.Source{Name: filepath.Base(path), Entries: envfile.Parse(data)})
		return nil
	}
	if _, err := os.Stat(filepath.Join(root, ".env")); err == nil {
		if err := add(filepath.Join(root, ".env")); err != nil {
			return nil, err
		}
	}
	for _, m := range matches {
		base := filepath.Base(m)
		suffix, ok := strings.CutPrefix(base, ".env.")
		if !ok || templateSuffixes[suffix] {
			continue
		}
		if st, err := os.Stat(m); err != nil || st.IsDir() {
			continue
		}
		if err := add(m); err != nil {
			return nil, err
		}
	}
	return sources, nil
}

func (a *app) envExampleCmd() *cobra.Command {
	var write, keep, check, force bool
	cmd := &cobra.Command{
		Use:   "example",
		Short: "Generate .env.example from your .env files, or check them for drift",
		Long: `Reads the KEY NAMES of .env and .env.<suffix> in the project root and prints a
.env.example. Values are blank. --keep-values keeps harmless ones, but keys that
look secret (TOKEN, SECRET, PASSWORD, KEY, ...) and URLs with embedded
credentials are always blank. Nothing is written unless you pass --write.

--check compares every .env file with .env.example and lists, per file, the keys
it is missing and the keys the example does not document. It exits 1 on drift.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := findRoot()
			sources, err := readEnvSources(root)
			if err != nil {
				return err
			}
			if len(sources) == 0 {
				return &ExitError{Code: exitGeneric, Msg: "✗ no .env files in " + root}
			}
			examplePath := filepath.Join(root, ".env.example")

			if check {
				data, err := os.ReadFile(examplePath)
				if err != nil {
					return &ExitError{Code: exitGeneric, Msg: "✗ no .env.example to check against: run `api env example --write` first"}
				}
				ex := envfile.Source{Entries: envfile.Parse(data)}
				drift := envfile.Check(ex.Keys(), sources)
				if len(drift) == 0 {
					a.say(fmt.Sprintf("✓ %d file(s) match .env.example (%d keys)", len(sources), len(ex.Entries)))
					return nil
				}
				var rows [][]string
				for _, d := range drift {
					rows = append(rows, []string{d.File, orDash(strings.Join(d.Missing, " ")), orDash(strings.Join(d.Extra, " "))})
				}
				ui.Table(a.io.Out, []string{"FILE", "MISSING (in example)", "EXTRA (undocumented)"}, rows, a.colorOut())
				return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ %d file(s) drift from .env.example", len(drift))}
			}

			out := envfile.Example(sources, keep)
			if !write {
				fmt.Fprint(a.io.Out, out)
				return nil
			}
			if _, err := os.Stat(examplePath); err == nil && !force {
				if existing, _ := os.ReadFile(examplePath); string(existing) == out {
					a.say("✓ .env.example is up to date")
					return nil
				}
				return &ExitError{Code: exitGeneric, Msg: "✗ .env.example exists and differs: use --force to overwrite (or review with `api env example`)"}
			}
			if err := os.WriteFile(examplePath, []byte(out), 0o644); err != nil {
				return err
			}
			a.say("✓ " + examplePath)
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&write, "write", false, "write .env.example instead of printing it")
	f.BoolVar(&force, "force", false, "with --write: overwrite a different existing .env.example")
	f.BoolVar(&keep, "keep-values", false, "keep harmless values (secret-looking keys and credential URLs stay blank)")
	f.BoolVar(&check, "check", false, "report drift between .env.* files and .env.example (exit 1)")
	return cmd
}

func (a *app) envInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init <suffix>",
		Short: "Create .env.<suffix> from .env.example (mode 0600)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			suffix := args[0]
			if suffix == "" || strings.ContainsAny(suffix, "/\\ ") || templateSuffixes[suffix] {
				return fmt.Errorf("bad suffix %q", suffix)
			}
			root := findRoot()
			data, err := os.ReadFile(filepath.Join(root, ".env.example"))
			if err != nil {
				return &ExitError{Code: exitGeneric, Msg: "✗ no .env.example in " + root + ": run `api env example --write` first"}
			}
			target := filepath.Join(root, ".env."+suffix)
			if _, err := os.Stat(target); err == nil {
				return &ExitError{Code: exitGeneric, Msg: "✗ already exists: " + target}
			}
			// O_EXCL + 0600: it is going to hold secrets
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := f.Write(data); err != nil {
				return err
			}
			a.say("✓ " + target + "  (fill in the values)")
			return nil
		},
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
