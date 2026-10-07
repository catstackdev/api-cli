package cli

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cybercat/api/internal/secretstore"

	"github.com/spf13/cobra"
)

// rootForTest returns the command tree the binary runs with.
func rootForTest(t *testing.T) *cobra.Command {
	t.Helper()
	a := &app{io: IO{Out: io.Discard, Err: io.Discard}, store: secretstore.NewMemory()}
	return a.rootCmd()
}

// The commands the wrapper evals are a security boundary (their stdout is run
// as shell code), and shells that loaded the old wrapper keep the old list.
// Changing the list therefore means raising shimContract: this test pins both.
func TestEmittingCommandsArePinnedToTheContract(t *testing.T) {
	root := rootForTest(t)
	want := []string{"env clear", "env load", "env use", "login", "logout"}
	if got := emittingPaths(root); !reflect.DeepEqual(got, want) {
		t.Fatalf("shell-emitting commands changed: %v\nwant %v\nraise shimContract (doctor.go) together with this list", got, want)
	}
	if shimContract != "1" {
		t.Fatalf("shimContract is %q: update the pin in this test with the list above", shimContract)
	}
}

// Being eval'd is decided by the annotation, never by a flag called --emit.
func TestEveryEmitFlagIsAnnotatedAndViceVersa(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		hasFlag := c.Flags().Lookup("emit") != nil
		marked := c.Annotations[annotationShellEmit] != ""
		if hasFlag != marked {
			t.Errorf("%q: --emit flag %v, shell-emit annotation %v: they must go together", c.CommandPath(), hasFlag, marked)
		}
		for _, k := range c.Commands() {
			walk(k)
		}
	}
	for _, k := range rootForTest(t).Commands() {
		walk(k)
	}
}

func TestGlobalFlagsComeFromTheRoot(t *testing.T) {
	value, bare := globalFlags(rootForTest(t))
	for _, w := range []string{"--env", "--base", "--token", "--color"} {
		if !contains(value, w) || !contains(bare, w+"=*") {
			t.Errorf("%s must be a value flag: %v / %v", w, value, bare)
		}
	}
	for _, w := range []string{"--no-pager", "--debug", "-C", "-M", "--color-output", "--monochrome-output"} {
		if !contains(bare, w) {
			t.Errorf("%s must be a bare flag: %v", w, bare)
		}
	}
	if casePattern(nil) != neverMatches {
		t.Error("an empty list must still be a valid case pattern")
	}
}

func TestShellInitOutput(t *testing.T) {
	for _, shell := range []string{"zsh", "bash"} {
		r := run(t, "", "shell-init", shell)
		if r.code != 0 {
			t.Fatalf("%s: %+v", shell, r)
		}
		for _, ph := range []string{"@CONTRACT@", "@VALUE_FLAGS@", "@BARE_FLAGS@", "@EMITTING@", "@COMPLETION@"} {
			if strings.Contains(r.out, ph) {
				t.Errorf("%s: placeholder %s left in the output", shell, ph)
			}
		}
		if !strings.Contains(r.out, "API_SHIM="+shimContract) || !strings.Contains(r.out, "'env load'") {
			t.Errorf("%s: contract or command list not stamped", shell)
		}
		if n := strings.Count(r.out, "\nAPI_COMPLETION_EOF\n"); n != 1 {
			t.Errorf("%s: the completion heredoc must end exactly once, found %d", shell, n)
		}
	}
	if r := run(t, "", "shell-init"); !strings.Contains(r.out, "_api::bin") {
		t.Errorf("zsh is the default: %+v", r)
	}
	if r := run(t, "", "shell-init", "fish"); r.code == 0 {
		t.Errorf("unsupported shell must fail: %+v", r)
	}
}

// stub is a fake `api` binary: it prints X=1 when asked to --emit (what the
// wrapper evals) and otherwise echoes its arguments.
const stubAPI = `#!/bin/sh
case " $* " in
*" --emit "*) echo "X=1" ;;
*) echo "plain: $*" ;;
esac
`

