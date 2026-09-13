# Upgrade C2 architecture spike

This document records the C2 prototype made from canonical `v0.2.8` commit
`662c045398f1461e6c9b65059626f922251ab27e`. It is design evidence, not the
eventual PR C implementation. PR C must be rebased or reimplemented after PR B
and must re-check every installation-ownership assumption below.

## Current release contract

`main.version` defaults to `dev`. GoReleaser builds `./cmd/dropin-miner` with
`-X main.version={{.Version}}`; for tag `v0.3.0`, GoReleaser supplies the bare
`0.3.0`, and `dropin-miner version` prints exactly `dropin-miner 0.3.0`. The
comment in `main.go` illustrates a v-prefixed manual ldflag, so updater parsing
normalizes either `X.Y.Z` or `vX.Y.Z` rather than comparing raw strings.
Development output such as `dev (revision+dirty)` is not a release identity and
must refuse self-upgrade before network or filesystem mutation.

Only stable `vX.Y.Z` releases are supported. Plain `upgrade` uses GitHub's
`releases/latest`; explicit `-version` resolves exactly `vX.Y.Z`. Drafts,
prereleases, build metadata, leading zeroes and malformed tags are refused.
Equal target and current is a successful no-op. A lower target is refused;
`-version` is not a hidden downgrade mechanism. The only downgrade path is the
locally preserved, separately validated `-rollback` slot.

`.goreleaser.yaml` produces these six archives:

| Target | Archive |
| --- | --- |
| linux/amd64 | `dropin-miner_X.Y.Z_linux_amd64.tar.gz` |
| linux/arm64 | `dropin-miner_X.Y.Z_linux_arm64.tar.gz` |
| darwin/amd64 | `dropin-miner_X.Y.Z_darwin_amd64.tar.gz` |
| darwin/arm64 | `dropin-miner_X.Y.Z_darwin_arm64.tar.gz` |
| windows/amd64 | `dropin-miner_X.Y.Z_windows_amd64.zip` |
| windows/arm64 | `dropin-miner_X.Y.Z_windows_arm64.zip` |

The checksum asset is `checksums.txt`. The runtime uses a small checked-in
naming function, not a generic Go-template evaluator. A release-check test
derives the complete matrix from the real `.goreleaser.yaml` and compares it to
the runtime function. Therefore a naming/matrix/format/checksum change fails the
release gate until both contracts are deliberately reconciled; it cannot leave
the updater silently requesting old names.

## Recommended component boundaries

`internal/selfupdate` separates five decisions:

1. A release source selects stable canonical GitHub releases and fetches exact
   named assets with a compiled-in origin, an HTTPS/host-limited redirect
   policy, a whole-operation context and per-body bounds. There is no production
   environment variable or participant authorization header.
2. A verifier first declares `[]AssetRequirement{Name, MaxBytes}` and then
   verifies a map of the fetched bytes. The SHA-256 verifier currently requests
   the archive and `checksums.txt`. A signature/provenance verifier can request
   signatures or certificates without restructuring discovery or download.
3. Archive inspection validates every pathname and entry type but materializes
   only the exact root `dropin-miner` or `dropin-miner.exe`. It rejects absolute
   paths, `..`, backslashes, links, devices/other types, duplicate executable
   entries, excessive total expansion and an excessive executable.
4. Candidate validation stages in the installed executable's directory,
   fsyncs and chmods it, and runs only `version` under a five-second sub-deadline
   with a minimal environment. It requires exact bare-version output and empty
   stderr.
5. A platform replacement strategy owns the state transition. Filesystem and
   command seams are injectable at hard failure points; parsing and path checks
   stay concrete.

The full command holds a sibling `dropin-miner.update.lock` across discovery,
download, verification, staging and replacement. Acquisition is non-blocking:
contention deterministically says another lifecycle operation is in progress.
The empty lockfile is never deleted. PR B/C1 setup and future uninstall should
use the same lock name and primitive.

## Bounds

The prototype uses:

| Object | Bound |
| --- | ---: |
| GitHub release JSON | 1 MiB |
| checksum text | 64 KiB |
| compressed archive | 64 MiB |
| executable member | 64 MiB |
| sum of declared uncompressed members | 128 MiB |
| HTTP client request | 2 minutes |
| whole command | 3 minutes |
| candidate `version` | 5 seconds |

