package query

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/grafana/dskit/multierror"
	"github.com/grafana/dskit/tracing"
	"github.com/parquet-go/parquet-go"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/grafana/pyroscope/v2/pkg/iter"
)

type RepeatedRow[T any] struct {
	Row    T
	Values [][]parquet.Value
}

type RepeatedColumnMorsel struct {
	Values  []parquet.Value
	Offsets []int
}

func (m RepeatedColumnMorsel) Row(i int) []parquet.Value {
	return m.Values[m.Offsets[i]:m.Offsets[i+1]]
}

type RepeatedRowMorsel[T any] struct {
	Rows    []T
	Columns []RepeatedColumnMorsel
}

type repeatedRowIterator[T any] struct {
	columns iter.Iterator[[][]parquet.Value]
	rows    iter.Iterator[T]
}

const (
	// The value specifies how many individual values to be
	// read (decoded) from the page.
	//
	// Too big read size does not make much sense: despite
	// the fact that this does not impact read amplification
	// as the page is already fully read, decoding of the
	// values is not free.
	//
	// How many values we expect per a row, the upper boundary?
	repeatedRowColumnIteratorReadSize = 2 << 10

	// Batch size specifies how many rows to read from a column at once.
	// Note that the batched rows are buffered in-memory but do not reference
	// the pages from which they were read.
	//
	// The default value is extremely conservative, as in most cases, rows are
	// quite large (e.g., profile samples). Given that we run many queries
	// concurrently, the memory waste outweighs the benefits of the "read-ahead"
	// optimization that batching is intended to provide.
	//
	// However, in cases where the rows are small (such as in time series),
	// the value should be increased significantly.
	defaultBatchSize = 4
)

func NewRepeatedRowIterator[T any](
	ctx context.Context,
	rows iter.Iterator[T],
	rowGroups []parquet.RowGroup,
	columns ...int,
) iter.Iterator[RepeatedRow[T]] {
	return NewRepeatedRowIteratorBatchSize(ctx, rows, rowGroups, defaultBatchSize, columns...)
}

func NewRepeatedRowIteratorBatchSize[T any](
	ctx context.Context,
	rows iter.Iterator[T],
	rowGroups []parquet.RowGroup,
	batchSize int64,
	columns ...int,
) iter.Iterator[RepeatedRow[T]] {
	rows, rowNumbers := iter.Tee(rows)
	return &repeatedRowIterator[T]{
		rows: rows,
		columns: NewMultiColumnIterator(ctx,
			WrapWithRowNumber(rowNumbers),
			int(batchSize),
			rowGroups,
			columns...,
		),
	}
}

func (x *repeatedRowIterator[T]) Next() bool {
	if !x.rows.Next() {
		return false
	}
	return x.columns.Next()
}

func (x *repeatedRowIterator[T]) At() RepeatedRow[T] {
	return RepeatedRow[T]{
		Values: x.columns.At(),
		Row:    x.rows.At(),
	}
}

func (x *repeatedRowIterator[T]) Err() error {
	return x.columns.Err()
}

func (x *repeatedRowIterator[T]) Close() error {
	return x.columns.Close()
}

type repeatedRowMorselIterator[T any] struct {
	ctx context.Context

	rows    iter.Iterator[T]
	rgs     []parquet.RowGroup
	columns []int
	maxRows int

	rgIndex int
	rgMin   int64
	rgMax   int64

	pending    T
	pendingRow int64
	hasPending bool

	rowNumbers []int64
	morsel     RepeatedRowMorsel[T]
	err        error
}

func NewRepeatedRowMorselIteratorBatchSize[T any](
	ctx context.Context,
	rows iter.Iterator[T],
	rowGroups []parquet.RowGroup,
	batchSize int64,
	columns ...int,
) iter.Iterator[RepeatedRowMorsel[T]] {
	if len(rowGroups) == 0 {
		return iter.NewEmptyIterator[RepeatedRowMorsel[T]]()
	}
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	return &repeatedRowMorselIterator[T]{
		ctx:     ctx,
		rows:    rows,
		rgs:     rowGroups,
		columns: columns,
		maxRows: int(batchSize),
	}
}

func (x *repeatedRowMorselIterator[T]) Next() bool {
	if x.err != nil {
		return false
	}
	if err := x.ctx.Err(); err != nil {
		x.err = err
		return false
	}

	row, rn, ok := x.nextRow()
	if !ok {
		return false
	}
	rgIndex, rgMin, rgMax, ok := x.seekRowGroup(rn)
	if !ok {
		return false
	}

	x.morsel.Rows = x.morsel.Rows[:0]
	x.rowNumbers = x.rowNumbers[:0]
	x.appendRow(row, rn-rgMin)

	for len(x.morsel.Rows) < x.maxRows {
		row, rn, ok = x.nextRow()
		if !ok {
			break
		}
		if rn >= rgMax {
			x.pending = row
			x.pendingRow = rn
			x.hasPending = true
			break
		}
		if rn < rgMin {
			x.err = ErrSeekOutOfRange
			return false
		}
		x.appendRow(row, rn-rgMin)
	}

	if err := x.readColumns(x.rgs[rgIndex]); err != nil {
		x.err = err
		return false
	}
	return true
}

