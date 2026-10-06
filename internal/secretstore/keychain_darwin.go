//go:build darwin

package secretstore

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

const (
	account = "api"
	// Entries are printable ASCII (`security -w` prints other data as ambiguous
	// hex):
	//   "b64:" + base64(secret)            unbound (also what older builds wrote)
	//   "v2:"  + base64({"s":..,"o":[..]}) with the origins it may be sent to
	markerB64 = "b64:"
	markerV2  = "v2:"
)

type payload struct {
	S string   `json:"s"`
	O []string `json:"o,omitempty"`
}

// decode reverses encode. Entries without a marker (added by hand with
// `security add-generic-password -w`) are unbound plain values.
func decode(stored string) (Secret, error) {
	switch {
	case strings.HasPrefix(stored, markerV2):
		b, err := base64.StdEncoding.DecodeString(stored[len(markerV2):])
		if err != nil {
			return Secret{}, fmt.Errorf("keychain: corrupt entry: %w", err)
		}
		var p payload
		if err := json.Unmarshal(b, &p); err != nil {
			return Secret{}, fmt.Errorf("keychain: corrupt entry: %w", err)
		}
		return Secret{Value: p.S, Origins: p.O}, nil
	case strings.HasPrefix(stored, markerB64):
		b, err := base64.StdEncoding.DecodeString(stored[len(markerB64):])
		if err != nil {
			return Secret{}, fmt.Errorf("keychain: corrupt entry: %w", err)
		}
		return Secret{Value: string(b)}, nil
	}
	return Secret{Value: stored}, nil
}

func encode(sec Secret) string {
	if len(sec.Origins) == 0 {
		return markerB64 + base64.StdEncoding.EncodeToString([]byte(sec.Value))
	}
	b, _ := json.Marshal(payload{S: sec.Value, O: sec.Origins})
	return markerV2 + base64.StdEncoding.EncodeToString(b)
}

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
