import shlex

SOURCE = "@source@"
BINARIES = "@testBinaries@/bin"
SERVICE_TESTS = "@serviceTests@"
AUDIT = "@audit@"
SUITE_TIMEOUT = 360

start_all()
machine.wait_for_unit("test-filesystems.service")
assert "kvm support: enabled" in machine.send_monitor_command("info kvm")
machine.wait_for_unit("wrtagweb.service")
machine.wait_for_open_port(7373)
machine.wait_for_open_port(7380)
machine.succeed("test $(stat -c %a /var/lib/wrtagweb) = 700")
machine.fail("runuser -u observer -- ls /var/lib/wrtagweb")
machine.fail("runuser -u observer -- ls /var/cache/wrtagweb")
machine.succeed("test $(systemctl show wrtagweb -p User --value) = importer; test $(systemctl show wrtagweb -p UMask --value) = 0007")
machine.succeed("pid=$(systemctl show wrtagweb -p MainPID --value); grep -E '^NoNewPrivs:[[:space:]]+1$' /proc/$pid/status; test $(stat -c %u /proc/$pid) = $(id -u importer)")
machine.succeed("ss -ltn | grep '127.0.0.1:7373'")
machine.succeed("pid=$(systemctl show wrtagweb -p MainPID --value); grep -E '^Groups:.*\\b2000\\b' /proc/$pid/status")
machine.succeed("dd if=/dev/urandom of=/srv/xfs/probe bs=1M count=1; cp --reflink=always /srv/xfs/probe /srv/xfs/clone")
machine.succeed("filefrag -v /srv/xfs/probe /srv/xfs/clone | tee /tmp/reflink.log | grep shared")
machine.fail("cp --reflink=always /srv/xfs/probe /srv/ext4/reflink")
machine.succeed("python3 -c 'import os, errno; p=\"/srv/ext4/probe\"; open(p,\"w\").close();\ntry: os.rename(p,\"/srv/xfs/exdev\")\nexcept OSError as e: assert e.errno == errno.EXDEV\nelse: raise AssertionError(\"expected EXDEV\")'")


def run_suite(filesystem, user, binary, cwd, env, selection=".", skip="", mask="0007"):
    name = f"{filesystem}-{user}-{binary}-{mask}"
    log = f"/tmp/{name}.log"
    flags = f"-test.v=test2json -test.count=1 -test.timeout={SUITE_TIMEOUT}s -test.run {shlex.quote(selection)}"
    if skip:
        flags += " -test.skip " + shlex.quote(skip)
    command = f"cd {cwd}; umask {mask}; {env} {BINARIES}/{binary}.test {flags} > {log} 2>&1"
    try:
        machine.succeed("runuser -u " + user + " -- sh -c " + shlex.quote(command), timeout=SUITE_TIMEOUT + 30)
        print(machine.succeed(f"python3 {AUDIT} {BINARIES}/test2json {filesystem} {log}"))
        machine.copy_from_machine(f"/tmp/{name}.json")
    except Exception:
        print(machine.succeed(f"tail -n 160 {log}"))
        raise
    finally:
        machine.copy_from_machine(log)
        machine.execute(f"test ! -f /tmp/{name}.jsonl || cp /tmp/{name}.jsonl /tmp/shared/")


try:
    for filesystem in ["ext4", "xfs"]:
        machine.succeed(f"test $(findmnt -n -o FSTYPE /srv/{filesystem}) = {filesystem}")
        cwd = f"/srv/{filesystem}/source"
        tmp = f"/srv/{filesystem}/tmp"
        root = f"/srv/{filesystem}/placement"
        machine.succeed(f"cp -R {SOURCE} {cwd}; chmod -R u+w {cwd}; chown -R importer:users {cwd}; install -d -o importer -m 700 {tmp}")
        machine.succeed(f"install -d -m 755 {root}; install -d -o producer -g music -m 2770 {root}/source; install -d -o importer -g music -m 2770 {root}/destination")
        for operation in ["move", "copy", "reflink", "retag"]:
            for mode in ["660", "640"]:
                for ext in ["flac", "mp3", "m4a"]:
                    path = f"{root}/source/{operation}-{mode}-{ext}"
                    machine.succeed(f"install -d -o producer -g music -m 2770 {path}; install -o producer -g music -m {mode} {SOURCE}/tags/testdata/empty.{ext} {path}/track.{ext}")
        env = f"TMPDIR={tmp} WRTAG_SHARED_PLACEMENT_ROOT={root}"
        run_suite(filesystem, "importer", "placement", cwd, env, skip="^TestSharedGroupObserver$")
        run_suite(filesystem, "observer", "placement", cwd, f"WRTAG_SHARED_PLACEMENT_ROOT={root}", selection="^TestSharedGroupObserver$")
        timestamps = f"/srv/{filesystem}/timestamps"
        machine.succeed(f"install -d -m 755 {timestamps}; install -d -o producer -g music -m 2770 {timestamps}/writable; install -d -o producer -g music -m 2750 {timestamps}/non-writable; touch -d @946684800 {timestamps}/{{writable,non-writable}}")
        run_suite(filesystem, "importer", "cli", cwd + "/cmd/wrtag", f"TMPDIR={tmp} WRTAG_SYNC_TIMESTAMP_FIXTURE={timestamps}", mask="0022")
        run_suite(filesystem, "importer", "cli", cwd + "/cmd/wrtag", f"TMPDIR={tmp} WRTAG_SYNC_TIMESTAMP_FIXTURE={timestamps}", skip="^TestScripts$/^dir-mode$")
        run_suite(filesystem, "importer", "web", cwd + "/cmd/wrtagweb", f"TMPDIR={tmp}")
    machine.succeed("install -d -o importer -m 700 /srv/ext4/casefold; chattr +F /srv/ext4/casefold")
    run_suite("ext4-casefold", "importer", "placement", "/srv/ext4/source", "TMPDIR=/srv/ext4/casefold", selection="^Test(MoveRejectsSameFileAliases|AlbumRequiredUnicodeNormalizedAliasPreservesOriginals)$")
    service_command = f"python3 {SERVICE_TESTS} {SOURCE}"
    machine.succeed(service_command + " imports > /tmp/service.log 2>&1", timeout=360)
    machine.succeed("systemctl stop wrtagweb")
    machine.succeed(service_command + " prepare-restart >> /tmp/service.log 2>&1")
    machine.succeed("systemctl start wrtagweb")
    machine.wait_for_open_port(7373)
    machine.succeed(service_command + " restart >> /tmp/service.log 2>&1")
except Exception:
    print(machine.succeed("tail -n 160 /tmp/service.log 2>/dev/null || true; journalctl -u wrtagweb -u fake-providers -n 120 --no-pager"))
    raise
finally:
    machine.succeed("journalctl -u wrtagweb -u fake-providers --no-pager > /tmp/services.log")
    machine.copy_from_machine("/tmp/services.log")
    machine.copy_from_machine("/tmp/reflink.log")
    machine.execute("touch /tmp/service.log")
    machine.copy_from_machine("/tmp/service.log")
