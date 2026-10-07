package cli

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// The shell wrappers that let the commands that print shell code change the
// caller's shell (gotchas 1, 2 and 12). They live next to the Go code so the
// binary is all a user installs.
//
//go:embed shim.zsh
var shimZsh string

//go:embed shim.bash
var shimBash string

// annotationShellEmit marks a command whose stdout under --emit is shell code
// the wrapper evals. The wrapper's command list is generated from it, so a
// command is eval'd because it was marked, never because it has a flag of that
// name. A new one must also raise shimContract (see doctor.go).
const annotationShellEmit = "shell-emit"

// markShellEmit flags cmd (which has an --emit flag) as eval'd by the wrapper.
func markShellEmit(cmd *cobra.Command) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[annotationShellEmit] = "true"
}

// neverMatches fills a `case` pattern list that would otherwise be empty.
const neverMatches = "'--api-no-such-flag'"

// emittingPaths lists every marked command as "word" or "word word", with the
// aliases of each word, sorted. Each entry is single-quoted for the shell.
func emittingPaths(root *cobra.Command) []string {
	var out []string
	var walk func(c *cobra.Command, prefixes []string)
	walk = func(c *cobra.Command, prefixes []string) {
		for _, k := range c.Commands() {
			names := append([]string{k.Name()}, k.Aliases...)
			var next []string
			for _, p := range prefixes {
				for _, n := range names {
					next = append(next, strings.TrimSpace(p+" "+n))
				}
			}
			if k.Annotations[annotationShellEmit] != "" {
				out = append(out, next...)
			}
			walk(k, next)
		}
	}
	walk(root, []string{""})
	sort.Strings(out)
	return out
}

// globalFlags splits the root's persistent flags into the ones that take a
// value (`--env x`, `--env=x`) and the bare ones (`--no-pager`, `-M`), as
// `case` patterns, so the wrapper can find the subcommand behind them.
func globalFlags(root *cobra.Command) (value, bare []string) {
	root.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		if f.NoOptDefVal != "" { // a bool: no value follows
			bare = append(bare, "--"+f.Name)
			if f.Shorthand != "" {
				bare = append(bare, "-"+f.Shorthand)
			}
			return
		}
		value = append(value, "--"+f.Name)
		bare = append(bare, "--"+f.Name+"=*")
		if f.Shorthand != "" {
			value = append(value, "-"+f.Shorthand)
		}
	})
	sort.Strings(value)
	sort.Strings(bare)
	return value, bare
}

func casePattern(words []string) string {
	if len(words) == 0 {
		return neverMatches
	}
	return strings.Join(words, " | ")
}

func quoteWords(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = "'" + w + "'"
	}
	return strings.Join(q, " ")
}

// shimText renders the wrapper for shell ("zsh" or "bash") from the command
// tree, with the contract number and the completion script stamped in.
func shimText(root *cobra.Command, shell string) (string, error) {
	var tmpl string
	var comp bytes.Buffer
	var err error
	switch shell {
	case "zsh":
		tmpl, err = shimZsh, root.GenZshCompletion(&comp)
	case "bash":
		tmpl, err = shimBash, root.GenBashCompletionV2(&comp, true)
	default:
		return "", fmt.Errorf("unsupported shell %q (zsh or bash)", shell)
	}
	if err != nil {
		return "", err
	}
	value, bare := globalFlags(root)
	// The completion goes in last: its text must not be scanned for placeholders.
	out := strings.NewReplacer(
		"@CONTRACT@", shimContract,
		"@VALUE_FLAGS@", casePattern(value),
		"@BARE_FLAGS@", casePattern(bare),
		"@EMITTING@", quoteWords(emittingPaths(root)),
	).Replace(tmpl)
	return strings.Replace(out, "@COMPLETION@", strings.TrimRight(comp.String(), "\n"), 1), nil
}

func (a *app) shellInitCmd() *cobra.Command {
	var install bool
	cmd := &cobra.Command{
		Use:   "shell-init [zsh|bash]",
		Short: "Print the shell integration (login, env load/use/clear, completion)",
		Long: `Print the wrapper that lets "api login" and "api env load|use|clear" change
your current shell, plus tab completion. Add to ~/.zshrc (zsh is the default):

  eval "$(command api shell-init)"

or to ~/.bashrc (bash 3.2+; completion needs bash 4+ and bash-completion):

  eval "$(command api shell-init bash)"

--install appends that line to the rc file of the shell in $SHELL (zsh or bash;
any other shell is refused) once. The binary works without the
wrapper for requests, but the commands that set session variables only print
code, and the wrapper evals it.`,
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"zsh", "bash"},
		RunE: func(cmd *cobra.Command, args []string) error {
			shell := "zsh"
			switch {
			case len(args) == 1:
				shell = args[0]
			case install: // writing a file: use YOUR shell, never a guess
				var err error
				if shell, err = a.loginShell(); err != nil {
					return err
				}
			}
			text, err := shimText(cmd.Root(), shell)
			if err != nil {
				return err
			}
			if install {
				return a.installShellInit(shell)
			}
			_, err = fmt.Fprint(a.io.Out, text)
			return err
		},
	}
	cmd.Flags().BoolVar(&install, "install", false, "append the eval line to your shell's rc file (once)")
	return cmd
}

// loginShell names the shell from $SHELL, for --install without an argument.
func (a *app) loginShell() (string, error) {
	sh := a.getenv("SHELL")
	switch name := filepath.Base(sh); name {
	case "zsh", "bash":
		return name, nil
	case ".":
		return "", fmt.Errorf("cannot tell your shell ($SHELL is empty): pass zsh or bash")
	default:
		return "", fmt.Errorf("%s is not supported (zsh and bash only): nothing was written", name)
	}
}

// installShellInit appends the eval line to the shell's rc file unless an
// `api shell-init` line is already there. It never rewrites the file.
func (a *app) installShellInit(shell string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	rc, line := filepath.Join(home, ".bashrc"), `eval "$(command api shell-init bash)"`
	if shell == "zsh" {
		dir := orDefault(a.getenv("ZDOTDIR"), home)
		rc, line = filepath.Join(dir, ".zshrc"), `eval "$(command api shell-init)"`
	}
	if b, err := os.ReadFile(rc); err == nil && strings.Contains(string(b), "api shell-init") {
		a.say("✓ " + rc + " already loads api shell-init")
		return nil
	}
	f, err := os.OpenFile(rc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "\n# api shell integration\n%s\n", line); err != nil {
		return err
	}
	a.say("✓ added to " + rc + ": open a new shell, or run `exec " + shell + "`")
	return nil
}
