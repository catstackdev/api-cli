package envfile

import (
	"strings"
	"testing"
)

const dev = "# Base URL of the API\nAPI_BASE_URL=https://dev.example.com\nexport API_TOKEN=tok-CANARY-123\n\nDB_URL=postgres://admin:CANARY-PASS@db:5432/app\nFEATURE = \"on # not a comment\"\n# trailing orphan comment\r\nRETRIES=3 # inline comment\r\n"

func TestParse(t *testing.T) {
	got := Parse([]byte(dev))
	want := []struct{ key, value string }{
		{"API_BASE_URL", "https://dev.example.com"}, {"API_TOKEN", "tok-CANARY-123"},
		{"DB_URL", "postgres://admin:CANARY-PASS@db:5432/app"}, {"FEATURE", "on # not a comment"}, {"RETRIES", "3"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].Key != w.key || got[i].Value != w.value {
			t.Errorf("entry %d = %+v, want %s=%s", i, got[i], w.key, w.value)
		}
	}
	if len(got[0].Comments) != 1 || got[0].Comments[0] != "# Base URL of the API" {
		t.Fatalf("comment above a key is kept: %+v", got[0].Comments)
	}
	if len(got[1].Comments) != 0 {
		t.Fatalf("a blank line must reset comments: %+v", got[1].Comments)
	}
}

func TestExampleNeverLeaksValuesByDefault(t *testing.T) {
	src := []Source{{Name: ".env", Entries: Parse([]byte(dev))}}
	out := Example(src, false)
	if strings.Contains(out, "CANARY") || strings.Contains(out, "dev.example.com") {
		t.Fatalf("a value leaked into the example:\n%s", out)
	}
	for _, line := range []string{"API_BASE_URL=\n", "API_TOKEN=\n", "DB_URL=\n", "RETRIES=\n"} {
		if !strings.Contains(out, line) {
			t.Errorf("missing %q in:\n%s", line, out)
		}
	}
	if !strings.Contains(out, "# Base URL of the API\nAPI_BASE_URL=") {
		t.Fatalf("comments lost:\n%s", out)
	}
}

func TestKeepValuesStillBlanksSecretsAndCredentialURLs(t *testing.T) {
	src := []Source{{Name: ".env", Entries: Parse([]byte(dev))}}
	out := Example(src, true)
	if strings.Contains(out, "CANARY") {
		t.Fatalf("secret leaked with --keep-values:\n%s", out)
	}
	if !strings.Contains(out, "API_BASE_URL=https://dev.example.com\n") || !strings.Contains(out, "RETRIES=3\n") {
		t.Fatalf("harmless values should be kept:\n%s", out)
	}
	if !strings.Contains(out, "API_TOKEN=\n") || !strings.Contains(out, "DB_URL=\n") {
		t.Fatalf("secret / credential URL not blanked:\n%s", out)
	}
}

func TestExampleIsTheUnionInFirstSeenOrder(t *testing.T) {
	a := Source{Name: ".env", Entries: Parse([]byte("A=1\nB=2\n"))}
	b := Source{Name: ".env.prod", Entries: Parse([]byte("B=9\nC=3\nA=7\n"))}
	out := Example([]Source{a, b}, false)
	idxA, idxB, idxC := strings.Index(out, "A="), strings.Index(out, "B="), strings.Index(out, "C=")
	if !(idxA < idxB && idxB < idxC) || strings.Count(out, "A=") != 1 {
		t.Fatalf("union/order wrong:\n%s", out)
	}
}

func TestCheck(t *testing.T) {
	ex := []string{"A", "B", "C"}
	srcs := []Source{
		{Name: ".env", Entries: Parse([]byte("A=1\nB=2\nC=3\n"))},
		{Name: ".env.staging", Entries: Parse([]byte("A=1\nD=4\n"))},
	}
	d := Check(ex, srcs)
	if len(d) != 1 || d[0].File != ".env.staging" || strings.Join(d[0].Missing, ",") != "B,C" || strings.Join(d[0].Extra, ",") != "D" {
		t.Fatalf("%+v", d)
	}
}

func TestSecretHeuristics(t *testing.T) {
	for _, k := range []string{"API_TOKEN", "DB_PASSWORD", "STRIPE_SECRET_KEY", "AUTH_HEADER", "SESSION_ID", "PRIVATE_KEY"} {
		if !IsSecretKey(k) {
			t.Errorf("%s should look secret", k)
		}
	}
	for _, k := range []string{"API_BASE_URL", "PORT", "LOG_LEVEL", "FEATURE"} {
		if IsSecretKey(k) {
			t.Errorf("%s should not look secret", k)
		}
	}
	if !HasCredentials("https://u:p@host/x") || HasCredentials("https://host/x") || HasCredentials("postgres://host:5432/db") {
		t.Fatal("credential URL detection")
	}
}
