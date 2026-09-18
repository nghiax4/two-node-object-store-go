# two_node_object_store — Design Notes

Learning project about scaling a two-node replicated object store,
following the target architecture in `two_node_storage_engine_plan_v2.md`
(this repo). Pivoted from `object_storage_platform` (a prior project,
same student, same collaboration model) rather than continuing that
project's own naive-first, felt-bottleneck-driven roadmap — see
`CLAUDE.md` for the full reasoning behind following a pre-written plan
this time. These notes capture the design decisions made so far, and
log each stage as it's implemented (see Stage Log).

## Document conventions

This file is read for two reasons: to reconstruct *why* a past decision
was made, and to answer "what's true right now." Those two needs get
different treatment:

- **Status section:** a short pointer only — current state and next
  steps, nothing else. It's fully safe to overwrite on every update
  *because* it's never allowed to hold anything that isn't already
  preserved permanently elsewhere (usually the Stage Log). If you're
  about to write a sentence of reasoning into Status, it belongs in a
  Stage Log entry instead, with just a pointer left behind in Status.
- **Log sections (Stage Log, Metrics Log):** strictly append-only. Once
  an entry is written, it is never edited or deleted, even after it's
  superseded — it's a historical record of what was true/decided at
  that point in time. A later change gets a *new* entry that says what
  changed and why; the old entry stays exactly as written, optionally
  with a one-line note pointing forward to whatever superseded it.
- **Decision/reference sections** (Architecture Philosophy, Public API,
  Stack, Naming, file layout & schema, etc.): these describe the
  *current* settled decision, so unlike the log they are edited in
  place as decisions evolve — but never silently. When a decision here
  changes, leave a visible in-place marker (e.g. `**Superseded
  YYYY-MM-DD**`, `**Renamed YYYY-MM-DD**`) with a short note on what the
  old decision was and why it changed, rather than deleting the old text
  outright. That keeps the evolution visible without duplicating a full
  log entry for every settled-decision tweak.

## Status / where we left off

Short pointer only, kept small on purpose — the actual reasoning behind
each decision lives in the Stage Log (append-only, entries are never
rewritten once written) and is linked from here rather than repeated.

- **Stage 1 (Bootstrap) complete** — see Stage Log.
- **Stage 2 (streaming write/read to disk) complete** — see Stage Log
  and Metrics Log.
- **Stage 3 (bbolt metadata integration) complete** — see Stage Log and
  Metrics Log.
- **Stage 4 (durable fsync mode) complete** — see Stage Log and Metrics
  Log.
- **Stage 5 (startup reconciliation) complete** — see Stage Log and
  Metrics Log.
- **Next up: Stage 6 (Milestone 1 validation pass)** — the plan's own
  1 KiB/1 MiB/100 MiB/1 GiB PUT+GET/restart/verify-persistence
  checklist, as an integration test closing the milestone gate. See
  Milestone 1 sub-stages below.

## Milestone 1 sub-stages

Milestone 1 (single-node correctness) bundles nine deliverables in the
plan. Landing it as one stage would hide the before/after signal each
stage is supposed to produce, so it's split further:

1. **Bootstrap** — go module, plan's proposed directory layout, minimal
   HTTP server with a health check. No storage logic yet.
2. **Streaming write/read to disk (no metadata)** — PUT streams through
   a bounded buffer into a temp file, CRC32c computed in the same pass,
   atomic rename into a hashed object dir; GET reads it back. First
   measurable streaming-vs-buffering memory comparison.
3. **bbolt metadata integration** — `objects` bucket, metadata committed
   alongside the file (buffered-mode ordering). GET resolves through
   bbolt.
4. **Durable fsync mode** — second commit path (fsync temp → rename →
   fsync parent dir → durable metadata commit), ordering documented
   explicitly. First buffered-vs-durable latency comparison.
5. **Startup reconciliation** — abandoned `*.partial` cleanup, metadata/
   file mismatch handling, one documented recovery policy.
6. **Milestone 1 validation pass** — the plan's own checklist (1 KiB/1
   MiB/100 MiB/1 GiB PUT+GET, restart, verify persistence) as an
   integration test closing the milestone gate.

## Process: documentation, testing, and metrics

Same standing rule as `object_storage_platform` (which itself carried
it over from `skin_market_data_platform`) — for every stage from the
first one onward:

- **Document as you go.** When a stage is implemented, add a short
  entry under Stage Log below: what was built, what design choices were
  made and why.
