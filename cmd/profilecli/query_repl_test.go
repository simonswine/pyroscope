package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/gorilla/websocket"
	"github.com/grafana/dskit/user"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	"github.com/grafana/pyroscope/v2/pkg/frontend/querysession"
	"github.com/grafana/pyroscope/v2/pkg/querybackend/metadatastream"
	"github.com/stretchr/testify/require"
)

func replTestParams(base string) *queryREPLParams {
	return &queryREPLParams{queryParams: &queryParams{phlareClient: &phlareClient{URL: base, TenantID: "t"}, From: "now-1h", To: "now", Query: "{}"}, PageSize: 10000}
}
func TestREPLCommandFlags(t *testing.T) {
	app := kingpin.New("profilecli", "test")
	p := addQueryREPLParams(app.Command("repl", "test"))
	command, err := app.Parse([]string{"repl", "--url=http://example.test/prefix", "--tenant-id=t", "--from=now-2h", "--page-size=10"})
	require.NoError(t, err)
	require.Equal(t, "repl", command)
	require.Equal(t, "now-2h", p.From)
	require.Equal(t, 10, p.PageSize)
}
func TestREPLCommandsAndPagination(t *testing.T) {
	s, err := newREPLState(replTestParams("http://unused"))
	require.NoError(t, err)
	for _, line := range []string{"from now-2h", "to now", `selector {service_name="abcd"}`, "page_size 2"} {
		a, err := s.command(line)
		require.NoError(t, err)
		require.Nil(t, a.request)
	}
	a, err := s.command("series job service_name")
	require.NoError(t, err)
	r := a.request
	require.Equal(t, "Series", r.Type)
	require.Equal(t, []string{"job", "service_name"}, r.LabelNames)
	require.Equal(t, 2, r.PageSize)
	require.Equal(t, []string{`{service_name="abcd"}`}, r.Matchers)
	require.InDelta(t, 2*time.Hour.Milliseconds(), r.End-r.Start, 100)
	_, err = s.command("next")
	require.ErrorContains(t, err, "in-flight")
	text, err := s.accept(querysession.Frame{ID: r.ID, Op: "page", Type: "Series", Strings: &querysession.StringDelta{Start: 0, Values: []string{"job", "api"}}, Series: [][][2]int{{{0, 1}}}, NextToken: "token"})
	require.NoError(t, err)
	require.Contains(t, text, `{job="api"}`)
	_, err = s.accept(querysession.Frame{ID: r.ID, Op: "complete"})
	require.NoError(t, err)
	_, err = s.command("page_size 1")
	require.NoError(t, err)
	a, err = s.command("next " + r.ID)
	require.NoError(t, err)
	require.Equal(t, "token", a.request.NextToken)
	require.Zero(t, a.request.PageSize)
	_, err = s.accept(querysession.Frame{ID: a.request.ID, Op: "page", Type: "Series", Strings: &querysession.StringDelta{Start: 2}})
	require.NoError(t, err)
	text, err = s.accept(querysession.Frame{ID: a.request.ID, Op: "complete", QueryComplete: true})
	require.NoError(t, err)
	require.Contains(t, text, "query_complete")
	_, err = s.command("next")
	require.ErrorContains(t, err, "no next")
	a, err = s.command("label_values job")
	require.NoError(t, err)
	require.Equal(t, "LabelValues", a.request.Type)
	require.Equal(t, "job", a.request.Name)
	require.Equal(t, 1, a.request.PageSize)
}
func TestREPLSharedDictionaryAcrossMultiplexedQueries(t *testing.T) {
	s, err := newREPLState(replTestParams("http://unused"))
	require.NoError(t, err)
	first, err := s.command("series")
	require.NoError(t, err)
	second, err := s.command("label_names")
	require.NoError(t, err)
	// Query two introduces the key before query one refers to it.
	_, err = s.accept(querysession.Frame{ID: second.request.ID, Op: "page", Type: "LabelNames", Strings: &querysession.StringDelta{Values: []string{"job"}}, Refs: []int{0}})
	require.NoError(t, err)
	text, err := s.accept(querysession.Frame{ID: first.request.ID, Op: "page", Type: "Series", Strings: &querysession.StringDelta{Start: 1, Values: []string{"api"}}, Series: [][][2]int{{{0, 1}}}})
	require.NoError(t, err)
	require.Contains(t, text, `{job="api"}`)
	require.Equal(t, []string{"job", "api"}, s.table)
}
func TestREPLClientAndServerStringTableResets(t *testing.T) {
	s, err := newREPLState(replTestParams("http://unused"))
	require.NoError(t, err)
	q, err := s.command("label_names")
	require.NoError(t, err)
	_, err = s.accept(querysession.Frame{ID: q.request.ID, Op: "page", Type: "LabelNames", Strings: &querysession.StringDelta{Values: []string{"old"}}, Refs: []int{0}, NextToken: "next"})
	require.NoError(t, err)
	_, err = s.accept(querysession.Frame{ID: q.request.ID, Op: "complete"})
	require.NoError(t, err)
	reset, err := s.command("reset_string_table")
	require.NoError(t, err)
	require.Equal(t, "reset_string_table", reset.request.Op)
	require.Equal(t, []string{"old"}, s.table) // Never clear before the ordered barrier.
	_, err = s.accept(querysession.Frame{ID: reset.request.ID, Op: "table_reset", TableEpoch: 1})
	require.NoError(t, err)
	require.Empty(t, s.table)
	require.Zero(t, s.tableBytes)
	require.Equal(t, "next", s.queries[q.request.ID].next)
	_, err = s.accept(querysession.Frame{ID: reset.request.ID, Op: "complete"})
	require.NoError(t, err)
	next, err := s.command("next")
	require.NoError(t, err)
	_, err = s.accept(querysession.Frame{Op: "table_reset", TableEpoch: 2})
	require.NoError(t, err) // Server-initiated while next is in flight.
	text, err := s.accept(querysession.Frame{ID: next.request.ID, Op: "page", Type: "LabelNames", TableEpoch: 2, Strings: &querysession.StringDelta{Values: []string{"new"}}, Refs: []int{0}})
	require.NoError(t, err)
	require.Contains(t, text, `"new"`)
	_, err = s.accept(querysession.Frame{ID: next.request.ID, Op: "complete", QueryComplete: true})
	require.NoError(t, err)
	require.Equal(t, []string{"new"}, s.table)
}

