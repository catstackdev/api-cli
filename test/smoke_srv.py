"""Stub API for test/smoke.zsh: REST login, a token that can be rotated, GraphQL
introspection, a flaky endpoint and two data sets for diff. Not a real server."""
import base64, json, sys, time
from http.server import BaseHTTPRequestHandler, HTTPServer

name, logfile, schema_file = sys.argv[1], sys.argv[2], sys.argv[3]
SCHEMA = open(schema_file).read()
state = {"n": 0, "logins": 0, "flaky": 0}
ITEMS = {
    "a": [{"id": 1, "n": "a"}, {"id": 2, "n": "b"}],
    "b": [{"id": 2, "n": "B"}, {"id": 1, "n": "a"}],
}

def b64(obj):
    return base64.urlsafe_b64encode(json.dumps(obj).encode()).rstrip(b"=").decode()

def token():
    return b64({"alg": "none"}) + "." + b64({"exp": int(time.time()) + 3600, "n": state["n"]}) + ".sig"

class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass

    def reply(self, obj, code=200, headers=None):
        body = obj if isinstance(obj, bytes) else json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        for k, v in (headers or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)

    def note(self):
        with open(logfile, "a") as f:
            f.write(f'{self.command} {self.path} auth={self.headers.get("Authorization", "")}\n')

    def authed(self):
        return self.headers.get("Authorization", "") == "Bearer " + token()

    def do_GET(self):
        self.note()
        p = self.path.split("?")[0]
        if p == "/me":
            return self.reply({"me": name}) if self.authed() else self.reply({"error": "unauthorized"}, 401)
        if p == "/items":
            return self.reply({"data": ITEMS[name]})
        if p == "/state":
            return self.reply({"token": token(), "logins": state["logins"]})
        if p == "/flaky":
            state["flaky"] += 1
            if state["flaky"] == 1:
                return self.reply({"error": "slow down"}, 429, {"Retry-After": "0"})
            return self.reply({"ok": True})
        self.reply({"path": p})

    def do_POST(self):
        self.note()
        n = int(self.headers.get("Content-Length", 0) or 0)
        raw = self.rfile.read(n)
        p = self.path.split("?")[0]
        if p == "/auth":
            state["logins"] += 1
            return self.reply({"data": {"token": token()}}, 200, {"X-Req-Id": "rid-%d" % state["logins"]})
        if p == "/rotate":
            state["n"] += 1
            return self.reply({"rotated": True})
        if p.startswith("/graphql/"):
            req = json.loads(raw or b"{}")
            q, v = req.get("query", ""), req.get("variables") or {}
            sch = json.loads(SCHEMA)
            if "__type(" in q:
                t = next((x for x in sch["types"] if x["name"] == v.get("n")), None)
                return self.reply({"data": {"__type": t}})
            if "types {" in q:
                return self.reply({"data": {"__schema": sch}})
            return self.reply({"data": {"__schema": {"queryType": {"fields": [{"name": "me"}, {"name": "user"}]},
                                                      "mutationType": {"fields": [{"name": "login"}]}}}})
        self.reply({"path": p})

srv = HTTPServer(("127.0.0.1", 0), H)
print(srv.server_address[1], flush=True)
srv.serve_forever()
