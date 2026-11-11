package metadatastream

import (
	"fmt"
	"strings"
	"testing"

	queryv1 "github.com/grafana/pyroscope/api/gen/proto/go/query/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestChunkBoundsAndRoundTrip(t *testing.T) {
	for _, kind := range []queryv1.QueryType{queryv1.QueryType_QUERY_SERIES_LABELS, queryv1.QueryType_QUERY_LABEL_NAMES, queryv1.QueryType_QUERY_LABEL_VALUES} {
		t.Run(kind.String(), func(t *testing.T) {
			var chunks []*queryv1.InvokeStreamResponse
			enc := NewEncoder(kind, 256, func(c *queryv1.InvokeStreamResponse) error {
				require.LessOrEqual(t, proto.Size(c), 256)
				require.Equal(t, proto.Size(c), c.SizeVT())
				chunks = append(chunks, c)
				return nil
			})
			var expected []Element
			for i := 0; i < 500; i++ {
				e := Element{&typesv1.LabelPair{Name: "service_name", Value: fmt.Sprintf("service-%d-π", i)}}
				if kind == queryv1.QueryType_QUERY_LABEL_NAMES {
					e[0].Name = fmt.Sprintf("label_%d", i)
					e[0].Value = ""
				}
				if kind == queryv1.QueryType_QUERY_SERIES_LABELS && i%20 == 0 {
					e = Element{}
				}
				expected = append(expected, e)
				require.NoError(t, enc.Add(e))
			}
			require.NoError(t, enc.Flush())
			require.Greater(t, len(chunks), 1)
			var actual []Element
			for _, chunk := range chunks {
				require.NoError(t, Decode(chunk, func(e Element) error { actual = append(actual, e); return nil }))
			}
			require.Equal(t, expected, actual)
		})
	}
}
func TestOversizedElement(t *testing.T) {
	enc := NewEncoder(queryv1.QueryType_QUERY_SERIES_LABELS, 64, func(*queryv1.InvokeStreamResponse) error { t.Fatal("must not emit oversized element"); return nil })
	err := enc.Add(Element{&typesv1.LabelPair{Name: "a", Value: strings.Repeat("x", 100)}})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
}
func TestInvalidReferences(t *testing.T) {
	for _, c := range []*queryv1.InvokeStreamResponse{
		{QueryType: queryv1.QueryType_QUERY_LABEL_NAMES, AttributeTable: &queryv1.AttributeTable{Keys: []string{"a"}}, AttributeRefs: []int64{0}},
		{QueryType: queryv1.QueryType_QUERY_LABEL_NAMES, AttributeRefs: []int64{-1}},
		{QueryType: queryv1.QueryType_QUERY_SERIES_LABELS, Series: []*queryv1.MetadataLabelSet{nil}},
	} {
		require.Error(t, Decode(c, func(Element) error { t.Fatal("invalid element visited"); return nil }))
	}
}
func TestValidate(t *testing.T) {
	require.Error(t, Validate(nil))
	require.Error(t, Validate(&queryv1.InvokeRequest{Query: []*queryv1.Query{{QueryType: queryv1.QueryType_QUERY_TREE}}}))
	require.NoError(t, Validate(&queryv1.InvokeRequest{Query: []*queryv1.Query{{QueryType: queryv1.QueryType_QUERY_LABEL_NAMES, LabelNames: &queryv1.LabelNamesQuery{}}}}))
}
