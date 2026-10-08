# Attribute index prototype and superseded design

> **Superseded for integration.** This document records the original standalone
> attribute-block prototype and its assessment. The active integration design is
> [Attribute index integration plan](plan-attribute-index-integration.md). New
> work uses `pkg/attributeindex`, calls the format `AttributeIndexV1`, embeds an
> attribute-index payload in `block.bin` as an anonymous pseudo-dataset, and
> rebuilds that payload during normal profile compaction. It does not create or
> compact independent `attributes.bin` objects.

## Historical status and scope

The isolated prototype, now in `pkg/attributeindex`, was reviewed through commit
`fd97b4897` and the subsequent reader and writer changes. The format is **not
frozen**, and production write-path, query-path, metastore, and compaction
integration do not yet exist.

The prototype implements parts of the historical steps 2–4 below, but does not
yet demonstrate legacy result equivalence, selective bounded query execution, or
safe time and coverage semantics. See [Implementation audit and next steps](#implementation-audit-and-next-steps)
for the historical inventory, review findings, and ordered follow-up work.

The prototype builds a dedicated, immutable attribute index for metadata queries:

- `LabelNames`
- `LabelValues`
- `Series`, including projected label sets
- Label selectors, with a future scoped and typed query model for OTLP

The standalone object and independently scheduled compaction described below are
historical only. The integration plan defines the current embedded-payload and
normal-profile-compaction design. `DatasetFormat1` remains reserved for the
existing tenant-wide TSDB index; the attribute index will use a new dataset
format when block integration is added.

Use a small custom, page-oriented format and an S3 range-buffered reader. Do not
introduce Arrow in the first implementation. Keep the existing profile storage
unchanged; a future OTLP fact store may use Parquet independently of this index.

## Motivation

The current V2 metadata query path opens entire TSDB sections even when it only
needs a small part of the index. Unfiltered `LabelNames` ultimately returns names
from the postings-offset table, but initializes the full TSDB reader first.

For tenant 13, a metastore lookup over 2026-09-15 13:59:30–15:59:30 UTC returned
861 blocks, each with one matching Format1 tenant index. This is one observed
query, not a general workload size. Network saturation was suspected during
local experiments, but was not established by measurements.

Two independent improvements are needed:

1. Read fewer bytes from each object.
2. Query fewer objects through independent attribute compaction.

Changing profile-query batching from 4 to 32 blocks per READ reduces internal
gRPC fan-out, but is not a global object-store I/O concurrency limit. That local
experiment is separate from this design.

Relevant existing code:

- `pkg/frontend/readpath/queryfrontend/query_frontend.go`
- `pkg/querybackend/query_label_names.go`
- `pkg/querybackend/query_label_values.go`
- `pkg/querybackend/query_series_labels.go`
- `pkg/block/section_tsdb.go`
- `pkg/block/dataset_index.go`
- `pkg/phlaredb/tsdb/index/index.go`

## Goals

- Answer attribute discovery without reading profile or symbol tables.
- Support selectors before projection, preserving conjunction semantics.
- Preserve scope and value types for future OTLP storage.
- Avoid per-entity Go maps, interface values, and protobuf materialization in
  the filtering path.
- Fetch once, decompress once, and query through buffer-backed views where
  possible; materialize only the result.
- Bound object-store requests, buffered bytes, decoded working sets, and output.
- Make entity granularity, time semantics, completeness, and compatibility
  explicit.

## Non-goals for the first implementation

- Replacing profile storage or building a general-purpose database.
- Arrow IPC as the storage format or an Arrow dependency.
- Arbitrary nested-path queries into arrays/maps.
- Silently coercing typed attributes into strings.
- Providing sample-level selectors by flattening sample attributes onto series.
- Claiming exact per-series time coverage from existing Format1 chunk metadata.

## Logical model

### Scoped attribute identity

An attribute key is `(scope, name)`. Scope is part of identity, not a string
prefix or response-only annotation.

Initial scope vocabulary:

| Scope | Meaning |
| --- | --- |
| `legacy` | Existing unscoped Pyroscope labels |
| `resource` | OTLP resource attributes |
| `instrumentation_scope` | OTLP instrumentation-scope attributes |
| `profile` | Profile-level attributes |
| `sample` | Sample-level attributes, once the matching entity index exists |

Keep scope IDs extensible. Preserve attribute keys exactly, including UTF-8.

Within one immutable attribute shard:

```text
attributeID -> (scope, name)
value reference -> (type, dictionary-local valueID)
entityID -> indexed entity
```

Use dense local IDs. A uint32 entity-ID representation is acceptable only with
an enforced shard-size limit; reject or split before overflow. IDs are not
stable across blocks or compaction generations.

### Typed values

Support a versioned tagged value model:

```text
string
bool
int64
float64
bytes
array<value>
map<string, value>
```

Type belongs to the value, not the attribute key. A key may have several observed
value types. Partition its dictionaries by type.

Semantics:

- `"42"`, `42`, and `42.0` are distinct stored values.
- Equality is type-aware; numeric coercion must be explicit if later supported.
- Regex matchers operate on strings, not stringified numbers or other types.
- ABSENT is distinct from empty string, zero, false, and empty collections.
- Arrays preserve order.
- Maps require canonical ordering and an explicit duplicate-key policy.
- Float equality/canonicalization, including NaNs and signed zero, must be
  specified before dictionary deduplication is implemented.
- Preserve complex values for retrieval. Whole-value equality requires a
  canonical encoding; nested-path indexes are a separate extension.

### Entity granularity

The first index is series-oriented: each entity is a unique complete label set.
Deduplicate using full canonical equality. Fingerprints accelerate lookup but
must not be the sole equality check.

The header declares the entity kind. Resource, profile, and sample indexes may
later use different entity kinds and explicit parent relationships.

Scope does not imply entity granularity. For example, a conjunction on two
sample attributes must match the same sample, not two different samples within
one profile. Inherited resource attributes must be mapped to the selected entity
universe explicitly. Do not enable sample scope on a series-only index as a
shortcut.

## Physical format

One immutable object per attribute shard, with independently readable pages:

```text
attributes.bin
  header
    magic, format version, required feature flags
    tenant, entity kind, shard scheme, time semantics
  scoped attribute directory
    scope/name strings, observed types
    dictionary, presence, postings, and column references
  typed value dictionary pages
  presence and attribute-value postings pages
  forward attribute column pages, grouped by entity-ID ranges
  activity/time pages
  optional entity relationship pages in future versions
  paged directory
  fixed-size footer
    root directory offset/length, version, checksum
```

Page descriptors contain a page kind, encoding/codec, offset, compressed length,
decoded length, and checksum. Counts and offsets must be validated before
allocation or access. Bound decompression sizes and nesting depth.

Keep the root directory small. Large page directories are paged themselves;
opening a block must not require loading every value or every page reference.

The metastore may duplicate the root-directory offset and length to avoid the
initial footer GET. Keep the footer so the object remains independently readable.

### Encodings

Use proven encodings rather than inventing a new compression system:

- Names: small sorted scoped directory, separate from value strings.
- Strings/bytes: offsets plus payloads; prefix compression with restart points
  where useful.
- Integer values: delta or bit-packed encoding as appropriate.
- Boolean values: compact bit representations.
- Float values: defined binary representation and canonicalization rules.
- Arrays/maps: canonical tagged payloads, bounded during decoding.
- Postings: sparse delta-encoded entity-ID lists or compressed bitmaps,
  selected by density.
- Forward columns: constant, run-length, sparse, or bit-packed value references,
  with ABSENT represented separately.

Dictionary page indexes support both string/value lookup and value-ID lookup.
Fetching a few result IDs must not require scanning the full dictionary.

Consider roughly 64–256 KiB pages as a benchmark starting point, not a promised
format constant. Use independently compressed pages; never compress the whole
object as one stream.

Small control structures may use versioned protobuf messages. Hot dictionary,
postings, and column payloads use specialized encodings. Choose exact codecs
and bitmap libraries after checking existing dependencies and benchmarking.

## Query execution

### Selector evaluation

Build a candidate entity set:

```text
C = entities active in query time range
    intersect matcher_1
    intersect matcher_2
    ...
```

Attribute-value postings select entities. Presence postings distinguish ABSENT
from stored values. Persist cardinality and encoded-size information to guide
predicate ordering and read planning.

Legacy Prometheus selectors must retain existing behavior:

- Equality uses the corresponding value postings.
- Regex uses anchored matching over the appropriate string dictionary.
- Negative matchers complement within the block's entity universe.
- Matchers that accept the empty string also account for absent labels.
- Multiple matchers are combined before metadata projection.

New typed selectors use structured keys and values, conceptually:

```text
{scope, name, operator, typed_value}
```

The public textual syntax is not decided by this plan. Legacy unscoped selectors
address the legacy compatibility view, not an implicit union of all scopes.

### Attribute names / LabelNames

For no selector and full coverage of the block's time extent, read only the
scoped attribute-name directory. An optional scope filter restricts discovery.

For a selector or partial time coverage, compute C and return keys whose
presence postings intersect C. If C is small, inspecting the relevant forward
row groups may be cheaper than reading presence postings for every key.

### Attribute values / LabelValues

For no selector and full time coverage, read only the requested scoped
attribute's dictionaries. New APIs return typed values.

For filtered queries, choose between:

1. Read the target forward column for C, deduplicate typed value IDs, then fetch
   only result dictionary pages.
2. For a small dictionary, test its value postings for intersection with C.

Use candidate size, dictionary cardinality, and page sizes to choose. High
cardinality must not force one GET per distinct value.

### Series / attribute-set projection

Compute C, read the requested attribute columns for selected entity row groups,
decode referenced dictionary pages, then deduplicate complete projected sets.

A full-set query reads all relevant attributes; a projection reads only its
requested attributes. Deduplicate across shards/blocks using scoped, typed
content, not their local IDs.

Large result sets remain expensive. Bound aggregation and response memory;
return an explicit limit error rather than silently truncating. Pagination or
streaming for new APIs is an open API decision.

## Time semantics

Do not approximate activity using just a series minimum and maximum timestamp.
A series present at 09:00 and 17:00 is not necessarily present at noon.

For a native timestamp-aware index, use:

- Coarse activity bitmaps, initially benchmarked at minute granularity.
- Paged per-entity event timestamps for exact checks in partially covered
  boundary buckets.

Union activity from fully covered buckets, check boundary timestamps, then
intersect with selectors. Full-block discovery can still use dictionary-only
shortcuts because every indexed entity has activity inside that block's extent.
Use a documented half-open interval convention internally, with legacy endpoint
translation tested explicitly.

Existing Format1 chunk metadata sets only SeriesIndex; its time fields are zero.
It cannot supply exact activity timestamps.

Therefore declare time semantics in block metadata:

- Native exact activity: sourced from actual profile/sample timestamps.
- Legacy coarse coverage: only the source dataset's coverage is known.

Do not silently upgrade legacy inputs to an exact index. Either retain enough
source-coverage information to preserve the existing query behavior, or keep
legacy reads as a fallback. A plain daily union must not replace partial-window
queries when it broadens their results. Exact backfill requires reading a source
that actually contains timestamps.

The first implementation should explicitly choose and test its legacy behavior
before independent hour/day compaction is enabled.

## S3 range-buffered reader

Implement an explicit page-fetching reader, not a `bufio.Reader` over a
whole-object GET and not an unrestricted ReaderAt that issues a GET for every
small decoder read.

```text
query planning
  -> required page references
  -> sorted/coalesced byte ranges
  -> S3 ranged GETs
  -> owned buffers
  -> bounded page decompression
  -> in-memory decoding/views
```

S3 serves one contiguous range per GET. Fetch disjoint pages separately or
coalesce them when reading the gap is cheaper than another request. Switch to
larger sequential ranges when most pages of a section are needed.

Reader requirements:

- Bound concurrent GETs and compressed/decoded bytes in flight separately.
- Respect context cancellation while fetching and decoding.
- Close response bodies on all paths; detect short reads and validate checksums.
- Retry bounded range reads, without unbounded retry amplification.
- Use immutable object identities so ranges cannot come from mixed generations.
- Keep fetched/decompressed buffers alive while views reference them.
- Materialize owned result values before returning buffers to pools.
- Avoid per-value S3 requests and avoid full-dataset prefetch.

Start with query-lifetime buffers and directory reuse within a query. Add a
bounded cross-query cache only after measurement. Cache keys must include tenant,
immutable object identity, and page identity.

This does not eliminate all copies. Network buffering, decompression output,
some encoded-column decoding, and API serialization remain. The goal is to avoid
unnecessary intermediate objects and repeated decoding.

## Independent compaction and publication

Build hourly attribute indexes and compact them into daily blocks. A daily block
must preserve sub-day activity/coverage, not just a union of attributes.

Use bounded-size shards, with a declared shard scheme. A stable hash of the full
canonical entity identity is a starting point. Do not require service_name as a
physical partition because queries and OTLP data may omit it.

Compaction merges full entity identities and typed dictionaries, remaps local
IDs, and preserves time information. It does not need profile values for native
attribute inputs with sufficient time data.

A grace delay is not proof of completeness. Publish immutable generations through
metastore manifests with explicit coverage and provenance:

- A reader selects a published base plus uncovered/newer deltas.
- Late-arriving data produces a delta even for an already compacted time slot.
- Compaction absorbs deltas into a new immutable generation.
- Publication atomically updates the manifest only after objects are durable.
- Delete replaced objects only after publication and a reader grace period.
- Regular profile compaction must not invalidate coverage merely by changing
  physical source-block IDs.

Define a compaction-stable lineage or ingestion frontier before allowing a base
to suppress legacy inputs. Hour/day replacements must not lose late data,
partially covered time intervals, or concurrent writes. Attribute compaction
must never delete regular profile blocks.

Coordinate this work with `plan-tsdb-only-compaction.md`, but do not inherit its
plain daily-union or delay-only completeness assumptions. That existing plan is
not modified by this proposal.

## API and compatibility

- New discovery APIs return scoped keys and typed values.
- Existing LabelNames/LabelValues/Series APIs remain compatibility adapters.
- Preserve current legacy selector, UTF-8 capability, empty-value, ordering,
  deduplication, and time behavior through explicit tests.
- Do not stringify all OTLP types or flatten all scopes into legacy labels.
- Enable mixed old/new reads only with explicit coverage selection; deduplication
  alone does not repair missing coverage or incorrect time semantics.
- Readers reject unsupported required features and invalid encodings. The planner
  chooses legacy fallback only where a compatible source is known to exist;
  corruption must not silently become an empty result.
- Metastore holds compact manifests/directories, not the full value dictionaries.

## Implementation audit and next steps

### Committed implementation through `fd97b4897`

All 13 implementation commits, starting at `d8e9cdf8c`, are confined to
`pkg/attributeindex`; there are no callers outside that package.

| Area | Implemented | Important remaining work |
| --- | --- | --- |
| Format and writer | Separate object, magic/version, fixed footer, CRCs, scoped key directory, copied and validated inputs | Required feature/encoding flags, bounded root/paged directories, cross-page validation, unique entity IDs, bounded writer/shards |
| Types and time | String, bool, int64, bytes; tagged values; explicit coarse mode; writer rejects exact activity | Float/array/map canonicalization; sample-scope guard; actual time extent and source coverage; reader rejection of unsupported exact mode |
| Dictionaries and postings | One dictionary page for all keys/types; one page for all presence/value postings; delta-encoded entity IDs | Key/type/value-range paging, selective lookup, cardinality/size statistics, measured sparse/dense choices |
| Forward columns | `fd97b4897` writes one page per key, with zero representing ABSENT | Query selection, entity row groups, per-page caching, matching column lengths |
| Queries | Scoped/typed names, values, series; postings-based matchers; projection and full-content result deduplication | Actual legacy adapters and differential tests; entity deduplication at write time; selective reads; output/working-set limits; time filtering |
| Reader ownership | Three range reads to open; lazy page reads; owned results; decoded query-lifetime cache; `Close` | Avoid full-cache clones in internal execution; cache limits; coalesced concurrent loads; cancellation during cached queries/decoding |
| Range execution | Standalone `PlanPageRanges` and `FetchRanges` helpers | Reader integration, request-size enforcement, retained/decoded-byte limits, error-path cleanup, bounded retries, shared multi-object budgets |

The current physical layout is still a small-fixture format: one full entity
page, one all-key dictionary page, one all-key postings page, one full-height
column per key, and an unpaged root directory. Pages are uncompressed. The
64 MiB page check happens after payload construction and is not a bound on
writer or decoder memory. The entity payload also duplicates attributes already
represented by columns and dictionaries.

### Unstaged implementation

There are no staged changes. The two Go files contain work toward selective
forward reads:

- `writer.go` writes entity count into header bytes 12–15; `reader.go` loads it.
  Nothing uses or validates that count yet. Earlier prototype objects have zero
  in those bytes, so using the count requires an explicit format compatibility
  decision, not silently treating older nonempty objects as empty.
- `Reader.ForwardColumnsFor` fetches requested keys' columns, but no query calls
  it. It still loads/clones all dictionaries, bypasses the column cache, scans
  page descriptors for each requested key, and treats a missing page for a
  known key like an absent key. It has no new tests and does not preserve the
  full reader's post-fetch close check.

Keep this work as an incomplete selective-query change, not a finished reader
optimization. Both plan files were untracked at review time;
`plan-tsdb-only-compaction.md` remains an alternative proposal, not implemented
compaction work.

### Review findings

1. **Corruption can escape validation and panic.** The reader accepts
   checksum-valid columns with inconsistent entity counts; `Series` then indexes
   past a shorter column. Postings are not validated against the entity universe
   (positive matching sometimes drops bad IDs; negative equality indexes them
   directly). Several decoders allocate from uint32-sized counts without first
   bounding them by available payload and a decoded-memory budget. Root-directory
   length is checked against object size, but has no independent memory cap.
   Duplicate/missing pages and invalid key references need structural checks.
2. **The performance helpers do not affect queries yet.** `candidateIDs` always
   fetches all forward columns, all dictionaries, and all postings, even for
   unfiltered series or a one-key projection. Splitting columns by key currently
   increases cold-query GET count with key count. `readPage` goes directly to
   `readRange`, never through `PlanPageRanges` or `FetchRanges`.
3. **Fetch budgets are not retained-memory budgets.** `FetchRanges` releases
   byte permits when each GET finishes but retains every returned buffer until
   the whole call returns. It bounds active requests, not accumulated buffers.
   Validation/acquisition errors after launching work can return without
   canceling and joining that work; cancellation can also obscure the original
   fetch error. `PlanPageRanges.MaxLength` only bounds coalescing, not individual
   pages. No decoded, cache, result, or cross-reader budget exists.
4. **Legacy compatibility is not established.** Regex compilation omits
   Prometheus's dot-matches-newline behavior: `x=~".*"` misses a stored `"a\nb"`.
   The existing `getSeriesLabels` drops empty-valued labels, treats an empty
   projection as full selection, and returns no sets for an entirely unknown
   projection. The prototype intentionally has different typed/full-set behavior;
   preserve that through a separate legacy adapter rather than losing stored
   values. Legacy absent-as-empty matching also currently applies to non-legacy
   scopes, without a separate typed absence contract. Duplicate projection keys
   produce duplicate attributes.
5. **Some logical-model guarantees are only documented.** `AddEntity` accepts
   sample scope on a series-only index and stores duplicate complete entities as
   separate IDs. Final result deduplication does not fix that entity universe or
   its storage cost. Coarse mode carries neither time bounds nor source coverage;
   query methods have no time range. The writer refuses exact mode, but the reader
   can accept metadata declaring it without requiring activity pages.
6. **Cache ownership is safe but expensive and incompletely tested.** Internal
   query execution repeatedly deep-clones complete dictionaries, postings, and
   columns. Concurrent cold calls can fetch/decode the same page more than once.
   Warm queries do not honor an already canceled context. The selective WIP adds
   a second loading path without shared caching/lifecycle behavior.

Validation performed:

- `go test -race -count=1 -cover ./pkg/attributeindex` passes on the working tree:
  13 existing top-level tests, 75.4% statement coverage. The committed Go sources
  also pass under a Go overlay, without changing the working files.
- Temporary review probes reproduced the column-length panic, regex mismatch,
  accepted sample scope and duplicate entities, duplicate projection keys,
  ignored warm-query cancellation, selective-reader cache/missing-page problems,
  oversized planned requests, and fetch work surviving an error return. These
  probes were removed after review; add permanent regression tests with the fixes.
- No benchmarks, fuzz targets, real Format1 comparison fixtures, S3 measurements,
  multi-tenant integration tests, or time/compaction tests exist in this package.
  Passing unit tests do not yet establish the first deliverable's result or
  performance claims. No production workload was queried during this review.

### Ordered follow-up work

#### A. Establish correctness and resource invariants first

Split into focused decoder/format and query-semantics commits:

- Define an experimental format revision/required-feature policy. Current commits
  have changed layouts while retaining version 1; explicitly reject incompatible
  prototype objects or provide tested compatibility. Make entity count validated
  control metadata, including zero-entity and attribute-free entities.
- Bound root bytes, descriptor/key/value/entity counts, decoded allocation, and
  writer growth before allocation. Reject inconsistent columns, out-of-universe
  postings, duplicate/invalid page references, unsupported exact mode, and sample
  scope in series indexes. Deduplicate entities by full canonical content before
  assigning IDs; enforce shard/entity limits before overflow or oversized work.
- Add permanent regression tests for the findings above and fuzz bounded decoders.
  Normalize or reject duplicate projection keys. Separate typed absence semantics
  from legacy matching; retain scoped/type distinctions in storage.
- Start a local Format1 parity harness using the existing metadata-query behavior
  as the oracle. Cover all four matchers, absent/empty values, newline and UTF-8
  values, conjunction, unknown/duplicate/nil/empty projections, duplicate inputs,
  and scope/type collisions. Use the same eligible source datasets on both sides;
  do not imply partial-time equivalence from a timeless union.

**Exit:** malformed objects return bounded errors rather than panics or oversized
allocations; the supported full-source legacy fixture matrix matches exactly.

#### B. Finish the selective-query work already in progress

- Make candidate evaluation return only a candidate set, using validated entity
  count instead of opening forward columns to discover the universe. No selectors
  means no postings read; an empty candidate set stops before materialization.
- Use presence postings for filtered names. Read only the target column for
  filtered values and only projected columns for projected series; full series
  remains an explicit all-column operation.
- Complete `ForwardColumnsFor` with indexed page lookup, shared per-page caching,
  count validation, distinct absent-key versus missing-page behavior, and defined
  concurrent load/close semantics. Retain owned public results but use read-only
  reader-owned data internally, with a lifetime that is safe under `Close`.
- Add byte/range-recording tests, not only total GET assertions. After opening,
  a projected query must not fetch unrelated columns or the entity payload, and a
  repeated query must not refetch/redecode cached pages. Test empty projections and
  attribute-free entities explicitly.

**Exit:** projection reduces actual column bytes read. All-key dictionaries and
postings remain a documented limitation until step D, not a claimed solved issue.

#### C. Connect a genuinely bounded range pipeline

- Validate all ranges before starting work, enforce individual maximum lengths,
  and cancel/join workers on every exit while preserving the original error.
- Execute bounded batches or consume/release ranges incrementally. Distinguish
  active compressed bytes, retained buffers, decoded/cache bytes, and result
  bytes. Merely holding permits until return in an all-buffers API can deadlock;
  define ownership and release points explicitly.
- Route reader control/page fetches through the budgeted pipeline, slice coalesced
  responses into pages, and validate each checksum before decoding. Share request
  limits across readers/objects in a query; per-reader limits alone do not bound
  tenant-wide fan-out. Check cancellation during CPU work and cache hits too.
- Add short-read, body-close, blocking-source, concurrent-close, cancellation,
  error-propagation, peak-memory and request-limit tests. Define bounded transient
  retries in coordination with bucket-client retries; never retry corrupt pages
  as if they were transient failures. Require immutable object identities.

**Exit:** actual reader queries, not just helper tests, demonstrate bounded
requests and live memory with no work surviving return/close unexpectedly.

#### D. Measure and complete page-level selectivity

Collect the current TSDB baseline using the step A harness while B/C are being
implemented; use it to guide this step rather than choosing codecs speculatively.

- Add an inspection/benchmark tool and fixed small/large, low/high-cardinality
  fixtures. Compare cold/warm names, values, equality/regex/negative selectors,
  and projected/full series against full TSDB reads. Keep the optional names-only
  TSDB optimization and gRPC batching as separately reported experiments.
- Split dictionaries/postings by key, with typed dictionary partitions and indexed
  value/ID ranges. Add entity-range column pages and paged directories so neither
  high cardinality nor key count forces whole-section reads or one GET per value.
- Remove or make optional the duplicate full entity payload once all required
  reads can reconstruct entities from columns. Add bounded/streaming output to
  the writer rather than building every page and the whole object simultaneously.
- Measure page sizes, coalescing gaps, density encodings and codecs. Deduplicate
  value IDs/projected references before copying final values, and enforce output
  limits before materializing an unbounded set.

**Exit:** report result equality, GETs, bytes fetched/decoded, allocations, peak
live memory, CPU and latency. Do not freeze v1 or claim savings before this data.

#### E. Resolve time and publication, then integrate

- Initially permit only same-source/full-coverage experiments and retain legacy
  reads otherwise. Specify source coverage for coarse inputs and actual timestamp
  inputs for native activity; implement half-open bounds and gapped/boundary-time
  tests before enabling time-aware replacement.
- Define compaction-stable lineage/frontiers, late-data deltas, atomic manifests,
  size-bounded shards, retention, and reader grace periods. Do not copy the
  TSDB-only plan's daily-union, fingerprint-only, or delay-only assumptions.
- Add writer integration behind independent write enablement and shadow reads
  against identical source coverage. Only after parity and manifest correctness
  should hourly/daily compaction and coverage-based legacy suppression be enabled.
- Add API/config/protobuf wiring and generated files when these contracts settle;
  keep legacy compatibility adapters distinct from scoped/typed APIs.

**Exit:** demonstrate late writes, partial windows, concurrent profile compaction,
publication failures and retention without missing/broadened results, cross-tenant
mixing, or deletion of profile objects. Then follow the staged rollout below.

The immediate implementation work is **A, then B and C**. Baseline collection can
run alongside them. Production integration and independent compaction are not the
next commits.

## Implementation sequence

### 1. Baseline and narrow current-format experiment

- Fix representative time ranges and collect existing query results.
- Measure object-store bytes, GET count, query latency, CPU, and memory.
- Optionally implement postings-offset-table-only reads for unfiltered
  LabelNames on current Format1 as a separate experiment.
- Keep gRPC batching experiments separate from attribute-block benchmarks.

### 2. AttributeIndexV1 primitives

- Specify scoped keys, typed equality, entity kinds, and time semantics.
- Implement the page directory, writer, buffered range reader, and inspection
  command using in-memory/local test buckets first.
- Implement independently encoded dictionaries, postings, and forward columns.
- Test round trips, deterministic encodings, buffer ownership, and corruption.

### 3. Series-oriented query prototype

- Build legacy-scoped string indexes from known fixtures.
- Implement names, values, and series with selectors and projection.
- Exercise native typed/scoped fixtures even before OTLP ingestion integration.
- Use an explicit coarse-time mode or timestamp-aware inputs; do not invent
  activity timestamps while converting Format1.

### 4. S3 experiments and bounded execution

- Benchmark range planning, coalescing, page sizes, and sparse/dense choices.
- Add request and byte budgets, cancellation, and bounded retry behavior.
- Compare with full TSDB reads and the narrow names-only optimization.

### 5. Writer integration, time indexes, and compaction

- Produce native activity data where timestamps are available.
- Define manifest lineage/frontier and late-data behavior.
- Add hourly/daily compaction and safe replacement with size-bounded shards.
- Treat retention, deletion, and in-flight readers as correctness requirements.

### 6. Shadow reads and staged rollout

- Keep writing and reading enablement independently controlled.
- Shadow fixed workloads and compare exact results under the declared semantics.
- Enable for selected tenants before expanding.
- Add configuration/protobuf definitions only once the design is settled;
  run `make generate` and include generated files with implementation changes.

## Validation

Correctness coverage:

- Scope collisions with identical names in different scopes.
- Mixed types on one key; empty versus absent; complex values and floats.
- Equality, negation, regex, and matchers accepting empty strings.
- Conjunction on the same entity and projection after filtering.
- Multi-tenant queries and cache isolation.
- Fingerprint collisions and full-content deduplication.
- Arbitrary time boundaries, gapped activity, late data, and mixed legacy inputs.
- Compaction ID remapping, publication races, retention, and reader grace periods.
- Corrupt/truncated pages, invalid offsets, unsupported versions, decompression
  limits, cancellation, and no buffer reuse while views remain live.

Benchmark workloads:

- Unfiltered names and values.
- Selective equality and broad regex selectors.
- Negative selectors and missing-label matches.
- Low-cardinality service names and high-cardinality attribute values.
- Projected versus full series sets.
- Full-slot versus partial-window queries.
- Small and large tenants, cold reads and directory/page reuse.

Report result equality, bytes fetched, GET count, bytes decoded, allocations,
peak buffered memory, CPU, and end-to-end latency. Do not set savings targets
until page-size and cardinality distributions have been measured.

## Open decisions before freezing v1

1. Exact float, map, and unsupported/unset OTLP value semantics.
2. Page codecs and compressed-postings implementation, preferably reusing
   existing dependencies.
3. Legacy time-coverage representation and native boundary timestamp cost.
4. Manifest lineage/frontier representation and retention interaction.
5. Size-based sharding thresholds and shard-scheme evolution.
6. Public scoped/typed selector API and large-result pagination.
7. Which directories belong in metastore metadata versus object pages.

## First deliverable

A series-oriented attribute-block writer and range-buffered reader, with scoped
and typed fixtures and a result-equivalent legacy metadata query prototype.
No Arrow dependency, no profile-storage migration, and no automatic replacement
of legacy blocks until time and completeness guarantees are demonstrated.
