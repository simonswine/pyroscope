package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/gorilla/websocket"
	"github.com/grafana/pyroscope/v2/pkg/frontend/querysession"
	phlaremodel "github.com/grafana/pyroscope/v2/pkg/model"
	"github.com/grafana/pyroscope/v2/pkg/operations"
)

const replHelp = `Commands:
  from now-2h                 Set query start (also accepts RFC3339/timestamps)
  to now                      Set query end
  selector {service_name="abcd"}
  page_size 10000              Set page size for new queries (1..10000)
  series [label_name ...]      Query series, optionally projecting labels
  label_names                 Query label names
  label_values job            Query values of a label
  next [query-id]              Fetch another page (defaults to latest query)
  cancel [query-id]            Cancel a query, including between pages
  reset_string_table          Request a connection-wide string table reset
  show                        Show defaults and query states
  help
  quit

Queries share one WebSocket and string table. Results are provisional until
query_complete. Sorting is page-local; duplicates are removed across pages.
Transport disconnects reconnect automatically, but unfinished requests are not replayed.`

type queryREPLParams struct {
	*queryParams
	PageSize int
}

func addQueryREPLParams(cmd commander) *queryREPLParams {
	p := &queryREPLParams{queryParams: addQueryParams(cmd)}
	cmd.Flag("page-size", "Default metadata page size (1..10000).").Default("10000").IntVar(&p.PageSize)
	return p
}
func queryREPL(ctx context.Context, p *queryREPLParams) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	// A blocked stdin scan must not outlive an interrupted REPL.
	defer os.Stdin.Close()
	info, err := os.Stdin.Stat()
	if err != nil {
		return err
	}
	err = runQueryREPL(ctx, p, os.Stdin, output(ctx), info.Mode()&os.ModeCharDevice != 0)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func querySessionURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", errors.New("invalid server URL")
	}
	switch u.Scheme {
	case "http", "ws":
		u.Scheme = "ws"
	case "https", "wss":
		u.Scheme = "wss"
	default:
		return "", errors.New("server URL must use http, https, ws, or wss")
	}
	if u.Host == "" || u.Fragment != "" {
		return "", errors.New("server URL must have a host and no fragment")
	}
	u = u.JoinPath("querier.v1.QuerierService", "Session")
	q := u.Query()
	q.Set("accept", "application/json")
	u.RawQuery = q.Encode()
	return u.String(), nil
}
func (c *phlareClient) dialQuerySession(ctx context.Context) (*websocket.Conn, error) {
	target, err := querySessionURL(c.URL)
	if err != nil {
		return nil, err
	}
	req := &http.Request{Header: make(http.Header)}
	c.setRequestHeaders(req)
	dialer := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 10 * time.Second, EnableCompression: false}
	transport := c.defaultTransport
	if transport == nil {
		transport = http.DefaultTransport
	}
	t, ok := transport.(*http.Transport)
	if !ok {
		return nil, errors.New("WebSocket REPL requires an http.Transport, not a custom RoundTripper")
	}
	dialer.Proxy = t.Proxy
	dialer.NetDialContext = t.DialContext
	dialer.NetDialTLSContext = t.DialTLSContext
	if t.TLSClientConfig != nil {
		dialer.TLSClientConfig = t.TLSClientConfig.Clone()
	}
	conn, resp, err := dialer.DialContext(ctx, target, req.Header)
	if err != nil {
		if resp != nil {
			if resp.Body != nil {
				_ = resp.Body.Close()
			}
			return nil, fmt.Errorf("query-session handshake: HTTP %d (requires the v2 query-frontend metadata endpoint)", resp.StatusCode)
		}
		return nil, fmt.Errorf("connect query session: %w", err)
	}
	conn.SetReadLimit(4 << 20)
	return conn, nil
}

type replQuery struct {
	id, kind, next            string
	busy, complete, cancelled bool
	disconnected              bool
}
type replPending struct {
	query     *replQuery
	cancel    bool
	page      bool
	reset     bool
	resetSeen bool
}
type replState struct {
	from, to, selector string
	pageSize, sequence int
	last               string
	queries            map[string]*replQuery
	pending            map[string]*replPending
	table              []string
	tableBytes         int
	tableEpoch         uint32
}
type replAction struct {
	request *querysession.Request
	message string
	quit    bool
}

