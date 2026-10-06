package cli

import (
	"strings"
	"testing"
)

func TestVersionStringIdentifiesTheBuild(t *testing.T) {
	oldV, oldB := Version, BuildTime
	t.Cleanup(func() { Version, BuildTime = oldV, oldB })
	Version, BuildTime = "abc123-dirty", "2026-10-05T10:00:00Z"
	got := VersionString()
	if !strings.HasPrefix(got, "abc123-dirty (built 2026-10-05T10:00:00Z, go") || !strings.HasSuffix(got, ")") {
		t.Fatalf("%q", got)
	}
	BuildTime = ""
	if !strings.Contains(VersionString(), "build time unknown") {
		t.Fatalf("%q", VersionString())
	}
}
