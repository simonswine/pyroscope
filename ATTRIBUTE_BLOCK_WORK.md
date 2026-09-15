# Attribute Block Implementation Work

## Current Status (from Assessment)

**Committed:** Format primitives, scoped keys, typed dictionaries, presence/value postings, per-key forward columns, metadata queries, decoded-page caching, reader lifecycle, standalone range-planning/fetching helpers.

**Unstaged:** Entity count in header and ForwardColumnsFor

**Coverage:** 75.4% with all tests passing under -race

## Critical Issues to Address

### 1. Malformed Blocks Can Panic ⚠️
- Checksum-valid block with inconsistent column lengths passes validation and panics in Series
- Allocation counts and postings IDs need stronger bounds
- No cross-page validation

### 2. Queries Are Not Selective Yet 🔴
- `candidateIDs` loads every column, all dictionaries, all postings—even for one-key projection
- Per-key split increases cold-query GET count without delivering projection savings
- `ForwardColumnsFor` exists but is not used in query path

### 3. Bounded Fetches Not Connected 🔴
- Reader queries bypass the range-planning helpers
- `FetchRanges` limits active requests but retains completed buffers outside byte accounting
- Some error paths return while requests remain active
- Individual pages can exceed MaxLength

### 4. Legacy Equivalence Unproven 🟡
- Regex matching differs from Prometheus for newline-containing values
- Series behavior differs around empty labels and projections
- Needs compatibility adapter and differential test harness

### 5. Model Guarantees Incomplete 🟡
- Writer accepts sample scope on series-only indexes
- Assigns separate IDs to duplicate entities
- Coarse-time metadata contains no actual coverage information

## Implementation Priority (from plan)

### Phase A: Correctness First (THIS WEEK)
1. ✅ Add entity count to header (unstaged)
2. ✅ Add ForwardColumnsFor method (unstaged)
3. 🔄 Define format revision/feature policy
4. 🔄 Add bounds checking: root bytes, counts, decoded allocation
5. 🔄 Reject inconsistent columns, out-of-universe postings
6. 🔄 Add cross-page validation
7. 🔄 Add permanent regression tests for panics
8. 🔄 Start Format1 parity harness (legacy differential testing)

### Phase B: Selective Query Work (NEXT)
1. Make candidateIDs use validated entity count instead of column length
2. Use presence postings for filtered names
3. Read only target columns for filtered values
4. Read only projected columns for series
5. Wire ForwardColumnsFor into query path
6. Add byte/range-recording tests

### Phase C: Bounded Reader Execution (AFTER B)
1. Fix worker cleanup and request limits
2. Account for retained/decoded memory
3. Route reader queries through budgeted pipeline
4. Add resource limit tests

### Phase D: Benchmark and Paging (AFTER A/B/C)
1. Establish TSDB baseline
2. Split dictionaries/postings by key
3. Add entity-range column pages
4. Measure codecs/page sizes

### Phase E: Integration (MUCH LATER)
- Time index, publication, compaction
- Production writer integration
- DO NOT START UNTIL A/B/C/D COMPLETE

## Work Log

### 2025-01-XX - Initial Assessment
- Reviewed current implementation
- Identified 5 main issue categories
- Unstaged changes: entity count + ForwardColumnsFor
- All tests passing, 75.4% coverage
- Starting Phase A correctness work

