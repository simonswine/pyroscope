package query

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/grafana/dskit/multierror"
	"github.com/grafana/dskit/tracing"
	"github.com/parquet-go/parquet-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/grafana/pyroscope/v2/pkg/iter"
)

type ColumnQuery struct {
	Name      string
	Index     int
	Predicate Predicate
	Select    bool
}

type ColumnMorsel struct {
	RowGroupIndex int
	RowGroupStart int64
	RowNumbers    []int64
	Columns       []RepeatedColumnMorsel
}

type scalarColumnMorselIterator struct {
	ctx  context.Context
	span oteltrace.Span

	rgs       []parquet.RowGroup
	columns   []ColumnQuery
	batchSize int

	driver  int
	selects []int

	rgIndex int
	rgStart int64
	scanner *scalarDriverScanner

	morsel ColumnMorsel
	err    error
}

func NewScalarColumnMorselIterator(
	ctx context.Context,
	rowGroups []parquet.RowGroup,
	batchSize int,
	columns ...ColumnQuery,
) iter.Iterator[ColumnMorsel] {
	if len(rowGroups) == 0 || len(columns) == 0 {
		return iter.NewEmptyIterator[ColumnMorsel]()
	}
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	_, ctx = tracing.StartSpanFromContext(ctx, "ScalarColumnMorselIterator")
	span := oteltrace.SpanFromContext(ctx)
	driver := 0
	for i, c := range columns {
		if c.Predicate != nil {
			driver = i
			break
		}
	}
	selects := make([]int, 0, len(columns))
	for i, c := range columns {
		if c.Select {
			selects = append(selects, i)
		}
	}
	if len(selects) == 0 {
		selects = append(selects, driver)
	}
	span.SetAttributes(
		attribute.Int("driver_column_index", columns[driver].Index),
		attribute.String("driver_column", columns[driver].Name),
		attribute.Int("columns", len(columns)),
		attribute.Int("selected_columns", len(selects)),
	)
	return &scalarColumnMorselIterator{
		ctx:       ctx,
		span:      span,
		rgs:       rowGroups,
		columns:   columns,
		batchSize: batchSize,
		driver:    driver,
		selects:   selects,
	}
}

func (i *scalarColumnMorselIterator) Next() bool {
	if i.err != nil {
		return false
	}
	for {
		if err := i.ctx.Err(); err != nil {
			i.err = err
			return false
		}
		if i.scanner == nil && !i.openNextRowGroup() {
			return false
		}

		i.morsel.RowNumbers = i.morsel.RowNumbers[:0]
		if !i.scanner.next(i.morsel.RowNumbers[:0], i.batchSize) {
			if err := i.scanner.err; err != nil {
				i.err = err
				return false
			}
			i.closeScanner()
			continue
		}
		i.morsel.RowNumbers = i.scanner.rows
		if err := i.filterCandidates(); err != nil {
			i.err = err
			return false
		}
		if len(i.morsel.RowNumbers) == 0 {
			continue
		}
		if err := i.readSelectedColumns(); err != nil {
			i.err = err
			return false
		}
		return true
	}
}

func (i *scalarColumnMorselIterator) openNextRowGroup() bool {
	for i.rgIndex < len(i.rgs) {
		rg := i.rgs[i.rgIndex]
		rgIndex := i.rgIndex
		rgStart := i.rgStart
		i.rgIndex++
		i.rgStart += rg.NumRows()

		s, err := newScalarDriverScanner(i.ctx, rg, i.columns[i.driver], i.batchSize)
		if err != nil {
			i.err = err
			return false
		}
		if s == nil {
			continue
		}
		i.scanner = s
		i.morsel.RowGroupIndex = rgIndex
		i.morsel.RowGroupStart = rgStart
		return true
	}
	return false
}

func (i *scalarColumnMorselIterator) closeScanner() {
	if i.scanner != nil {
		_ = i.scanner.close()
		i.scanner = nil
	}
}

func (i *scalarColumnMorselIterator) filterCandidates() error {
	for col, q := range i.columns {
		if col == i.driver || q.Predicate == nil || len(i.morsel.RowNumbers) == 0 {
			continue
		}
		values, keep, err := readScalarColumnRows(i.ctx, i.scanner.rg, q, i.morsel.RowNumbers)
		if err != nil {
			return err
		}
		_ = values
		var kept int
		for row, ok := range keep {
			if !ok {
				continue
			}
			i.morsel.RowNumbers[kept] = i.morsel.RowNumbers[row]
			kept++
		}
		i.morsel.RowNumbers = i.morsel.RowNumbers[:kept]
	}
	return nil
}

