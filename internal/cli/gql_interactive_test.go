package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pickEnv: a configured env (endpoints, prefix v2/) the shell is on, fake fzf and
// editor first on PATH.
func pickEnv(t *testing.T) *schemaSrv {
	t.Helper()
	s, _ := schemaSetup(t)
	fakeTools(t)
	srv := os.Getenv("API_BASE_URL")
	writeConfig(t, "[env.dev]\nurl = \""+srv+"\"\nendpoints = [\"user\", \"auth\"]\nendpoint_prefix = \"v2/\"\n")
	t.Setenv("API_ENV", "dev")
	t.Setenv("API_BASE_URL", srv)
	return s
}

// ran counts the requests that were not introspection.
func (s *schemaSrv) ran(contains string) int {
	n := 0
	for _, b := range s.bodies {
		if strings.Contains(b, contains) && !strings.Contains(b, "__schema") {
			n++
		}
	}
	return n
}

func TestGQLPickRunsTheGeneratedQueryWhenSaved(t *testing.T) {
	s := pickEnv(t)
	t.Setenv("FAKE_PLAIN_LINE", "2") // sorted: mutation login, query me, query user
	t.Setenv("EDITOR_TOUCH", "1")    // saved unchanged: run the skeleton as is
	r := run(t, "", "gql", "--pick", "-e", "user")
	if r.code != 0 || s.ran("query Me") != 1 {
		t.Fatalf("%+v bodies=%v", r, s.bodies)
	}
	if !strings.Contains(r.err, "→ api gql -e v2/user 'query Me { me { id name } }'") {
		t.Fatalf("the one-liner is missing:\n%s", r.err)
	}
	if s.paths[len(s.paths)-1] != "/graphql/v2/user" || s.auths[len(s.auths)-1] != "Bearer TOK-0123456789-SECRETTAIL" {
		t.Fatalf("%v %v", s.paths, s.auths)
	}
}

