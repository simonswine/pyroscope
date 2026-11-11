package querysession

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/grafana/dskit/tenant"
)

// Internal aliases keep the transport implementation concise.
type frame = Frame
type stringDelta = StringDelta

type connection struct {
	handler         *Handler
	ws              *websocket.Conn
	tenant          string
	writeMu         sync.Mutex
	strings         map[string]int
	dictionaryBytes int64
	tableEpoch      uint32
	mu              sync.Mutex
	active          map[string]context.CancelFunc
	sessions        map[string]*session
}

// ServeHTTP expects the ordinary read-path authentication middleware to have
// established tenant identity before upgrade. Only same-origin browsers can
// upgrade; non-browser clients may omit Origin. No compression is negotiated.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Query().Get("accept") != "application/json" {
		http.Error(w, "accept=application/json is required", http.StatusNotAcceptable)
		return
	}
	ids, err := tenant.TenantIDs(r.Context())
	if err != nil {
		http.Error(w, "tenant required", http.StatusUnauthorized)
		return
	}
	tenantID := tenant.JoinTenantIDs(ids)
	h.mu.Lock()
	if h.closed || h.connections >= h.config.MaxConnections {
		h.mu.Unlock()
		http.Error(w, "connection limit exceeded", http.StatusTooManyRequests)
		return
	}
	h.connections++
	h.mu.Unlock()
	defer func() { h.mu.Lock(); h.connections--; h.mu.Unlock() }()
	ws, err := (&websocket.Upgrader{EnableCompression: false, ReadBufferSize: 4096, WriteBufferSize: 4096}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c := &connection{handler: h, ws: ws, tenant: tenantID, strings: make(map[string]int), active: make(map[string]context.CancelFunc), sessions: make(map[string]*session)}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.clients, c); h.mu.Unlock() }()
	var workers sync.WaitGroup
	defer func() {
		cancel()
		c.mu.Lock()
		for _, stop := range c.active {
			stop()
		}
		for _, s := range c.sessions {
			s.cancel()
		}
		c.mu.Unlock()
		workers.Wait()
		_ = h.charge(tenantID, -c.dictionaryBytes)
	}()
	ws.SetReadLimit(64 << 10)
	for {
		_ = ws.SetReadDeadline(time.Now().Add(h.config.TTL))
		kind, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		if kind != websocket.TextMessage {
			return
		}
		var req Request
		if err = req.UnmarshalJSON(data); err != nil {
			return
		}
		if len(req.ID) == 0 || len(req.ID) > 128 {
			return
		}
		if req.Op == "reset_string_table" {
			c.mu.Lock()
			duplicate := c.active[req.ID] != nil
			c.mu.Unlock()
			if duplicate {
				return
			}
			c.writeMu.Lock()
			err = c.resetTableLocked(req.ID)
			if err == nil {
				err = c.writeLocked(frame{ID: req.ID, Op: "complete"})
			}
			c.writeMu.Unlock()
			if err != nil {
				return
			}
			continue
		}
		if req.Op == "cancel" {
			c.mu.Lock()
			stop := c.active[req.TargetID]
			s := c.sessions[req.TargetID]
			duplicate := c.active[req.ID] != nil
			c.mu.Unlock()
			if duplicate {
				return
			}
			if stop != nil {
				stop()
			}
			if s != nil {
				s.cancel()
			}
			if err = c.write(frame{ID: req.ID, Op: "complete"}); err != nil {
				return
			}
			continue
		}
		c.mu.Lock()
		h.mu.Lock()
		for id, s := range c.sessions {
			if s.expired {
				delete(c.sessions, id)
			}
		}
		h.mu.Unlock()
		if req.Op == "query" && c.sessions[req.ID] != nil {
			c.mu.Unlock()
			return
		}
		if _, ok := c.active[req.ID]; ok {
			c.mu.Unlock()
			return
		}
		if len(c.active) >= h.config.MaxActiveRequests {
			c.mu.Unlock()
			if c.writeError(req.ID, ErrLimit) != nil {
				return
			}
			continue
		}
		taskCtx, stop := context.WithCancel(ctx)
		c.active[req.ID] = stop
		c.mu.Unlock()
		workers.Add(1)
		go func(req Request) {
			defer workers.Done()
			defer stop()
			defer func() { c.mu.Lock(); delete(c.active, req.ID); c.mu.Unlock() }()
			var cursor *cursor
			var err error
			switch req.Op {
			case "query":
				// Session execution outlives this page task, but not the connection.
				cursor, err = h.start(ctx, tenantID, req)
				if err == nil {
					c.mu.Lock()
					c.sessions[req.ID] = cursor.session
					c.mu.Unlock()
				}
			case "next":
				cursor, err = h.lookup(tenantID, req.NextToken)
			default:
				err = errors.New("unsupported operation")
			}
			if err == nil {
				var p *page
				p, err = cursor.get(taskCtx, req.PageSize)
				if err == nil {
					err = c.writePage(req.ID, cursor.session.kind, p)
				}
			}
			if err != nil {
				if c.writeError(req.ID, err) != nil {
					cancel()
					_ = ws.Close()
				}
			}
		}(req)
	}
}
func (c *connection) write(f frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writeLocked(f)
}
func (c *connection) writeLocked(f frame) error {
	b, err := f.MarshalJSON()
	if err != nil {
		return err
	}
	if len(b) > c.handler.config.MaxFrameBytes {
		return ErrLimit
	}
	_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.ws.WriteMessage(websocket.TextMessage, b)
}
func (c *connection) writeError(id string, err error) error {
	code, message := "query_failed", "metadata query failed; restart query"
	switch {
	case errors.Is(err, ErrLimit):
		code, message = "resource_exhausted", err.Error()
	case errors.Is(err, ErrCursor):
		code, message = "cursor_unavailable", err.Error()
	case errors.Is(err, context.Canceled):
		code, message = "cancelled", "query cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		code, message = "deadline_exceeded", "query expired"
	}
	return c.write(frame{ID: id, Op: "error", Code: code, Message: message})
}

