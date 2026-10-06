package secretstore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// The entry format is shared by every backend (macOS Keychain, Secret Service),
// so a secret written on one machine reads the same on another.

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
			return Secret{}, fmt.Errorf("credential store: corrupt entry: %w", err)
		}
		var p payload
		if err := json.Unmarshal(b, &p); err != nil {
			return Secret{}, fmt.Errorf("credential store: corrupt entry: %w", err)
		}
		return Secret{Value: p.S, Origins: p.O}, nil
	case strings.HasPrefix(stored, markerB64):
		b, err := base64.StdEncoding.DecodeString(stored[len(markerB64):])
		if err != nil {
			return Secret{}, fmt.Errorf("credential store: corrupt entry: %w", err)
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