func newREPLState(p *queryREPLParams) (*replState, error) {
	if p.PageSize < 1 || p.PageSize > 10000 {
		return nil, errors.New("page size must be between 1 and 10000")
	}
	if _, _, err := p.parseFromTo(); err != nil {
		return nil, err
	}
	if _, err := phlaremodel.ParseMetricSelector(p.Query); err != nil {
		return nil, fmt.Errorf("invalid selector: %w", err)
	}
	return &replState{from: p.From, to: p.To, selector: p.Query, pageSize: p.PageSize, queries: make(map[string]*replQuery), pending: make(map[string]*replPending)}, nil
}
func (s *replState) id(prefix string) string { s.sequence++; return prefix + strconv.Itoa(s.sequence) }
func splitREPLCommand(line string) (string, string) {
	line = strings.TrimSpace(line)
	i := strings.IndexFunc(line, unicode.IsSpace)
	if i < 0 {
		return line, ""
	}
	return line[:i], strings.TrimSpace(line[i:])
}
func (s *replState) command(line string) (replAction, error) {
	cmd, arg := splitREPLCommand(line)
	args := strings.Fields(arg)
	switch cmd {
	case "":
		return replAction{}, nil
	case "help":
		if arg != "" {
			return replAction{}, errors.New("usage: help")
		}
		return replAction{message: replHelp}, nil
	case "quit", "exit":
		if arg != "" {
			return replAction{}, errors.New("usage: quit")
		}
		return replAction{quit: true}, nil
	case "reset_string_table":
		if arg != "" {
			return replAction{}, errors.New("usage: reset_string_table")
		}
		if len(s.pending) >= 32 {
			return replAction{}, errors.New("too many in-flight requests")
		}
		req := &querysession.Request{ID: s.id("r"), Op: "reset_string_table"}
		s.pending[req.ID] = &replPending{reset: true}
		return replAction{request: req}, nil
	case "from", "to":
		if _, err := operations.ParseTime(arg); err != nil {
			return replAction{}, fmt.Errorf("invalid %s time: %w", cmd, err)
		}
		if cmd == "from" {
			s.from = arg
		} else {
			s.to = arg
		}
		return replAction{message: cmd + " " + arg}, nil
	case "selector":
		if _, err := phlaremodel.ParseMetricSelector(arg); err != nil {
			return replAction{}, fmt.Errorf("invalid selector: %w", err)
		}
		s.selector = arg
		return replAction{message: "selector " + arg}, nil
	case "page_size":
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 || n > 10000 {
			return replAction{}, errors.New("usage: page_size <1..10000>")
		}
		s.pageSize = n
		return replAction{message: fmt.Sprintf("page_size %d", n)}, nil
	case "show":
		if arg != "" {
			return replAction{}, errors.New("usage: show")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "from %s\nto %s\nselector %s\npage_size %d", s.from, s.to, s.selector, s.pageSize)
		ids := make([]string, 0, len(s.queries))
		for id := range s.queries {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			q := s.queries[id]
			fmt.Fprintf(&b, "\n[%s] %s: busy=%t query_complete=%t cancelled=%t has_next=%t disconnected=%t", q.id, q.kind, q.busy, q.complete, q.cancelled, q.next != "", q.disconnected)
		}
		return replAction{message: b.String()}, nil
	case "series", "label_names", "label_values":
		if cmd == "label_names" && arg != "" {
			return replAction{}, errors.New("usage: label_names")
		}
		if cmd == "label_values" && len(args) != 1 {
			return replAction{}, errors.New("usage: label_values <label-name>")
		}
		// Resolve relative times afresh for each query, not when defaults are set.
		p := queryParams{From: s.from, To: s.to}
		from, to, err := p.parseFromTo()
		if err != nil {
			return replAction{}, err
		}
		req := &querysession.Request{ID: s.id("q"), Op: "query", Start: from.UnixMilli(), End: to.UnixMilli(), Matchers: []string{s.selector}, PageSize: s.pageSize}
		switch cmd {
		case "series":
			req.Type = "Series"
			req.LabelNames = args
		case "label_names":
			req.Type = "LabelNames"
		case "label_values":
			req.Type = "LabelValues"
			req.Name = args[0]
		}
		if len(s.pending) >= 8 {
			return replAction{}, errors.New("too many in-flight requests; wait or cancel a query")
		}
		q := &replQuery{id: req.ID, kind: req.Type, busy: true}
		s.queries[q.id] = q
		s.pending[req.ID] = &replPending{query: q}
		s.last = q.id
		return replAction{request: req, message: fmt.Sprintf("[%s] %s started", req.ID, req.Type)}, nil
	case "next", "cancel":
		if len(args) > 1 {
			return replAction{}, fmt.Errorf("usage: %s [query-id]", cmd)
		}
		id := s.last
		if len(args) == 1 {
			id = args[0]
		}
		q := s.queries[id]
		if q == nil {
			return replAction{}, errors.New("unknown query; run a query first")
		}
		if q.disconnected {
			return replAction{}, errors.New("query belongs to a disconnected session; run it again")
		}
		if cmd == "cancel" {
			req := &querysession.Request{ID: s.id("c"), Op: "cancel", TargetID: q.id}
			s.pending[req.ID] = &replPending{query: q, cancel: true}
			return replAction{request: req}, nil
		}
		if q.busy {
			return replAction{}, errors.New("query already has an in-flight page")
		}
		if q.cancelled {
			return replAction{}, errors.New("query is cancelled; restart it")
		}
		if q.next == "" {
			return replAction{}, errors.New("query has no next page")
		}
		if len(s.pending) >= 8 {
			return replAction{}, errors.New("too many in-flight requests")
		}
		req := &querysession.Request{ID: s.id("p"), Op: "next", NextToken: q.next}
		q.busy = true
		s.pending[req.ID] = &replPending{query: q}
		return replAction{request: req}, nil
	default:
		return replAction{}, fmt.Errorf("unknown command %q; type help", cmd)
	}
}

