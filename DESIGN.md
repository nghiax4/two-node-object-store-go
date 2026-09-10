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
- **Stage 2 (streaming write/read to disk) implemented, correctness
  tested, metric still outstanding.** Code exists
  (`internal/checksum/crc32c.go`, `internal/storage/{files,store}.go`,
  updated `internal/api/*` and `cmd/storage/main.go`); manually
  verified via curl and now also covered by an automated test
  (`internal/storage/store_test.go`, table-driven over empty/small/
  exactly-32KiB/over-32KiB payloads, plus a missing-key case) — `go
  test ./internal/storage/...` passes. Still needed before logging a
  Stage Log entry and calling Stage 2 done: the promised
  streaming-vs-buffered memory metric. Plan: add a permanent second
  write path, `Store.PutBuffered` (reads the full body via
  `io.ReadAll` before writing, sharing the same temp-file/rename tail
  as `Put`), exposed via its own endpoint sharing the same key-space
  as streaming `Put`, then compare memory use between the two paths.

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

### Metrics log

| Stage | Metric | Before | After | Notes |
|-------|--------|--------|-------|-------|
| 1 — Bootstrap | — | — | — | No performance dimension: pure scaffolding, correctness-only (verified via `curl`). First metric arrives in Stage 2 (streaming vs. buffered memory use). |
