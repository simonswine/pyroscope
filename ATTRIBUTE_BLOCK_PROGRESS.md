# Attribute Index Implementation Progress

## Summary

Continuing work on the attribute index implementation assessment. Two major phases completed:
**Phase A (Correctness)** and **Phase B (Selective Queries)**. Ready to start Phase C.

## Commits

### 1. Phase A: Correctness and Validation (ac5639c2c)
**Goal:** Fix malformed blocks that can panic, add bounds checking

**Changes:**
- Added entity count to block header (offset 12-16, uint32)
- Added ForwardColumnsFor() method for selective column reading
- Implemented comprehensive cross-page validation (validation.go):
  * Entity count consistency across all forward columns
  * Postings entity IDs within bounds
  * Forward column value IDs within dictionary bounds
  * Reject excessive entity counts (max 256M)
  * Allocation estimation and rejection (16GB budget)
- Queries use validated header entity count, not derived from columns
- 5 permanent regression tests for malformed blocks

**Results:**
- Issue #1 (malformed panics) **FIXED** ✅
- All tests pass with -race
- Coverage: 75.6%

**Remaining Phase A:**
- Format revision/feature policy
- Entity deduplication
- Legacy differential test harness

### 2. Phase B: Selective Queries (f03d7b9b5)
**Goal:** Only fetch columns needed for filtering and projection

**Changes:**
- Created candidateIDsSelective() that identifies needed keys
- Sparse column handling: map[keyIndex][]uint32 instead of [][]uint32
- Updated Series(), Values(), Names() to use selective path
- Enhanced ForwardColumnsFor() validation
- 3 tests with range-tracking source to verify selectivity

**Results:**
- Issue #2 (queries not selective) **PARTIALLY FIXED** 🟡
- 10-key block with 2-key query: 4 pages fetched, not 10+
- Column fetches are selective ✅
- Dictionary/postings fetches remain full (single-page format limitation)
- All tests pass with -race
- Coverage: 73.9%

**Limitations:**
- Dictionaries and postings are single pages (can't split without format change)
- Phase D will enable per-key splits
- Presence queries could skip columns entirely (future optimization)

## Current Status vs Assessment

| Issue | Status | Details |
|-------|--------|---------|
| 1. Malformed blocks panic | ✅ FIXED | Validation added, regression tests, bounds checking |
| 2. Queries not selective | 🟡 PARTIAL | Columns selective, dicts/postings still full (format limit) |
| 3. Bounded fetches not connected | ❌ TODO | Phase C: wire FetchRanges, fix worker cleanup |
| 4. Legacy equivalence unproven | ❌ TODO | Phase A continuing: differential test harness |
| 5. Model guarantees incomplete | ❌ TODO | Phase A continuing: entity dedup, scope validation |

## Next Steps

### Phase C: Bounded Reader Execution (NEXT)
1. Examine ranges.go and FetchRanges implementation
2. Wire reader queries through budgeted pipeline
3. Fix worker cleanup and request limits
4. Account for retained/decoded memory
5. Add resource limit tests (cancellation, peak memory, concurrent close)

### Phase A Continuing (In Parallel)
1. Define format revision/required feature policy
2. Add entity deduplication (reject duplicate content, not duplicate IDs)
3. Reject sample scope on series-only index
4. Create legacy Format1 parity harness (Prometheus matcher equivalence)

### Phase D: Benchmarking (After A/B/C)
1. Establish TSDB baseline
2. Split dictionaries/postings by key
3. Add entity-range column pages
4. Measure codecs/page sizes

### Phase E: Integration (Much Later)
- NOT starting until A/B/C/D complete
- Time index, publication, compaction
- Production writer integration

## Test Coverage

- **Total:** 73.9%
- **Validation:** 5 regression tests for malformed blocks
- **Selective queries:** 3 tests verifying fetch reduction
- **Race detector:** All tests pass with -race

## Files Added/Modified

### Phase A
- `pkg/attributeindex/validation.go` (new)
- `pkg/attributeindex/validation_test.go` (new)
- `pkg/attributeindex/reader.go` (modified)
- `pkg/attributeindex/writer.go` (modified)
- `pkg/attributeindex/query.go` (modified)

### Phase B
- `pkg/attributeindex/query_selective.go` (new)
- `pkg/attributeindex/query_selective_test.go` (new)
- `pkg/attributeindex/query.go` (modified)
- `pkg/attributeindex/reader.go` (modified)

## Key Achievements

1. **Correctness First:** Malformed blocks no longer panic, return bounded errors
2. **Selective Fetching:** Reduced cold-query GET count for projected queries
3. **Validated Execution:** Entity count from header, not derived
4. **Test Coverage:** Permanent regression suite for malformed data
5. **Documentation:** Detailed tracking in ATTRIBUTE_BLOCK_WORK.md

## Technical Debt

1. **Dictionaries/postings single-page:** Can't be selective until format v2
2. **Legacy equivalence:** No differential test harness yet
3. **Entity deduplication:** Currently assigns separate IDs to identical content
4. **Bounded execution:** FetchRanges helpers not connected to reader
5. **Format policy:** No explicit version bump or required-feature mechanism

## Performance Gains (Phase B)

**Before:** Query with projection = all columns loaded  
**After:** Query with projection = only needed columns loaded

**Example:** 10-key block, query 2 keys:
- Before: 12+ page fetches (header + footer + dir + dicts + postings + 10 columns)
- After: 4 page fetches during query (dicts + postings + 2 columns)
- Savings: ~67% fewer column page fetches

**Future (Phase D):** Split dicts/postings → further reduction possible

## Risks Mitigated

1. ✅ Out-of-bounds panics from malformed data
2. ✅ Excessive memory allocation from corrupt entity counts
3. ✅ Inconsistent column lengths causing nil dereferences
4. ✅ Unbounded allocation from invalid page lengths
5. 🟡 Cold-query performance (partially: columns selective, dicts/postings pending)

## Risks Remaining

1. ❌ Unbounded worker execution (Phase C)
2. ❌ Legacy matcher mismatches (Phase A)
3. ❌ Duplicate entity ID assignment (Phase A)
4. ❌ No format revision policy (Phase A)
5. ❌ Sample scope on series-only index accepted (Phase A)
