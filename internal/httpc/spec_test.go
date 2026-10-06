package httpc

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHeaderHelpersAreCaseInsensitiveAndKeepOrder(t *testing.T) {
	s := Spec{}
	s.SetHeader("Accept", "a")
	s.SetHeader("X-One", "1")
	s.SetHeader("accept", "b") // replaces in place, does not append or move
	want := []Header{{"Accept", "b"}, {"X-One", "1"}}
	if !reflect.DeepEqual(s.Headers, want) {
		t.Fatalf("headers = %v, want %v", s.Headers, want)
	}

	s.DefaultHeader("ACCEPT", "ignored") // the caller already set it
	s.DefaultHeader("Content-Type", "application/json")
	if got := s.Header("accept"); got != "b" {
		t.Errorf("DefaultHeader overrode a caller header: %q", got)
	}
	if got := s.Header("content-type"); got != "application/json" {
		t.Errorf("DefaultHeader did not add a missing header: %q", got)
	}
	if !s.HasHeader("x-ONE") || s.HasHeader("X-Two") || s.Header("X-Two") != "" {
		t.Error("HasHeader/Header must match any case and report a missing header as absent")
	}
}

// --dry and the `--` passthrough print CurlArgs: it has to say exactly what the
// native client sends, in a stable order.
func TestCurlArgs(t *testing.T) {
	s := Spec{
		Method:         "POST",
		URL:            "https://api.test/x",
		Headers:        []Header{{"Authorization", "Bearer t"}, {"Accept", "application/json"}},
		Body:           `{"a":1}`,
		Fields:         []string{"f=@file.txt"},
		Data:           []string{"k=v", "k2=v2"},
		Timeout:        2500 * time.Millisecond,
		ConnectTimeout: 3 * time.Second,
		Insecure:       true,
	}
	want := []string{
		"-sS", "--fail-with-body", "--connect-timeout", "3", "--max-time", "2.5", "-k",
		"-X", "POST", "https://api.test/x",
		"-H", "Authorization: Bearer t", "-H", "Accept: application/json",
		"-d", `{"a":1}`, "-F", "f=@file.txt", "-d", "k=v", "-d", "k2=v2",
	}
	if got := s.CurlArgs(); !reflect.DeepEqual(got, want) {
		t.Errorf("CurlArgs:\n got %q\nwant %q", got, want)
	}

	// zero timeouts and no -k add nothing
	plain := Spec{Method: "GET", URL: "https://api.test/"}.CurlArgs()
	if want := []string{"-sS", "--fail-with-body", "-X", "GET", "https://api.test/"}; !reflect.DeepEqual(plain, want) {
		t.Errorf("plain CurlArgs = %q, want %q", plain, want)
	}
}

func TestResolveURL(t *testing.T) {
	cases := []struct {
		name, base, target string
		query              []string
		want               string
		wantErr            string
	}{
		{"path joined to base", "https://api.test", "/users", nil, "https://api.test/users", ""},
		{"trailing slash on base", "https://api.test/", "/users", nil, "https://api.test/users", ""},
		{"base path is kept", "https://api.test/v1", "/users", nil, "https://api.test/v1/users", ""},
		{"absolute URL ignores the base", "https://api.test", "https://other.test/x", nil, "https://other.test/x", ""},
		{"absolute URL needs no base", "", "https://other.test/x", nil, "https://other.test/x", ""},
		{"query appended and encoded", "https://api.test", "/s", []string{"q=a b", "n=1"}, "https://api.test/s?n=1&q=a+b", ""},
		{"repeated keys are kept", "https://api.test", "/s", []string{"t=a", "t=b"}, "https://api.test/s?t=a&t=b", ""},
		{"existing query is preserved", "https://api.test", "/s?x=1", []string{"y=2"}, "https://api.test/s?x=1&y=2", ""},
		{"a bare key has an empty value", "https://api.test", "/s", []string{"flag"}, "https://api.test/s?flag=", ""},
		{"path without a base", "", "/users", nil, "", "API_BASE_URL not set"},
		{"unparseable URL with a query", "", "http://[::1", []string{"a=b"}, "", "bad url"},
	}
	for _, c := range cases {
		got, err := ResolveURL(c.base, c.target, c.query)
		switch {
		case c.wantErr != "":
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err = %v, want it to contain %q", c.name, err, c.wantErr)
			}
		case err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case got != c.want:
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNewRequestBodies(t *testing.T) {
	ctx := context.Background()
	readBody := func(t *testing.T, s Spec) (string, string) {
		t.Helper()
		req, err := NewRequest(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		b := ""
		if req.Body != nil {
			raw, _ := io.ReadAll(req.Body)
			b = string(raw)
		}
		return b, req.Header.Get("Content-Type")
	}

	t.Run("raw body sets no content type of its own", func(t *testing.T) {
		b, ct := readBody(t, Spec{Method: "POST", URL: "http://x/", Body: `{"a":1}`})
		if b != `{"a":1}` || ct != "" {
			t.Errorf("body %q, content-type %q", b, ct)
		}
	})
	t.Run("-d fields are joined and form-encoded", func(t *testing.T) {
		b, ct := readBody(t, Spec{Method: "POST", URL: "http://x/", Data: []string{"a=1", "b=2"}})
		if b != "a=1&b=2" || ct != "application/x-www-form-urlencoded" {
			t.Errorf("body %q, content-type %q", b, ct)
		}
	})
	t.Run("an explicit content type wins over the generated one", func(t *testing.T) {
		_, ct := readBody(t, Spec{Method: "POST", URL: "http://x/", Data: []string{"a=1"},
			Headers: []Header{{"Content-Type", "text/plain"}}})
		if ct != "text/plain" {
			t.Errorf("content-type = %q", ct)
		}
	})
	t.Run("no body means no body", func(t *testing.T) {
		if b, ct := readBody(t, Spec{Method: "GET", URL: "http://x/"}); b != "" || ct != "" {
			t.Errorf("body %q, content-type %q", b, ct)
		}
	})
	t.Run("-F builds a multipart form with a file part", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "note.txt")
		if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
			t.Fatal(err)
		}
		req, err := NewRequest(ctx, Spec{Method: "POST", URL: "http://x/", Fields: []string{"name=ann", "doc=@" + file}})
		if err != nil {
			t.Fatal(err)
		}
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("not a valid multipart body: %v", err)
		}
		if got := req.MultipartForm.Value["name"]; len(got) != 1 || got[0] != "ann" {
			t.Errorf("text field = %v", got)
		}
		fh := req.MultipartForm.File["doc"]
		if len(fh) != 1 || fh[0].Filename != "note.txt" {
			t.Fatalf("file part = %v", fh)
		}
		f, _ := fh[0].Open()
		defer f.Close()
		if raw, _ := io.ReadAll(f); string(raw) != "hello" {
			t.Errorf("file content = %q", raw)
		}
	})
	t.Run("bad input is an error, not a panic", func(t *testing.T) {
		if _, err := NewRequest(ctx, Spec{Method: "POST", URL: "http://x/", Fields: []string{"novalue"}}); err == nil {
			t.Error("-F without = must fail")
		}
		if _, err := NewRequest(ctx, Spec{Method: "POST", URL: "http://x/", Fields: []string{"f=@/no/such/file"}}); err == nil {
			t.Error("-F with a missing file must fail")
		}
		if _, err := NewRequest(ctx, Spec{Method: "BAD METHOD", URL: "http://x/"}); err == nil {
			t.Error("an invalid method must fail")
		}
	})
}
