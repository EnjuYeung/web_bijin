"""Local OpenAI-format recognition service for browser tests. Never calls a real AI.

The model name picks the behavior: fixture-model answers, fixture-slow answers
after 3 seconds, fixture-bad answers words instead of JSON. GET /requests lists
what it was asked; POST /reset forgets it.
"""
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

answers = {
    "misc": {"author": "Fixture Studio", "models": ["Misc Model"]},
    "batch": {"author": "Someone Else", "models": ["Batch Girl"]},
    "子目录": {"author": "existing studio", "models": []},
}
requests = []
lock = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def send(self, status, body):
        raw = json.dumps(body, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        if self.path == "/requests":
            with lock:
                return self.send(200, requests)
        self.send(200, {"ok": True})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))) or b"{}")
        if self.path == "/reset":
            with lock:
                requests.clear()
            return self.send(200, {"ok": True})
        if self.path != "/v1/chat/completions":
            return self.send(404, {"error": {"message": "no such path"}})
        prompt = json.loads(body["messages"][1]["content"])
        with lock:
            requests.append({
                "model": body.get("model"),
                "authorization": self.headers.get("Authorization"),
                "reasoning": body.get("reasoning_effort"),
                "format": (body.get("response_format") or {}).get("type"),
                "temperature": "temperature" in body,
                "albums": [album["album"] for album in prompt["albums"]],
                "roster": prompt["roster"],
            })
        model = body.get("model")
        if model == "fixture-slow":
            time.sleep(3)
        if model == "fixture-bad":
            content = "I am not sure who these people are."
        else:
            content = json.dumps({"albums": [{"album": album["album"], **answers.get(album["album"], {"author": "", "models": []})} for album in prompt["albums"]]}, ensure_ascii=False)
        self.send(200, {"choices": [{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": "stop"}]})


ThreadingHTTPServer(("127.0.0.1", 18095), Handler).serve_forever()
