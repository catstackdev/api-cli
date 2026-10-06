package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTools puts a scripted fzf and editor first on PATH. fzf prints line
// $FAKE_LINE of its stdin (after the $FAKE_KEY line when called with --expect);
// FAKE_CANCEL=1 makes it exit like Esc. The editor records what it was given.
func fakeTools(t *testing.T) (record string) {
	t.Helper()
	bin := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("fzf", `#!/bin/sh
[ "$FAKE_CANCEL" = 1 ] && exit 130
expect=0
for a in "$@"; do case "$a" in --expect*) expect=1;; esac; done
in=$(mktemp); cat > "$in"
line=${FAKE_PLAIN_LINE:-1}
if [ "$expect" = 1 ]; then echo "$FAKE_KEY"; line=${FAKE_LINE:-1}; fi
sed -n "${line}p" "$in"
rm -f "$in"
`)
	write("fake-editor", `#!/bin/sh
for f; do :; done
cp "$f" "$EDITOR_RECORD"
basename "$f" > "$EDITOR_RECORD.name"
[ -n "$EDITOR_REPLACE" ] && cp "$EDITOR_REPLACE" "$f"
[ "$EDITOR_TOUCH" = 1 ] && touch "$f"
[ "$EDITOR_FAIL" = 1 ] && exit 1
exit 0
`)
	record = filepath.Join(t.TempDir(), "edited")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("EDITOR_RECORD", record)
	t.Setenv("EDITOR", filepath.Join(bin, "fake-editor"))
	t.Setenv("VISUAL", "")
	t.Setenv("API_EDITOR", "")
	t.Setenv("FAKE_KEY", "")
	t.Setenv("FAKE_CANCEL", "")
	for _, k := range []string{"FAKE_PLAIN_LINE", "EDITOR_REPLACE", "EDITOR_TOUCH", "EDITOR_FAIL"} {
		t.Setenv(k, "")
	}
	return record
}

const meDetail = "me: User\n\nreturns User\n  id: ID!\n  name: String\n"

func TestSchemaPickerPrintsTheChosenItem(t *testing.T) {
	schemaSetup(t)
	fakeTools(t)
	t.Setenv("FAKE_LINE", "4") // sorted index: enum, input, mutation, "query me"
	r := run(t, "", "schema", "user", "--prefix", "v2/")
	if r.code != 0 || r.out != meDetail {
		t.Fatalf("%+v", r)
	}
}

