package query

import (
	"context"
	"testing"

	"github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scalarMorselTestRow struct {
	A int64
	B int64
	C int64
}

func TestScalarColumnMorselIterator(t *testing.T) {
	groups := scalarMorselRowGroups(t,
		[]scalarMorselTestRow{
			{A: 1, B: 10, C: 100},
			{A: 2, B: 20, C: 200},
			{A: 3, B: 30, C: 300},
		},
		[]scalarMorselTestRow{
			{A: 4, B: 40, C: 400},
		},
	)

	it := NewScalarColumnMorselIterator(context.Background(), groups, 10,
		ColumnQuery{Name: "A", Index: 0, Predicate: NewIntBetweenPredicate(2, 4), Select: true},
		ColumnQuery{Name: "B", Index: 1, Predicate: NewIntBetweenPredicate(20, 30), Select: true},
		ColumnQuery{Name: "C", Index: 2, Select: true},
	)
	defer func() { require.NoError(t, it.Close()) }()

	require.True(t, it.Next())
	m := it.At()
	assert.Equal(t, 0, m.RowGroupIndex)
	assert.Equal(t, int64(0), m.RowGroupStart)
	assert.Equal(t, []int64{1, 2}, m.RowNumbers)
	assert.Equal(t, []int64{2, 3}, scalarColumnValues(m.Columns[0]))
	assert.Equal(t, []int64{20, 30}, scalarColumnValues(m.Columns[1]))
	assert.Equal(t, []int64{200, 300}, scalarColumnValues(m.Columns[2]))

	assert.False(t, it.Next())
	assert.NoError(t, it.Err())
}

func TestScalarColumnMorselIterator_RowGroupAndBatchBoundaries(t *testing.T) {
	groups := scalarMorselRowGroups(t,
		[]scalarMorselTestRow{
			{A: 1, B: 10, C: 100},
			{A: 2, B: 20, C: 200},
			{A: 3, B: 30, C: 300},
		},
		[]scalarMorselTestRow{
			{A: 4, B: 40, C: 400},
			{A: 5, B: 50, C: 500},
		},
	)

	it := NewScalarColumnMorselIterator(context.Background(), groups, 2,
		ColumnQuery{Name: "A", Index: 0, Predicate: NewIntBetweenPredicate(1, 5), Select: true},
	)
	defer func() { require.NoError(t, it.Close()) }()

	var got [][]int64
	var rowGroups []int
	for it.Next() {
		m := it.At()
		rowGroups = append(rowGroups, m.RowGroupIndex)
		got = append(got, append([]int64(nil), m.RowNumbers...))
	}
	require.NoError(t, it.Err())
	assert.Equal(t, []int{0, 0, 1}, rowGroups)
	assert.Equal(t, [][]int64{{0, 1}, {2}, {0, 1}}, got)
}

func TestScalarColumnMorselIterator_Cancellation(t *testing.T) {
	groups := scalarMorselRowGroups(t, []scalarMorselTestRow{{A: 1, B: 10, C: 100}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	it := NewScalarColumnMorselIterator(ctx, groups, 10,
		ColumnQuery{Name: "A", Index: 0, Predicate: NewIntBetweenPredicate(1, 1), Select: true},
	)
	assert.False(t, it.Next())
	assert.ErrorIs(t, it.Err(), context.Canceled)
	assert.NoError(t, it.Close())
}

func scalarMorselRowGroups(t *testing.T, rows ...[]scalarMorselTestRow) []parquet.RowGroup {
	t.Helper()
	groups := make([]parquet.RowGroup, 0, len(rows))
	for _, rg := range rows {
		buffer := parquet.NewBuffer()
		for _, row := range rg {
			require.NoError(t, buffer.Write(row))
		}
		groups = append(groups, buffer)
	}
	return groups
}

func scalarColumnValues(c RepeatedColumnMorsel) []int64 {
	values := make([]int64, len(c.Offsets)-1)
	for i := range values {
		values[i] = c.Row(i)[0].Int64()
	}
	return values
}
