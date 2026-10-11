package ui

import (
	"bytes"
	"testing"
)

func TestViewFeedsStdinAndWritesToTheGivenStdout(t *testing.T) {
	var out bytes.Buffer
	if err := View(&out, []byte("body\n"), []string{"sh", "-c", "echo seen; cat"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "seen\nbody\n" {
		t.Fatalf("got %q", got)
	}
}

func TestViewReportsAViewerThatCannotRunOrFails(t *testing.T) {
	var out bytes.Buffer
	if View(&out, []byte("x"), []string{"definitely-not-a-viewer-xyz"}) == nil {
		t.Fatal("a missing viewer must be an error (the caller prints the body instead)")
	}
	if View(&out, []byte("x"), []string{"sh", "-c", "cat >/dev/null; exit 3"}) == nil {
		t.Fatal("a viewer that exits non-zero must be an error")
	}
	if View(&out, []byte("x"), nil) == nil {
		t.Fatal("no command must be an error")
	}
}
