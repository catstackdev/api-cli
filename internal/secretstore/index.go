package secretstore

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Index remembers the NAMES of the secrets you stored (never values, never
// origins), because a credential store cannot be listed safely. It makes
// `api secret list`, name completion and the doctor check possible. It is a
// convenience: the credential store stays the source of truth, and entries that
// have disappeared from it are reported as missing, not trusted.
type Index struct{ Path string }

// Names returns the indexed names, sorted. A missing file is an empty index.
func (i Index) Names() []string {
	data, err := os.ReadFile(i.Path)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && ValidateName(l) == nil && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// Add records a name (idempotent).
func (i Index) Add(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	for _, n := range i.Names() {
		if n == name {
			return nil
		}
	}
	return i.write(append(i.Names(), name))
}

// Remove forgets a name (idempotent).
func (i Index) Remove(name string) error {
	var keep []string
	for _, n := range i.Names() {
		if n != name {
			keep = append(keep, n)
		}
	}
	return i.write(keep)
}

func (i Index) write(names []string) error {
	if err := os.MkdirAll(filepath.Dir(i.Path), 0o700); err != nil {
		return err
	}
	sort.Strings(names)
	text := strings.Join(names, "\n")
	if text != "" {
		text += "\n"
	}
	f, err := os.CreateTemp(filepath.Dir(i.Path), ".secrets-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, i.Path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Chmod(i.Path, 0o600)
}