The public `v0.2.8` compressed archives range from 7,140,100 to 7,915,895
bytes, so 64 MiB is roughly eight times the current maximum. It is generous
enough for foreseeable static-binary growth but still meaningfully rejects a
runaway or substituted asset. The independent 128 MiB total-expanded bound is
load-bearing: a small compressed archive can otherwise consume unbounded CPU
while the reader searches for a duplicate executable after finding the first.
These numbers should be reviewed when a legitimate release approaches half a
bound, not silently raised after an updater failure.

Checksum parsing accepts only a 64-hex digest and one safe basename per
non-empty line. Any malformed line, duplicate filename, unsafe filename,
missing target or digest mismatch rejects the release. Remote checksum names
are never joined to a local path.

## Installation ownership

The native installers currently place the executable in
`~/.tokendrop/bin`. The npm postinstall places it beside
`npm/bin/dropin-miner.js`, with `package.json` and `install.js` one directory
above; the JavaScript wrapper launches that exact native file. Replacing it
behind npm would mutate package-manager-owned content and can be overwritten by
the next install.

The prototype resolves symlinks and classifies that complete npm layout as
`npm`, a partial or contradictory layout as `ambiguous`, and other regular
executables as `native`. npm and ambiguous cases refuse native replacement.
The message directs a global installation to
`npm install -g dropin-miner@latest`; project-local users must update their own
dependency/lockfile instead.

This is intentionally one isolated seam, not a final ownership protocol. PR B
may introduce durable installation metadata or change the package layout. If it
does, PR C should consume that authority and retain the present layout only as
a backward-compatibility fallback. Architecture-owner review must decide
whether an absent marker means legacy-native or ambiguous for arbitrary copied
binaries.

## POSIX replacement state machine

Let `C` be the canonical executable, `N` the verified same-directory
candidate, `P` be `C.previous`, and `S` a fully copied and fsynced snapshot of
`C` in the same directory.

1. Create `S`. Failure leaves `C`, `N` and `P` unchanged; retry is safe.
2. `rename(N, C)`. POSIX atomically replaces the directory entry, so `C` is
   never absent. Failure leaves `C` and `P` unchanged; staging cleanup is safe.
3. Fsync the directory. On failure, atomically rename `S` back over `C` and
   fsync again. Successful recovery restores the original `C` and preserves
   `P`; failed recovery is explicitly manual-intervention state.
4. `rename(S, P)`, atomically replacing an existing one-level rollback only
   after candidate verification and installation. Failure triggers the same
   restore of `S` over `C`, leaving the old `P` untouched when recovery works.
5. Fsync the directory, then execute `C version` and require the target. A
   final fsync failure means both visible names changed but durability is
   uncertain; it is not reported as success. A post-install identity failure
   likewise never reports success and requires inspection.

The snapshot is a copy rather than a hard link so the protocol does not depend
on hard-link support or ownership semantics. There is no two-rename interval in
which `C` is absent on POSIX.

## Windows replacement state machine

Windows is deliberately separate. The prototype uses `MoveFileEx` with
`MOVEFILE_WRITE_THROUGH`, and `MOVEFILE_REPLACE_EXISTING` only where replacement
is intended:

1. Reserve a unique absent displaced pathname `D` beside `C`.
2. Move the running `C` to `D`. Failure leaves everything unchanged.
3. Move `N` to the now-free `C`. On failure, move `D` back to `C`; a failed
   restoration leaves `C` absent and requires immediate manual intervention.
4. Replace `P` with `D`. On failure, first move `C` back to `N`, then `D` back
   to `C`, preserving the old `P`; either failed recovery is manual state.
5. Execute the new canonical `C version` before success is printed.

The old process continues executing its already mapped image during these
name changes. Microsoft documents that executable images are mapped sections
and that mapped executable files cannot be deleted, while rename requires
delete rights; `MoveFileEx` documents replace/write-through behavior but does
not promise that the running image can be renamed on every supported filesystem
and endpoint-security configuration. This spike had no Windows host, so the
critical first move is **not production-qualified**. PR C must run an actual
Windows subprocess test where a child executable replaces its own pathname,
then exercise first move, second move, restoration, existing `P`, and another
upgrade. If that test disproves or destabilizes direct rename, use a detached
helper that waits for the parent process to exit; do not fall back to
PowerShell or delayed-reboot success claims.