- **Write tests before declaring a stage done.** Tests should check
  *correctness* independent from performance.
- **Measure, don't just feel.** Every stage needs a concrete number
  attached to what it's meant to improve or demonstrate, both before
  and after where applicable. Log results in the Metrics Log table
  below.

### Stage log

**Stage 1 — Bootstrap (2026-09-02)**

Built the repo skeleton per the plan's proposed structure:
`cmd/storage/main.go`, `internal/api/{routes,handlers}.go`, empty
`internal/{storage,replication,checksum}` packages reserved for later
stages. Module name `two_node_object_store` (no path prefix — not
published as an importable module). Routing uses stdlib
`http.ServeMux` rather than a third-party router; nothing here needs
path params or middleware chaining yet, and pulling in a router before
that's true would be exactly the kind of ahead-of-need dependency the
plan's Optimization Policy warns against. `main.go` takes `-addr` and
`-data-dir` flags; `-data-dir` is parsed but unused until Stage 2 wires
up actual storage.

Correctness check: `go build ./...` succeeds, server starts, `curl -i
http://localhost:8080/healthz` returns `200 OK` with body `ok`.

**Sidequests logged:** `http.ServeMux` vs. third-party routers, `flag`
package basics, graceful shutdown — see `SIDEQUESTS.md`.
*(Note, 2026-09-08: `SIDEQUESTS.md` discontinued — sidequests aren't
tracked as a separate file going forward. This pointer is kept as-is
for the historical record; see Status for current process.)*

**Stage 2 — Streaming write/read to disk (2026-09-09)**

`Store.Put` (`internal/storage/store.go`) streams the PUT body through
a 32 KiB buffer (`io.CopyBuffer`, matching `io.Copy`'s own internal
default) into a temp file in `<data-dir>/tmp`, computing CRC32C via a
`hash.Hash` in the same pass (`io.MultiWriter(tmpFile, hasher)`), then
atomically renames into a hashed object path (`objectPath`, sha256 of
the key, first two hex chars as a directory shard). `Store.Get` opens
and stats the object directly. `internal/checksum/crc32c.go` holds the
shared Castagnoli table. HTTP layer (`internal/api/handlers.go`) is a
thin pass-through: `handlePut`/`handleGet` call the store and map
`os.ErrNotExist` to 404.

32 KiB was a deliberate choice, not the default left untouched: it's
the same buffer size `io.Copy` uses internally when given no buffer,
which is itself a long-validated balance between memory use and
syscall count (each loop iteration is one `read` + one `write`
syscall — a 1 KiB buffer would mean 32x more syscalls to move the same
data, for a memory saving that's already negligible next to what the
comparison below shows).

**Second write path added deliberately for the Stage 2 metric:**
`Store.PutBuffered` — reads the entire body via `io.ReadAll` before
writing, sharing the same temp-file/atomic-rename tail as `Put` (code
is intentionally duplicated between the two rather than factored into
a shared helper; abstracting for two call sites, one of which exists
solely to produce a one-time measurement, would be exactly the kind of
ahead-of-need refactor the plan's Optimization Policy warns against).
Exposed permanently via `PUT /objects/{key}/buffered`
(`internal/api/routes.go`), sharing the same key-space as streaming
`Put` — a buffered and a streaming PUT of the same key overwrite each
other, by design (kept permanent per student's call: this project
isn't consumer-facing, so a second, deliberately-worse write path
living alongside the real one is acceptable as a standing
demonstration rather than throwaway benchmark code).

Correctness check: `internal/storage/store_test.go`, table-driven over
empty/small/exactly-32KiB/over-32KiB payloads (asserting `Size`,
`CRC32C` against an independently-computed checksum, and byte-identical
readback) plus a missing-key case (`errors.Is(err, os.ErrNotExist)`).
`go build ./...`, `go vet ./...`, and `go test ./...` all pass. Also
manually verified live over HTTP: buffered PUT followed by a plain GET
returns the same bytes, confirming the shared key-space works as
designed.

Metric methodology and results are in the Metrics Log below, but the
short version: streaming allocates ~34 KiB per PUT regardless of
payload size; a naive buffered PUT allocates ~375 MiB per PUT for a
64 MiB payload — not 1x the payload as a naive mental model would
suggest, but ~5.9x it. Root cause, confirmed via an isolated
instrumented trace of `io.ReadAll`'s internal buffer (not part of the
repo — a throwaway diagnostic, reproducible from the reasoning below):
`io.ReadAll` doesn't know the body's final length up front, so it
grows its internal buffer incrementally (46 separate reallocations for
a 64 MiB read, from 512 B up to ~75 MiB), and *every* growth step
re-copies everything read so far into the new, bigger array. Summing
all 46 intermediate allocations reproduces the benchmark's measured
number almost exactly. Important nuance for interpreting the number
correctly: Go's `B/op` is cumulative allocation traffic
(`runtime.MemStats.TotalAlloc` delta) — total bytes that passed
through the allocator during the op, including ones immediately
discarded — not peak resident memory. Buffered `PutBuffered`'s actual
peak RSS at any instant is closer to ~75 MiB (its final buffer size),
not 375 MiB simultaneously. The honest claim is "buffered churns ~5.9x
the payload through the allocator per request" (which is still a real
cost — it's also why buffered was ~2.9x slower per op), not "buffered
holds 5.9x the payload in RAM at once."

