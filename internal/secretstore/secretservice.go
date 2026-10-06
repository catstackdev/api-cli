package secretstore

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// secretService keeps secrets in the freedesktop Secret Service through
// libsecret's `secret-tool`. It has no OS build tag so it is tested everywhere
// against a fake binary; only Default (default_linux.go) selects it.
//
// Like the Keychain backend, the secret never appears in an argument list: it
// is written to `secret-tool store` on stdin and read from `lookup` on stdout.
// Items carry the attributes service=api:NAME account=api.
type secretService struct{ bin string }

func newSecretService(bin string) Store { return secretService{bin: bin} }

func attrs(name string) []string {
	return []string{"service", Service(name), "account", account}
}

func (s secretService) Get(name string) (Secret, error) {
	if err := ValidateName(name); err != nil {
		return Secret{}, err
	}
	var out, stderr bytes.Buffer
	cmd := exec.Command(s.bin, append([]string{"lookup"}, attrs(name)...)...)
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		// secret-tool exits 1 with no output when nothing matches; anything it
		// says on stderr (no D-Bus session, locked keyring) is a real failure.
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return Secret{}, fmt.Errorf("secret service: %s", msg)
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 && out.Len() == 0 {
			return Secret{}, ErrNotFound
		}
		return Secret{}, fmt.Errorf("secret service: %w", err)
	}
	return decode(out.String())
}

func (s secretService) Set(name string, secret Secret) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	args := append([]string{"store", "--label=" + Service(name)}, attrs(name)...)
	cmd := exec.Command(s.bin, args...)
	cmd.Stdin = strings.NewReader(encode(secret))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("secret service: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Delete looks the item up first: `secret-tool clear` exits 0 whether or not
// anything matched, and the Store contract reports a missing item.
func (s secretService) Delete(name string) error {
	if _, err := s.Get(name); err != nil && !isCorrupt(err) {
		return err
	}
	var stderr bytes.Buffer
	cmd := exec.Command(s.bin, append([]string{"clear"}, attrs(name)...)...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("secret service: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// isCorrupt lets Delete remove an entry that no longer decodes: it must stay
// possible to get rid of one.
func isCorrupt(err error) bool {
	return err != nil && strings.Contains(err.Error(), "corrupt entry")
}
