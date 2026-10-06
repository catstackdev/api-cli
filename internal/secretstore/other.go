//go:build !darwin && !linux

package secretstore

// Default has no backing store on this system.
func Default() Store { return unavailable{} }