**Stage 3 — bbolt metadata integration (2026-09-14)**

Added `go.etcd.io/bbolt` as the metadata store. `Store.New` opens
`<data-dir>/metadata.db` and creates a single `objects` bucket if it
doesn't already exist (`internal/storage/store.go`). Metadata is a
manually-encoded fixed-width 20-byte record — `size` (8 bytes),
`crc32c` (4 bytes), `updated_at` as a Unix timestamp (8 bytes), all
big-endian (`encodeMeta`/`decodeMeta`, `internal/storage/metadata.go`)
— rather than a general-purpose encoding like JSON or gob. At this
fixed, small, internal-only schema, a manual encoding is simpler to
reason about and avoids pulling in a serialization library or paying
reflection overhead for three fields nothing outside this package ever
sees.

**Write side:** `Put` and `PutBuffered` both call a shared
`commitMeta(key, size, crc32c)` helper after their atomic rename
succeeds, so metadata is only ever written once the object file is
durably in place under its final name (buffered-mode ordering per the
plan; the fsync-durable ordering is Stage 4's concern, not this one).
`TestPutCommitsMetadata` verifies `Put`'s commit against a direct bbolt
read. `PutBuffered`'s metadata commit is deliberately left without its
own test — student's call: `PutBuffered` already had zero correctness
coverage before this stage (demonstration-only endpoint, see Stage 2),
and duplicating `TestPutCommitsMetadata` for it wouldn't exercise
anything the shared `commitMeta` helper doesn't already cover once.

**Read side:** `Get` now resolves through bbolt instead of trusting the
filesystem. It looks up the key in the `objects` bucket first; a miss
there is an immediate `os.ErrNotExist`, *even if the object file
happens to still exist on disk* — metadata is the authority on whether
a key exists, and reconciling a metadata/file disagreement in either
direction is explicitly Stage 5's job (startup reconciliation), not
this one. On a hit, `Get` copies the bbolt value's bytes out of the
transaction (`append([]byte(nil), b...)`) before decoding — a bbolt
value is a slice backed directly by its `mmap`, valid only for the
transaction's lifetime, so decoding it after `View` returns would be
reading unmapped memory. `Size`/`CRC32C` in the returned `GetResult`
now come from the decoded metadata rather than `f.Stat()`, matching the
plan's instruction not to re-derive integrity/size info the metadata
store already owns. `handleGet` now also sets `X-Checksum-CRC32C` on
responses, matching `handlePut`'s existing header.

**Considered and declined:** a test that writes an object normally,
then overwrites its on-disk file directly (bypassing `Put`) to prove
`Get`'s size/crc come from bbolt rather than a fresh `stat` — declined
as too surgical (student's call): it doesn't exercise any path the
system reaches through its own API, only an internal implementation
detail. Consistent with the `PutBuffered` call above.

**No Stage 3 metric.** Discussed and deliberately skipped: the only
thing this stage's GET-path change trades is one `fstat` syscall for
one in-process bbolt bucket lookup. `metadata.db` is already `mmap`'d
at `Store.New` time, so the bbolt lookup makes no syscalls of its own —
it's a mutex-guarded B+tree walk over already-mapped memory. Both the
removed `fstat` and the added lookup are sub-microsecond, no-disk-I/O
operations, and neither touches the part of GET that actually dominates
its cost (opening and streaming the object's bytes, unchanged by this
stage). Unlike Stage 2's buffered-vs-streaming comparison — a real,
reproducible, order-of-magnitude difference — isolating this one would
most likely produce a noise-level number dressed up as a metric. Logged
as correctness-only, same treatment as Stage 1.

Correctness check: `go build ./...`, `go vet ./...`, `go test ./...`
all pass. `TestStorePutGetRoundTrip` now also asserts `GetResult.Size`/
`CRC32C` against the independently-computed CRC (previously only
checked stat size and byte-identical readback).
`TestStoreGetMissingKey` tightened to assert
`errors.Is(err, os.ErrNotExist)` specifically, matching what
`handleGet` actually branches on, rather than "any error."

**Stage 4 — Durable fsync mode (2026-09-14)**

Added a second commit path to `Store.Put` for the plan's "Durable
Mode," chosen as a `Durability` parameter (`Buffered`/`Durable`, a
small `int`-backed enum) rather than a separate sibling method —
different from how `Put`/`PutReadAll` stayed split. Reasoning: `Put`
vs. `PutReadAll` differ in read strategy (streamed vs. whole-body), a
real algorithmic fork worth reading as two independent top-to-bottom
functions; buffered vs. durable differ only in *whether two `fsync`
calls happen* around the otherwise-identical write sequence, which
reads better as a branch inside one sequence than as two near-duplicate
functions. `Durable` inserts `tmpFile.Sync()` right after the streaming
copy (before `tmpFile.Close()`), and after the rename succeeds, opens
the *sharded object subdirectory* the rename happened in (not the
top-level `objects/` dir) and `Sync()`s it — the exact ordering the
plan specifies: `write → fsync(file) → rename → fsync(dir) → commit
metadata`. `commitMeta`'s call is unchanged in either mode: bbolt's
`db.Update` already fsyncs its own file on every commit by default (no
`NoSync` set), so "durable metadata commit" was already true before
this stage — Stage 4's actual new work is the two filesystem-level
`fsync`s around the object file, not a metadata change.

**HTTP surface:** rather than a second route (`/objects/{key}/durable`,
mirroring `/readall`), durability is selected via an `X-Durability`
request header on the single `PUT /objects/{key}` route — same
API-shape reasoning as the `Store.Put` decision, one level up. Absent
header or `X-Durability: buffered` → `Buffered` (today's only behavior,
unchanged, so every existing plain PUT keeps working); `durable` →
`Durable`; any other value is a `400`, deliberately not silently
downgraded to `Buffered` — a typo'd durability request should fail
loudly rather than quietly serve a weaker guarantee than the client
asked for. No requirement to send the header at all: this store has no
external clients besides its own tests/benchmarks/demos, so there's no
one to protect from an implicit default the way a public API might need
to.

**Naming:** `PutBuffered` (the Stage 2 `io.ReadAll` baseline) was
renamed to **`PutReadAll`** in a small dedicated commit before this
stage's real work started, freeing up "Buffered" to mean what the plan
means by it (a commit-durability mode) without colliding with the
unrelated read-strategy method that happened to share the word first.
The Stage 2 Stage Log entry above still says `PutBuffered` — left as
originally written per this document's own append-only convention; it
was accurate at the time.

Correctness check: `TestStorePutDurableRoundTrip` (new) — a single
representative payload through `Put(..., Durable)`, asserting CRC32C
and byte-identical `Get` readback. Deliberately *not* folded into
`TestStorePutGetRoundTrip`'s existing size-boundary table (which would
double it to 8 cases): that table exists to test the streaming copy
loop's behavior at buffer-size boundaries, which durability doesn't
interact with — the two `fsync` calls are unconditionally appended
around the same copy loop regardless of payload size, so re-running
every size boundary under `Durable` would re-prove the same streaming
logic twice rather than add new signal. `go build ./...`,
`go vet ./...`, `go test ./...` all pass.

