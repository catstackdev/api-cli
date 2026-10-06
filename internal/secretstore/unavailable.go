package secretstore

type unavailable struct{}

func (unavailable) Get(string) (Secret, error) { return Secret{}, ErrUnavailable }
func (unavailable) Set(string, Secret) error   { return ErrUnavailable }
func (unavailable) Delete(string) error        { return ErrUnavailable }
