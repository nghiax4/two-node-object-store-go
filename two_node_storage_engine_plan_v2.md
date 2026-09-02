# High-Performance 2-Node Replicated Object Store

## Project Goal

Build a focused two-node storage engine in Go that demonstrates:

- Streaming object I/O without full-object buffering
- Primary-replica asynchronous replication
- Persistent recovery of missed replication work
- Crash-aware local write semantics
- CRC32c integrity verification
- Measurable network, disk, latency, recovery, and memory performance
- Clear comparison between application performance and underlying hardware limits

The project is intentionally scoped to be completable within roughly two weeks.

The goal is **not** to reproduce S3, build a highly available database, or solve active-active consistency. The goal is to build a small distributed storage system that is correct enough to benchmark seriously and simple enough to finish and explain well in interviews.

---

# Architecture

```text
                         ┌───────────────────┐
                         │      Client       │
                         └─────────┬─────────┘
                                   │
                         PUT / GET / DELETE
                                   │
                                   ▼
                    ┌──────────────────────────┐
                    │          NODE A          │
                    │         PRIMARY          │
                    │                          │
                    │ HTTP API                 │
                    │ local NVMe storage       │
                    │ CRC32c metadata          │
                    │ persistent repl. queue   │
                    └────────────┬─────────────┘
                                 │
                          async replication
                                 │
                                 ▼
                    ┌──────────────────────────┐
                    │          NODE B          │
                    │         REPLICA          │
                    │                          │
                    │ read API                 │
                    │ local NVMe storage       │
                    │ CRC32c metadata          │
                    └──────────────────────────┘
```

## Consistency Model

- All client writes go to Node A.
- Node A is statically configured as the primary.
- Node B is statically configured as the replica.
- Node A acknowledges a write after the configured local durability condition is satisfied.
- Replication to Node B is asynchronous.
- Reads may be served from Node A or Node B.
- Node B may temporarily lag behind Node A.
- If Node B is unavailable, Node A persists pending replication work.
- When Node B returns, replication resumes and the replica converges.
- No leader election or automatic promotion is required.
- If Node A fails, writes become unavailable until Node A returns.

This system deliberately prioritizes simplicity, measurable performance, and explicit eventual consistency over high availability.

---

# Core Design Principles

## 1. Finishable Before Feature-Rich

The project should stop adding distributed features once:

```text
single-node storage works
        +
two-node async replication works
        +
replication recovery works
```

At that point, effort moves to:

```text
benchmarking
profiling
correctness validation
documentation
```

## 2. Benchmark Credibility Over Big Numbers

A smaller number with a defensible methodology is better than a large number produced by page cache, burst credits, or an overloaded load generator.

Always distinguish:

```text
cold storage performance
warm-cache performance
buffered writes
durable fsync writes
short burst throughput
sustained throughput
```

## 3. Measure Before Optimizing

Use:

```text
fio
iperf3
pprof
process RSS
Go heap statistics
```

before making low-level optimizations.

Do not introduce:

```text
sync.Pool
custom buffer pools
parallel replication workers
custom TCP protocols
```

unless measurements show they are needed.

---

# Must-Have Features

## Object API

Required:

```text
PUT /objects/{key}
GET /objects/{key}
```

Optional if time permits:

```text
DELETE /objects/{key}
HEAD /objects/{key}
```

---

# Local Storage Layout

A simple structure is sufficient:

```text
/data/
├── objects/
│   ├── aa/
│   ├── ab/
│   └── ...
│
├── tmp/
│
└── metadata.db
```

Object paths may be derived from a hash of the key to avoid placing an excessive number of files in one directory.

Avoid overengineering this.

---

# Metadata Store

Use:

```text
go.etcd.io/bbolt
```

instead of BadgerDB.

Reasons:

- Small embedded dependency
- ACID transactions
- mmap-backed reads
- Low application heap overhead
- Simple mental model
- Single-writer limitation is acceptable for small metadata operations
- Object metadata and replication queue entries can be committed transactionally

Suggested buckets:

```text
objects
replication
```

Example object metadata:

```text
key
size
crc32c
updated_at
```

Example replication entry:

```text
sequence
operation
key
```

Do not store full object contents inside bbolt.

---

# Streaming Write Path

Incoming request bodies must be streamed directly to disk.

Never do:

```go
data, err := io.ReadAll(r.Body)
```

for object contents.

Desired flow:

