package querysession

import (
	"encoding/json"
	"testing"

	querysessionv1 "github.com/grafana/pyroscope/api/gen/proto/go/querysession/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestRequestUsesGeneratedProtobufJSON(t *testing.T) {
	for _, r := range []Request{
		{ID: "q1", Op: "query", Type: "Series", Start: 1 << 60, End: (1 << 60) + 100, Matchers: []string{`{job="api"}`}, LabelNames: []string{"job"}, PageSize: 10},
		{ID: "n1", Op: "next", NextToken: "opaque", PageSize: 10},
		{ID: "c1", Op: "cancel", TargetID: "q1"},
		{ID: "r1", Op: "reset_string_table"},
	} {
		b, err := json.Marshal(r)
		require.NoError(t, err)
		var wire querysessionv1.SessionRequest
		require.NoError(t, protojson.Unmarshal(b, &wire))
		require.Equal(t, r.ID, wire.GetId())
		if r.Op == "query" {
			require.Equal(t, r.Start, wire.GetQuery().GetStart())
			require.Contains(t, string(b), `"start":"1152921504606846976"`)
		}
		if r.Op == "reset_string_table" {
			require.NotNil(t, wire.GetResetStringTable())
		}
		var decoded Request
		require.NoError(t, json.Unmarshal(b, &decoded))
		require.Equal(t, r, decoded)
	}
}
func TestFrameUsesGeneratedProtobufJSON(t *testing.T) {
	for _, f := range []Frame{
		{ID: "q1", Op: "page", Type: "Series", TableEpoch: 2, Strings: &StringDelta{Values: []string{"job", "api"}}, Series: [][][2]int{{{0, 1}}}, NextToken: "next"},
		{ID: "q2", Op: "page", Type: "LabelValues", Strings: &StringDelta{Values: []string{"api"}}, Refs: []int{0}},
		{ID: "q1", Op: "complete", QueryComplete: true},
		{ID: "q1", Op: "error", Code: "cancelled", Message: "cancelled"},
		{ID: "r1", Op: "table_reset", TableEpoch: 3},
		{Op: "table_reset", TableEpoch: 4},
	} {
		b, err := json.Marshal(f)
		require.NoError(t, err)
		var wire querysessionv1.SessionFrame
		require.NoError(t, protojson.Unmarshal(b, &wire))
		require.Equal(t, f.ID, wire.GetId())
		if f.Op == "page" {
			require.NotNil(t, wire.GetPage())
			require.Equal(t, f.TableEpoch, wire.GetPage().GetTableEpoch())
		}
		var decoded Frame
		require.NoError(t, json.Unmarshal(b, &decoded))
		require.Equal(t, f, decoded)
	}
}
func TestProtocolRejectsInvalidPayloads(t *testing.T) {
	for _, b := range []string{
		`{}`, `{"id":"q1","op":"query"}`,
		`{"id":"q1","query":{},"cancel":{}}`,
		`{"id":"q1","query":{"query_type":"QUERY_TREE"}}`,
		`{"id":"q1","query":{"query_type":"QUERY_LABEL_NAMES","page_size":-1}}`,
		`{"id":"a","id":"b","reset_string_table":{}}`,
		`{"id":"a","reset_string_table":{},"unknown":1}`,
	} {
		var r Request
		require.Error(t, r.UnmarshalJSON([]byte(b)), b)
	}
	for _, b := range []string{
		`{}`, `{"id":"q1","complete":{},"error":{}}`,
		`{"id":"q1","page":{"query_type":"QUERY_LABEL_NAMES","refs":[-1]}}`,
		`{"table_reset":{"epoch":-1}}`,
	} {
		var f Frame
		require.Error(t, f.UnmarshalJSON([]byte(b)), b)
	}
}