func (i *scalarColumnMorselIterator) readSelectedColumns() error {
	if cap(i.morsel.Columns) < len(i.selects) {
		i.morsel.Columns = make([]RepeatedColumnMorsel, len(i.selects))
	}
	i.morsel.Columns = i.morsel.Columns[:len(i.selects)]
	for out, col := range i.selects {
		values, keep, err := readScalarColumnRows(i.ctx, i.scanner.rg, ColumnQuery{
			Name:  i.columns[col].Name,
			Index: i.columns[col].Index,
		}, i.morsel.RowNumbers)
		if err != nil {
			return err
		}
		c := &i.morsel.Columns[out]
		c.Values = c.Values[:0]
		if cap(c.Offsets) < len(i.morsel.RowNumbers)+1 {
			c.Offsets = make([]int, len(i.morsel.RowNumbers)+1)
		}
		c.Offsets = c.Offsets[:len(i.morsel.RowNumbers)+1]
		c.Offsets[0] = 0
		for row, v := range values {
			if !keep[row] {
				return ErrSeekOutOfRange
			}
			c.Values = append(c.Values, v)
			c.Offsets[row+1] = len(c.Values)
		}
	}
	return nil
}

func (i *scalarColumnMorselIterator) At() ColumnMorsel { return i.morsel }
func (i *scalarColumnMorselIterator) Err() error       { return i.err }
func (i *scalarColumnMorselIterator) Close() error {
	i.closeScanner()
	if i.span != nil {
		i.span.End()
	}
	return nil
}

type scalarDriverScanner struct {
	ctx context.Context
	rg  parquet.RowGroup
	q   ColumnQuery

	pages parquet.Pages
	page  parquet.Page

	values parquet.ValueReader
	buf    []parquet.Value
	bufN   int

	nextRow    int64
	pageMaxRow int64
	rows       []int64
	err        error
}

func newScalarDriverScanner(ctx context.Context, rg parquet.RowGroup, q ColumnQuery, batchSize int) (*scalarDriverScanner, error) {
	if q.Index < 0 || q.Index >= len(rg.ColumnChunks()) {
		return nil, fmt.Errorf("column %d not found", q.Index)
	}
	chunk := rg.ColumnChunks()[q.Index]
	if q.Predicate != nil {
		ci, err := chunk.ColumnIndex()
		if err != nil {
			return nil, err
		}
		if !q.Predicate.KeepColumnChunk(ci) {
			return nil, nil
		}
	}
	return &scalarDriverScanner{
		ctx:   ctx,
		rg:    rg,
		q:     q,
		pages: chunk.Pages(),
		rows:  make([]int64, 0, batchSize),
	}, nil
}

func (s *scalarDriverScanner) next(rows []int64, batchSize int) bool {
	s.rows = rows
	for len(s.rows) < batchSize {
		if err := s.ctx.Err(); err != nil {
			s.err = err
			return false
		}
		v, ok := s.nextValue()
		if !ok {
			return len(s.rows) > 0
		}
		if s.q.Predicate == nil || s.q.Predicate.KeepValue(v) {
			s.rows = append(s.rows, s.nextRow-1)
		}
	}
	return true
}

func (s *scalarDriverScanner) nextValue() (parquet.Value, bool) {
	for {
		if s.page == nil || s.nextRow >= s.pageMaxRow {
			if !s.readNextPage() {
				return parquet.Value{}, false
			}
		}
		if len(s.buf) == 0 || s.bufN >= len(s.buf) {
			s.buf = slicesCap(s.buf, repeatedRowColumnIteratorReadSize)
			n, err := s.values.ReadValues(s.buf)
			if err != nil && err != io.EOF {
				s.err = err
				return parquet.Value{}, false
			}
			s.buf = s.buf[:n]
			s.bufN = 0
			if n == 0 {
				s.page = nil
				continue
			}
		}
		v := s.buf[s.bufN].Clone()
		s.bufN++
		s.nextRow++
		return v, true
	}
}

func (s *scalarDriverScanner) readNextPage() bool {
	for {
		if s.page != nil {
			parquet.Release(s.page)
			s.page = nil
		}
		page, err := s.pages.ReadPage()
		if page == nil || err != nil {
			if err != nil && err != io.EOF {
				s.err = err
			}
			return false
		}
		pageRows := page.NumRows()
		if s.q.Predicate != nil && !s.q.Predicate.KeepPage(page) {
			s.nextRow += pageRows
			parquet.Release(page)
			continue
		}
		s.page = page
		s.pageMaxRow = s.nextRow + pageRows
		s.values = page.Values()
		s.buf = s.buf[:0]
		s.bufN = 0
		return true
	}
}

func (s *scalarDriverScanner) close() error {
	if s.page != nil {
		parquet.Release(s.page)
		s.page = nil
	}
	if s.pages != nil {
		return s.pages.Close()
	}
	return nil
}

type scalarColumnRowReader struct {
	ctx  context.Context
	span oteltrace.Span

	pages parquet.Pages
	page  parquet.Page

	values parquet.ValueReader
	buf    []parquet.Value
	bufN   int

	nextRow    int64
	pageMaxRow int64
	pageKept   bool
	err        error
}

