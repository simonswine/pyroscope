package querysession

import (
	"errors"
	"fmt"

	querysessionv1 "github.com/grafana/pyroscope/api/gen/proto/go/querysession/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// Request is a normalized command used by session execution. It is not a wire
// schema: JSON methods use the generated opaque-API messages defined in
// api/querysession/v1/querysession.proto, shared by the frontend and profilecli.
// This value type avoids copying protobuf message-state locks into sessions.
type Request struct {
	ID, Op, Type         string
	Start, End           int64
	Matchers, LabelNames []string
	Name                 string
	PageSize             int
	NextToken, TargetID  string
}

// StringDelta is the normalized form of a generated SessionStringDelta.
type StringDelta struct {
	Start  int
	Values []string
}

// Frame is a normalized server event. Its JSON representation is exclusively
// defined by the generated SessionFrame oneof, not by Go struct field tags.
type Frame struct {
	ID, Op, Type  string
	Strings       *StringDelta
	Series        [][][2]int
	Refs          []int
	NextToken     string
	QueryComplete bool
	Code, Message string
	TableEpoch    uint32
}

var protocolJSON = protojson.MarshalOptions{UseProtoNames: true}

func metadataQueryType(kind string) (querysessionv1.QueryType, error) {
	switch kind {
	case "Series":
		return querysessionv1.QueryType_QUERY_SERIES_LABELS, nil
	case "LabelNames":
		return querysessionv1.QueryType_QUERY_LABEL_NAMES, nil
	case "LabelValues":
		return querysessionv1.QueryType_QUERY_LABEL_VALUES, nil
	default:
		return 0, fmt.Errorf("unsupported metadata query type %q", kind)
	}
}
func metadataQueryName(kind querysessionv1.QueryType) (string, error) {
	switch kind {
	case querysessionv1.QueryType_QUERY_SERIES_LABELS:
		return "Series", nil
	case querysessionv1.QueryType_QUERY_LABEL_NAMES:
		return "LabelNames", nil
	case querysessionv1.QueryType_QUERY_LABEL_VALUES:
		return "LabelValues", nil
	default:
		return "", fmt.Errorf("unsupported metadata query type %v", kind)
	}
}
func wireIndex(v int) (uint32, error) {
	if v < 0 || uint64(v) > uint64(^uint32(0)) {
		return 0, errors.New("invalid unsigned protocol index")
	}
	return uint32(v), nil
}

func (r Request) MarshalJSON() ([]byte, error) {
	out := querysessionv1.SessionRequest_builder{Id: r.ID}
	switch r.Op {
	case "query":
		kind, err := metadataQueryType(r.Type)
		if err != nil {
			return nil, err
		}
		size, err := wireIndex(r.PageSize)
		if err != nil {
			return nil, err
		}
		out.Query = querysessionv1.SessionQuery_builder{QueryType: kind, Start: r.Start, End: r.End, Matchers: r.Matchers, LabelNames: r.LabelNames, Name: r.Name, PageSize: size}.Build()
	case "next":
		size, err := wireIndex(r.PageSize)
		if err != nil {
			return nil, err
		}
		out.Next = querysessionv1.SessionNext_builder{NextToken: r.NextToken, PageSize: size}.Build()
	case "cancel":
		out.Cancel = querysessionv1.SessionCancel_builder{TargetId: r.TargetID}.Build()
	case "reset_string_table":
		out.ResetStringTable = querysessionv1.SessionResetRequest_builder{}.Build()
	default:
		return nil, fmt.Errorf("unsupported request operation %q", r.Op)
	}
	return protocolJSON.Marshal(out.Build())
}
func (r *Request) UnmarshalJSON(b []byte) error {
	var in querysessionv1.SessionRequest
	if err := protojson.Unmarshal(b, &in); err != nil {
		return err
	}
	out := Request{ID: in.GetId()}
	switch in.WhichPayload() {
	case querysessionv1.SessionRequest_Query_case:
		p := in.GetQuery()
		kind, err := metadataQueryName(p.GetQueryType())
		if err != nil {
			return err
		}
		out.Op, out.Type = "query", kind
		out.Start, out.End = p.GetStart(), p.GetEnd()
		out.Matchers, out.LabelNames = p.GetMatchers(), p.GetLabelNames()
		out.Name = p.GetName()
		out.PageSize = int(p.GetPageSize())
	case querysessionv1.SessionRequest_Next_case:
		out.Op = "next"
		out.NextToken = in.GetNext().GetNextToken()
		out.PageSize = int(in.GetNext().GetPageSize())
	case querysessionv1.SessionRequest_Cancel_case:
		out.Op = "cancel"
		out.TargetID = in.GetCancel().GetTargetId()
	case querysessionv1.SessionRequest_ResetStringTable_case:
		out.Op = "reset_string_table"
	default:
		return errors.New("missing request payload")
	}
	*r = out
	return nil
}

func (f Frame) MarshalJSON() ([]byte, error) {
	out := querysessionv1.SessionFrame_builder{Id: f.ID}
	switch f.Op {
	case "page":
		kind, err := metadataQueryType(f.Type)
		if err != nil {
			return nil, err
		}
		p := querysessionv1.SessionPage_builder{QueryType: kind, TableEpoch: f.TableEpoch, NextToken: f.NextToken}
		if f.Strings != nil {
			start, err := wireIndex(f.Strings.Start)
			if err != nil {
				return nil, err
			}
			p.Strings = querysessionv1.SessionStringDelta_builder{Start: start, Values: f.Strings.Values}.Build()
		}
		for _, set := range f.Series {
			labels := querysessionv1.SessionLabelSet_builder{}
			for _, pair := range set {
				key, err := wireIndex(pair[0])
				if err != nil {
					return nil, err
				}
				value, err := wireIndex(pair[1])
				if err != nil {
					return nil, err
				}
				labels.Labels = append(labels.Labels, querysessionv1.SessionLabelRef_builder{Key: key, Value: value}.Build())
			}
			p.Series = append(p.Series, labels.Build())
		}
		for _, ref := range f.Refs {
			v, err := wireIndex(ref)
			if err != nil {
				return nil, err
			}
			p.Refs = append(p.Refs, v)
		}
		out.Page = p.Build()
	case "complete":
		out.Complete = querysessionv1.SessionComplete_builder{QueryComplete: f.QueryComplete}.Build()
	case "error":
		out.Error = querysessionv1.SessionError_builder{Code: f.Code, Message: f.Message}.Build()
	case "table_reset":
		out.TableReset = querysessionv1.SessionTableReset_builder{Epoch: f.TableEpoch}.Build()
	default:
		return nil, fmt.Errorf("unsupported frame operation %q", f.Op)
	}
	return protocolJSON.Marshal(out.Build())
}
func (f *Frame) UnmarshalJSON(b []byte) error {
	var in querysessionv1.SessionFrame
	if err := protojson.Unmarshal(b, &in); err != nil {
		return err
	}
	out := Frame{ID: in.GetId()}
	switch in.WhichPayload() {
	case querysessionv1.SessionFrame_Page_case:
		p := in.GetPage()
		kind, err := metadataQueryName(p.GetQueryType())
		if err != nil {
			return err
		}
		out.Op, out.Type = "page", kind
		out.TableEpoch = p.GetTableEpoch()
		out.NextToken = p.GetNextToken()
		if delta := p.GetStrings(); delta != nil {
			out.Strings = &StringDelta{Start: int(delta.GetStart()), Values: delta.GetValues()}
		}
		for _, set := range p.GetSeries() {
			if set == nil {
				return errors.New("nil protocol label set")
			}
			labels := make([][2]int, 0, len(set.GetLabels()))
			for _, pair := range set.GetLabels() {
				if pair == nil {
					return errors.New("nil protocol label reference")
				}
				labels = append(labels, [2]int{int(pair.GetKey()), int(pair.GetValue())})
			}
			out.Series = append(out.Series, labels)
		}
		for _, ref := range p.GetRefs() {
			out.Refs = append(out.Refs, int(ref))
		}
	case querysessionv1.SessionFrame_Complete_case:
		out.Op = "complete"
		out.QueryComplete = in.GetComplete().GetQueryComplete()
	case querysessionv1.SessionFrame_Error_case:
		out.Op = "error"
		out.Code = in.GetError().GetCode()
		out.Message = in.GetError().GetMessage()
	case querysessionv1.SessionFrame_TableReset_case:
		out.Op = "table_reset"
		out.TableEpoch = in.GetTableReset().GetEpoch()
	default:
		return errors.New("missing server payload")
	}
	*f = out
	return nil
}
