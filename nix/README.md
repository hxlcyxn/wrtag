# Nix development and automatic tests

Use the Git-backed flake reference `.`. Do not use `path:.`, which can copy
ignored local files into the Nix store before application source filtering.
The flake supports `x86_64-linux` and `aarch64-linux`.

```bash
nix develop
nix build .#default --no-link
nix flake check -L --keep-failed
```

`nix flake check` runs the full Go race suite, filesystem VM tests, and VM
boot/reboot smoke checks for the current architecture. Individual checks:

```bash
nix build .#checks.x86_64-linux.unit --no-link -L
nix build .#checks.x86_64-linux.vm --no-link -L --keep-failed
nix build .#checks.x86_64-linux.vm-boot --no-link -L --keep-failed
```

VM execution requires Linux and accessible `/dev/kvm`, including access for the
Nix build user. Each VM uses 4 GiB RAM, two vCPUs and two disposable 2 GiB disks.
Nested hosts must expose virtualization. Missing KVM is unavailable coverage,
not a passing test. Evaluating another architecture does not execute its tests.

The filesystem VM runs the complete root-package, CLI and web suites against
ext4 and XFS, including album replacement, rollback, recovery-path detection,
foreign-owner placement, observer writes and timestamp checks. Observer tests
run after the importer suite. The full race suite covers all Go packages.

XFS reflinks and cross-filesystem rename failures are probed explicitly. Test
skips fail acceptance except for named filesystem limitations: case-sensitive
and non-Unicode-normalizing names on both disks, and unsupported reflinks on
ext4. These cases are reported as unavailable, not passed. XFS reflink skips
are failures. Additional alias tests run without skips in an ext4 casefold
directory, exercising case-insensitive and Unicode-normalized names on Linux.
This does not establish native macOS or Windows behavior. Per-suite logs and
JSON summaries are copied to the test result.
The CLI baseline uses umask `0022` because its directory-mode fixture asserts
`0755`. A second CLI run uses `0007`, excluding only that fixture; it is still
covered by the baseline. Other suites and the service use `0007`.

Service checks use synthetic repository audio fixtures and a loopback fake
HTTP metadata/cover provider. They exercise real CLI and authenticated web
imports, same-path retagging, album replacement, shared-group output,
source preservation/removal, provider failure,
SSE delivery, environment-file key loading, private state/cache, and job
persistence/interruption handling across service restart. No private albums,
live providers or production credentials are required. The fixed API key is
public disposable test data. This is not a production service module or a
complete security or recovery acceptance suite.

The boot check uses the same node configuration as the optional interactive
image, verifies tools and mounts, then reboots and verifies disk persistence.
No manual test is needed for the checks. The interactive image is available as
`.#vm`; it autologs in as root and must remain disposable. Its separate QEMU
launcher is not exercised by the NixOS test driver.

Successful VM outputs contain suite logs, JSON summaries, service journals and
filesystem probe evidence. Use `nix build --print-out-paths` to locate them.
`--keep-failed` retains the build directory printed by Nix after a failure.
Native macOS/Windows behavior and a complete ext4/XFS operation/recovery matrix
remain outside this Linux test coverage.
