"""Local S3 fixture for browser tests. Never connects to a real bucket."""
from datetime import datetime, timezone
from email.utils import format_datetime
from hashlib import md5
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, unquote, urlsplit
from xml.sax.saxutils import escape

root = Path(__file__).resolve().parents[2] / "photos"
objects = {
    "子目录/云端 日落.jpg": (root / "wide.jpg").read_bytes(),
    "cloud/portrait.jpg": (root / "tall.jpg").read_bytes(),
    "notes.txt": b"Not a photo",
}
upload_ids = {}
modified = datetime(2026, 9, 1, tzinfo=timezone.utc)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def send(self, status, body=b"", kind="application/xml", etag=None, cache=None, upload_id=None):
        self.send_response(status)
        self.send_header("Content-Type", kind)
        self.send_header("Content-Length", str(len(body)))
        if self.headers.get("Origin") == "http://127.0.0.1:18092":
            self.send_header("Access-Control-Allow-Origin", self.headers["Origin"])
            self.send_header("Access-Control-Allow-Methods", "GET, HEAD, PUT")
            self.send_header("Access-Control-Allow-Headers", "content-type,if-none-match,x-amz-meta-bijin-upload")
        if upload_id:
            self.send_header("X-Amz-Meta-Bijin-Upload", upload_id)
        if etag:
            self.send_header("ETag", etag)
            self.send_header("Last-Modified", format_datetime(modified, usegmt=True))
        if cache:
            self.send_header("Cache-Control", cache)
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)

    def do_OPTIONS(self):
        self.send(204)

    def do_PUT(self):
        parsed=urlsplit(self.path)
        bucket,_,key=unquote(parsed.path).lstrip("/").partition("/")
        if bucket != "family":
            return self.send(404)
        query=parse_qs(parsed.query)
        if not query.get("X-Amz-Credential",[""])[0].startswith("test-ak/"):
            return self.send(403)
        if key in objects and self.headers.get("If-None-Match")=="*":
            return self.send(412)
        objects[key]=self.rfile.read(int(self.headers["Content-Length"]))
        upload_ids[key]=self.headers.get("X-Amz-Meta-Bijin-Upload")
        self.send(200)

    def do_HEAD(self):
        self.do_GET()

    def do_GET(self):
        path = urlsplit(self.path)
        bucket, _, key = unquote(path.path).lstrip("/").partition("/")
        query = parse_qs(path.query, keep_blank_values=True)
        # Header or presigned-URL credentials; signatures are not verified.
        presigned = query.get("X-Amz-Credential", [""])[0].startswith("test-ak/")
        if "Credential=test-ak/" not in self.headers.get("Authorization", "") and not presigned:
            return self.send(403, b"<Error><Code>InvalidAccessKeyId</Code><Message>Unknown key</Message></Error>")
        if bucket != "family":
            return self.send(404, b"<Error><Code>NoSuchBucket</Code><Message>Unknown bucket</Message></Error>")
        if not key and "location" in query:
            return self.send(200, b'<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>')
        if not key:
            prefix = query.get("prefix", [""])[0]
            keys = sorted(k for k in objects if k.startswith(prefix))
            items = "".join(f"<Contents><Key>{escape(k)}</Key><LastModified>2026-09-01T00:00:00.000Z</LastModified><ETag>\"{md5(objects[k]).hexdigest()}\"</ETag><Size>{len(objects[k])}</Size></Contents>" for k in keys)
            body = f'<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>family</Name><Prefix>{escape(prefix)}</Prefix><KeyCount>{len(keys)}</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>{items}</ListBucketResult>'
            return self.send(200, body.encode())
        if key not in objects:
            return self.send(404, b"<Error><Code>NoSuchKey</Code><Message>Unknown key</Message></Error>")
        data = objects[key]
        kind = query.get("response-content-type", ["image/jpeg" if key.endswith(".jpg") else "text/plain"])[0]
        self.send(200, data, kind, '"' + md5(data).hexdigest() + '"', query.get("response-cache-control", [None])[0], upload_ids.get(key))


if __name__ == "__main__":
    print("Local S3 fixture listening on 127.0.0.1:18093", flush=True)
    ThreadingHTTPServer(("127.0.0.1", 18093), Handler).serve_forever()
