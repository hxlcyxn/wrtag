import json
import pathlib
import subprocess
import sys

BINARY, FILESYSTEM, LOG = sys.argv[1:]
log = pathlib.Path(LOG)
with log.open("rb") as source:
    encoded = subprocess.check_output([BINARY], stdin=source, text=True)
log.with_suffix(".jsonl").write_text(encoded)
events = [json.loads(line) for line in encoded.split("\n") if line]
outputs = {}
skipped = []
passed = []
for event in events:
    test = event.get("Test", "")
    if event["Action"] == "output":
        outputs[test] = outputs.get(test, "") + event["Output"]
    elif event["Action"] == "skip":
        skipped.append(test)
    elif event["Action"] == "pass" and test:
        passed.append(test)
    elif event["Action"] == "fail":
        raise AssertionError(event)
assert passed, f"no tests passed: {LOG}"
for test in skipped:
    output = outputs[test]
    expected = (
        FILESYSTEM in ("ext4", "xfs")
        and test == "TestAlbumRequiredUnicodeNormalizedAliasPreservesOriginals"
        and "filesystem does not alias Unicode-normalized names" in output
    ) or (
        FILESYSTEM in ("ext4", "xfs")
        and test.startswith("TestMoveRejectsSameFileAliases/case-insensitive/")
        and "filesystem is case-sensitive" in output
    ) or (
        FILESYSTEM == "ext4"
        and "reflink" in test.lower()
        and "actual reflink unavailable:" in output
    )
    assert expected, f"unexpected skip: {test}: {output}"
summary = json.dumps({"suite": log.stem, "passed": len(passed), "unavailable": skipped})
log.with_suffix(".json").write_text(summary + "\n")
print(summary)
