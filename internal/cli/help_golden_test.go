package cli

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"cybercat/api/internal/secretstore"

	"github.com/spf13/cobra"
)

var update = flag.Bool("update", false, "rewrite golden files")

// helpText renders the help of every command, recursively. It is the safety net
// for refactors of flag handling: command names, flags, defaults and wording must
// not change unless a change is intended (then run with -update and review).
func helpText(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"CURL_MAX_TIME", "CURL_CONNECT_TIMEOUT", "API_COLOR", "API_SUMMARY", "API_PAGER", "API_DEBUG", "NO_COLOR"} {
		t.Setenv(k, "")
	}
	t.Setenv("API_CONFIG", filepath.Join(t.TempDir(), "none.toml"))
	var out bytes.Buffer
	a := &app{io: IO{In: strings.NewReader(""), Out: &out, Err: &out}, store: secretstore.NewMemory()}
	root := a.rootCmd()

	var b strings.Builder
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		var buf bytes.Buffer
		c.SetOut(&buf)
		c.SetErr(&buf)
		_ = c.Help()
		fmt.Fprintf(&b, "===== %s =====\n%s\n", path, buf.String())
		kids := c.Commands()
		sort.Slice(kids, func(i, j int) bool { return kids[i].Name() < kids[j].Name() })
		for _, k := range kids {
			if k.Hidden || k.Name() == "help" || k.Name() == "completion" {
				continue
			}
			walk(k, path+" "+k.Name())
		}
	}
	walk(root, "api")
	return b.String()
}

func TestHelpGolden(t *testing.T) {
	got := helpText(t)
	path := filepath.Join("testdata", "help.golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no golden file: run `go test ./internal/cli -run TestHelpGolden -update`: %v", err)
	}
	if string(want) != got {
		// show the first differing line, not 3000 lines
		wl, gl := strings.Split(string(want), "\n"), strings.Split(got, "\n")
		for i := 0; i < len(wl) && i < len(gl); i++ {
			if wl[i] != gl[i] {
				t.Fatalf("help text changed at line %d:\n  want: %q\n  got:  %q\n(intended? run with -update and review the diff)", i+1, wl[i], gl[i])
			}
		}
		t.Fatalf("help text changed in length (want %d lines, got %d); run with -update and review", len(wl), len(gl))
	}
}
