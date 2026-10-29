import http.server
import pathlib
import sys
import urllib.parse

ROOT = pathlib.Path(sys.argv[1]).resolve()
ADDRESS = ("127.0.0.1", 7380)


class Providers(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        path = urllib.parse.urlsplit(self.path).path
        file = (ROOT / path.lstrip("/")).resolve()
        if file.is_dir():
            file /= "index.html"
        if not file.is_relative_to(ROOT) or not file.is_file():
            self.send_error(404)
            return
        data = file.read_bytes().replace(
            b"file:///testdata/responses/", b"http://127.0.0.1:7380/"
        ).replace(b"file:///coverartarchive/", b"http://127.0.0.1:7380/coverartarchive/")
        self.send_response(200)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


http.server.ThreadingHTTPServer(ADDRESS, Providers).serve_forever()