```text
HTTP request body
      │
      ▼
small bounded buffer
      │
      ├── update CRC32c
      │
      ▼
temporary file
      │
      ▼
commit
```

CRC32c should be computed during the same pass as the disk write.

---

# Local Write Commit Semantics

Support two explicit modes.

## Buffered Mode

Designed for maximum throughput.

Conceptually:

```text
write temp file
rename temp → final
commit metadata
enqueue replication entry
ACK client
```

This mode does **not** claim crash-durable persistence against sudden machine or power loss.

## Durable Mode

Designed to demonstrate the cost of stronger local durability.

Conceptually:

```text
write temp file
      ↓
fsync(temp file)
      ↓
rename(temp, final)
      ↓
fsync(parent directory)
      ↓
commit metadata + replication queue entry durably
      ↓
ACK client
```

The exact ordering between file commit and metadata commit should be documented carefully.

At minimum, startup reconciliation must handle states such as:

```text
file exists, metadata missing
metadata exists, file missing
partial temp file exists
```

Do not attempt to provide transactional atomicity across the filesystem and bbolt beyond what the project can honestly support.

Instead, make recovery deterministic.

---

# Startup Reconciliation

On startup:

```text
remove abandoned *.partial files
verify metadata references existing object paths
detect obvious inconsistent states
```

A simple recovery policy is acceptable.

Example:

```text
metadata references missing file
→ remove metadata entry
→ record warning
```

or:

```text
file exists but metadata missing
→ rebuild metadata if possible
```

Choose one clear policy and document it.

The goal is predictable restart behavior, not a full filesystem journal.

---

# Read Path

Serve regular files with:

```go
io.Copy(responseWriter, file)
```

where possible.

This allows Go/Linux to use the optimized file-to-socket path such as `sendfile()` on eligible connections.

Required validation:

```text
run server under strace
perform large GET
verify sendfile() appears
```

If TLS, middleware, or another abstraction prevents the zero-copy path, document that honestly.

---

# CRC32c Integrity

Use:

```go
hash/crc32
```

with the Castagnoli polynomial.

CRC32c is calculated:

```text
during client PUT
during replica ingestion
```

Store:

```text
object size
CRC32c
```

with metadata.

Do not recalculate CRC on every GET because that would defeat the zero-copy read path.

The project does not require a full background scrubber.

---

# Asynchronous Replication

After a successful local write:

```text
local commit
    ↓
persistent replication entry
    ↓
ACK client
    ↓
background replication worker
    ↓
stream current object state to Node B
```

Use HTTP for replication.

Do not implement gRPC or a custom binary protocol unless HTTP becomes a demonstrated bottleneck.

---

# Replication Queue Semantics

Keep the queue deliberately simple.

A replication entry means:

> Synchronize the current state of this key to the replica.

It does **not** mean:

> Send the exact historical bytes that existed when this queue entry was created.

Example:

```text
PUT foo = v1
queue sync(foo)

PUT foo = v2
queue sync(foo)
```

If the first queue item is processed after v2 exists:

```text
worker opens current foo
worker sends v2
worker calculates checksum over v2
replica stores v2
```

The second entry may send v2 again.

That is inefficient but correct for the MVP.

Queue deduplication is optional.

---

# Replication Worker

The MVP uses exactly:

```text
ONE replication worker
```

This avoids same-key ordering problems.

Flow:

```text
load oldest queue entry
       ↓
open current object state
       ↓
stream to Node B
       ↓
replica verifies received bytes
       ↓
replica commits object
       ↓
HTTP success
       ↓
remove queue entry
```

On failure:

```text
leave queue entry intact
back off
retry later
```

Use:

```text
bounded retry delay
context cancellation
HTTP client timeouts
connection reuse
```

Keep retry behavior simple.

---

# Replication Checksum Semantics

The checksum sent to Node B must describe the actual bytes transmitted.

Do not store:

```text
queue entry says checksum(v1)
```

and then open a file that may now contain v2.

Instead:

```text
open current object
stream object
calculate or use current metadata checksum
send matching checksum
```

A safe design is to include current:

```text
size
CRC32c
```

in replication headers and verify them after the replica receives the object.

---

# Replica Ingestion

Node B exposes an internal endpoint such as:

```text
PUT /internal/objects/{key}
```

The replica should:

```text
stream body → temp file
calculate CRC32c
verify expected size/checksum
commit local file
commit metadata
return success
```

Only after this succeeds may Node A remove the replication queue entry.

---

