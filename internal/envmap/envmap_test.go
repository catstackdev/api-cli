package envmap

import (
	"strings"
	"testing"

	"cybercat/api/internal/config"
)

func TestFrameworkPrefixBeatsTheBareName(t *testing.T) {
	stmts, _ := Mapping([]string{"GRAPHQL_URL", "VITE_GRAPHQL_URL"}, config.Vars{})
	if len(stmts) == 0 || !strings.Contains(stmts[0], `"$VITE_GRAPHQL_URL"`) {
		t.Fatalf("the framework variable is the one the app reads: %q", stmts)
	}
}

// A blank placeholder (VITE_GRAPHQL_URL=) must not wipe a URL that is already set.
func TestCopyHappensOnlyWhenTheSourceIsNonEmpty(t *testing.T) {
	stmts, _ := Mapping([]string{"VITE_GRAPHQL_URL"}, config.Vars{})
	if !strings.HasPrefix(stmts[0], `[ -n "${VITE_GRAPHQL_URL-}" ] && `) {
		t.Fatalf("not guarded: %q", stmts[0])
	}
}

func TestAFileThatDefinesTheAPINameItselfIsLeftAlone(t *testing.T) {
	stmts, _ := Mapping([]string{"API_GRAPHQL_URL", "VITE_GRAPHQL_URL", "API_BASE_URL"}, config.Vars{})
	if len(stmts) != 0 {
		t.Fatalf("nothing to copy, got %q", stmts)
	}
}

func TestConfiguredNameBeatsDetectionEvenIfTheFileLacksIt(t *testing.T) {
	stmts, notes := Mapping([]string{"VITE_GRAPHQL_URL"}, config.Vars{GraphQLURL: "MY_GQL"})
	if len(stmts) == 0 || !strings.Contains(stmts[0], `"$MY_GQL"`) || strings.Contains(stmts[0], "VITE_") {
		t.Fatalf("stmts=%q notes=%q", stmts, notes)
	}
}

// Deriving the base URL from the session would overwrite the server you are
// logged in to: only a GraphQL variable THIS file provides may be used.
func TestBaseIsDerivedOnlyFromAGraphQLVariableTheFileProvides(t *testing.T) {
	stmts, _ := Mapping([]string{"VITE_GRAPHQL_URL"}, config.Vars{})
	joined := strings.Join(stmts, "\n")
	if !strings.Contains(joined, `case "${VITE_GRAPHQL_URL-}" in */graphql) API_BASE_URL=`) {
		t.Fatalf("expected the base to be derived from the file's GraphQL variable:\n%s", joined)
	}
	if none, _ := Mapping(nil, config.Vars{}); len(none) != 0 {
		t.Fatalf("a file with no URL variable must change nothing: %q", none)
	}
}

func TestStatementsReferenceNamesNeverValues(t *testing.T) {
	stmts, _ := Mapping([]string{"VITE_API_URL", "VITE_GRAPHQL_URL"}, config.Vars{})
	for _, s := range stmts {
		if strings.Contains(s, "http") {
			t.Errorf("a value leaked into the statement: %q", s)
		}
	}
}

func TestDetectURLVars(t *testing.T) {
	base, gql := DetectURLVars([]string{"REACT_APP_API_URL", "VITE_GRAPHQL_URL"})
	if base != "REACT_APP_API_URL" || gql != "VITE_GRAPHQL_URL" {
		t.Fatalf("got %q %q", base, gql)
	}
	base, gql = DetectURLVars([]string{"API_BASE_URL", "API_GRAPHQL_URL", "VITE_API_URL"})
	if base != "" || gql != "" {
		t.Fatalf("the API_* name itself wins, nothing to detect: %q %q", base, gql)
	}
}