// resetTableLocked is an ordered barrier: old frames have already been written,
// and all later pages are encoded using fresh IDs in the new epoch.
func (c *connection) resetTableLocked(id string) error {
	if c.tableEpoch == ^uint32(0) {
		return ErrLimit
	}
	epoch := c.tableEpoch + 1
	if err := c.writeLocked(frame{ID: id, Op: "table_reset", TableEpoch: epoch}); err != nil {
		return err
	}
	_ = c.handler.charge(c.tenant, -c.dictionaryBytes)
	c.strings = make(map[string]int)
	c.dictionaryBytes = 0
	c.tableEpoch = epoch
	return nil
}

// buildPage assigns IDs transactionally, without changing the connection table.
func buildPage(id, kind string, p *page, table map[string]int, epoch uint32) (frame, map[string]int, int64) {
	delta := &stringDelta{Start: len(table), Values: make([]string, 0)}
	added := make(map[string]int)
	var addedBytes int64
	intern := func(s string) int {
		if id, ok := table[s]; ok {
			return id
		}
		if id, ok := added[s]; ok {
			return id
		}
		id := len(table) + len(added)
		added[s] = id
		delta.Values = append(delta.Values, s)
		addedBytes += int64(len(s) + 64)
		return id
	}
	f := frame{ID: id, Op: "page", Type: kind, Strings: delta, NextToken: p.next, TableEpoch: epoch}
	for _, e := range p.elements {
		if kind == "Series" {
			set := make([][2]int, 0, len(e))
			for _, pair := range e {
				set = append(set, [2]int{intern(pair.Name), intern(pair.Value)})
			}
			f.Series = append(f.Series, set)
		} else {
			v := e[0].Value
			if kind == "LabelNames" {
				v = e[0].Name
			}
			f.Refs = append(f.Refs, intern(v))
		}
	}
	return f, added, addedBytes
}

func (c *connection) writePage(id, kind string, p *page) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	f, added, addedBytes := buildPage(id, kind, p, c.strings, c.tableEpoch)
	if len(c.strings)+len(added) > c.handler.config.MaxDictionaryEntries || c.dictionaryBytes+addedBytes > int64(c.handler.config.MaxDictionaryBytes) {
		// An unsolicited reset is safe even with multiplexed requests: the same
		// writer emits the reset barrier and all subsequent page frames.
		if len(c.strings) == 0 || c.tableEpoch == ^uint32(0) {
			return ErrLimit
		}
		f, added, addedBytes = buildPage(id, kind, p, nil, c.tableEpoch+1)
		if len(added) > c.handler.config.MaxDictionaryEntries || addedBytes > int64(c.handler.config.MaxDictionaryBytes) {
			return ErrLimit
		}
		if err := c.resetTableLocked(""); err != nil {
			return err
		}
	}
	b, err := f.MarshalJSON()
	if err != nil {
		return err
	}
	if len(b) > c.handler.config.MaxFrameBytes {
		return ErrLimit
	}
	if err = c.handler.charge(c.tenant, addedBytes); err != nil {
		return err
	}
	_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err = c.ws.WriteMessage(websocket.TextMessage, b); err != nil {
		_ = c.handler.charge(c.tenant, -addedBytes)
		return err
	}
	for s, id := range added {
		c.strings[s] = id
	}
	c.dictionaryBytes += addedBytes
	return c.writeLocked(frame{ID: id, Op: "complete", QueryComplete: p.exhausted})
}
