# Sync timestamp regression tests

Run the portable regressions offline:

```sh
GOPROXY=off GOTOOLCHAIN=local go test ./cmd/wrtag -run TestSync -count=1
```

These cover dry-run, successful in-place sync, moved-away sources, failed
processing and missing paths. Linux also checks that both timestamps become the
same kernel-supplied time. Reads during dry-run can still update access times;
the regression checks that the sync marker does not change modification time.

`TestSyncTimestampForeignOwner` is an opt-in Linux permission check. It skips
unless `WRTAG_SYNC_TIMESTAMP_FIXTURE` names an existing fixture directory. Run it
as an unprivileged caller, with these two children owned by a different user:

- `writable`: group-writable/searchable by the caller, with an old mtime.
- `non-writable`: searchable but not writable by the caller.

The test verifies actual write access, different ownership, successful timestamp
updates on the first directory, and permission denial with unchanged timestamps
on the second. Same-user tests or root execution do not establish this property.

For example, compile a test binary on the host, then copy it into a disposable
Linux guest with its own account database:

```sh
CGO_ENABLED=0 GOPROXY=off GOTOOLCHAIN=local go test -c -o /tmp/wrtag-sync.test ./cmd/wrtag
```

Run the following setup **only inside that disposable guest**, as guest root.
Do not create accounts or change ownership on the host or use a real library.
The guest needs no network; use an already available image without pulling it.

```sh
groupadd music
useradd -M -g music producer
useradd -M -g music importer
mkdir -p /tmp/sync-fixture/writable /tmp/sync-fixture/non-writable
chown -R producer:music /tmp/sync-fixture
chmod 755 /tmp/sync-fixture
chmod 2770 /tmp/sync-fixture/writable
chmod 2550 /tmp/sync-fixture/non-writable
touch -a -m -t 200001010000 /tmp/sync-fixture/writable /tmp/sync-fixture/non-writable
runuser -u importer -- env WRTAG_SYNC_TIMESTAMP_FIXTURE=/tmp/sync-fixture \
  /tmp/wrtag-sync.test -test.run TestSyncTimestampForeignOwner -test.v
```

Discard only the disposable guest afterward. This tests the CLI timestamp helper,
not album placement, rollback, web imports or non-owner behavior on fallback
platforms. The fallback uses explicit current timestamps and may require stronger
permissions than a kernel-now update.