# Replica Recovery

Required failure scenario:

```text
1. Stop Node B.
2. Continue writing to Node A.
3. Observe persistent queue growth.
4. Restart Node A if desired.
5. Restart Node B.
6. Replication worker resumes.
7. Queue drains.
8. Verify all expected objects exist on Node B.
```

The persistent replication queue must survive restarting Node A.

This is the primary distributed-systems reliability feature of the project.

---

# Explicitly Out of Scope

Do not implement these unless the project is already complete, benchmarked, and documented.

- Active-active client writes
- Conflict resolution
- Lamport clocks
- Vector clocks
- Consensus
- Raft
- Paxos
- Leader election
- Automatic failover
- Replica promotion
- Split-brain handling
- Quorum reads
- Quorum writes
- Cross-node transactions
- Read repair
- Anti-entropy scans
- Background bit-rot repair
- Read proxying
- Backfill-on-read
- Distributed metadata service
- Custom TCP protocol
- gRPC
- Custom WAL
- Kubernetes
- Multi-region deployment
- Multi-AZ durability

---

# Suggested Go Stack

Core:

```text
net/http
os
io
hash/crc32
context
sync
time
runtime
```

Metadata:

```text
go.etcd.io/bbolt
```

Profiling:

```text
net/http/pprof
```

Potential later optimizations:

```text
io.CopyBuffer
sync.Pool
multiple hash-sharded replication workers
custom reusable buffers
```

Do not use them until profiling justifies them.

---

# Proposed Repository Structure

```text
.
├── cmd/
│   └── storage/
│       └── main.go
│
├── internal/
│   ├── api/
│   │   ├── handlers.go
│   │   └── routes.go
│   │
│   ├── storage/
│   │   ├── store.go
│   │   ├── files.go
│   │   ├── durability.go
│   │   └── metadata.go
│   │
│   ├── replication/
│   │   ├── queue.go
│   │   ├── worker.go
│   │   └── client.go
│   │
│   └── checksum/
│       └── crc32c.go
│
├── scripts/
│   ├── benchmark_get.sh
│   ├── benchmark_replication.sh
│   ├── benchmark_recovery.sh
│   ├── failure_test.sh
│   └── deploy.sh
│
├── Dockerfile
├── docker-compose.yml
├── go.mod
├── go.sum
└── README.md
```

Avoid adding layers or interfaces just for architectural appearance.

---

# Development Milestones

These milestones are dependency-based rather than day-based.

The two-week schedule should remain flexible.

---

## Milestone 1 — Single-Node Correctness

Deliver:

- PUT
- GET
- streaming writes
- CRC32c
- bbolt metadata
- temporary files
- atomic rename
- startup reconciliation
- buffered durability mode
- durable fsync mode

Validation:

```text
PUT 1 KiB
PUT 1 MiB
PUT 100 MiB
PUT 1 GiB

GET all objects
verify exact bytes
verify metadata
restart server
verify persistence
```

Completion gate:

> A single node can reliably stream large objects to disk and retrieve them without loading the full object into Go memory.

---

## Milestone 2 — Fast Read Path and Early Baseline

Deliver:

- `io.Copy` file-serving path
- correct Content-Length
- basic GET benchmark
- syscall verification of `sendfile()`
- initial CPU and heap profile

Also run a first hardware baseline:

```text
fio
```

and compare it to application GET throughput.

Completion gate:

> A reproducible large-object GET number exists before distributed complexity is introduced.

---

## Early AWS Smoke Deployment

Do this shortly after Milestone 2.

This is not the final benchmark deployment.

Goal:

```text
prove binary runs on EC2
prove local NVMe is mounted correctly
prove private networking works
prove Node A can reach Node B
prove deployment scripts work
```

Then tear down the instances.

The purpose is to expose cloud/networking surprises early instead of during final benchmarking.

---

## Milestone 3 — Two-Node Replication

Deliver:

- primary and replica roles
- peer configuration
- internal replication endpoint
- one background replication worker
- object streaming over HTTP
- replica-side checksum validation

Validation:

```text
PUT object → Node A
wait
GET object → Node B
compare bytes
compare checksum
```

Completion gate:

> Objects written to Node A automatically appear on Node B.

---

## Milestone 4 — Persistent Replication Recovery

Deliver:

- bbolt-backed replication queue
- retry behavior
- queue persistence
- replica outage recovery
- primary restart recovery
- clear queue-depth observability

Validation:

