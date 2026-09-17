# Phase C: Bounded Reader Execution - Detailed Plan

## Current State (Assessment Finding #3)

**Problem:** "Bounded fetches are not connected or fully bounded"

### Issues:
1. **Reader queries bypass helpers**
   - `reader.readPage()` calls `readRange()` directly
   - No use of `FetchRanges` or `PlanPageRanges`
   - Each page read is independent, no coalescing

2. **FetchRanges limitations**
   - Limits active requests ✓
   - Accounts for in-flight compressed bytes ✓
   - But: completed buffers retained outside byte accounting
   - After `FetchRanges` returns, semaphore is released but buffers remain
   - Decoded/cached bytes have no budget

3. **Error path issues**
   - Current implementation: errgroup cleanup looks correct (defer in each goroutine)
   - Assessment claims "some error paths return while requests remain active"
   - Need to verify cancellation propagates correctly

4. **Individual page limits**
   - Pages can exceed MaxLength in current validation
   - Need to check page.length <= options.MaxBytesInFlight before accepting

## Required Changes

### 1. Wire Reader to Use FetchRanges

**Current:**
```go
func (r *Reader) readPage(ctx context.Context, page pageDescriptor, name string) ([]byte, error) {
    data, err := readRange(ctx, r.source, r.object, page.offset, int64(page.length))
    // ...
}
```

**After Phase C:**
```go
// Reader should plan all page fetches upfront, then execute via FetchRanges
func (r *Reader) fetchPages(ctx context.Context, pageIndexes []int) ([][]byte, error) {
    pages := make([]pageDescriptor, len(pageIndexes))
    for i, idx := range pageIndexes {
        pages[i] = r.pages[idx]
    }
    
    planOptions := RangePlanOptions{
        MaxGap:    64 << 10, // Coalesce pages within 64KB
        MaxLength: 16 << 20, // Max 16MB per coalesced range
    }
    ranges, err := PlanPageRanges(pages, planOptions)
    // ...
    
    fetchOptions := FetchOptions{
        MaxConcurrent:    10,
        MaxBytesInFlight: 64 << 20, // 64MB in-flight budget
    }
    buffers, err := FetchRanges(ctx, r.source, r.object, ranges, fetchOptions)
    // Slice and validate each page from coalesced buffers
    // ...
}
```

### 2. Add Decoded/Cached Memory Budget

**Problem:** FetchRanges only accounts for in-flight bytes. Once returned, buffers + decoded data are unbounded.

**Solution:** Add reader-level memory tracking:

```go
type Reader struct {
    // ... existing fields ...
    
    // Memory accounting
    decodedBytes int64
    maxDecodedBytes int64
    decodedMu sync.Mutex
}

func (r *Reader) trackDecoded(bytes int64) error {
    r.decodedMu.Lock()
    defer r.decodedMu.Unlock()
    if r.decodedBytes + bytes > r.maxDecodedBytes {
        return fmt.Errorf("decoded memory %d + %d exceeds budget %d", 
            r.decodedBytes, bytes, r.maxDecodedBytes)
    }
    r.decodedBytes += bytes
    return nil
}
```

### 3. Ensure Proper Worker Cleanup

**Verify:**
- ✓ errgroup.Wait() blocks until all goroutines complete
- ✓ Context cancellation propagates to all workers
- ✓ Semaphore.Acquire respects context
- ✓ defer cleanup in each goroutine releases semaphore and channel

**Add tests for:**
- Context cancellation mid-fetch
- Error in one fetch cancels others
- No goroutine leaks on error
- Semaphore properly released on all paths

### 4. Enforce Individual Range Limits

**Add to FetchRanges validation:**
```go
func FetchRanges(...) ([][]byte, error) {
    // ... existing validation ...
    
    // NEW: reject oversized ranges before starting work
    for i, r := range ranges {
        if r.Length > options.MaxBytesInFlight {
            return nil, fmt.Errorf("range %d length %d exceeds budget %d",
                i, r.Length, options.MaxBytesInFlight)
        }
    }
    
    // ... rest of implementation ...
}
```

### 5. Route All Reader Fetches Through Bounded Pipeline

**Changes needed:**

#### a. ForwardColumns → use fetchPages
```go
func (r *Reader) ForwardColumns(ctx context.Context) ([][]uint32, error) {
    // Identify all forward column page indexes
    pageIndexes := []int{}
    for i, page := range r.pages {
        if page.kind == pageForwardColumn {
            pageIndexes = append(pageIndexes, i)
        }
    }
    
    // Fetch via bounded pipeline
    buffers, err := r.fetchPages(ctx, pageIndexes)
    // ... decode and validate ...
}
```

#### b. ForwardColumnsFor → use fetchPages
Already selective, just needs to use bounded fetch

#### c. Dictionaries, Postings, Entities → use fetchPages
Single page each, but should still go through pipeline for consistency and accounting

### 6. Add Resource Limit Tests

**Test scenarios:**