func TestGQLPickQuittingWithoutSavingCancels(t *testing.T) {
	s := pickEnv(t)
	t.Setenv("FAKE_PLAIN_LINE", "2")
	r := run(t, "", "gql", "--pick", "-e", "user")
	if r.code != exitGeneric || !strings.Contains(r.err, "cancelled") || s.ran("query Me") != 0 {
		t.Fatalf("%+v bodies=%v", r, s.bodies)
	}
	// an editor that fails (vim :cq) cancels too
	t.Setenv("EDITOR_TOUCH", "1")
	t.Setenv("EDITOR_FAIL", "1")
	if r := run(t, "", "gql", "--pick", "-e", "user"); r.code == 0 || s.ran("query Me") != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestGQLPickAsksBeforeRunningAMutation(t *testing.T) {
	s := pickEnv(t)
	t.Setenv("FAKE_PLAIN_LINE", "1") // mutation login
	t.Setenv("EDITOR_TOUCH", "1")

	r := run(t, "n\n", "gql", "--pick", "-e", "user")
	if r.code != exitGeneric || !strings.Contains(r.err, "changes data") || !strings.Contains(r.err, "(not run)") || s.ran("mutation Login") != 0 {
		t.Fatalf("declined: %+v", r)
	}
	if r := run(t, "", "gql", "--pick", "-e", "user"); s.ran("mutation Login") != 0 { // no answer = no
		t.Fatalf("an empty answer must mean no: %+v", r)
	}
	r = run(t, "y\n", "gql", "--pick", "-e", "user")
	if r.code != 0 || s.ran("mutation Login") != 1 {
		t.Fatalf("confirmed: %+v", r)
	}
	// the variables hold a password-looking name: the printed one-liner leaves them out
	if !strings.Contains(r.err, "left out") || strings.Contains(r.err, `"password"`) || strings.Contains(r.err, "username") {
		t.Fatalf("the one-liner shows variables:\n%s", r.err)
	}
	// --yes skips the question
	n := s.ran("mutation Login")
	if r := run(t, "", "gql", "--pick", "-e", "user", "--yes"); r.code != 0 || s.ran("mutation Login") != n+1 {
		t.Fatalf("--yes: %+v", r)
	}
}

func TestGQLPickAsksAgainWhenYouEditAQueryIntoAMutation(t *testing.T) {
	s := pickEnv(t)
	t.Setenv("FAKE_PLAIN_LINE", "2") // query me
	edited := filepath.Join(t.TempDir(), "edited.graphql")
	_ = os.WriteFile(edited, []byte("mutation Sneaky { logout }\n"), 0o600)
	t.Setenv("EDITOR_REPLACE", edited)
	r := run(t, "n\n", "gql", "--pick", "-e", "user")
	if r.code == 0 || s.ran("Sneaky") != 0 || !strings.Contains(r.err, "changes data") {
		t.Fatalf("an edited-in mutation ran without asking: %+v", r)
	}
}

func TestGQLPickSendsTheVariablesYouEdited(t *testing.T) {
	s := pickEnv(t)
	t.Setenv("FAKE_PLAIN_LINE", "3") // query user(id)
	edited := filepath.Join(t.TempDir(), "edited.graphql")
	_ = os.WriteFile(edited, []byte("# note\nquery U($id: ID!) { user(id: $id) { id } }\n### variables\n{\"id\": \"7\"}\n"), 0o600)
	t.Setenv("EDITOR_REPLACE", edited)
	r := run(t, "", "gql", "--pick", "-e", "user")
	if r.code != 0 || s.ran(`"variables":{"id":"7"}`) != 1 {
		t.Fatalf("%+v bodies=%v", r, s.bodies)
	}
	if !strings.Contains(r.err, `'{"id":"7"}'`) {
		t.Fatalf("a harmless variable belongs in the one-liner:\n%s", r.err)
	}
	// invalid variables: nothing is sent
	_ = os.WriteFile(edited, []byte("query Bad { me { id } }\n### variables\n{not json\n"), 0o600)
	if r := run(t, "", "gql", "--pick", "-e", "user"); r.code != exitUsage || !strings.Contains(r.err, "not valid JSON") || s.ran("query Bad") != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestGQLPickRemembersTheLastEndpoint(t *testing.T) {
	s := pickEnv(t)
	state := t.TempDir()
	srvURL := os.Getenv("API_BASE_URL")
	writeConfig(t, "[env.dev]\nurl = \""+srvURL+"\"\nendpoints = [\"auth\", \"user\"]\nendpoint_prefix = \"v2/\"\n")
	t.Setenv("EDITOR_TOUCH", "1")
	t.Setenv("FAKE_PLAIN_LINE", "1") // the endpoint list picks its first line; so does the item list

	// first time the list is as configured: fzf takes "auth"
	runShared(t, state, "y\n", "gql", "--pick")
	if s.paths[len(s.paths)-1] != "/graphql/v2/auth" {
		t.Fatalf("%v", s.paths)
	}
	if data, _ := os.ReadFile(filepath.Join(state, "api", "last-endpoint")); strings.TrimSpace(string(data)) != "auth" {
		t.Fatalf("last-endpoint = %q", data)
	}
	// now put "user" in front by choosing it explicitly, then it leads the list
	runShared(t, state, "y\n", "gql", "--pick", "-e", "user")
	_ = os.WriteFile(filepath.Join(state, "api", "last-endpoint"), []byte("user\n"), 0o600)
	runShared(t, state, "y\n", "gql", "--pick")
	if s.paths[len(s.paths)-1] != "/graphql/v2/user" {
		t.Fatalf("the last endpoint should lead the list: %v", s.paths)
	}
}

func TestGQLWithoutAQueryStillSaysSoWhenNotInteractive(t *testing.T) {
	schemaSetup(t)
	if r := run(t, "", "gql"); r.code == 0 || !strings.Contains(r.err, "empty query") {
		t.Fatalf("%+v", r)
	}
}

func TestCommentLinesNeverReachTheQuery(t *testing.T) {
	if got := stripCommentLines("# header\nquery { a }\n  # note\n"); strings.Contains(got, "#") || !strings.Contains(got, "query { a }") {
		t.Fatalf("%q", got)
	}
}

func TestEditorDocSplit(t *testing.T) {
	q, v := splitEditorDoc("query { a }\n### variables\n{\"a\": 1}\n")
	if strings.TrimSpace(q) != "query { a }" || v != `{"a": 1}` {
		t.Fatalf("%q %q", q, v)
	}
	if q, v := splitEditorDoc("query { a }"); q != "query { a }" || v != "" {
		t.Fatalf("%q %q", q, v)
	}
}
