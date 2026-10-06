package cli

import (
	"fmt"
	"strings"

	"cybercat/api/internal/config"
	"cybercat/api/internal/session"
)

// Common names projects use for the API URL, most specific first. A framework
// prefix (VITE_, REACT_APP_, NEXT_PUBLIC_) wins over the bare name: it is the
// one the app actually reads.
var (
	baseURLVars = []string{
		session.EnvBaseURL, "VITE_API_URL", "VITE_API_BASE_URL", "VITE_BASE_URL",
		"REACT_APP_API_URL", "REACT_APP_API_BASE_URL", "REACT_APP_BASE_URL",
		"NEXT_PUBLIC_API_URL", "NEXT_PUBLIC_API_BASE_URL",
		"API_URL", "API_BASE_URL", "BASE_URL", "API_HOST", "API_ENDPOINT",
	}
	graphqlURLVars = []string{
		session.EnvGraphQLURL, "VITE_GRAPHQL_URL", "VITE_GRAPHQL_API", "VITE_GRAPHQL_ENDPOINT",
		"REACT_APP_GRAPHQL_URL", "REACT_APP_GRAPHQL_API", "REACT_APP_GRAPHQL_ENDPOINT",
		"NEXT_PUBLIC_GRAPHQL_URL", "NEXT_PUBLIC_GRAPHQL_API", "NEXT_PUBLIC_GRAPHQL_ENDPOINT",
		"GRAPHQL_URL", "GRAPHQL_API", "GRAPHQL_ENDPOINT", "GRAPHQL_URI",
	}
)

func firstPresent(have map[string]bool, candidates []string) string {
	for _, c := range candidates {
		if have[c] {
			return c
		}
	}
	return ""
}

// envMapping returns the shell statements (and notes for the user) that copy a
// project's own URL variables into API_BASE_URL / API_GRAPHQL_URL after its
// .env file has been sourced. keys are the NAMES defined in that file; values
// stay in the shell: they are only referenced here, never read.
//
// A [vars] entry in config wins over detection. When only a GraphQL URL ending
// in /graphql exists, the base URL is derived from it.
func envMapping(keys []string, v config.Vars) (stmts, notes []string) {
	have := map[string]bool{}
	for _, k := range keys {
		have[k] = true
	}

	pick := func(configured string, candidates []string, target string) string {
		if configured != "" {
			return configured // the user said so, even if this file lacks it
		}
		if have[target] {
			return "" // the file already defines the API_* name itself
		}
		return firstPresent(have, candidates)
	}
	gql := pick(v.GraphQLURL, graphqlURLVars, session.EnvGraphQLURL)
	base := pick(v.BaseURL, baseURLVars, session.EnvBaseURL)

	// copy only when the source variable is non-empty, so a blank placeholder
	// (VITE_GRAPHQL_URL=) never wipes a value that is already set
	if gql != "" && gql != session.EnvGraphQLURL {
		stmts = append(stmts, fmt.Sprintf(`[ -n "${%s-}" ] && %s="$%s"`, gql, session.EnvGraphQLURL, gql))
		notes = append(notes, fmt.Sprintf("GraphQL URL ← $%s", gql))
	}
	if base != "" && base != session.EnvBaseURL {
		stmts = append(stmts, fmt.Sprintf(`[ -n "${%s-}" ] && %s="$%s"`, base, session.EnvBaseURL, base))
		notes = append(notes, fmt.Sprintf("base URL ← $%s", base))
	} else if base == "" && !have[session.EnvBaseURL] {
		// No base variable at all: derive it from <base>/graphql, but only from a
		// GraphQL variable THIS file provides. Deriving from whatever happens to be
		// in the session would overwrite the base URL you are logged in to.
		src := gql
		if src == "" && have[session.EnvGraphQLURL] {
			src = session.EnvGraphQLURL
		}
		if src != "" {
			stmts = append(stmts, fmt.Sprintf(`case "${%s-}" in */graphql) %s="${%s%%/graphql}";; esac`,
				src, session.EnvBaseURL, src))
			notes = append(notes, fmt.Sprintf("base URL ← $%s without /graphql", src))
		}
	}
	return stmts, notes
}

// detectURLVars reports which variables of the given names would be used as the
// GraphQL / base URL ("" when none, or when the API_* name itself is defined).
func detectURLVars(keys []string) (baseURL, graphqlURL string) {
	have := map[string]bool{}
	for _, k := range keys {
		have[k] = true
	}
	if !have[session.EnvGraphQLURL] {
		graphqlURL = firstPresent(have, graphqlURLVars)
	}
	if !have[session.EnvBaseURL] {
		baseURL = firstPresent(have, baseURLVars)
	}
	return baseURL, graphqlURL
}

func joinStmts(stmts []string) string { return strings.Join(stmts, "; ") }
