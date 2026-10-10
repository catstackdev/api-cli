package cli

import (
	"strings"
	"testing"
)

// -H goes through httpc.ParseHeader: a line break or a bad name is a usage
// error before anything is sent, and the message never repeats the value.
func TestHeaderFlagRefusesLineBreaksAndBadNames(t *testing.T) {
	t.Setenv("API_BASE_URL", "https://api.test")
	for name, h := range map[string]string{
		"line break": "X-API-Key: s3cr3t\r\nX-Evil: 1",
		"bad name":   "X Key: s3cr3t",
		"no name":    ": s3cr3t",
		"no colon":   "s3cr3t",
	} {
		t.Run(name, func(t *testing.T) {
			r := run(t, "", "get", "/x", "--dry", "-H", h)
			if r.code != 2 || strings.Contains(r.err, "s3cr3t") || strings.Contains(r.out, "X-Evil") {
				t.Fatalf("%+v", r)
			}
		})
	}
	if r := run(t, "", "get", "/x", "--dry", "-H", "X-API-Key: ok"); r.code != 0 || !strings.Contains(r.out, "'X-API-Key: ") {
		t.Fatalf("%+v", r)
	}
}
