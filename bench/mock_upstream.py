"""Minimal OpenAI-compatible mock upstream for benchmark runs.

It answers /v1/chat/completions (JSON and SSE) and /v1/embeddings with fixed
payloads, so benchmarks measure the Go gateway instead of the model provider.

Usage:
    python bench/mock_upstream.py            # listens on 127.0.0.1:9999
    MOCK_PORT=9998 python bench/mock_upstream.py
"""

import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(os.environ.get("MOCK_PORT", "9999"))


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_POST(self):  # noqa: N802 - required by BaseHTTPRequestHandler
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b"{}"
        try:
            body = json.loads(raw or b"{}")
        except json.JSONDecodeError:
            body = {}

        if self.path.endswith("/embeddings"):
            self._send_json(
                {
                    "object": "list",
                    "model": body.get("model", "mock"),
                    "data": [
                        {"object": "embedding", "index": 0, "embedding": [0.01 * i for i in range(8)]}
                    ],
                }
            )
            return

        if self.path.endswith("/chat/completions"):
            if body.get("stream"):
                self._send_sse()
            else:
                self._send_json(
                    {
                        "id": "chatcmpl-mock",
                        "object": "chat.completion",
                        "model": body.get("model", "mock"),
                        "choices": [
                            {
                                "index": 0,
                                "message": {"role": "assistant", "content": "pong"},
                                "finish_reason": "stop",
                            }
                        ],
                        "usage": {"prompt_tokens": 8, "completion_tokens": 2, "total_tokens": 10},
                    }
                )
            return

        self.send_error(404, "unsupported path")

    def _send_json(self, payload):
        data = json.dumps(payload).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _send_sse(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        for i in range(5):
            chunk = {
                "id": "chatcmpl-mock",
                "object": "chat.completion.chunk",
                "choices": [{"index": 0, "delta": {"content": "tok%d" % i}}],
            }
            self.wfile.write(b"data: " + json.dumps(chunk).encode() + b"\n\n")
            self.wfile.flush()
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()
        self.close_connection = True

    def log_message(self, *_args):
        pass


if __name__ == "__main__":
    print("mock upstream listening on http://127.0.0.1:%d/v1" % PORT, flush=True)
    ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