```text
stop Node B
write many objects
confirm queue grows
restart Node A
confirm queue remains
restart Node B
wait for queue to drain
verify all replicated objects
```

Completion gate:

> Temporary replica outages do not erase pending replication work while the primary storage remains intact.

Once this milestone is complete:

> STOP ADDING DISTRIBUTED FEATURES.

Move to benchmarking.

---

## Milestone 5 — Automated Reliability Testing

Deliver:

- reproducible local two-node environment
- integration tests
- large-object tests
- replica failure tests
- restart tests
- checksum verification
- repeated retry validation

Docker Compose is useful if it saves time.

If Compose becomes a distraction, running two local processes is acceptable.

Correctness matters more than container polish.

---

## Milestone 6 — Performance Characterization

Deliver:

- hardware baselines
- cold GET benchmark
- warm GET benchmark
- buffered PUT benchmark
- durable PUT benchmark
- replication benchmark
- recovery benchmark
- memory measurements
- p50/p95/p99 latency where meaningful
- pprof profiles
- charts/tables for README

Completion gate:

> The repository contains reproducible performance numbers with enough methodology to survive follow-up questions.

---

# Benchmark Methodology

This section is mandatory.

Benchmark methodology must be decided before collecting headline numbers.

---

# 1. Separate Cold and Warm Reads

Linux page cache can make repeated file reads dramatically faster than NVMe.

Therefore report them separately.

## Cold Read Benchmark

Options:

### Method A

Use a working set substantially larger than RAM.

Example:

```text
RAM: 32 GiB
working set: 100+ GiB
```

### Method B

For controlled benchmark runs:

```bash
sync
echo 1 | sudo tee /proc/sys/vm/drop_caches
```

Then perform the read.

Use cache dropping only in isolated benchmark environments.

## Warm Read Benchmark

Read the same object again without clearing page cache.

Report explicitly:

```text
cold GET
warm-cache GET
```

Never compare a warm-cache GET number directly against a direct-I/O fio baseline as if both measure NVMe.

---

# 2. Match fio Conditions to Application Conditions

Record whether fio uses:

```text
buffered filesystem I/O
direct I/O
```

If comparing object-store reads to fio, ensure the comparison is meaningful.

Possible report:

```text
fio direct sequential read
fio buffered sequential read
object store cold GET
object store warm GET
```

This avoids misleading hardware-efficiency claims.

---

# 3. Separate Buffered and Durable Writes

Report:

```text
buffered PUT throughput
durable fsync PUT throughput
```

Do not call buffered writes durable.

A strong result may look like:

```text
buffered:  900 MiB/s
durable:   420 MiB/s
```

The gap itself is an interesting systems result.

---

# 4. Distinguish Burst From Sustained Network Performance

Before replication benchmarking:

```bash
iperf3
```

between Node A and Node B.

Run both:

```text
short test
longer sustained test
```

Record the actual observed bandwidth.

For burstable AWS networking, do not report a brief peak as sustained application throughput.

Application replication efficiency should use:

```text
sustained application throughput
────────────────────────────────
sustained measured iperf3 bandwidth
```

---

# 5. Use a Separate Load Generator

For serious RPS measurements, use a third instance.

Topology:

```text
          Load Generator
                │
         ┌──────┴──────┐
         ▼             ▼
      Node A         Node B
```

Do not run the main load generator on the server being measured if avoidable.

That prevents the benchmark client from stealing:

```text
CPU
memory bandwidth
network bandwidth
file descriptors
```

from the storage server.

---

# AWS Benchmark Environment

Preferred final topology:

```text
Load Generator
      │
      ▼
┌───────────────┐
│ Node A        │
│ Primary       │
│ local NVMe    │
└───────┬───────┘
        │
        │ private same-AZ network
        ▼
┌───────────────┐
│ Node B        │
│ Replica       │
│ local NVMe    │
└───────────────┘
```

Use:

- same Availability Zone
- private IP traffic
- instance-store NVMe
- Cluster Placement Group if convenient
- short-lived benchmark sessions
- explicit teardown scripts

A `c6id` instance is acceptable because it includes local NVMe instance storage.

Do not use EBS for the final "local NVMe storage engine" headline benchmark.

---

# Hardware Baselines

## Network

Use:

```bash
iperf3
```

Record:

```text
single-stream short-run bandwidth
single-stream sustained bandwidth
multi-stream sustained bandwidth
```

## Disk

Use:

```bash
fio
```

