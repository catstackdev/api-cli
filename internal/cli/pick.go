package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// fzfPick shows lines in fzf and returns the chosen 1-based index. A cancelled
// pick is an ExitError with no message; a missing fzf says what to do.
func (a *app) fzfPick(prompt string, lines []string) (int, error) {
	path, err := exec.LookPath("fzf")
	if err != nil {
		return 0, &ExitError{Code: exitGeneric, Msg: "✗ picking needs fzf in PATH (or pass a name)"}
	}
	var in bytes.Buffer
	for i, l := range lines {
		fmt.Fprintf(&in, "%d\t%s\n", i+1, l)
	}
	var out bytes.Buffer
	c := exec.Command(path, "--delimiter", "\t", "--with-nth", "2..", "--prompt", prompt+"> ", "--height", "40%", "--reverse")
	c.Stdin, c.Stdout, c.Stderr = &in, &out, os.Stderr
	if err := c.Run(); err != nil {
		return 0, &ExitError{Code: exitGeneric} // cancelled
	}
	n, err := strconv.Atoi(strings.SplitN(strings.TrimSpace(out.String()), "\t", 2)[0])
	if err != nil || n < 1 || n > len(lines) {
		return 0, fmt.Errorf("could not read fzf's choice")
	}
	return n, nil
}
