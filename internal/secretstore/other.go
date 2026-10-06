//go:build !darwin

package secretstore

type unavailable struct{}

// Default has no backing store on this system.
func Default() Store { return unavailable{} }

func (unavailable) Get(string) (Secret, error) { return Secret{}, ErrUnavailable }
func (unavailable) Set(string, Secret) error   { return ErrUnavailable }
func (unavailable) Delete(string) error        { return ErrUnavailable }
