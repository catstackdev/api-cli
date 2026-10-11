package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// View shows data in an interactive viewer (jless, nvim, ...): argv is the
// program and its arguments, run WITHOUT a shell, with data on its stdin and w as
// its stdout (the viewer takes its keys from the terminal itself). Unlike a
// pager, a viewer that fails matters: the error comes back so the caller can
// print the data some other way and never lose it.
func View(w io.Writer, data []byte, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("no viewer command")
	}
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdin = bytes.NewReader(data)
	c.Stdout = w
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return err
	}
	return nil
}
