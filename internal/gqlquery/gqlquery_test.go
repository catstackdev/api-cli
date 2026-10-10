package gqlquery

import "testing"

func TestIsReadOnly(t *testing.T) {
	for q, want := range map[string]bool{
		"query { me { id } }":                      true,
		"{ __typename }":                           true,
		"# mutation in a comment\n{ a }":           true,
		"query Q { a } # subscription":             true,
		"mutation { do }":                          false,
		"mutation M($i: ID!) { do(id:$i) { ok } }": false,
		"  \n mutation { do }":                     false,
		"query { a } mutation { b }":               false, // a second operation
		"{ a }\nsubscription { b }":                false,
		"query { a }\n# ok\nmutation { b }":        false, // a comment must not hide what follows
		"query{a}mutation{b}":                      false,
		"query { a(x: \"mutation\") }":             true, // conservative the other way is fine, but pin what it does today
	} {
		if got := IsReadOnly(q); got != want {
			t.Errorf("IsReadOnly(%q) = %v, want %v", q, got, want)
		}
	}
}

func TestIsSubscription(t *testing.T) {
	for q, want := range map[string]bool{
		"subscription { x }":                  true,
		"# c\n  subscription S { x }":         true,
		"# subscription\nquery { me { id } }": false,
		"query { me { id } }":                 false,
		"mutation { subscription }":           false,
	} {
		if got := IsSubscription(q); got != want {
			t.Errorf("IsSubscription(%q) = %v, want %v", q, got, want)
		}
	}
}

func TestStripCommentsKeepsOnlyWholeLineComments(t *testing.T) {
	got := StripComments("# header\nquery { a }\n  # note\nquery { b(x:\"#\") }")
	if got != "query { a }\nquery { b(x:\"#\") }" {
		t.Fatalf("%q", got)
	}
}

func TestBodyAndTextRoundTrip(t *testing.T) {
	body, err := Body("query($i: ID!) { a(i:$i) }", `{"i":"1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if Text(body) != "query($i: ID!) { a(i:$i) }" {
		t.Fatalf("%q", Text(body))
	}
	if Text("not json") != "" || Text(`{"x":1}`) != "" {
		t.Fatal("a payload with no query has no text")
	}
	if _, err := Body("q", "{bad"); err == nil {
		t.Fatal("invalid variables JSON must be an error, not a malformed body")
	}
}

func TestTarget(t *testing.T) {
	for _, c := range []struct{ base, ep, want string }{
		{"", "", "/graphql"},
		{"https://a/graphql", "", "https://a/graphql"},
		{"https://a/graphql", "v2/user", "https://a/graphql/v2/user"},
		{"https://a/graphql/", "/v2/user", "https://a/graphql/v2/user"},
		{"https://a/graphql", "https://b/x", "https://b/x"},
		{"", "v2", "/graphql/v2"},
	} {
		if got := Target(c.base, c.ep); got != c.want {
			t.Errorf("Target(%q,%q) = %q, want %q", c.base, c.ep, got, c.want)
		}
	}
}
