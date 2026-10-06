package httpfile

import (
	"strings"
	"testing"
)

func TestParseAssertAnnotations(t *testing.T) {
	f, err := Parse([]byte(`
### list
# @filter .data
# @assert .status == 200
// @assert .body.data | length > 0
# @assert   .headers["content-type"] | test("json")   
GET /users

### other
GET /x
`))
	if err != nil {
		t.Fatal(err)
	}
	r := f.Requests[0]
	want := []Assert{
		{".status == 200", 4},
		{".body.data | length > 0", 5},
		{`.headers["content-type"] | test("json")`, 6},
	}
	if len(r.Asserts) != len(want) {
		t.Fatalf("%+v", r.Asserts)
	}
	for i, w := range want {
		if r.Asserts[i] != w {
			t.Errorf("assert %d: got %+v want %+v", i, r.Asserts[i], w)
		}
	}
	if r.Filter != ".data" || len(f.Requests[1].Asserts) != 0 {
		t.Fatalf("filter or neighbour disturbed: %+v / %+v", r, f.Requests[1])
	}
}

func TestParseEmptyAssertIsAnError(t *testing.T) {
	_, err := Parse([]byte("### a\n# @assert\nGET /x\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "@assert") {
		t.Fatalf("%v", err)
	}
}

// Other tools ignore unknown # @words, and so must a request that has none:
// an @assert in the body (after the blank line) is body text, not an assertion.
func TestAssertInsideABodyIsJustBody(t *testing.T) {
	f, err := Parse([]byte("### a\nPOST /x\nContent-Type: text/plain\n\n# @assert .status == 200\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Requests[0].Asserts) != 0 || !strings.Contains(f.Requests[0].Body, "@assert") {
		t.Fatalf("%+v", f.Requests[0])
	}
}