References: [MoveFileEx](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-movefileexa)
and [Executable Images](https://learn.microsoft.com/en-us/windows-hardware/drivers/ifs/executable-images).

## Rollback state machine

`P` means exactly the canonical binary displaced by the most recent successful
upgrade or rollback. There is one slot, not a history. A second successful
upgrade replaces `P` with the binary it just displaced.

Rollback performs no network request. It requires `P` to exist, be a bounded
regular file, execute `version` within the same short timeout, and report one
canonical stable version different from current. It copies `P` to a new staged
candidate and validates the copy, then runs the ordinary platform replacement
transaction. On success the old `P` becomes `C`, and the displaced current `C`
becomes the new `P`; this makes a second rollback an explicit one-level swap.
All replacement failure states are identical to upgrade. Missing, malformed,
unexecutable or same-version `P` leaves `C` untouched.

## Failure semantics

| Failure | Canonical executable | Candidate / previous | Retry / action |
| --- | --- | --- | --- |
| GitHub unavailable, non-200, malformed/oversized response | untouched | untouched | safe retry |
| unsupported platform, missing/oversized asset | untouched | untouched | fix/release-owner action |
| malformed/missing/duplicate checksum or mismatch | untouched | no stage | do not retry blindly; inspect release |
| malformed/unsafe/bomb archive | untouched | no stage | release-owner action |
| candidate wrong version, writes stderr, cannot execute or times out | untouched | staging removed best-effort | release-owner action |
| cannot snapshot/preserve current | untouched | staging removed best-effort; old `P` untouched | permissions/filesystem action |
| POSIX candidate rename fails | untouched | old `P` untouched | safe retry |
| POSIX post-install step fails and recovery succeeds | restored old binary | old `P` untouched | safe retry after diagnosis |
| POSIX recovery or final durability fails | explicitly uncertain/changed | recovery artifact may remain | manual intervention; never success |
| Windows first move fails | untouched | old `P` untouched | safe retry after process/security diagnosis |
| Windows second/final move fails and recovery succeeds | restored old binary | old `P` untouched; candidate may remain | safe retry after diagnosis |
| Windows recovery fails | may be absent or changed | displaced path retained if possible | immediate manual intervention |
| post-replacement canonical identity fails | changed, not claimed successful | `P` contains displaced binary if committed | manual rollback/inspection |
| rollback source missing/invalid | untouched | `P` untouched | reinstall or repair ownership |
| concurrent lifecycle operation | untouched | untouched | deterministic retry later |
| npm-managed or ambiguous install | untouched | untouched | use npm / resolve ownership |
| development or unversioned build | untouched, no network | untouched | install a release first |

No path above prints success unless the canonical pathname itself executes and
reports the validated target version.

## Carry-forward decision

Production-worthy concepts and code are the version type and comparison,
artifact matrix plus GoReleaser cross-contract test, bounded reader, release
metadata validation, verifier-required-assets contract, conservative checksum
parser, archive inspector, candidate environment/timeout validation, and the
non-blocking lifecycle lock. The injected POSIX transaction is also a strong PR
C starting point, subject to review of its final-fsync recovery policy.

Rewrite or re-qualify the command wiring after PR B: ownership detection and
user guidance must follow B's final install metadata; errors should be mapped
into any command-wide typed/machine reporting convention B introduces. The
Windows strategy is a testable hypothesis, not production-ready code, until it
passes native Windows self-replacement tests. The hard-coded three-minute whole
operation budget and cleanup policy should also be reviewed against real slow
network measurements.

Architecture-owner decisions still required are: whether PR B metadata is
mandatory or backward-compatible; whether rollback should permit a newer
`P` (the prototype permits it and treats rollback as local slot restoration);
whether a final durability failure should trigger another risky automatic
rename; whether Windows adopts direct rename or a post-exit helper; and whether
the initial signing verifier needs additional per-asset or aggregate download
bounds before PR C ships.