Metric methodology and results in the Metrics Log below; short
version: durable PUT is ~3.47x slower than buffered for a 64 MiB
payload, with essentially flat memory allocation (+378 B/op, +4
allocs/op) — confirming the added cost is the two blocking `fsync`
syscalls waiting on physical disk, not anything allocator-related.
Unlike Stage 3's skipped metric (an in-memory lookup vs. a cheap
syscall, both sub-microsecond), this is a real, reproducible,
disk-bound cost — closer in kind to Stage 2's result than Stage 3's.
*(Note, 2026-09-14: the single-run 3.47x figure above turned out to be
one point in a much wider spread than expected — see the follow-up
entry directly below.)*

**Stage 4 follow-up — benchmark variance across repeated runs
(2026-09-14)**

Re-ran `BenchmarkPutBuffered`/`BenchmarkPutDurable` (same command,
same machine) five times total across the original run and four
reruns. Buffered stayed in a tight band — 39.0–51.5 ms, ~25% spread.
Durable did not — 57.9–178.8 ms, a swing of over 3x between the
fastest and slowest observed run, on a code path that does nothing
data-dependent or branchy that would explain that on its own. Per-run
ratios: 3.47x, 1.95x, 1.53x, 1.13x, 1.87x — not a stable multiplier,
median ≈1.6x.

