import base64
import hashlib
import os
import pathlib
import pwd
import shutil
import sqlite3
import stat
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

SOURCE = pathlib.Path(sys.argv[1])
PHASE = sys.argv[2]
BASE = "http://127.0.0.1:7373"
KEY = "disposable-vm-only"
RELEASE = "e47d04a4-7460-427d-a731-cc82386d85f1"
DB = "/var/lib/wrtagweb/jobs.db"
DEST = pathlib.Path("/srv/xfs/library/Kat Moda")
TIMEOUT = 45
MUSIC_GID = 2000


def request(path, data=None, key=KEY, expected=200):
    headers = {}
    if key is not None:
        headers["Authorization"] = "Basic " + base64.b64encode((":" + key).encode()).decode()
    encoded = urllib.parse.urlencode(data).encode() if data is not None else None
    req = urllib.request.Request(BASE + path, data=encoded, headers=headers)
    try:
        response = urllib.request.urlopen(req, timeout=TIMEOUT)
    except urllib.error.HTTPError as error:
        response = error
    assert response.status == expected, (path, response.status, response.read())
    return response


def job_rows():
    with sqlite3.connect(DB, timeout=TIMEOUT) as db:
        db.row_factory = sqlite3.Row
        return [dict(row) for row in db.execute("select * from jobs order by id")]


def wait_job(job_id, status):
    deadline = time.monotonic() + TIMEOUT
    while time.monotonic() < deadline:
        job = next(row for row in job_rows() if row["id"] == job_id)
        if job["status"] not in ("", "in-progress"):
            assert job["status"] == status, job
            return job
        time.sleep(0.1)
    raise AssertionError(("job timeout", job_rows()))


def tracks(path):
    path.mkdir(mode=0o2770)
    os.chown(path, pwd.getpwnam("producer").pw_uid, MUSIC_GID)
    path.chmod(0o2770)
    for number in range(1, 4):
        track = path / f"{number}.flac"
        shutil.copyfile(SOURCE / "tags/testdata/empty.flac", track)
        os.chown(track, pwd.getpwnam("producer").pw_uid, MUSIC_GID)
        track.chmod(0o660)
    return path


def hashes(path):
    return {file.name: hashlib.sha256(file.read_bytes()).hexdigest() for file in path.glob("*.flac")}


def check_destination():
    files = list(DEST.glob("*.flac"))
    assert len(files) == 3, list(DEST.iterdir())
    for file in files:
        info = file.stat()
        assert stat.S_IMODE(info.st_mode) == 0o660, info
        assert info.st_gid == MUSIC_GID, info
        subprocess.run(["runuser", "-u", "observer", "--", "test", "-w", str(file)], check=True)
        tags = subprocess.check_output(["metaflac", "--export-tags-to=-", str(file)], text=True)
        assert RELEASE in tags and "TITLE=" in tags, tags
    assert (DEST / "cover.jpg").is_file()
    assert not list(DEST.parent.glob(".wrtag-*"))


def submit(operation, path, release=RELEASE, status="complete"):
    before = job_rows()
    with request("/sse") as events:
        assert events.headers["Content-Type"] == "text/event-stream"
        with request("/op/" + operation, {"path": str(path), "mbid": release, "confirm": "true"}):
            pass
        line = events.readline()
        assert line.startswith(b"data: "), line
    after = job_rows()
    assert len(after) == len(before) + 1, after
    return wait_job(after[-1]["id"], status)


if PHASE == "imports":
    for endpoint in ("/", "/jobs", "/dirs?path=/srv", "/debug/pprof/"):
        for key in (None, "wrong-key"):
            with request(endpoint, key=key, expected=401):
                pass
    for key in (None, "wrong-key"):
        with request("/op/copy", {"path": "/srv/xfs/incoming"}, key=key, expected=401):
            pass
    with request("/") as response:
        cookie = response.headers["Set-Cookie"]
        assert "HttpOnly" in cookie and "Secure" in cookie and "SameSite=Lax" in cookie
    cookie_request = urllib.request.Request(BASE + "/jobs", headers={"Cookie": "api-key=" + KEY})
    with urllib.request.urlopen(cookie_request, timeout=TIMEOUT) as response:
        assert response.status == 200
    with request("/op/invalid", {"path": "/srv/xfs/incoming"}, expected=400):
        pass
    with request("/op/copy", {"path": "relative"}, expected=400):
        pass
    assert not job_rows()
    bad = tracks(pathlib.Path("/srv/xfs/incoming/provider-failure"))
    before = hashes(bad)
    job = submit("copy", bad, "00000000-0000-0000-0000-000000000002", "error")
    assert job["error"] and hashes(bad) == before, job
    for filesystem, operation in (("xfs", "copy"), ("xfs", "reflink"), ("xfs", "move"), ("ext4", "move")):
        source = tracks(pathlib.Path(f"/srv/{filesystem}/incoming/web-{operation}"))
        before = hashes(source)
        job = submit(operation, source)
        assert not job["error"] and job["dest_path"] == str(DEST), job
        check_destination()
        if operation == "move":
            assert not source.exists()
        else:
            assert hashes(source) == before
        if filesystem == "xfs" and operation == "copy":
            submit("move", DEST)
            check_destination()
            replacement = tracks(pathlib.Path("/srv/xfs/incoming/web-replacement"))
            submit("move", replacement)
            check_destination()
            assert not replacement.exists()
        shutil.rmtree(DEST)
    for filesystem, operation in (("ext4", "copy"), ("ext4", "move"), ("xfs", "reflink")):
        source = tracks(pathlib.Path(f"/srv/{filesystem}/incoming/cli-{operation}"))
        before = hashes(source)
        command = ["runuser", "-u", "importer", "--", "env", "WRTAG_MB_BASE_URL=http://127.0.0.1:7380/musicbrainz/ws/2/", "WRTAG_CAA_BASE_URL=http://127.0.0.1:7380/coverartarchive/", "WRTAG_MB_RATE_LIMIT=0", "WRTAG_CAA_RATE_LIMIT=0", "sh", "-c", 'umask 0007; exec wrtag -file-mode 0660 -path-format "$1" "$2" -yes -mbid "$3" "$4"', "sh", "/srv/xfs/library/{{ .Release.Title | safepath }}/{{ pad0 2 .Track.Position }} {{ .Track.Title | safepath }}{{ .Ext }}", operation, RELEASE, str(source)]
        subprocess.run(command, check=True, timeout=TIMEOUT)
        check_destination()
        if operation == "move":
            assert not source.exists()
        else:
            assert hashes(source) == before
        shutil.rmtree(DEST)
    pathlib.Path("/tmp/jobs-before-restart").write_text(str(len(job_rows())))
elif PHASE == "prepare-restart":
    with sqlite3.connect(DB, timeout=TIMEOUT) as db:
        db.execute("insert into jobs (source_path, operation, status, time) values (?, ?, ?, datetime('now'))", ("/srv/xfs/incoming/interrupted", "copy", "in-progress"))
elif PHASE == "restart":
    rows = job_rows()
    assert len(rows) == int(pathlib.Path("/tmp/jobs-before-restart").read_text()) + 1
    assert rows[-1]["status"] == "error" and "interrupted" in rows[-1]["error"], rows[-1]
    assert sum(row["status"] == "complete" for row in rows) == 6, rows
    with request("/jobs") as response:
        assert b"interrupted" in response.read()
else:
    raise AssertionError(PHASE)
print("service checks passed:", PHASE)
