package ui

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
)

// DefaultPager quits by itself when the output fits one screen (-F), passes
// colour through (-R) and does not clear the screen (-X).
const DefaultPager = "less -FRX"

// PagerCmd is $API_PAGER, or DefaultPager. A set-but-empty API_PAGER turns
// paging off.
func PagerCmd() string { return PagerCmdWith(os.LookupEnv) }

// PagerCmdWith is PagerCmd with an injectable environment.
func PagerCmdWith(lookup func(string) (string, bool)) string {
	if v, ok := lookup("API_PAGER"); ok {
		return v
	}
	return DefaultPager
}

// PagerOptions says whether and how to page.
type PagerOptions struct {
	Cmd     string
	Enabled bool
}

// ShouldPage is true for an interactive stdout that has not opted out.
func ShouldPage(w any, disabled bool) bool { return ShouldPageWith(w, disabled, os.LookupEnv) }

// ShouldPageWith is ShouldPage with an injectable environment.
func ShouldPageWith(w any, disabled bool, lookup func(string) (string, bool)) bool {
	return !disabled && IsTerminal(w) && PagerCmdWith(lookup) != ""
}

// Page writes data to w, through the pager when enabled. If the pager cannot
// run at all (missing command) the data is written directly, so output is
// never lost. A pager the user quits early is not an error.
func Page(w io.Writer, data []byte, o PagerOptions) error {
	if len(data) == 0 {
		return nil // nothing to show: do not start a pager for it
	}
	if !o.Enabled || o.Cmd == "" {
		_, err := w.Write(data)
		return err
	}
	c := exec.Command("sh", "-c", o.Cmd)
	c.Stdin = bytes.NewReader(data)
	c.Stdout = w
	c.Stderr = os.Stderr
	err := c.Run()
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() != 126 && ee.ExitCode() != 127 {
		return nil // the pager ran; non-zero is just "user quit"
	}
	_, werr := w.Write(data)
	return werr
}