```go
// TestReaderRespectsMemoryBudget: ensure decoded+cached bytes stay within limit
// TestReaderCancelsDuringFetch: context cancel mid-fetch cleans up properly
// TestReaderHandlesErrorInFetch: error in one goroutine cancels others
// TestReaderNoGoroutineLeaks: no workers survive reader.Close()
// TestReaderConcurrentClose: close during active fetch
// TestReaderPeakMemory: measure actual peak memory usage
// TestFetchRangesReleasesOnError: semaphore released on error paths
// TestFetchRangesRespectsContext: cancellation stops new fetches
```

## Memory Budget Hierarchy

1. **In-flight bytes** (FetchRanges.MaxBytesInFlight)
   - Compressed response buffers actively being fetched
   - Released when fetch completes
   - Prevents too many concurrent large requests

2. **Decoded bytes** (Reader.maxDecodedBytes)
   - Decompressed page data after fetch
   - Before decoding into typed structures
   - Released when page decoded into cache

3. **Cached bytes** (Reader cache)
   - Fully decoded dictionaries, postings, columns
   - Retained until reader.Close()
   - Should have separate budget

4. **Result bytes** (query results)
   - Materialized entities returned to caller
   - Outside reader's control
   - Caller's responsibility

**Example budgets:**
- MaxBytesInFlight: 64 MB (compressed, transient)
- maxDecodedBytes: 128 MB (decompressed, transient)
- maxCachedBytes: 256 MB (decoded structures, persisted)

## Implementation Order

1. ✅ **Prep:** Entity count validation (done in Phase A)
2. ✅ **Prep:** ForwardColumnsFor selective reader (done in Phase B)
3. **C.1:** Add decoded/cached memory tracking to Reader
4. **C.2:** Implement Reader.fetchPages() using FetchRanges
5. **C.3:** Convert ForwardColumns to use fetchPages
6. **C.4:** Convert ForwardColumnsFor to use fetchPages
7. **C.5:** Convert Dictionaries, Postings, Entities to use fetchPages
8. **C.6:** Add resource limit enforcement
9. **C.7:** Add comprehensive tests (cancellation, cleanup, limits)
10. **C.8:** Measure and document peak memory behavior

## Success Criteria (Phase C Exit)

- [ ] All reader page fetches use FetchRanges
- [ ] In-flight, decoded, and cached bytes tracked separately
- [ ] Memory budgets enforced before allocation
- [ ] Context cancellation stops all in-flight work
- [ ] No goroutine leaks on error or close
- [ ] Tests verify bounded memory under concurrent queries
- [ ] Tests verify proper cleanup on all error paths
- [ ] Documentation explains memory budget hierarchy

## Current FetchRanges Issues to Fix

### Issue: Byte accounting scope
**Problem:** Semaphore released when fetch completes, but caller retains buffers  
**Fix:** Document that this is correct - FetchRanges bounds in-flight only. Reader must track decoded separately.

### Issue: Individual range validation
**Problem:** Range.Length checked after semaphore.Acquire  
**Fix:** Validate all ranges upfront before starting any work

### Issue: Error during acquire
**Problem:** If Acquire fails mid-loop, need to release already-acquired permits  
**Fix:** Track acquired permits, release on error before Acquire completes loop

### Better implementation:
```go
func FetchRanges(...) ([][]byte, error) {
    // Validate ALL ranges before acquiring anything
    for i, r := range ranges {
        if r.Length <= 0 || r.Length > options.MaxBytesInFlight {
            return nil, fmt.Errorf("invalid range %d length %d", i, r.Length)
        }
    }
    
    // Now acquire and launch
    buffers := make([][]byte, len(ranges))
    requests := make(chan struct{}, options.MaxConcurrent)
    bytes := semaphore.NewWeighted(options.MaxBytesInFlight)
    group, ctx := errgroup.WithContext(ctx)
    
    // Track what we've acquired in case we need to abort
    acquired := []int64{}
    for i, planned := range ranges {
        if err := bytes.Acquire(ctx, planned.Length); err != nil {
            // Release everything acquired so far
            for _, prev := range acquired {
                bytes.Release(prev)
            }
            return nil, err
        }
        acquired = append(acquired, planned.Length)
        
        select {
        case requests <- struct{}{}:
        case <-ctx.Done():
            // Release all acquired
            for _, prev := range acquired {
                bytes.Release(prev)
            }
            return nil, ctx.Err()
        }
        
        // Launch goroutine with proper cleanup
        // ...
    }
    // ...
}
```

## References

- Assessment finding #3: "Bounded fetches not connected"
- `pkg/attributeindex/ranges.go` - FetchRanges implementation
- `pkg/attributeindex/ranges_test.go` - existing tests (basic)
- `pkg/attributeindex/reader.go` - current direct readRange usage

## Estimated Effort

- Implementation: 2-3 days
- Testing: 1-2 days
- Review and iteration: 1 day
- **Total: 4-6 days**

After Phase C, queries will have:
- Bounded concurrent requests ✓
- Bounded in-flight bytes ✓
- Bounded decoded bytes ✓
- Bounded cached bytes ✓
- Proper cancellation ✓
- No goroutine leaks ✓