Conclusion: the honest metric here isn't a single "durable is Nx
slower" figure — it's that **buffered latency is stable and durable
latency is not**, meaning the two `fsync` calls are the volatile
ingredient, not the streaming/copy logic shared by both modes (which
is exactly the part that stays stable). Leading hypothesis, not
confirmed: this machine runs under WSL2, where the filesystem sits on
a virtual disk backed by a file on the Windows host — `fsync`'s whole
job is to wait for the underlying storage to confirm durability, and
when that storage is itself virtualized, its latency is exposed to
whatever the Windows host's disk cache/scheduler/other I/O is doing at
that instant, in a way a bare-metal Linux disk wouldn't be. Not
verified by instrumenting the host side, so stated as a hypothesis, not
a fact — a real production benchmark run would want to either confirm
this (e.g. compare against a bare-metal Linux run) or run enough
samples to characterize the distribution properly (more than 5 points,
outlier handling) rather than trust a handful of point measurements.

No code change this entry — purely a measurement correction, logged
because presenting the original single-run number as *the* metric
would have been the wrong lesson to take from Stage 4.

**Stage 5 — Startup reconciliation (2026-09-18)**

Split into two sub-stages, landed together in this entry.

**5a — leftover temp-file cleanup.** `Store.cleanupTmpDir`
(`internal/storage/reconcile.go`, new file) reads `<data-dir>/tmp` and
removes every entry it finds, called from `New` right after the
`objects` bucket is created. `Put`/`PutReadAll` already remove their own
temp file on every return path, success or error (`defer os.Remove`), so
anything still in `tmp/` at startup only got there via a hard crash
mid-write — this is cleanup for that window specifically, not a general
`tmp/` policy.

**5b — bidirectional metadata/file reconciliation.** Two more states
handled, both via `Store.reconcileObjects` (same file): a bbolt entry
with no backing object file, and an object file with no backing bbolt
entry. Implemented as one bbolt walk feeding one filesystem walk rather
than two independent passes: `pruneDanglingMetadata` cursors every key
in the `objects` bucket, `os.Stat`s its `objectPath`, deletes the entry
in place (`Cursor.Delete`, bbolt's documented-safe pattern for deleting
during iteration) and logs a warning if the file's gone — and while it's
already there, records every *surviving* path into a `map[string]struct{}`.
`removeOrphanFiles` then does one `filepath.WalkDir` over `objects/`,
deleting (with a warning) any file not in that set. `objectPath` is a
one-way `sha256(key)` → path function, so there's no way to recover a
key from a bare file on disk — this expected-path-set approach is what
makes the file→metadata direction checkable at all without needing the
key back.

**Neither direction is reachable from `Put`'s own crash window** under
its current ordering (write temp → rename → `commitMeta`, `Durable`
mode's two `fsync`s included): every early return happens before
`commitMeta`, so a crash mid-`Put` can only ever leave an orphan file
behind, never dangling metadata. Both directions are still implemented,
per the plan's explicit "at minimum" list — dangling metadata defends
against something outside `Put`'s own control entirely (a file deleted
out-of-band, a disk-level disagreement between the object tree and
`metadata.db`), not a bug in this codebase.

**Policy: delete-and-warn in both directions, not rebuild.** The
metadata→missing-file direction has an obvious answer (a bbolt entry
pointing at nothing is simply wrong). The file→missing-metadata
direction had a real alternative on the table — rebuild metadata from
the orphan file instead of deleting it, recovering data from exactly the
crash window described above. Discussed and declined, for two reasons:

1. **Client-visible semantics.** A crash between rename and `commitMeta`
   means the client never got a success response — by that contract, the
   write is defined as not-having-happened. Rebuilding would silently
   flip an unacknowledged write into a successful one on the next
   restart, with no client action involved — "ACK is the only source of
   truth for whether a write happened" is the cleaner invariant to keep.
