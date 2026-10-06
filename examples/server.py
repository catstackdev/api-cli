#!/usr/bin/env python3
"""A tiny in-memory API for trying the .http examples. Python 3 stdlib only.

    python3 examples/server.py            # http://127.0.0.1:8787
    python3 examples/server.py 9000       # another port

Endpoints (what examples/*.http call):
    GET    /health               {"status": "ok"}
    GET    /users?page=&limit=   {"data": [...]}
    GET    /users/search?q=      {"data": [...]}   (name contains q)
    GET    /users/{id}           {"data": {...}} or 404
    POST   /users                201 {"data": {...}}  (JSON body: name, email)
    DELETE /users/{id}           204

It never deletes its seed users (ids below 100), so the examples can be re-run.
No auth: it ignores Authorization. Not for anything but trying the examples.
"""

import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

USERS = {
    1: {"id": 1, "name": "Ann", "email": "ann@example.test"},
    2: {"id": 2, "name": "Bob", "email": "bob@example.test"},
    3: {"id": 3, "name": "Annette", "email": "annette@example.test"},
    42: {"id": 42, "name": "Dent", "email": "dent@example.test"},
}
SEED_LIMIT = 100  # ids below this are never deleted
next_id = SEED_LIMIT


class Handler(BaseHTTPRequestHandler):
    def reply(self, status, payload=None):
        body = b"" if payload is None else json.dumps(payload).encode()
        self.send_response(status)
        if body:
            self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("X-Req-Id", "demo-1")
        self.end_headers()
        self.wfile.write(body)

    def user_id(self, path):
        tail = path.rsplit("/", 1)[-1]
        return int(tail) if tail.isdigit() else None

    def do_GET(self):
        url = urlparse(self.path)
        q = parse_qs(url.query)
        if url.path == "/health":
            return self.reply(200, {"status": "ok"})
        if url.path == "/users":
            page = int(q.get("page", ["1"])[0])
            limit = int(q.get("limit", ["10"])[0])
            rows = sorted(USERS.values(), key=lambda u: u["id"])
            return self.reply(200, {"data": rows[(page - 1) * limit : page * limit]})
        if url.path == "/users/search":
            term = q.get("q", [""])[0].lower()
            limit = int(q.get("limit", ["10"])[0])
            rows = [u for u in USERS.values() if term in u["name"].lower()]
            return self.reply(200, {"data": rows[:limit]})
        if url.path.startswith("/users/"):
            uid = self.user_id(url.path)
            if uid is not None and uid in USERS:
                return self.reply(200, {"data": USERS[uid]})
        self.reply(404, {"error": "not found"})

    def do_POST(self):
        global next_id
        if urlparse(self.path).path != "/users":
            return self.reply(404, {"error": "not found"})
        raw = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        try:
            body = json.loads(raw or b"{}")
        except ValueError:
            return self.reply(400, {"error": "body is not JSON"})
        user = {
            "id": next_id,
            "name": body.get("name", ""),
            "email": body.get("email", ""),
        }
        USERS[next_id] = user
        next_id += 1
        self.reply(201, {"data": user})

    def do_DELETE(self):
        uid = self.user_id(urlparse(self.path).path)
        if uid is not None and uid >= SEED_LIMIT:
            USERS.pop(uid, None)
        self.reply(204)


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8787
    print(f"example API on http://127.0.0.1:{port}  (Ctrl-C to stop)", file=sys.stderr)
    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