func TestSchemaPickerCtrlEOpensTheEditor(t *testing.T) {
	schemaSetup(t)
	record := fakeTools(t)
	t.Setenv("FAKE_LINE", "4")
	t.Setenv("FAKE_KEY", "ctrl-e")
	r := run(t, "", "schema", "user", "--prefix", "v2/")
	if r.code != 0 || r.out != "" {
		t.Fatalf("nothing should be printed when the editor opens: %+v", r)
	}
	got, _ := os.ReadFile(record)
	name, _ := os.ReadFile(record + ".name")
	if string(got) != meDetail || strings.TrimSpace(string(name)) != "user-me.graphql" {
		t.Fatalf("editor got %q in %q", got, name)
	}
	// --edit does the same without the key
	t.Setenv("FAKE_KEY", "")
	_ = os.Remove(record)
	if r := run(t, "", "schema", "user", "--prefix", "v2/", "--edit"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if got, _ := os.ReadFile(record); string(got) != meDetail {
		t.Fatalf("--edit: %q", got)
	}
}

func TestSchemaPickerChoosesTheEndpointFromConfigWhenNoneGiven(t *testing.T) {
	schemaSetup(t)
	fakeTools(t)
	srvURL := os.Getenv("API_BASE_URL")
	// no env selected in the shell; exactly one env lists endpoints
	writeConfig(t, "[env.dev]\nurl = \""+srvURL+"\"\nendpoints = [\"user\"]\nendpoint_prefix = \"v2/\"\n")
	t.Setenv("API_ENV", "")
	t.Setenv("API_BASE_URL", srvURL)
	t.Setenv("FAKE_LINE", "4")
	r := run(t, "", "schema")
	if r.code != 0 || r.out != meDetail {
		t.Fatalf("%+v", r)
	}
}

func TestSchemaPickerNeedsEndpointsOrAName(t *testing.T) {
	schemaSetup(t)
	fakeTools(t)
	if r := run(t, "", "schema"); r.code != exitUsage || !strings.Contains(r.err, "name an endpoint") {
		t.Fatalf("%+v", r)
	}
}

func TestSchemaPickerCancelAndMissingFzf(t *testing.T) {
	schemaSetup(t)
	fakeTools(t)
	t.Setenv("FAKE_CANCEL", "1")
	if r := run(t, "", "schema", "user", "--prefix", "v2/"); r.code != exitGeneric || r.out != "" {
		t.Fatalf("cancel: %+v", r)
	}
	t.Setenv("PATH", t.TempDir()) // no fzf anywhere
	if r := run(t, "", "schema", "user", "--prefix", "v2/"); r.code == 0 || !strings.Contains(r.err, "fzf is not installed") {
		t.Fatalf("no fzf: %+v", r)
	}
}

func TestEditorComesFromYourConfigNeverFromAProjectFile(t *testing.T) {
	schemaSetup(t)
	record := fakeTools(t)
	t.Setenv("FAKE_LINE", "4")
	bin := filepath.Dir(os.Getenv("EDITOR"))
	marker := filepath.Join(t.TempDir(), "project-editor-ran")
	trap := filepath.Join(bin, "trap-editor")
	if err := os.WriteFile(trap, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// a project file (maybe a cloned repo) names an editor: it must not be run
	projectWith(t, map[string]string{".api.toml": "[defaults]\neditor = \"" + trap + "\"\n"})
	r := run(t, "", "schema", "user", "--prefix", "v2/", "--edit")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the project config chose the program that was run")
	}
	if got, _ := os.ReadFile(record); string(got) != meDetail {
		t.Fatalf("the real editor should have run: %+v", r)
	}
	if !strings.Contains(r.err, "only honoured in the global config") {
		t.Fatalf("no warning: %q", r.err)
	}
	// and config set refuses a project file
	if r := run(t, "", "config", "set", "defaults.editor", "nvim", "--local"); r.code == 0 {
		t.Fatalf("%+v", r)
	}

	// your own global config is honoured, with arguments
	_ = os.Remove(record)
	_ = os.Chdir(t.TempDir())
	writeConfig(t, "[defaults]\neditor = \""+filepath.Join(bin, "fake-editor")+" --wait\"\n")
	t.Setenv("EDITOR", trap) // would trip the marker if it won
	if r := run(t, "", "schema", "user", "--prefix", "v2/", "--edit"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("$EDITOR beat defaults.editor")
	}
}

func TestSchemaShowPrintsFromASavedSchema(t *testing.T) {
	file := filepath.Join(t.TempDir(), "schema.json")
	data, _ := os.ReadFile("../schema/testdata/user.json")
	_ = os.WriteFile(file, data, 0o600)
	r := run(t, "", "schema", "show", "--file", file, "query", "me")
	if r.code != 0 || r.out != meDetail {
		t.Fatalf("%+v", r)
	}
	if r := run(t, "", "schema", "show", "--file", file, "query", "nope"); r.code == 0 {
		t.Fatalf("%+v", r)
	}
}

func TestDefaultEnvForEndpointsOnlyWhenUnambiguous(t *testing.T) {
	schemaSetup(t)
	srv := os.Getenv("API_BASE_URL")
	writeConfig(t, "[env.a]\nurl = \""+srv+"\"\nendpoints = [\"x\"]\n[env.b]\nurl = \""+srv+"\"\nendpoints = [\"y\"]\n")
	t.Setenv("API_ENV", "")
	if r := run(t, "", "schema", "endpoints"); r.code == 0 {
		t.Fatalf("two envs list endpoints: it must not guess: %+v", r)
	}
	if r := run(t, "", "--env", "b", "schema", "endpoints"); r.out != "y\n" {
		t.Fatalf("%+v", r)
	}
	t.Setenv("API_ENV", "a")
	t.Setenv("API_BASE_URL", srv)
	if r := run(t, "", "schema", "endpoints"); r.out != "x\n" {
		t.Fatalf("the shell's env wins: %+v", r)
	}
}