func TestWrapperBehaviour(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api"), []byte(stubAPI), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args    string
		changes bool // the wrapper eval'd the output (X=1 reached this shell)
	}{
		{"login", true},
		{"login /auth '{}' .token", true},
		{"--env x login", true},
		{"--env=x login", true},
		{"-M --no-pager --token t logout", true},
		{"env load", true},
		{"env load staging", true},
		{"env use dev", true},
		{"-M env use dev", true},
		{"env clear", true},
		{"env status", false}, // `env` alone must never match 'env load'
		{"env", false},
		{"", false},
		{"login --help", false},
		{"env use --help", false},
		{"get /login", false},
		{"get /x .data", false},
		{"gql login", false},
	}
	shells := []struct{ name, bin, prelude, flag string }{
		{"zsh", "zsh", "setopt no_unset", ""},
		{"bash", "bash", "set -u", ""},
	}
	if _, err := os.Stat("/bin/bash"); err == nil { // macOS ships bash 3.2
		shells = append(shells, struct{ name, bin, prelude, flag string }{"bash", "/bin/bash", "set -u", ""})
	}
	for _, sh := range shells {
		if _, err := exec.LookPath(sh.bin); err != nil {
			t.Logf("%s not installed: skipped", sh.bin)
			continue
		}
		text, err := shimText(rootForTest(t), sh.name)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "shim."+sh.name)
		if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, c := range cases {
			script := sh.prelude + "\nexport PATH='" + dir + "':\"$PATH\"\nsource '" + file + "'\n" +
				"api " + c.args + " >/dev/null 2>&1; echo \"X=${X-unset}\""
			cmd := exec.Command(sh.bin, "-c", script)
			cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("%s %q: %v\n%s", sh.bin, c.args, err, out)
				continue
			}
			got := strings.TrimSpace(string(out))
			want := "X=unset"
			if c.changes {
				want = "X=1"
			}
			if got != want {
				t.Errorf("%s: api %s: got %q, want %q", sh.bin, c.args, got, want)
			}
		}
	}
}

func TestShellInitInstallAppendsOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ZDOTDIR", "")
	t.Setenv("SHELL", "/bin/zsh")
	rc := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(rc, []byte("export A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home) // run() keeps a HOME that is already a temp dir
	for i := 0; i < 2; i++ {
		if r := run(t, "", "shell-init", "--install"); r.code != 0 {
			t.Fatalf("%+v", r)
		}
	}
	b, _ := os.ReadFile(rc)
	if strings.Count(string(b), "api shell-init") != 1 || !strings.HasPrefix(string(b), "export A=1\n") {
		t.Fatalf("must append once and keep the file:\n%s", b)
	}
}

func TestShellInstallFollowsTheLoginShell(t *testing.T) {
	cases := []struct{ shell, file, want string }{
		{"/bin/zsh", ".zshrc", "api shell-init)"},
		{"/usr/local/bin/bash", ".bashrc", "api shell-init bash)"},
	}
	for _, c := range cases {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ZDOTDIR", "")
		t.Setenv("SHELL", c.shell)
		if r := run(t, "", "shell-init", "--install"); r.code != 0 {
			t.Fatalf("%s: %+v", c.shell, r)
		}
		if b, _ := os.ReadFile(filepath.Join(home, c.file)); !strings.Contains(string(b), c.want) {
			t.Errorf("%s: %s lacks %q:\n%s", c.shell, c.file, c.want, b)
		}
	}
	for _, sh := range []string{"/usr/bin/fish", ""} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("SHELL", sh)
		if r := run(t, "", "shell-init", "--install"); r.code == 0 {
			t.Errorf("SHELL=%q must be refused: %+v", sh, r)
		}
		if ents, _ := os.ReadDir(home); len(ents) != 0 {
			t.Errorf("SHELL=%q: nothing may be written, found %v", sh, ents)
		}
	}
}
