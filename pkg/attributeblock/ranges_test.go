package attributeblock

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanPageRanges(t *testing.T) {
	pages := []pageDescriptor{
		{offset: 400, length: 100},
		{offset: 100, length: 100},
		{offset: 220, length: 50},
		{offset: 1_000, length: 100},
	}
	ranges, err := PlanPageRanges(pages, RangePlanOptions{MaxGap: 25, MaxLength: 300})
	require.NoError(t, err)
	require.Equal(t, []Range{
		{Offset: 100, Length: 170, PageIndexes: []int{1, 2}},
		{Offset: 400, Length: 100, PageIndexes: []int{0}},
		{Offset: 1_000, Length: 100, PageIndexes: []int{3}},
	}, ranges)
}

func TestPlanPageRangesDoesNotExceedRequestBudget(t *testing.T) {
	ranges, err := PlanPageRanges([]pageDescriptor{
		{offset: 0, length: 100},
		{offset: 100, length: 100},
	}, RangePlanOptions{MaxGap: 0, MaxLength: 150})
	require.NoError(t, err)
	require.Len(t, ranges, 2)
}

func TestPlanPageRangesRejectsInvalidOptionsAndPages(t *testing.T) {
	_, err := PlanPageRanges(nil, RangePlanOptions{MaxLength: 0})
	require.ErrorContains(t, err, "positive")
	_, err = PlanPageRanges([]pageDescriptor{{offset: 0, length: 0}}, RangePlanOptions{MaxLength: 1})
	require.ErrorContains(t, err, "invalid page")
}