// accept validates every dictionary delta and reference before exposing output.
func (s *replState) accept(f querysession.Frame) (string, error) {
	if f.Op == "table_reset" {
		if s.tableEpoch == ^uint32(0) || f.TableEpoch != s.tableEpoch+1 {
			return "", errors.New("protocol: invalid string-table reset epoch")
		}
		if f.ID != "" {
			pending := s.pending[f.ID]
			if pending == nil || !pending.reset || pending.resetSeen {
				return "", errors.New("protocol: unexpected string-table reset response")
			}
			pending.resetSeen = true
		}
		s.table = nil
		s.tableBytes = 0
		s.tableEpoch = f.TableEpoch
		id := f.ID
		if id == "" {
			id = "server"
		}
		return fmt.Sprintf("[%s] string table reset (epoch %d)", id, s.tableEpoch), nil
	}
	pending := s.pending[f.ID]
	if pending == nil {
		return "", fmt.Errorf("protocol: response for unknown request %q", f.ID)
	}
	q := pending.query
	switch f.Op {
	case "error":
		delete(s.pending, f.ID)
		if !pending.cancel && !pending.reset {
			q.busy = false
			q.next = ""
		}
		return fmt.Sprintf("[%s] error %q: %q (restart query if needed)", f.ID, f.Code, f.Message), nil
	case "complete":
		if pending.reset {
			if !pending.resetSeen {
				return "", errors.New("protocol: reset completion without reset barrier")
			}
			delete(s.pending, f.ID)
			return fmt.Sprintf("[%s] reset complete", f.ID), nil
		}
		if pending.cancel {
			delete(s.pending, f.ID)
			q.cancelled = true
			q.next = ""
			return fmt.Sprintf("[%s] cancellation acknowledged for %s", f.ID, q.id), nil
		}
		if !pending.page {
			return "", errors.New("protocol: completion without a page")
		}
		if f.QueryComplete != (q.next == "") {
			return "", errors.New("protocol: inconsistent query completion")
		}
		delete(s.pending, f.ID)
		q.busy = false
		q.complete = f.QueryComplete
		if q.complete {
			return fmt.Sprintf("[%s] query_complete (%s)", f.ID, q.id), nil
		}
		return fmt.Sprintf("[%s] page complete; next %s (results provisional)", f.ID, q.id), nil
	case "page":
		if pending.cancel || pending.reset || pending.page || f.Type != q.kind {
			return "", errors.New("protocol: unexpected page")
		}
		if f.TableEpoch != s.tableEpoch {
			return "", errors.New("protocol: page uses stale or unknown string-table epoch")
		}
		if len(f.Series) > 10000 || len(f.Refs) > 10000 || len(f.NextToken) > 1024 {
			return "", errors.New("protocol: oversized page")
		}
		if f.Strings == nil || f.Strings.Start != len(s.table) {
			return "", errors.New("protocol: invalid string-table delta")
		}
		n := 0
		for _, v := range f.Strings.Values {
			n += len(v) + 16
		}
		if len(f.Strings.Values) > 100000-len(s.table) || n > (32<<20)-s.tableBytes {
			return "", errors.New("protocol: string-table limit exceeded")
		}
		s.table = append(s.table, f.Strings.Values...)
		s.tableBytes += n
		resolve := func(id int) (string, error) {
			if id < 0 || id >= len(s.table) {
				return "", errors.New("protocol: invalid string reference")
			}
			return s.table[id], nil
		}
		var b strings.Builder
		count := 0
		if q.kind == "Series" {
			if len(f.Refs) > 0 {
				return "", errors.New("protocol: string refs in Series page")
			}
			count = len(f.Series)
			for _, set := range f.Series {
				fmt.Fprintf(&b, "[%s] {", f.ID)
				for i, pair := range set {
					name, err := resolve(pair[0])
					if err != nil {
						return "", err
					}
					value, err := resolve(pair[1])
					if err != nil {
						return "", err
					}
					if i > 0 {
						b.WriteString(", ")
					}
					// Quoting prevents terminal injection. Bound expansion too: a small
					// dictionary may be referenced thousands of times in one frame.
					name, value = replLabelName(name), strconv.Quote(value)
					if len(name)+len(value) > (16<<20)-b.Len()-256 {
						return "", errors.New("page exceeds 16 MiB rendered-output limit")
					}
					fmt.Fprintf(&b, "%s=%s", name, value)
				}
				b.WriteString("}\n")
			}
		} else {
			if len(f.Series) > 0 {
				return "", errors.New("protocol: Series in string page")
			}
			count = len(f.Refs)
			for _, ref := range f.Refs {
				v, err := resolve(ref)
				if err != nil {
					return "", err
				}
				v = strconv.Quote(v)
				if len(v) > (16<<20)-b.Len()-256 {
					return "", errors.New("page exceeds 16 MiB rendered-output limit")
				}
				fmt.Fprintf(&b, "[%s] %s\n", f.ID, v)
			}
		}
		q.next = f.NextToken
		pending.page = true
		fmt.Fprintf(&b, "[%s] %d elements", f.ID, count)
		return b.String(), nil
	default:
		return "", fmt.Errorf("protocol: unknown message operation %q", f.Op)
	}
}
func replLabelName(name string) string {
	// Legacy names can be displayed bare; quote all other names defensively.
	for i, r := range name {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return strconv.Quote(name)
		}
	}
	if name == "" {
		return `""`
	}
	return name
}

