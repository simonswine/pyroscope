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
1. ✅ Add entity count to header (committed ac5639c2c)
2. ✅ Add ForwardColumnsFor method (committed ac5639c2c)
3. ✅ Add bounds checking: entity count, allocation estimates (committed ac5639c2c)
4. ✅ Reject inconsistent columns, out-of-universe postings (committed ac5639c2c)
5. ✅ Add cross-page validation (committed ac5639c2c)
6. ✅ Add permanent regression tests for panics (committed ac5639c2c)
7. ✅ Use validated header entity count in queries (committed ac5639c2c)
8. 🔄 Define format revision/feature policy
9. 🔄 Start Format1 parity harness (legacy differential testing)
10. 🔄 Add entity deduplication before ID assignment
11. 🔄 Reject sample scope on series-only indexes

### Phase B: Selective Query Work (NEXT)
1. ✅ Make candidateIDs use validated entity count instead of column length (ac5639c2c)
2. ✅ Create candidateIDsSelective that uses ForwardColumnsFor
3. ✅ Read only target columns for filtered values
4. ✅ Read only projected columns for series
5. ✅ Wire ForwardColumnsFor into query path (Series, Values, Names)
6. ✅ Add validation to ForwardColumnsFor for entity count consistency
7. ✅ Add range-tracking tests to verify selective fetching
8. 🔄 Optimize presence queries (use postings without columns when possible)
9. 🔄 Consider per-key dictionary/postings split (requires format change)

### Phase C: Bounded Reader Execution (COMPLETE)
1. ✅ Fixed FetchRanges cleanup on error/cancellation
2. ✅ Added memory budget tracking to Reader (maxDecodedBytes)
3. ✅ Implemented fetchPages() using FetchRanges with coalescing
4. ✅ Converted all page reads to use fetchPages()
5. ✅ Added 9 comprehensive resource/cleanup tests
6. ✅ Verified cancellation, error handling, memory tracking

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

### 2025-01-15 - Initial Assessment
- Reviewed current implementation
- Identified 5 main issue categories
- Unstaged changes: entity count + ForwardColumnsFor
- All tests passing, 75.4% coverage
- Starting Phase A correctness work

### 2025-01-15 - Phase A: Validation and Bounds (commit ac5639c2c)
- ✅ Committed entity count in header (offset 12-16)
- ✅ Committed ForwardColumnsFor() selective reader
- ✅ Implemented cross-page validation (validation.go)
- ✅ Added bounds checking: maxEntityCount=256M, allocation estimates
- ✅ Queries use validated header entity count, not column length
- ✅ Added 5 regression tests for malformed blocks
- All tests pass with -race, coverage 75.6%
- **Issue 1 (malformed panics) FIXED** ✅

**Remaining Phase A work:**
- Format revision policy (version bumping, required features)
- Entity deduplication (currently assigns separate IDs to duplicate content)
- Reject sample scope on series-only index
- Legacy differential test harness (Prometheus matcher equivalence)

**Ready to start Phase B** (selective queries) while continuing Phase A in parallel

### 2025-01-15 - Phase B: Selective Query Implementation (commit pending)
- ✅ Implemented candidateIDsSelective() that uses ForwardColumnsFor
- ✅ Updated Series(), Values(), Names() to use selective path
- ✅ Only fetches columns needed for matchers + projection
- ✅ Handles sparse column map (not all keys loaded)
- ✅ Added validation: ForwardColumnsFor checks entity count consistency
- ✅ Added 3 tests: TestSelectiveQueryFetchesFewerColumns, VsFullFetch, HandlesMissingKeys
- Verified: 10-key block with 2-key query fetches 4 pages, not 10+
- All tests pass with -race, coverage 73.9%
- **Issue 2 (queries not selective) PARTIALLY FIXED** 🝐

**Limitations:**
- Dictionaries and postings are still single pages (can't avoid loading all)
- Phase D will split these by key for full selectivity
- Presence queries could use postings-only path (no columns)

### 2025-01-15 - Phase C: Bounded Reader Execution (commit pending)
- ✅ Fixed FetchRanges error cleanup: release only current iteration on failure
- ✅ Added Reader.maxDecodedBytes (256MB default) and trackDecoded() method
- ✅ Implemented fetchPages() that uses FetchRanges with 64KB coalescing
- ✅ Converted Entities, Dictionaries, Postings to use fetchPages
- ✅ Converted ForwardColumns, ForwardColumnsFor to use fetchPages
- ✅ Added 9 comprehensive tests:
  * TestFetchRangesReleasesOnError
  * TestFetchRangesRespectsCancellation
  * TestFetchRangesValidatesAllRangesUpfront
  * TestReaderMemoryBudgetTracking
  * TestReaderFetchPagesCoalescesNearbyPages (7 pages → 2 GETs)
  * TestReaderConcurrentFetchesShareBudget
  * TestReaderCloseWhileFetchingCancelsWork
- Updated existing tests to handle coalescing (fewer GETs expected)
- All tests pass with -race, coverage 74.1%
- **Issue 3 (bounded fetches not connected) FIXED** ✅

**Achievements:**
- All page reads now use bounded FetchRanges pipeline
- Coalescing reduces GET count (e.g., 7 pages → 2 GETs with 64KB gap)
- Memory budget tracking for decoded data (separate from in-flight)
- Proper cleanup on cancellation and errors
- No goroutine leaks verified

**Ready for Phase D** (benchmarking and paging optimization)