Record at minimum:

```text
sequential read throughput
sequential write throughput
```

Also record:

```text
direct vs buffered mode
block size
queue depth
working-set size
```

The README should make hardware baselines reproducible.

---

# Benchmark 1 — Large-Object GET

Suggested object sizes:

```text
64 MiB
256 MiB
1 GiB
```

Suggested concurrency:

```text
1
4
16
```

Report:

```text
cold MiB/s
warm MiB/s
CPU utilization
Go heap
RSS
```

Headline comparison:

```text
cold application throughput
───────────────────────────
matched fio baseline
```

---

# Benchmark 2 — Small-Object GET

Suggested object sizes:

```text
4 KiB
64 KiB
```

Suggested concurrency:

```text
16
64
128
256
```

Measure:

```text
requests/sec
p50
p95
p99
errors
CPU
```

Do not treat any arbitrary RPS threshold as mandatory.

Directional interpretation only:

```text
5k RPS      acceptable
10k+        good
20k+        excellent
30k+        excellent if methodology is sound
```

If performance is unexpectedly low, profile before guessing.

---

# Benchmark 3 — PUT Throughput

Suggested sizes:

```text
64 KiB
1 MiB
64 MiB
256 MiB
```

Measure both:

```text
buffered mode
durable fsync mode
```

Report:

```text
requests/sec
MiB/s
p50
p95
p99
```

Clearly state that application acknowledgment measures local commit latency, not replication completion.

---

# Benchmark 4 — Replication Throughput

Measure:

```text
successfully replicated bytes
─────────────────────────────
elapsed time
```

Report:

```text
MiB/s
Gbps
% of sustained iperf3 bandwidth
queue depth
CPU utilization
```

Example methodology:

```text
iperf3 sustained:        6.1 Gbps
storage replication:     5.2 Gbps
efficiency:               85%
```

Use only actual measured results.

---

# Benchmark 5 — Replica Recovery

Procedure:

```text
1. Stop Node B.
2. Write a fixed workload to Node A.
3. Record queued objects and bytes.
4. Restart Node B.
5. Measure until queue reaches zero.
6. Verify expected object count.
7. Verify checksums.
```

Report:

```text
queued objects
queued GiB
recovery duration
objects/sec
MiB/s
checksum mismatches
```

This benchmark should be considered one of the project's primary resume metrics.

---

# Benchmark 6 — Memory Efficiency

Test at least:

```text
1 GiB upload
1 GiB replication
1 GiB GET
```

Measure:

```text
Go heap
process RSS
allocation rate
GC CPU/time
```

Because bbolt uses mmap, report both:

```text
Go heap
RSS
```

Do not claim total process memory based only on Go heap.

A useful result has the form:

```text
object size: 1 GiB
peak Go heap: X MiB
peak RSS: Y MiB
```

---

# Profiling

Expose:

```text
/debug/pprof/
```

Collect:

```text
CPU profile
heap profile
allocation profile
goroutine profile
```

Questions to answer:

```text
Is the server CPU-bound?
Is CRC32c significant?
Is HTTP allocation-heavy?
Is disk limiting GET?
Is network limiting replication?
Is one replication worker sufficient?
Is GC consuming meaningful CPU?
```

---

# Optimization Policy

Only optimize observed bottlenecks.

Potential improvements:

```text
io.CopyBuffer
larger reusable buffers
HTTP transport tuning
connection reuse
sync.Pool
hash-sharded replication workers
queue deduplication
```

Each optimization should ideally produce:

```text
before
after
explanation
```

Example:

```text
replication: 3.8 → 5.1 Gbps
alloc rate: 420 → 90 MiB/s
p99 GET: 24 → 15 ms
```

---

# Replication Parallelism Stretch Goal

Do not begin with multiple workers.

If one worker becomes a verified throughput bottleneck, add:

```text
N workers
```

but shard by key:

```text
worker = hash(key) % N
```

This preserves per-key ordering.

Do not let arbitrary workers pull operations for the same key from one shared queue.

---

# Scope Protection Rules

## Rule 1

Once replication recovery works, stop adding distributed features.

## Rule 2

Benchmarking is not optional.

## Rule 3

The README is not optional.

## Rule 4

No active-active writes.

## Rule 5

No consensus.

## Rule 6

No leader election.

## Rule 7

No custom networking protocol unless HTTP is measured as the bottleneck.

## Rule 8

No `sync.Pool` until allocation profiling proves it useful.

