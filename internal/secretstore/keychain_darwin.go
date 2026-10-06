//go:build darwin

package secretstore

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
)

type keychain struct{}

// Default is the macOS Keychain.
func Default() Store { return keychain{} }

func (keychain) Get(name string) (Secret, error) {
	if err := ValidateName(name); err != nil {
		return Secret{}, err
	}
	out, err := exec.Command("security", "find-generic-password", "-a", account, "-s", Service(name), "-w").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 44 { // errSecItemNotFound
			return Secret{}, ErrNotFound
		}
		return Secret{}, fmt.Errorf("keychain: %w", err)
	}
	return decode(strings.TrimRight(string(out), "\n"))
}

// Set sends the secret to `security -i` on STDIN, hex-encoded: it never appears
// in a process argument list (ps) and needs no shell quoting.
func (keychain) Set(name string, secret Secret) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	cmd := exec.Command("security", "-i")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -U -a %s -s %s -X %s\n",
		account, Service(name), hex.EncodeToString([]byte(encode(secret)))))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (keychain) Delete(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	err := exec.Command("security", "delete-generic-password", "-a", account, "-s", Service(name)).Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 44 {
			return ErrNotFound
		}
		return fmt.Errorf("keychain: %w", err)
	}
	return nil
}