func readScalarColumnRows(ctx context.Context, rg parquet.RowGroup, q ColumnQuery, rowNumbers []int64) ([]parquet.Value, []bool, error) {
	values := make([]parquet.Value, len(rowNumbers))
	keep := make([]bool, len(rowNumbers))
	if len(rowNumbers) == 0 {
		return values, keep, nil
	}
	if q.Index < 0 || q.Index >= len(rg.ColumnChunks()) {
		return nil, nil, fmt.Errorf("column %d not found", q.Index)
	}
	chunk := rg.ColumnChunks()[q.Index]
	if q.Predicate != nil {
		ci, err := chunk.ColumnIndex()
		if err != nil {
			return nil, nil, err
		}
		if !q.Predicate.KeepColumnChunk(ci) {
			return values, keep, nil
		}
	}

	_, ctx = tracing.StartSpanFromContext(ctx, "ScalarColumnRows")
	reader := &scalarColumnRowReader{
		ctx:   ctx,
		span:  oteltrace.SpanFromContext(ctx),
		pages: chunk.Pages(),
	}
	reader.span.SetAttributes(
		attribute.Int("columnIndex", q.Index),
		attribute.String("column", q.Name),
	)
	defer func() {
		reader.span.End()
	}()
	defer reader.close()

	for i, rn := range rowNumbers {
		v, ok, err := reader.read(rn, q.Predicate)
		if err != nil {
			reader.span.RecordError(err)
			reader.span.SetStatus(codes.Error, err.Error())
			return nil, nil, err
		}
		if !ok {
			continue
		}
		values[i] = v
		keep[i] = true
	}
	return values, keep, nil
}

func (r *scalarColumnRowReader) read(rowNumber int64, pred Predicate) (parquet.Value, bool, error) {
	if err := r.ctx.Err(); err != nil {
		return parquet.Value{}, false, err
	}
	if r.page == nil || rowNumber >= r.pageMaxRow {
		if err := r.seekPage(rowNumber, pred); err != nil {
			return parquet.Value{}, false, err
		}
	}
	if !r.pageKept {
		return parquet.Value{}, false, nil
	}
	for r.nextRow <= rowNumber {
		v, ok, err := r.nextValue()
		if err != nil || !ok {
			return parquet.Value{}, false, err
		}
		if r.nextRow-1 == rowNumber {
			if pred != nil && !pred.KeepValue(v) {
				return parquet.Value{}, false, nil
			}
			return v, true, nil
		}
	}
	return parquet.Value{}, false, ErrSeekOutOfRange
}

func (r *scalarColumnRowReader) seekPage(rowNumber int64, pred Predicate) error {
	if r.page != nil {
		parquet.Release(r.page)
		r.page = nil
	}
	if err := r.pages.SeekToRow(rowNumber); err != nil {
		return err
	}
	pageReadStart := time.Now()
	page, err := r.pages.ReadPage()
	if err != nil {
		if err == io.EOF && page != nil {
			err = nil
		} else {
			return err
		}
	}
	if page == nil {
		return ErrSeekOutOfRange
	}
	pageRows := page.NumRows()
	r.page = page
	r.values = page.Values()
	r.nextRow = rowNumber
	r.pageMaxRow = rowNumber + pageRows
	r.pageKept = pred == nil || pred.KeepPage(page)
	r.buf = r.buf[:0]
	r.bufN = 0
	if r.span.IsRecording() {
		r.span.AddEvent("Page read", oteltrace.WithAttributes(
			attribute.Int64("seek_row", rowNumber),
			attribute.Int64("page_read_ms", time.Since(pageReadStart).Milliseconds()),
			attribute.Int64("page_num_rows", pageRows),
		))
	}
	return nil
}

func (r *scalarColumnRowReader) nextValue() (parquet.Value, bool, error) {
	for {
		if len(r.buf) == 0 || r.bufN >= len(r.buf) {
			r.buf = slicesCap(r.buf, repeatedRowColumnIteratorReadSize)
			n, err := r.values.ReadValues(r.buf)
			if err != nil && err != io.EOF {
				return parquet.Value{}, false, err
			}
			r.buf = r.buf[:n]
			r.bufN = 0
			if n == 0 {
				return parquet.Value{}, false, ErrSeekOutOfRange
			}
		}
		v := r.buf[r.bufN].Clone()
		r.bufN++
		r.nextRow++
		return v, true, nil
	}
}

func (r *scalarColumnRowReader) close() error {
	var err multierror.MultiError
	if r.pages != nil {
		err.Add(r.pages.Close())
	}
	if r.page != nil {
		parquet.Release(r.page)
	}
	return err.Err()
}

func slicesCap(values []parquet.Value, capacity int) []parquet.Value {
	if cap(values) < capacity {
		return make([]parquet.Value, capacity)
	}
	return values[:capacity]
}