func (x *repeatedRowMorselIterator[T]) appendRow(row T, localRowNumber int64) {
	x.morsel.Rows = append(x.morsel.Rows, row)
	x.rowNumbers = append(x.rowNumbers, localRowNumber)
}

func (x *repeatedRowMorselIterator[T]) nextRow() (row T, rn int64, ok bool) {
	if x.hasPending {
		x.hasPending = false
		return x.pending, x.pendingRow, true
	}
	if !x.rows.Next() {
		x.err = x.rows.Err()
		return row, 0, false
	}
	row = x.rows.At()
	return row, rowNumberOf(row), true
}

func (x *repeatedRowMorselIterator[T]) seekRowGroup(rn int64) (index int, min, max int64, ok bool) {
	if rn < x.rgMin {
		x.err = ErrSeekOutOfRange
		return 0, 0, 0, false
	}
	for x.rgIndex < len(x.rgs) && rn >= x.rgMax {
		x.rgMin = x.rgMax
		x.rgMax += x.rgs[x.rgIndex].NumRows()
		x.rgIndex++
	}
	if rn >= x.rgMax {
		x.err = ErrSeekOutOfRange
		return 0, 0, 0, false
	}
	return x.rgIndex - 1, x.rgMin, x.rgMax, true
}

func (x *repeatedRowMorselIterator[T]) readColumns(rg parquet.RowGroup) error {
	if cap(x.morsel.Columns) < len(x.columns) {
		x.morsel.Columns = make([]RepeatedColumnMorsel, len(x.columns))
	}
	x.morsel.Columns = x.morsel.Columns[:len(x.columns)]

	for i, column := range x.columns {
		c := &x.morsel.Columns[i]
		c.Values = c.Values[:0]
		if cap(c.Offsets) < len(x.morsel.Rows)+1 {
			c.Offsets = make([]int, len(x.morsel.Rows)+1)
		}
		c.Offsets = c.Offsets[:len(x.morsel.Rows)+1]
		for j := range c.Offsets {
			c.Offsets[j] = 0
		}

		rows := iter.NewSliceIterator(x.rowNumbers)
		values := NewRepeatedRowColumnIterator(x.ctx, rows, []parquet.RowGroup{rg}, column)
		for row := range x.morsel.Rows {
			if !values.Next() {
				err := values.Err()
				if err == nil {
					err = ErrSeekOutOfRange
				}
				_ = values.Close()
				return err
			}
			for _, v := range values.At() {
				c.Values = append(c.Values, v.Clone())
			}
			c.Offsets[row+1] = len(c.Values)
		}
		if err := values.Err(); err != nil {
			_ = values.Close()
			return err
		}
		if err := values.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (x *repeatedRowMorselIterator[T]) At() RepeatedRowMorsel[T] { return x.morsel }
func (x *repeatedRowMorselIterator[T]) Err() error               { return x.err }
func (x *repeatedRowMorselIterator[T]) Close() error             { return nil }

type rowNumberIterator[T any] struct{ it iter.Iterator[T] }

func WrapWithRowNumber[T any](it iter.Iterator[T]) iter.Iterator[int64] {
	return &rowNumberIterator[T]{it}
}

func (x *rowNumberIterator[T]) Next() bool   { return x.it.Next() }
func (x *rowNumberIterator[T]) Err() error   { return x.it.Err() }
func (x *rowNumberIterator[T]) Close() error { return x.it.Close() }

func (x *rowNumberIterator[T]) At() int64 {
	return rowNumberOf(x.it.At())
}

func rowNumberOf[T any](row T) int64 {
	v := any(row)
	switch r := v.(type) {
	case RowGetter:
		return r.RowNumber()
	case int64:
		return r
	case uint32:
		return int64(r)
	default:
		panic(fmt.Sprintf("unknown row type: %T", v))
	}
}

type multiColumnIterator struct {
	r []iter.Iterator[int64]
	c []iter.Iterator[[]parquet.Value]
	v [][]parquet.Value
}

func NewMultiColumnIterator(
	ctx context.Context,
	rows iter.Iterator[int64],
	batchSize int,
	rowGroups []parquet.RowGroup,
	columns ...int,
) iter.Iterator[[][]parquet.Value] {
	m := multiColumnIterator{
		c: make([]iter.Iterator[[]parquet.Value], len(columns)),
		v: make([][]parquet.Value, len(columns)),
		// Even if there is just one column, we do need to tee it,
		// as the source rows iterator is owned by caller, and we
		// must never close it on our own.
		r: iter.TeeN(rows, len(columns)),
	}
	for i, column := range columns {
		m.c[i] = iter.NewAsyncBatchIterator[[]parquet.Value](
			NewRepeatedRowColumnIterator(ctx, m.r[i], rowGroups, column),
			batchSize,
			CloneParquetValues,
			ReleaseParquetValues,
		)
	}
	return &m
}

func (m *multiColumnIterator) Next() bool {
	for i, x := range m.c {
		if !x.Next() {
			return false
		}
		m.v[i] = x.At()
	}
	return true
}

func (m *multiColumnIterator) At() [][]parquet.Value { return m.v }

func (m *multiColumnIterator) Err() error {
	var err multierror.MultiError
	for i := range m.c {
		err.Add(m.c[i].Err())
		err.Add(m.r[i].Err())
	}
	return err.Err()
}

func (m *multiColumnIterator) Close() error {
	var err multierror.MultiError
	for i := range m.c {
		err.Add(m.c[i].Close())
		err.Add(m.r[i].Close())
	}
	return err.Err()
}

var ErrSeekOutOfRange = fmt.Errorf("bug: south row is out of range")

type repeatedRowColumnIterator struct {
	ctx  context.Context
	span oteltrace.Span

	rows     iter.Iterator[int64]
	rgs      []parquet.RowGroup
	column   int
	readSize int

	pages parquet.Pages
	page  parquet.Page

	minRGRowNum   int64
	maxRGRowNum   int64
	maxPageRowNum int64

	rowsRead    int64
	rowsFetched int64
	pageBytes   int64

	pageReads prometheus.Counter

	vit  *repeatedValuePageIterator
	prev int64
	err  error
}

func NewRepeatedRowColumnIterator(ctx context.Context, rows iter.Iterator[int64], rgs []parquet.RowGroup, column int) iter.Iterator[[]parquet.Value] {
	r := repeatedRowColumnIterator{
		rows:     rows,
		rgs:      rgs,
		column:   column,
		vit:      getRepeatedValuePageIteratorFromPool(),
		readSize: repeatedRowColumnIteratorReadSize,
	}
	if len(rgs) == 0 {
		return iter.NewEmptyIterator[[]parquet.Value]()
	}
	s := rgs[0].Schema()
	if len(s.Columns()) <= column {
		return iter.NewErrIterator[[]parquet.Value](fmt.Errorf("column %d not found", column))
	}
	tableName := strings.ToLower(s.Name()) + "s"
	columnName := strings.Join(s.Columns()[column], ".")
	r.initMetrics(getMetricsFromContext(ctx), tableName, columnName)
	_, r.ctx = tracing.StartSpanFromContext(ctx, "RepeatedRowColumnIterator")
	r.span = oteltrace.SpanFromContext(r.ctx)
	r.span.SetAttributes(
		attribute.String("table", tableName),
		attribute.String("column", columnName),
	)
	return &r
}

func (x *repeatedRowColumnIterator) initMetrics(metrics *Metrics, table, column string) {
	x.pageReads = metrics.pageReadsTotal.WithLabelValues(table, column)
}

func (x *repeatedRowColumnIterator) Next() bool {
	if !x.rows.Next() || x.err != nil {
		return false
	}
	rn := x.rows.At()
	if rn >= x.maxRGRowNum {
		if !x.seekRowGroup(rn) {
			return false
		}
	}
	rn -= x.minRGRowNum
	if x.page == nil || rn >= x.maxPageRowNum {
		if !x.readPage(rn) {
			return false
		}
		// readPage ensures that the first row in the
		// page matches rn, therefore we don't need to
		// skip anything.
		x.prev = rn - 1
	}
	// Skip rows to the rn.
	next := int(rn - x.prev)
	x.prev = rn
	for i := 0; i < next; i++ {
		if !x.vit.Next() {
			x.err = ErrSeekOutOfRange
			return false
		}
	}
	x.rowsRead++
	return true
}

func (x *repeatedRowColumnIterator) seekRowGroup(rn int64) bool {
	for i, rg := range x.rgs {
		x.minRGRowNum = x.maxRGRowNum
		x.maxRGRowNum += rg.NumRows()
		if rn >= x.maxRGRowNum {
			continue
		}
		x.rgs = x.rgs[i+1:]
		return x.openChunk(rg)
	}
	return false
}

func (x *repeatedRowColumnIterator) openChunk(rg parquet.RowGroup) bool {
	x.page = nil
	x.vit.reset(nil, 0)
	if x.pages != nil {
		if x.err = x.pages.Close(); x.err != nil {
			return false
		}
	}
	x.pages = rg.ColumnChunks()[x.column].Pages()
	return true
}

func (x *repeatedRowColumnIterator) readPage(rn int64) bool {
	if x.err = x.ctx.Err(); x.err != nil {
		return false
	}
	if x.err = x.pages.SeekToRow(rn); x.err != nil {
		return false
	}
	readPageStart := time.Now()
	if x.page, x.err = x.pages.ReadPage(); x.err != nil {
		if x.err != io.EOF {
			x.span.RecordError(x.err)
			x.span.SetStatus(codes.Error, x.err.Error())
			return false
		}
		x.err = nil
		// ReadPage should never return page along with EOF,
		// however this is not a strict contract.
		if x.page == nil {
			return false
		}
	}
	x.pageReads.Add(1)
	pageReadDurationMs := time.Since(readPageStart).Milliseconds()
	// NumRows return the number of row in the page
	// not counting skipped ones (because of SeekToRow).
	// The implementation is quite expensive, therefore
	// we should call it once per page.
	pageNumRows := x.page.NumRows()
	x.pageBytes += x.page.Size()
	x.maxPageRowNum = rn + pageNumRows
	x.rowsFetched += pageNumRows
	x.vit.reset(x.page, x.readSize)
	if x.span.IsRecording() {
		x.span.AddEvent("Page read", oteltrace.WithAttributes(
			attribute.Int64("min_rg_row", x.minRGRowNum),
			attribute.Int64("max_rg_row", x.maxRGRowNum),
			attribute.Int64("seek_row", x.minRGRowNum+rn),
			attribute.Int64("page_read_ms", pageReadDurationMs),
			attribute.Int64("page_num_rows", pageNumRows),
		))
	}
	return true
}

func (x *repeatedRowColumnIterator) At() []parquet.Value { return x.vit.At() }
func (x *repeatedRowColumnIterator) Err() error          { return x.err }
func (x *repeatedRowColumnIterator) Close() (err error) {
	putRepeatedValuePageIteratorToPool(x.vit)
	if x.pages != nil {
		err = x.pages.Close()
	}
	x.span.SetAttributes(
		attribute.Int64("page_bytes", x.pageBytes),
		attribute.Int64("rows_fetched", x.rowsFetched),
		attribute.Int64("rows_read", x.rowsRead),
	)
	x.span.End()
	return err
}

var repeatedValuePageIteratorPool = sync.Pool{New: func() any { return new(repeatedValuePageIterator) }}

func getRepeatedValuePageIteratorFromPool() *repeatedValuePageIterator {
	return repeatedValuePageIteratorPool.Get().(*repeatedValuePageIterator)
}

func putRepeatedValuePageIteratorToPool(x *repeatedValuePageIterator) {
	x.reset(nil, 0)
	repeatedValuePageIteratorPool.Put(x)
}

// RepeatedValuePageIterator iterates over repeated fields.
// FIXME(kolesnikovae): Definition level is ignored.
type repeatedValuePageIterator struct {
	page parquet.ValueReader
	buf  []parquet.Value
	off  int
	row  []parquet.Value
	err  error
}

func (x *repeatedValuePageIterator) At() []parquet.Value { return x.row }
func (x *repeatedValuePageIterator) Err() error          { return x.err }
func (x *repeatedValuePageIterator) Close() error        { return nil }

func (x *repeatedValuePageIterator) reset(page parquet.Page, readSize int) {
	if cap(x.buf) < readSize {
		x.buf = make([]parquet.Value, 0, readSize)
	}
	x.page = nil
	if page != nil {
		x.page = page.Values()
	}
	x.buf = x.buf[:0]
	x.row = x.row[:0]
	x.err = nil
	x.off = 0
}

func (x *repeatedValuePageIterator) Next() bool {
	if x.err != nil {
		return false
	}
	x.row = x.row[:0]
	var err error
	var n int
loop:
	for {
		buf := x.buf[x.off:]
		for _, v := range buf {
			if v.RepetitionLevel() == 0 && len(x.row) > 0 {
				// Found a new row.
				break loop
			}
			x.row = append(x.row, v)
			x.off++
		}
		// Refill the buffer.
		x.buf = x.buf[:cap(x.buf)]
		x.off = 0
		n, err = x.page.ReadValues(x.buf)
		x.buf = x.buf[:n]
		if err != nil && err != io.EOF {
			x.err = err
		}
		if n == 0 {
			break
		}
	}
	return len(x.row) > 0
}
