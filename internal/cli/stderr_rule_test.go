package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// User-facing stderr goes through a.say, a.ask or a.trace (root.go), so styling
// and the plain-on-a-pipe rule have one home. A bare fmt.Fprint*(a.io.Err, ...)
// anywhere else is how an unstyled "✗ ..." line sneaks back in.
func TestStderrIsWrittenOnlyThroughTheHelpers(t *testing.T) {
	bare := regexp.MustCompile(`fmt\.Fprint\w*\(\s*a\.io\.Err\b`)
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if f == "root.go" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if bare.MatchString(line) {
				t.Errorf("%s:%d writes to stderr directly: use a.say / a.ask / a.trace\n\t%s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
