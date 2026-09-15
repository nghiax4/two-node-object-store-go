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
- **Next up: Stage 4 (durable fsync mode)** — second commit path
  (fsync temp → rename → fsync parent dir → durable metadata commit),
  first buffered-vs-durable latency comparison. See Milestone 1
  sub-stages below.

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

### Metrics log

| Stage | Metric | Before | After | Notes |
|-------|--------|--------|-------|-------|
| 1 — Bootstrap | — | — | — | No performance dimension: pure scaffolding, correctness-only (verified via `curl`). First metric arrives in Stage 2 (streaming vs. buffered memory use). |
| 2 — Streaming write/read | Memory allocated per PUT (`B/op`, `go test -bench=. -benchmem`, 64 MiB random payload, `benchtime=20x`) | 393,473,547 B (~375.2 MiB) — naive buffered `PutBuffered` (`io.ReadAll` then write) | 35,218 B (~34.4 KiB) — streaming `Put` (32 KiB bounded buffer) | ~11,171x reduction. Streaming's number tracks the 32 KiB copy buffer almost exactly and is flat regardless of payload size. Buffered's number is ~5.9x the 64 MiB payload itself — see Stage Log entry for the confirmed mechanism (`io.ReadAll`'s unsized incremental buffer growth, traced to 46 reallocation steps). This measures cumulative allocation traffic, not peak resident memory (buffered peak RSS ≈ 75 MiB, its final buffer size). |
| 2 — Streaming write/read | Latency per PUT (`ns/op`, same benchmark run) | 129,563,501 ns (~130 ms) — buffered | 44,853,172 ns (~45 ms) — streaming | ~2.9x faster streaming; consistent with buffered's extra allocator/copy overhead from the 46-step growth pattern above. |
| 3 — bbolt metadata integration | — | — | — | No performance dimension measured, deliberately: GET's change trades one `fstat` syscall for one in-process bbolt bucket lookup against an already-`mmap`'d file — both sub-microsecond, no-disk-I/O operations, neither touching the actual read/stream cost. Discussed and skipped rather than producing a noise-level number; see Stage Log entry for the full reasoning. |
