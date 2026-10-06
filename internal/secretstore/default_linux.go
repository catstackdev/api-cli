//go:build linux

package secretstore

import "os/exec"

// Default is the freedesktop Secret Service (GNOME Keyring, KWallet, KeePassXC)
// through `secret-tool` from libsecret, or no store when that tool is missing.
func Default() Store {
	bin, err := exec.LookPath("secret-tool")
	if err != nil {
		return unavailable{}
	}
	return newSecretService(bin)
}
