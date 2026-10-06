package har

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cybercat/api/internal/httpc"
)

func exchange() (httpc.Spec, *httpc.Response) {
	spec := httpc.Spec{
		Method: "POST",
		URL:    "https://api.example.com/auth/login?limit=5&access_token=Q-CANARY",
		Headers: []httpc.Header{
			{Name: "Authorization", Value: "Bearer H-CANARY-TOKEN"},
			{Name: "Content-Type", Value: "application/json"},
			{Name: "X-Request-Id", Value: "abc"},
		},
		Body: `{"username":"me","password":"B-CANARY","nested":{"refreshToken":"R-CANARY","ok":true},"list":[{"secret":"L-CANARY"}]}`,
	}
	resp := &httpc.Response{
		Status: 200, StatusText: "OK", Proto: "HTTP/1.1",
		Header: http.Header{
			"Content-Type": {"application/json"},
			"Set-Cookie":   {"sid=S-CANARY; HttpOnly"},
			"X-Thing":      {"1"},
		},
		Body:     []byte(`{"data":{"login":{"accessToken":"A-CANARY","name":"me"}}}`),
		Duration: 120 * time.Millisecond,
	}
	return spec, resp
}

func TestSecretsAreMaskedByDefault(t *testing.T) {
	spec, resp := exchange()
	e := Build(spec, resp, time.Unix(0, 0), Options{Version: "t"})
	data, _ := json.Marshal(e)
	if strings.Contains(string(data), "CANARY") {
		t.Fatalf("a secret reached the HAR:\n%s", data)
	}
	for _, keep := range []string{"limit=5", "abc", "login", "X-Thing"} {
		if !strings.Contains(string(data), keep) {
			t.Errorf("harmless data %q was lost:\n%s", keep, data)
		}
	}
	// the bodies are JSON inside a JSON string: check them decoded
	req := e.Request.PostData.Text
	if !strings.Contains(req, `"username":"me"`) || !strings.Contains(req, `"ok":true`) || !strings.Contains(req, `"password":"`+Redacted+`"`) {
		t.Fatalf("request body: %s", req)
	}
	if res := e.Response.Content.Text; !strings.Contains(res, `"name":"me"`) || !strings.Contains(res, `"accessToken":"`+Redacted+`"`) {
		t.Fatalf("response body: %s", res)
	}
	if !strings.Contains(string(data), Redacted) {
		t.Fatal("nothing was marked redacted")
	}
}

func TestIncludeSecretsIsExplicit(t *testing.T) {
	spec, resp := exchange()
	e := Build(spec, resp, time.Unix(0, 0), Options{IncludeSecrets: true})
	data, _ := json.Marshal(e)
	for _, want := range []string{"H-CANARY-TOKEN", "B-CANARY", "A-CANARY", "S-CANARY", "Q-CANARY"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("--har-secrets should keep %q", want)
		}
	}
}

func TestFormBodiesAndCredentialURLs(t *testing.T) {
	spec := httpc.Spec{Method: "POST", URL: "https://user:URL-CANARY@host/x", Data: []string{"email=a@b.c", "password=F-CANARY"}}
	resp := &httpc.Response{Status: 204, Proto: "HTTP/1.1", Header: http.Header{}}
	data, _ := json.Marshal(Build(spec, resp, time.Now(), Options{}))
	if strings.Contains(string(data), "CANARY") || !strings.Contains(string(data), "email=a@b.c") {
		t.Fatalf("%s", data)
	}
}

func TestBinaryBodyIsBase64(t *testing.T) {
	spec := httpc.Spec{Method: "GET", URL: "https://h/x"}
	resp := &httpc.Response{Status: 200, Proto: "HTTP/1.1", Header: http.Header{}, Body: []byte{0xff, 0xfe, 0x00}}
	e := Build(spec, resp, time.Now(), Options{})
	if e.Response.Content.Encoding != "base64" || e.Response.Content.Text != "//4A" || e.Response.Content.Size != 3 {
		t.Fatalf("%+v", e.Response.Content)
	}
}

func TestAppendBuildsOneArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "out.har")
	spec, resp := exchange()
	for i := 0; i < 3; i++ {
		if err := Append(path, Build(spec, resp, time.Now(), Options{}), "v1"); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(path)
	var doc Doc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if doc.Log.Version != "1.2" || doc.Log.Creator.Name != "api" || len(doc.Log.Entries) != 3 {
		t.Fatalf("%+v", doc.Log)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	// a corrupt file is replaced, not appended to
	_ = os.WriteFile(path, []byte("not json"), 0o600)
	if err := Append(path, Build(spec, resp, time.Now(), Options{}), "v1"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	_ = json.Unmarshal(data, &doc)
	if len(doc.Log.Entries) != 1 {
		t.Fatalf("%d entries", len(doc.Log.Entries))
	}
}