type replLine struct {
	line string
	err  error
	eof  bool
}
type replResponse struct {
	conn          *websocket.Conn
	frame         querysession.Frame
	err           error
	protocolError bool
}

type replDialResult struct {
	conn  *websocket.Conn
	err   error
	retry time.Duration
}

// A reconnect is a new session, not a retry of possibly delivered requests.
func (s *replState) connectionLost() {
	for _, q := range s.queries {
		q.busy = false
		q.next = ""
		q.disconnected = true
	}
	clear(s.pending)
	s.table = nil
	s.tableBytes = 0
	s.tableEpoch = 0
}

func needsREPLConnection(line string) bool {
	cmd, _ := splitREPLCommand(line)
	switch cmd {
	case "series", "label_names", "label_values", "next", "cancel", "reset_string_table":
		return true
	}
	return false
}

func readREPLResponses(ctx context.Context, conn *websocket.Conn, responses chan<- replResponse) {
	for {
		kind, b, err := conn.ReadMessage()
		var f querysession.Frame
		protocolError := false
		if err == nil {
			if kind != websocket.TextMessage {
				err = errors.New("protocol: expected JSON text frame")
			} else {
				err = f.UnmarshalJSON(b)
			}
			protocolError = err != nil
		} else {
			var closeErr *websocket.CloseError
			if errors.Is(err, websocket.ErrReadLimit) {
				protocolError = true
			}
			if errors.As(err, &closeErr) {
				switch closeErr.Code {
				case websocket.CloseProtocolError, websocket.CloseUnsupportedData, websocket.CloseInvalidFramePayloadData, websocket.ClosePolicyViolation, websocket.CloseMessageTooBig, websocket.CloseMandatoryExtension:
					protocolError = true
				}
			}
		}
		select {
		case responses <- replResponse{conn: conn, frame: f, err: err, protocolError: protocolError}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func reconnectQuerySession(ctx context.Context, client *phlareClient, results chan<- replDialResult) {
	delay := 250 * time.Millisecond
	// Also delay the first attempt so an endpoint that repeatedly upgrades and
	// immediately closes cannot cause a tight reconnect loop.
	initial := time.NewTimer(delay)
	select {
	case <-ctx.Done():
		initial.Stop()
		return
	case <-initial.C:
	}
	for {
		if ctx.Err() != nil {
			return
		}
		conn, err := client.dialQuerySession(ctx)
		select {
		case results <- replDialResult{conn: conn, err: err, retry: delay}:
		case <-ctx.Done():
			if conn != nil {
				_ = conn.Close()
			}
			return
		}
		if err == nil {
			return
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, 10*time.Second)
	}
}

func runQueryREPL(ctx context.Context, p *queryREPLParams, input io.Reader, out io.Writer, prompt bool) error {
	state, err := newREPLState(p)
	if err != nil {
		return err
	}
	conn, err := p.phlareClient.dialQuerySession(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The wrapper owns stdin. Tests supplying pipes should close them on return.
	lines := make(chan replLine)
	go func() {
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 4096), 64<<10)
		for scanner.Scan() {
			select {
			case lines <- replLine{line: scanner.Text()}:
			case <-ctx.Done():
				return
			}
		}
		select {
		case lines <- replLine{err: scanner.Err(), eof: true}:
		case <-ctx.Done():
		}
	}()
	responses := make(chan replResponse)
	go readREPLResponses(ctx, conn, responses)
	print := func(text string) error {
		if text == "" {
			return nil
		}
		_, err := fmt.Fprintln(out, text)
		return err
	}
	dials := make(chan replDialResult)
	var lostWorkErr error
	disconnect := func(reason error) error {
		if conn == nil {
			return nil
		}
		_ = conn.Close()
		conn = nil
		if len(state.pending) > 0 {
			lostWorkErr = fmt.Errorf("connection lost with unfinished requests (not replayed): %w", reason)
		}
		state.connectionLost()
		if err := print(fmt.Sprintf("Disconnected: %q. Reconnecting; old queries and cursors are invalid. Unfinished requests will not be replayed.", reason.Error())); err != nil {
			return err
		}
		go reconnectQuerySession(ctx, p.phlareClient, dials)
		return nil
	}
	if prompt {
		if err = print("Connected. Type help for commands."); err != nil {
			return err
		}
		if _, err = fmt.Fprint(out, "profilecli> "); err != nil {
			return err
		}
	}
	for {
		if lines == nil && len(state.pending) == 0 {
			if prompt {
				return nil
			}
			return lostWorkErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case dial := <-dials:
			if dial.err != nil {
				if err = print(fmt.Sprintf("Reconnect failed: %q; retrying in %s. Local commands and quit still work.", dial.err.Error(), dial.retry)); err != nil {
					return err
				}
				continue
			}
			conn = dial.conn
			go readREPLResponses(ctx, conn, responses)
			if err = print("Reconnected. Query defaults preserved; run unfinished queries again."); err != nil {
				return err
			}
			if prompt {
				if _, err = fmt.Fprint(out, "profilecli> "); err != nil {
					return err
				}
			}
		case in := <-lines:
			if in.err != nil {
				return in.err
			}
			if in.eof {
				lines = nil
				continue
			}
			if conn == nil && needsREPLConnection(in.line) {
				if err = print("Reconnecting; retry the command after reconnection."); err != nil {
					return err
				}
				if prompt {
					if _, err = fmt.Fprint(out, "profilecli> "); err != nil {
						return err
					}
				}
				continue
			}
			action, cmdErr := state.command(in.line)
			if cmdErr != nil {
				if err = print("error: " + cmdErr.Error()); err != nil {
					return err
				}
			} else {
				if action.quit {
					return nil
				}
				if action.request != nil {
					b, marshalErr := action.request.MarshalJSON()
					if marshalErr != nil {
						return marshalErr
					}
					if len(b) > 64<<10 {
						return errors.New("request exceeds 64 KiB WebSocket limit")
					}
					sendErr := conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
					if sendErr == nil {
						sendErr = conn.WriteMessage(websocket.TextMessage, b)
					}
					if sendErr != nil {
						if err = disconnect(sendErr); err != nil {
							return err
						}
						continue
					}
				}
				if err = print(action.message); err != nil {
					return err
				}
			}
			if prompt {
				if _, err = fmt.Fprint(out, "profilecli> "); err != nil {
					return err
				}
			}
		case response := <-responses:
			// A write failure can leave an old reader's final event queued while
			// a new connection is established. Never apply it to the new table.
			if response.conn != conn {
				continue
			}
			if response.err != nil {
				if response.protocolError {
					return response.err
				}
				if err = disconnect(response.err); err != nil {
					return err
				}
				continue
			}
			text, protocolErr := state.accept(response.frame)
			if protocolErr != nil {
				return protocolErr
			}
			if err = print(text); err != nil {
				return err
			}
		}
	}
}
