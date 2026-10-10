package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// A header from a config file (possibly a cloned repo's) must not be able to
// start another header line, and its error must not echo the value.
func TestReadRejectsMalformedHeaders(t *testing.T) {
	for name, body := range map[string]string{
		"line break in value": "[env.dev]\nheaders = { \"X-A\" = \"v\\r\\nX-Evil: 1\" }\n",
		"space in name":       "[env.dev]\nheaders = { \"X A\" = \"v\" }\n",
		"empty name":          "[env.dev]\nheaders = { \"\" = \"v\" }\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.toml")
			write(t, path, body)
			_, err := Read(path)
			if err == nil || !strings.Contains(err.Error(), "env.dev") || strings.Contains(err.Error(), "X-Evil") {
				t.Fatalf("want an env.dev header error without the value, got %v", err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "ok.toml")
	write(t, path, "[env.dev]\nheaders = { \"X-Tenant\" = \"dev\", \"Accept-Language\" = \"en, fr;q=0.5\" }\n")
	if _, err := Read(path); err != nil {
		t.Fatal(err)
	}
}

func TestSetRejectsMalformedHeaders(t *testing.T) {
	for _, tc := range [][2]string{{"env.dev.headers.X-A", "v\nX-Evil: 1"}, {"env.dev.headers.X A", "v"}} {
		var c Config
		if err := Set(&c, tc[0], tc[1]); err == nil {
			t.Errorf("%q = %q accepted", tc[0], tc[1])
		}
	}
	var c Config
	if err := Set(&c, "env.dev.headers.X-Tenant", "dev"); err != nil || c.Env["dev"].Headers["X-Tenant"] != "dev" {
		t.Fatalf("%v %+v", err, c.Env)
	}
}

func TestSubscriptionKeysAreValidatedAndRoundTrip(t *testing.T) {
	for name, body := range map[string]string{
		"a url":        "[env.dev]\nsubscriptions = \"wss://evil.example/graphql\"\n",
		"protocol":     "[env.dev]\nws_protocol = \"graphql-sse\"\n",
		"double slash": "[env.dev]\nsubscriptions = \"//evil.example/graphql\"\n",
	} {
		path := filepath.Join(t.TempDir(), "c.toml")
		write(t, path, body)
		if _, err := Read(path); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	path := filepath.Join(t.TempDir(), "ok.toml")
	write(t, path, "[env.dev]\nurl = \"https://x\"\nsubscriptions = \"/graphql\"\nws_protocol = \"graphql-ws\"\nws_token_in_init = true\n")
	c, err := Read(path)
	if err != nil || c.Env["dev"].Subscriptions != "/graphql" || c.Env["dev"].WSProtocol != "graphql-ws" || c.Env["dev"].WSTokenInInit == nil || !*c.Env["dev"].WSTokenInInit {
		t.Fatalf("%v %+v", err, c.Env["dev"])
	}
	// Set keeps the other keys, also through a login edit (it used to rebuild the Env by hand)
	for _, kv := range [][2]string{{"env.dev.subscriptions", "/ws"}, {"env.dev.ws_protocol", "graphql-transport-ws"}, {"env.dev.ws_token_in_init", "false"}, {"env.dev.login.path", "/auth"}} {
		if err := Set(&c, kv[0], kv[1]); err != nil {
			t.Fatalf("%v: %v", kv, err)
		}
	}
	e := c.Env["dev"]
	if e.Subscriptions != "/ws" || e.WSProtocol != "graphql-transport-ws" || e.WSTokenInInit == nil || *e.WSTokenInInit || e.Login == nil {
		t.Fatalf("%+v", e)
	}
	if err := Set(&c, "env.dev.subscriptions", "https://evil.example/x"); err == nil {
		t.Error("a URL was accepted")
	}
	if v, ok := Get(c, "env.dev.ws_token_in_init"); !ok || v != "false" {
		t.Errorf("%q %v", v, ok)
	}
}

func TestBothTemplatesDocumentSubscriptions(t *testing.T) {
	for name, tpl := range map[string]string{"global": Render(Global, "", ""), "project": Render(Project, "", "")} {
		for _, want := range []string{`# subscriptions = "/graphql"`, `# ws_protocol = "graphql-transport-ws"`, `# ws_token_in_init = true`, "api gql"} {
			if !strings.Contains(tpl, want) {
				t.Errorf("%s template lacks %q", name, want)
			}
		}
	}
}

func TestMergeKeepsAndOverridesSubscriptionKeys(t *testing.T) {
	yes := true
	merged := Merge(Config{Env: map[string]Env{"dev": {URL: "http://a", Subscriptions: "/g", WSProtocol: "graphql-ws", WSTokenInInit: &yes}}},
		Config{Env: map[string]Env{"dev": {}}})
	e := merged.Env["dev"]
	if e.Subscriptions != "/g" || e.WSProtocol != "graphql-ws" || e.WSTokenInInit == nil || !*e.WSTokenInInit {
		t.Fatalf("an empty project env must not erase them: %+v", e)
	}
}
