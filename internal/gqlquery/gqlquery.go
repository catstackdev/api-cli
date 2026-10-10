// Package gqlquery holds the decisions made on GraphQL text: does it change
// data, is it a subscription, where does a request for it go. They are pure
// functions of strings, so every command that sends or saves a query asks the
// same place.
package gqlquery

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	writeOp        = regexp.MustCompile(`(^|[\s}{)])(mutation|subscription)\b`)
	comment        = regexp.MustCompile(`#[^\n]*`)
	subscriptionOp = regexp.MustCompile(`^\s*subscription\b`)
)

// IsReadOnly is deliberately conservative: `api diff` sends the request to TWO
// environments, and the confirmation of `api gql --pick` relies on it, so
// anything that looks like a mutation (or a subscription) is not read-only.
func IsReadOnly(q string) bool {
	return !writeOp.MatchString(comment.ReplaceAllString(q, ""))
}

// IsSubscription reports whether the operation is a subscription (comment lines
// do not count): it then runs over a WebSocket, not as an HTTP POST.
func IsSubscription(query string) bool {
	return subscriptionOp.MatchString(StripComments(query))
}

// StripComments drops whole-line # comments (the header an editor file adds, and
// any you write): collapsed to one line, a comment would swallow the query.
func StripComments(q string) string {
	var keep []string
	for _, l := range strings.Split(q, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

// Body is the JSON request body of a GraphQL call. varsJSON must be valid JSON.
func Body(query, varsJSON string) (string, error) {
	b, err := json.Marshal(struct {
		Query     string          `json:"query"`
		Variables json.RawMessage `json:"variables"`
	}{query, json.RawMessage(varsJSON)})
	return string(b), err
}

// Text is the query text of a Body payload ("" if it has none).
func Text(payload string) string {
	var r struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal([]byte(payload), &r)
	return r.Query
}

// Target resolves -e: "" is the base, a full URL is used as-is, anything else is
// appended to the GraphQL URL (default "/graphql" on the base URL).
func Target(graphqlURL, endpoint string) string {
	base := graphqlURL
	if base == "" {
		base = "/graphql"
	}
	switch {
	case endpoint == "":
		return base
	case strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://"):
		return endpoint
	default:
		return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(endpoint, "/")
	}
}
