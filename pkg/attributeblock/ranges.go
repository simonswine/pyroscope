package attributeblock

import (
	"cmp"
	"fmt"
	"slices"
)

// Range is one contiguous object-store request. Page indexes identify the
// descriptors covered by the range; callers slice each page from the owned
// response buffer before decoding it.
type Range struct {
	Offset      int64
	Length      int64
	PageIndexes []int
}

// RangePlanOptions controls request coalescing. A zero MaxGap only coalesces
// adjacent pages. MaxLength is required so a broad query cannot turn into an
// unbounded whole-object prefetch.
type RangePlanOptions struct {
	MaxGap    int64
	MaxLength int64
}

func (o RangePlanOptions) valid() error {
	if o.MaxGap < 0 {
		return fmt.Errorf("maximum range gap must not be negative")
	}
	if o.MaxLength <= 0 {
		return fmt.Errorf("maximum range length must be positive")
	}
	return nil
}

// PlanPageRanges sorts page descriptors by object offset and coalesces nearby
// pages. It only plans I/O; callers remain responsible for checksum validation
// of every individual page.
func PlanPageRanges(pages []pageDescriptor, options RangePlanOptions) ([]Range, error) {
	if err := options.valid(); err != nil {
		return nil, err
	}
	type indexedPage struct {
		index int
		page  pageDescriptor
	}
	indexed := make([]indexedPage, len(pages))
	for i, page := range pages {
		if page.offset < 0 || page.length == 0 {
			return nil, fmt.Errorf("invalid page %d range", i)
		}
		indexed[i] = indexedPage{index: i, page: page}
	}
	slices.SortFunc(indexed, func(a, b indexedPage) int { return cmp.Compare(a.page.offset, b.page.offset) })

	result := make([]Range, 0, len(indexed))
	for _, current := range indexed {
		pageEnd := current.page.offset + int64(current.page.length)
		if pageEnd < current.page.offset {
			return nil, fmt.Errorf("page %d range overflows", current.index)
		}
		if len(result) == 0 {
			result = append(result, Range{Offset: current.page.offset, Length: int64(current.page.length), PageIndexes: []int{current.index}})
			continue
		}
		last := &result[len(result)-1]
		lastEnd := last.Offset + last.Length
		gap := current.page.offset - lastEnd
		combinedLength := pageEnd - last.Offset
		if gap >= 0 && gap <= options.MaxGap && combinedLength <= options.MaxLength {
			last.Length = combinedLength
			last.PageIndexes = append(last.PageIndexes, current.index)
			continue
		}
		result = append(result, Range{Offset: current.page.offset, Length: int64(current.page.length), PageIndexes: []int{current.index}})
	}
	return result, nil
}
