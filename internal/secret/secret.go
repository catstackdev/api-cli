// Package secret answers one question in one place: does this NAME look like it
// holds a secret? It is used wherever values must not be shown or stored (HAR
// export, history, .env examples) so the answer cannot drift between them.
//
// config keeps its own, narrower rule for config-file keys on purpose (see
// config.IsSecretKey): refusing every key that merely contains "key" there
// would reject harmless names like an alias called "monkey".
package secret

import "regexp"

var (
	// name matches variable / header / field / form-key names.
	name = regexp.MustCompile(`(?i)(token|secret|passw|pwd|key|credential|auth|private|salt|cert|dsn|signature|session|cookie|otp)`)
	// param additionally matches the OAuth callback "code" in query strings.
	param = regexp.MustCompile(`(?i)(token|secret|passw|pwd|key|credential|auth|private|salt|cert|dsn|signature|session|cookie|otp|code)`)
)

// Name reports whether a variable, header or JSON field name looks secret.
func Name(s string) bool { return name.MatchString(s) }

// Param reports whether a URL query / form parameter name looks secret.
func Param(s string) bool { return param.MatchString(s) }