func TestREPLRejectsStaleResetEpochs(t *testing.T) {
	s, err := newREPLState(replTestParams("http://unused"))
	require.NoError(t, err)
	_, err = s.accept(querysession.Frame{Op: "table_reset", TableEpoch: 1})
	require.NoError(t, err)
	for _, epoch := range []uint32{0, 1, 3} {
		_, err = s.accept(querysession.Frame{Op: "table_reset", TableEpoch: epoch})
		require.Error(t, err)
	}
	q, err := s.command("label_names")
	require.NoError(t, err)
	for _, epoch := range []uint32{0, 2} {
		_, err = s.accept(querysession.Frame{ID: q.request.ID, Op: "page", Type: "LabelNames", TableEpoch: epoch, Strings: &querysession.StringDelta{}})
		require.Error(t, err)
	}
	reset, err := s.command("reset_string_table")
	require.NoError(t, err)
	_, err = s.accept(querysession.Frame{ID: reset.request.ID, Op: "complete"})
	require.ErrorContains(t, err, "without reset barrier")
	_, err = s.accept(querysession.Frame{ID: "unknown", Op: "table_reset", TableEpoch: 2})
	require.Error(t, err)
}

func TestREPLInvalidCommandsDoNotChangeDefaults(t *testing.T) {
	s, err := newREPLState(replTestParams("http://unused"))
	require.NoError(t, err)
	for _, line := range []string{"from nonsense", "to nonsense", "selector not{valid", "page_size 0", "page_size 10001", "label_values", "label_values a b", "label_names job", "next unknown", "cancel unknown", "nope"} {
		_, err = s.command(line)
		require.Error(t, err, line)
	}
	require.Equal(t, "now-1h", s.from)
	require.Equal(t, "now", s.to)
	require.Equal(t, "{}", s.selector)
	require.Equal(t, 10000, s.pageSize)
}
func TestREPLProtocolValidation(t *testing.T) {
	tests := []querysession.Frame{
		{ID: "unknown", Op: "complete"},
		{ID: "q1", Op: "complete", QueryComplete: true},
		{ID: "q1", Op: "page", Type: "LabelNames", Strings: &querysession.StringDelta{Start: 1}},
		{ID: "q1", Op: "page", Type: "LabelNames", Strings: &querysession.StringDelta{}, Refs: []int{-1}},
		{ID: "q1", Op: "page", Type: "Series", Strings: &querysession.StringDelta{}},
	}
	for i, f := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			s, err := newREPLState(replTestParams("http://unused"))
			require.NoError(t, err)
			_, err = s.command("label_names")
			require.NoError(t, err)
			_, err = s.accept(f)
			require.Error(t, err)
		})
	}
}
func TestREPLQuotesTerminalControlsAndLimitsExpansion(t *testing.T) {
	s, err := newREPLState(replTestParams("http://unused"))
	require.NoError(t, err)
	a, err := s.command("label_names")
	require.NoError(t, err)
	text, err := s.accept(querysession.Frame{ID: a.request.ID, Op: "page", Type: "LabelNames", Strings: &querysession.StringDelta{Values: []string{"\x1b[31m\n"}}, Refs: []int{0}})
	require.NoError(t, err)
	require.NotContains(t, text, "\x1b")
	require.Contains(t, text, `\x1b`)
	a, err = s.command("label_values job")
	require.NoError(t, err)
	_, err = s.accept(querysession.Frame{ID: a.request.ID, Op: "page", Type: "LabelValues", Strings: &querysession.StringDelta{Start: 1, Values: []string{strings.Repeat("x", 1<<20)}}, Refs: []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}})
	require.ErrorContains(t, err, "rendered-output limit")
}
func TestREPLQueryErrorAndCancel(t *testing.T) {
	s, err := newREPLState(replTestParams("http://unused"))
	require.NoError(t, err)
	a, err := s.command("series")
	require.NoError(t, err)
	cancel, err := s.command("cancel")
	require.NoError(t, err)
	require.Equal(t, a.request.ID, cancel.request.TargetID)
	_, err = s.accept(querysession.Frame{ID: cancel.request.ID, Op: "complete"})
	require.NoError(t, err)
	text, err := s.accept(querysession.Frame{ID: a.request.ID, Op: "error", Code: "cancelled", Message: "query cancelled"})
	require.NoError(t, err)
	require.Contains(t, text, "cancelled")
	require.False(t, s.queries[a.request.ID].busy)
	_, err = s.command("next")
	require.ErrorContains(t, err, "cancelled")
}
func TestQuerySessionURL(t *testing.T) {
	actual, err := querySessionURL("https://example.test/api/datasources/proxy/uid/abc?extra=value")
	require.NoError(t, err)
	require.Equal(t, "wss://example.test/api/datasources/proxy/uid/abc/querier.v1.QuerierService/Session?accept=application%2Fjson&extra=value", actual)
	for _, base := range []string{"ftp://example.test", "http:///missing", "http://example.test/#fragment"} {
		_, err = querySessionURL(base)
		require.Error(t, err)
	}
}
func TestREPLHandshakeAuthentication(t *testing.T) {
	for _, basic := range []bool{false, true} {
		t.Run(fmt.Sprint(basic), func(t *testing.T) {
			headers := make(chan http.Header, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/prefix/querier.v1.QuerierService/Session", r.URL.Path)
				require.Equal(t, "application/json", r.URL.Query().Get("accept"))
				headers <- r.Header.Clone()
				c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer c.Close()
				_, _, _ = c.ReadMessage()
			}))
			defer server.Close()
			client := &phlareClient{URL: server.URL + "/prefix", TenantID: "t", BearerToken: "bearer"}
			if basic {
				client.BasicAuth.Username = "username"
				client.BasicAuth.Password = "password"
			}
			conn, err := client.dialQuerySession(context.Background())
			require.NoError(t, err)
			defer conn.Close()
			h := <-headers
			require.Equal(t, "t", h.Get("X-Scope-OrgID"))
			require.Equal(t, userAgentHeader, h.Get("User-Agent"))
			if basic {
				u, p, ok := (&http.Request{Header: h}).BasicAuth()
				require.True(t, ok)
				require.Equal(t, "username", u)
				require.Equal(t, "password", p)
			} else {
				require.Equal(t, "Bearer bearer", h.Get("Authorization"))
			}
		})
	}
}
func TestREPLWithQuerySessionServer(t *testing.T) {
	var mu sync.Mutex
	var requests []querysession.Request
	handler, err := querysession.New(querysession.DefaultConfig(), func(_ context.Context, r querysession.Request, visit func(metadatastream.Element) error) error {
		mu.Lock()
		requests = append(requests, r)
		mu.Unlock()
		switch r.Type {
		case "Series":
			return visit(metadatastream.Element{&typesv1.LabelPair{Name: "service_name", Value: "abcd"}})
		case "LabelNames":
			return visit(metadatastream.Element{&typesv1.LabelPair{Name: "job"}})
		case "LabelValues":
			return visit(metadatastream.Element{&typesv1.LabelPair{Name: r.Name, Value: "api"}})
		}
		return errors.New("unexpected query")
	})
	require.NoError(t, err)
	defer handler.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(user.InjectOrgID(r.Context(), "t")))
	}))
	defer server.Close()
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = runQueryREPL(ctx, replTestParams(server.URL), strings.NewReader("reset_string_table\nfrom now-2h\nselector {service_name=\"abcd\"}\nseries\nlabel_names\nlabel_values job\n"), &out, false)
	require.NoError(t, err)
	require.Contains(t, out.String(), `{service_name="abcd"}`)
	require.Contains(t, out.String(), `"job"`)
	require.Contains(t, out.String(), `"api"`)
	require.Equal(t, 3, strings.Count(out.String(), "query_complete"))
	require.Contains(t, out.String(), "string table reset (epoch 1)")
	require.Contains(t, out.String(), "reset complete")
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 3)
	for _, r := range requests {
		require.Equal(t, []string{`{service_name="abcd"}`}, r.Matchers)
		require.InDelta(t, 2*time.Hour.Milliseconds(), r.End-r.Start, 100)
	}
}
func TestREPLContextCancellation(t *testing.T) {
	handler, err := querysession.New(querysession.DefaultConfig(), func(ctx context.Context, _ querysession.Request, _ func(metadatastream.Element) error) error {
		<-ctx.Done()
		return ctx.Err()
	})
	require.NoError(t, err)
	defer handler.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(user.InjectOrgID(r.Context(), "t")))
	}))
	defer server.Close()
	input, write := io.Pipe()
	defer input.Close()
	defer write.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = runQueryREPL(ctx, replTestParams(server.URL), input, io.Discard, false)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