## Rule 9

Do not chase arbitrary benchmark targets.

Measure:

```text
hardware limit
application result
efficiency
bottleneck
```

## Rule 10

If forced to choose between one more feature and one more trustworthy benchmark:

> choose the benchmark.

---

# Minimum Successful Version

The project is successful if it finishes with:

```text
PUT
GET
streaming object writes
CRC32c
bbolt metadata
buffered durability mode
fsync durability mode
two nodes
async primary-replica replication
persistent replication queue
replica outage recovery
primary restart recovery
cold/warm GET benchmark
PUT durability comparison
replication benchmark
recovery benchmark
memory benchmark
hardware baselines
README
```

DELETE is optional.

Terraform is optional.

Fancy dashboards are optional.

---

# Stretch Goals

## Tier 1

- DELETE replication
- HEAD
- graceful shutdown
- queue deduplication
- richer metrics endpoint
- improved retry policy
- benchmark automation
- deployment automation

## Tier 2

- hash-sharded replication workers
- `sync.Pool` based on profiling
- Prometheus metrics
- Terraform
- benchmark dashboard
- structured fault injection

## Not for Initial Two-Week Scope

- active-active
- automatic failover
- anti-entropy
- read repair
- replica promotion
- consensus
- multi-node membership

---

# Failure Semantics

Document these clearly.

## Replica Failure

```text
primary remains writable
queue grows
replica becomes stale
replication resumes after recovery
```

## Primary Failure

```text
new writes unavailable
replica may serve already replicated reads
```

## Primary Storage Loss Before Replication

Because replication is asynchronous:

```text
local ACK
↓
replication pending
↓
primary storage permanently lost
```

may lose an acknowledged object.

Do not claim zero data loss.

## Network Partition

```text
primary continues accepting writes
replica falls behind
queue grows
replica converges after connectivity returns
```

## Buffered Durability Mode

A successful response does not imply crash-safe persistence.

## Durable Mode

A successful response is intended to represent the project's strongest local persistence path, subject to the documented filesystem/metadata recovery model.

---

# Final Deliverables

The repository should contain:

```text
working Go server
two-node demo
persistent replication recovery
automated correctness tests
failure-recovery tests
hardware baseline scripts
benchmark scripts
pprof workflow
AWS deployment script
benchmark result tables
benchmark graphs
architecture diagram
failure semantics documentation
polished README
```

---

# Resume Metric Strategy

Do not decide the exact resume bullet before measurements exist.

Aim to produce numbers from several categories so the project remains resume-worthy even if one benchmark disappoints.

Possible metrics:

```text
cold GET throughput
warm GET throughput
small-object RPS
p99 latency
buffered PUT throughput
durable PUT throughput
replication Gbps
% of measured network bandwidth
recovery objects/sec
recovery GiB/sec
peak Go heap
peak RSS
checksum mismatch count
```

A strong final bullet structure:

> Built a two-node replicated object store in Go with streaming I/O, CRC32c verification, crash-aware local commits, and persistent asynchronous replication; sustained **X Gbps replication** and **Y MiB/s cold reads**, reaching **Z% of measured host/network bandwidth**.

Possible reliability bullet:

> Persisted replication work across replica outages and primary process restarts, recovering **N objects / M GiB in T seconds** with **zero checksum mismatches**.

Possible durability/performance bullet:

> Benchmarked buffered vs `fsync`-durable writes and streamed **1 GiB objects** with **X MiB peak Go heap / Y MiB RSS**, using `pprof`, `fio`, and `iperf3` to identify storage and network bottlenecks.

Use only actual measured results.

---

# Definition of Done

The project is done when:

- Large objects can be streamed into the primary without full-object buffering.
- CRC32c metadata is stored and verified.
- Buffered and durable write modes both work.
- GET uses the intended efficient file-serving path.
- Two nodes communicate over the private network.
- Node A replicates objects asynchronously to Node B.
- Pending replication survives Node B outages.
- Pending replication survives restarting Node A.
- The replica eventually converges.
- Cold and warm cache behavior are measured separately.
- Buffered and durable write performance are measured separately.
- Raw disk and network baselines are recorded.
- Replication throughput is compared against sustained network bandwidth.
- Memory reporting includes both Go heap and process RSS.
- Failure semantics are documented honestly.
- Benchmark methodology is reproducible.
- The README contains real measured numbers and clear architecture diagrams.

Once these are true, stop adding features and polish the project.