2. **Forward-compatibility with Milestone 3+.** Once replication exists,
   the natural place to enqueue "replicate this to the replica" is
   alongside `commitMeta` in `Put` — one event, two consequences. A
   metadata entry manufactured by reconciliation would never pass
   through that enqueue step, silently invisible to the replica unless
   reconciliation is *also* taught to reach into a replication queue that
   doesn't exist yet at this stage. Delete keeps bbolt-entry-creation to
   the single code path that already knows how to do everything else a
   committed write needs to trigger.

**Correctness check:** `TestStoreNewCleansTmpDir` (pre-creates a stray
file under `tmp/`, asserts it's gone after `New`).
`TestStoreReconcileRemovesDanglingMetadata` (real `Put` through the
API, then the object file is removed directly to simulate external
tampering, store closed and reopened — reconciliation only runs inside
`New` — asserting `Get` now returns `os.ErrNotExist`).
`TestStoreReconcileRemovesOrphanFile` (a file written directly to its
`objectPath` with no `Put` involved at all, since this state can't be
produced through the API — single `New` call, asserting the file is
gone afterward). `go build ./...`, `go vet ./...`, `go test ./...` all
pass. Warnings use stdlib `log.Printf`, matching the only logging
approach already in the codebase (`cmd/storage/main.go`'s startup/
listen messages) rather than adding a logging dependency for two lines.

### Metrics log

| Stage | Metric | Before | After | Notes |
|-------|--------|--------|-------|-------|
| 1 — Bootstrap | — | — | — | No performance dimension: pure scaffolding, correctness-only (verified via `curl`). First metric arrives in Stage 2 (streaming vs. buffered memory use). |
| 2 — Streaming write/read | Memory allocated per PUT (`B/op`, `go test -bench=. -benchmem`, 64 MiB random payload, `benchtime=20x`) | 393,473,547 B (~375.2 MiB) — naive buffered `PutBuffered` (`io.ReadAll` then write) | 35,218 B (~34.4 KiB) — streaming `Put` (32 KiB bounded buffer) | ~11,171x reduction. Streaming's number tracks the 32 KiB copy buffer almost exactly and is flat regardless of payload size. Buffered's number is ~5.9x the 64 MiB payload itself — see Stage Log entry for the confirmed mechanism (`io.ReadAll`'s unsized incremental buffer growth, traced to 46 reallocation steps). This measures cumulative allocation traffic, not peak resident memory (buffered peak RSS ≈ 75 MiB, its final buffer size). |
| 2 — Streaming write/read | Latency per PUT (`ns/op`, same benchmark run) | 129,563,501 ns (~130 ms) — buffered | 44,853,172 ns (~45 ms) — streaming | ~2.9x faster streaming; consistent with buffered's extra allocator/copy overhead from the 46-step growth pattern above. |
| 3 — bbolt metadata integration | — | — | — | No performance dimension measured, deliberately: GET's change trades one `fstat` syscall for one in-process bbolt bucket lookup against an already-`mmap`'d file — both sub-microsecond, no-disk-I/O operations, neither touching the actual read/stream cost. Discussed and skipped rather than producing a noise-level number; see Stage Log entry for the full reasoning. |
| 4 — Durable fsync mode | Latency per PUT (`ns/op`, `go test -bench -benchmem`, 64 MiB random payload, `benchtime=20x`), single run | 51,484,435 ns (~51.5 ms) — `Buffered` | 178,843,903 ns (~178.8 ms) — `Durable` | ~3.47x slower durable in this one run. Memory allocation essentially flat between the two (44,723 B/op, 77 allocs — buffered; 45,101 B/op, 81 allocs — durable), confirming the added latency is the `fsync` syscalls, not allocator overhead. **Superseded by the row below** — this single run understated how much this number moves between runs. |
| 4 follow-up — Durable fsync mode, repeated runs | Same benchmark, 5 total runs (1 original + 4 reruns) | Buffered: 39.0–51.5 ms across runs (~25% spread) | Durable: 57.9–178.8 ms across runs (>3x spread); per-run ratio ranged 1.13x–3.47x, median ≈1.6x | The spread itself is the finding: buffered is stable, durable is not, meaning `fsync` latency (not the shared streaming/copy logic) is the volatile ingredient. See Stage Log follow-up entry for the WSL2-virtualized-disk hypothesis and why it's stated as unconfirmed. |
| 5 — Startup reconciliation | — | — | — | No performance dimension: reconciliation runs once per process startup inside `New`, never on a request path, so there's no per-operation cost to compare before/after the way Stages 2/4 have one. Correctness-only, same treatment as Stage 3. |
