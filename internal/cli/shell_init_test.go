package cli

import (
	"os/exec"
	"strings"
	"testing"
)

func TestShellInitStampsContract(t *testing.T) {
	r := run(t, "", "shell-init")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(r.out, "@CONTRACT@") || !strings.Contains(r.out, "API_SHIM="+shimContract+")") {
		t.Errorf("contract not stamped:\n%s", r.out)
	}
	if r := run(t, "", "shell-init", "fish"); r.code == 0 {
		t.Errorf("unsupported shell must fail: %+v", r)
	}
}

func TestShellInitIsValidZsh(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	cmd := exec.Command(zsh, "-n", "-c", shimText())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("zsh -n: %v\n%s", err, out)
	}
}
