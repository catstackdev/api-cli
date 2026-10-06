package openapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func opBody(t *testing.T, spec, method, path string) string {
	t.Helper()
	idx, err := Parse([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range idx.Operations {
		if o.Method == method && o.Path == path {
			return o.Body
		}
	}
	t.Fatalf("%s %s not in the index", method, path)
	return ""
}

// The editor skeleton: required fields first, then the rest alphabetically,
// one placeholder per type, $refs and allOf resolved.
func TestBodySkeletonOpenAPI3(t *testing.T) {
	spec := `{"openapi":"3.0.0","paths":{"/users":{"post":{"requestBody":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/NewUser"}}}}}}},
	"components":{"schemas":{
	  "Base":{"type":"object","properties":{"id":{"type":"string"}}},
	  "NewUser":{"allOf":[{"$ref":"#/components/schemas/Base"},{"type":"object","required":["name"],"properties":{
	     "name":{"type":"string"},"age":{"type":"integer"},"score":{"type":"number"},"active":{"type":"boolean"},
	     "tags":{"type":"array","items":{"type":"string"}},"address":{"type":"object","properties":{"street":{"type":"string"}}},
	     "status":{"type":"string","enum":["new","old"]},"created":{"type":"string","format":"date-time"}}}]}}}}`
	got := opBody(t, spec, "POST", "/users")
	want := `{"name":"","active":false,"address":{"street":""},"age":0,"created":"","id":"","score":0,"status":"new","tags":[]}`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestBodySkeletonSwagger2(t *testing.T) {
	spec := `{"swagger":"2.0","paths":{"/x":{"put":{"parameters":[{"name":"id","in":"path"},{"name":"body","in":"body","schema":{"$ref":"#/definitions/D"}}]}}},
	"definitions":{"D":{"type":"object","properties":{"title":{"type":"string"},"n":{"type":"integer"}}}}}`
	if got := opBody(t, spec, "PUT", "/x"); got != `{"n":0,"title":""}` {
		t.Fatalf("%s", got)
	}
}

func TestNoBodyNoSkeleton(t *testing.T) {
	spec := `{"openapi":"3.0.0","paths":{"/a":{"get":{"summary":"x"},"post":{"requestBody":{"content":{"text/plain":{"schema":{"type":"string"}}}}}}}}`
	if got := opBody(t, spec, "GET", "/a"); got != "" {
		t.Fatalf("GET: %q", got)
	}
	if got := opBody(t, spec, "POST", "/a"); got != "" {
		t.Fatalf("a non-JSON body has no skeleton: %q", got)
	}
}

// Server data: a cyclic schema must terminate, and nothing hostile or huge may
// reach the file the user is about to edit and send.
func TestBodySkeletonIsBoundedAndSanitised(t *testing.T) {
	cyc := `{"openapi":"3.0.0","paths":{"/n":{"post":{"requestBody":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Node"}}}}}}},
	"components":{"schemas":{"Node":{"type":"object","properties":{"child":{"$ref":"#/components/schemas/Node"},"v":{"type":"string"}}}}}}`
	got := opBody(t, cyc, "POST", "/n")
	if !json.Valid([]byte(got)) || len(got) > MaxBody {
		t.Fatalf("cycle: %q", got)
	}

	var props []string
	for i := 0; i < 200; i++ {
		props = append(props, `"p`+itoa(i)+`":{"type":"string"}`)
	}
	big := `{"openapi":"3.0.0","paths":{"/b":{"post":{"requestBody":{"content":{"application/json":{"schema":{"type":"object","properties":{` + strings.Join(props, ",") + `}}}}}}}}}`
	g := opBody(t, big, "POST", "/b")
	var m map[string]any
	if err := json.Unmarshal([]byte(g), &m); err != nil || len(m) > MaxBodyProps || len(g) > MaxBody {
		t.Fatalf("a big schema is capped: %d props, %d bytes, %v", len(m), len(g), err)
	}

	hostile := `{"openapi":"3.0.0","paths":{"/h":{"post":{"requestBody":{"content":{"application/json":{"schema":{"type":"object","properties":{
	  "ok":{"type":"string"},
	  "bad key":{"type":"string"},
	  "esc\u001b[2J":{"type":"string"},
	  "x\ny":{"type":"string"},
	  "e":{"type":"string","enum":["a\u001b[31mb"]}}}}}}}}}}`
	hg := opBody(t, hostile, "POST", "/h")
	if strings.ContainsAny(hg, "\x1b\x07") || strings.Contains(hg, "bad key") || !strings.Contains(hg, `"ok"`) {
		t.Fatalf("hostile: %q", hg)
	}
	var hm map[string]any
	if err := json.Unmarshal([]byte(hg), &hm); err != nil {
		t.Fatalf("not JSON: %q", hg)
	}
	for k, v := range hm { // json.Marshal escapes ESC as \u001b: check the DECODED values
		if s, ok := v.(string); ok && strings.ContainsAny(s, "\x1b\x07") {
			t.Fatalf("a control character reached the value of %q: %q", k, s)
		}
	}
}
