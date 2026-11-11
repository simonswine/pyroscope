// Package querysession implements multiplexed metadata queries with frontend-owned
// pagination sessions. It deliberately does not expose backend chunk boundaries.
package querysession

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	"github.com/grafana/pyroscope/v2/pkg/querybackend/metadatastream"
	"github.com/grafana/pyroscope/v2/pkg/util"
)

var (
	ErrLimit  = errors.New("metadata resource limit exceeded")
	ErrCursor = errors.New("pagination token unavailable; restart query")
)

type Stream func(context.Context, Request, func(metadatastream.Element) error) error

type Config struct {
	TTL                  time.Duration `yaml:"session_ttl"`
	MaxFrameBytes        int           `yaml:"max_frame_bytes"`
	MaxTenantBytes       int64         `yaml:"max_tenant_bytes"`
	MaxProcessBytes      int64         `yaml:"max_process_bytes"`
	MaxDictionaryBytes   int           `yaml:"max_dictionary_bytes"`
	MaxDictionaryEntries int           `yaml:"max_dictionary_entries"`
	MaxActiveRequests    int           `yaml:"max_active_requests"`
	MaxTenantSessions    int           `yaml:"max_tenant_sessions"`
	MaxSessions          int           `yaml:"max_sessions"`
	MaxConnections       int           `yaml:"max_connections"`
}

func DefaultConfig() Config {
	return Config{TTL: 5 * time.Minute, MaxFrameBytes: 4 << 20, MaxTenantBytes: 64 << 20, MaxProcessBytes: 256 << 20, MaxDictionaryBytes: 16 << 20, MaxDictionaryEntries: 100000, MaxActiveRequests: 8, MaxTenantSessions: 16, MaxSessions: 256, MaxConnections: 256}
}
func (c Config) Validate() error {
	if c.TTL <= 0 || c.MaxFrameBytes < 4096 || c.MaxTenantBytes <= 0 || c.MaxProcessBytes < c.MaxTenantBytes || c.MaxDictionaryBytes <= 0 || c.MaxDictionaryEntries <= 0 || c.MaxActiveRequests <= 0 || c.MaxTenantSessions <= 0 || c.MaxSessions < c.MaxTenantSessions || c.MaxConnections <= 0 {
		return errors.New("invalid metadata WebSocket limits")
	}
	return nil
}

type Handler struct {
	config         Config
	stream         Stream
	mu             sync.Mutex
	cursors        map[string]*cursor
	sessions       map[*session]struct{}
	tenantBytes    map[string]int64
	tenantSessions map[string]int
	bytes          int64
	connections    int
	closed         bool
	clients        map[*connection]struct{}
}

func New(config Config, stream Stream) (*Handler, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Handler{config: config, stream: stream, cursors: make(map[string]*cursor), sessions: make(map[*session]struct{}), tenantBytes: make(map[string]int64), tenantSessions: make(map[string]int), clients: make(map[*connection]struct{})}, nil
}

// Close stops admission, closes hijacked HTTP connections, and releases sessions.
func (h *Handler) Close() {
	h.mu.Lock()
	h.closed = true
	clients := make([]*connection, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	sessions := make([]*session, 0, len(h.sessions))
	for s := range h.sessions {
		sessions = append(sessions, s)
	}
	h.mu.Unlock()
	for _, c := range clients {
		_ = c.ws.Close()
	}
	for _, s := range sessions {
		h.expire(s)
	}
}

type result struct {
	element metadatastream.Element
	err     error
	done    bool
}
type page struct {
	elements  []metadatastream.Element
	next      string
	exhausted bool
}
type cursor struct {
	session *session
	page    *page
}
type session struct {
	mu           sync.Mutex // serializes cursor generation and replays
	owner        *Handler
	tenant, kind string
	size         int
	ctx          context.Context
	cancel       context.CancelFunc
	results      chan result
	seen         map[string]struct{}
	pending      metadatastream.Element
	tokens       []string
	charged      int64 // protected by owner.mu
	terminal     error
	expired      bool // protected by owner.mu
}

func token() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("generate metadata token: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (h *Handler) charge(tenant string, n int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.chargeLocked(tenant, n)
}
func (h *Handler) chargeLocked(tenant string, n int64) error {
	ids := strings.Split(tenant, "|")
	if n > 0 {
		if n > h.config.MaxProcessBytes-h.bytes {
			return ErrLimit
		}
		for _, id := range ids {
			if n > h.config.MaxTenantBytes-h.tenantBytes[id] {
				return ErrLimit
			}
		}
	}
	h.bytes += n
	for _, id := range ids {
		h.tenantBytes[id] += n
		if h.tenantBytes[id] == 0 {
			delete(h.tenantBytes, id)
		}
	}
	return nil
}
func (s *session) charge(n int64) error {
	h := s.owner
	h.mu.Lock()
	defer h.mu.Unlock()
	if s.expired {
		return ErrCursor
	}
	if err := h.chargeLocked(s.tenant, n); err != nil {
		return err
	}
	s.charged += n
	return nil
}
func (h *Handler) expire(s *session) {
	h.mu.Lock()
	if s.expired {
		h.mu.Unlock()
		return
	}
	s.expired = true
	s.cancel()
	for _, t := range s.tokens {
		delete(h.cursors, t)
	}
	s.tokens = nil
	delete(h.sessions, s)
	for _, id := range strings.Split(s.tenant, "|") {
		h.tenantSessions[id]--
		if h.tenantSessions[id] == 0 {
			delete(h.tenantSessions, id)
		}
	}
	_ = h.chargeLocked(s.tenant, -s.charged)
	h.mu.Unlock()
	// Never take session.mu while holding owner.mu: page generation takes them
	// in the opposite order. Cancellation unblocks a pending generation first.
	s.mu.Lock()
	s.seen = nil
	s.pending = nil
	s.mu.Unlock()
}
func (h *Handler) start(ctx context.Context, tenant string, r Request) (*cursor, error) {
	if r.PageSize == 0 {
		r.PageSize = 10000
	}
	if r.PageSize < 1 || r.PageSize > 10000 {
		return nil, errors.New("page_size must be between 1 and 10000")
	}
	switch r.Type {
	case "Series", "LabelNames", "LabelValues":
	default:
		return nil, errors.New("unsupported metadata query type")
	}
	sctx, cancel := context.WithTimeout(ctx, h.config.TTL)
	s := &session{owner: h, tenant: tenant, kind: r.Type, size: r.PageSize, ctx: sctx, cancel: cancel, results: make(chan result), seen: make(map[string]struct{})}
	h.mu.Lock()
	atLimit := h.closed || len(h.sessions) >= h.config.MaxSessions
	for _, id := range strings.Split(tenant, "|") {
		atLimit = atLimit || h.tenantSessions[id] >= h.config.MaxTenantSessions
	}
	if atLimit {
		h.mu.Unlock()
		cancel()
		return nil, ErrLimit
	}
	// Reserve room for one decoded backend chunk and the bounded input envelope.
	reserve := int64(metadatastream.DefaultChunkBytes + (128 << 10))
	if err := h.chargeLocked(tenant, reserve); err != nil {
		h.mu.Unlock()
		cancel()
		return nil, err
	}
	s.charged = reserve
	h.sessions[s] = struct{}{}
	for _, id := range strings.Split(tenant, "|") {
		h.tenantSessions[id]++
	}
	c := &cursor{session: s}
	t := token()
	s.tokens = append(s.tokens, t)
	h.cursors[t] = c
	h.mu.Unlock()
	// Expiry is independent of WebSocket activity and releases unconsumed streams.
	time.AfterFunc(h.config.TTL, func() { h.expire(s) })
	go func() {
		err := util.RecoverPanic(func() error {
			return h.stream(sctx, r, func(e metadatastream.Element) error {
				select {
				case s.results <- result{element: e}:
					return nil
				case <-sctx.Done():
					return sctx.Err()
				}
			})
		})()
		select {
		case s.results <- result{err: err, done: true}:
		case <-sctx.Done():
		}
	}()
	return c, nil
}
func (h *Handler) lookup(tenant, t string) (*cursor, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.cursors[t]
	if c == nil || c.session.tenant != tenant || c.session.expired {
		return nil, ErrCursor
	}
	return c, nil
}

// normalize returns exact, collision-free identity and immutable canonical labels.
func normalize(kind string, e metadatastream.Element) (metadatastream.Element, string, error) {
	out := make(metadatastream.Element, len(e))
	for i, p := range e {
		if p == nil {
			return nil, "", errors.New("nil metadata label")
		}
		out[i] = &typesv1.LabelPair{Name: p.Name, Value: p.Value}
	}
	if kind == "Series" {
		slices.SortFunc(out, func(a, b *typesv1.LabelPair) int {
			if n := strings.Compare(a.Name, b.Name); n != 0 {
				return n
			}
			return strings.Compare(a.Value, b.Value)
		})
	} else if len(out) != 1 {
		return nil, "", errors.New("invalid string metadata element")
	}
	// JSON array encoding is unambiguous even when strings contain separators.
	b, err := json.Marshal(out)
	return out, string(b), err
}
func elementBytes(e metadatastream.Element) int64 {
	n := int64(128 + len(e)*64)
	for _, p := range e {
		n += int64(2 * (len(p.Name) + len(p.Value)))
	}
	return n
}

// Upper bound for page wire cost without knowing the connection dictionary:
// every string is new and every reference occupies 20 decimal digits. Include
// JSON escaping by using the same encoder as the wire representation.
func elementWireBytes(kind string, e metadatastream.Element) int {
	n := 64
	for _, p := range e {
		a, _ := json.Marshal(p.Name)
		b, _ := json.Marshal(p.Value)
		n += len(a) + len(b) + 48
	}
	return n
}
func compare(kind string, a, b metadatastream.Element) int {
	if kind != "Series" {
		if kind == "LabelNames" {
			return strings.Compare(a[0].Name, b[0].Name)
		}
		return strings.Compare(a[0].Value, b[0].Value)
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if n := strings.Compare(a[i].Name, b[i].Name); n != 0 {
			return n
		}
		if n := strings.Compare(a[i].Value, b[i].Value); n != 0 {
			return n
		}
	}
	return len(a) - len(b)
}
func (c *cursor) get(ctx context.Context, size int) (*page, error) {
	s := c.session
	s.mu.Lock()
	defer s.mu.Unlock()
	if size != 0 && size != s.size {
		return nil, errors.New("page_size is fixed for the query")
	}
	s.owner.mu.Lock()
	expired := s.expired
	s.owner.mu.Unlock()
	if expired {
		return nil, ErrCursor
	}
	if c.page != nil {
		return c.page, nil
	}
	if s.terminal != nil {
		return nil, s.terminal
	}
	p := &page{elements: make([]metadatastream.Element, 0, s.size)}
	wireBytes := 1024 // envelope, bounded request ID, cursor and dictionary start
	for len(p.elements) < s.size {
		if err := ctx.Err(); err != nil {
			s.cancel()
			s.terminal = err
			return nil, err
		}
		e := s.pending
		s.pending = nil
		if e == nil {
			var r result
			select {
			case r = <-s.results:
			case <-ctx.Done():
				s.cancel()
				s.terminal = ctx.Err()
				return nil, s.terminal
			case <-s.ctx.Done():
				s.terminal = s.ctx.Err()
				return nil, s.terminal
			}
			if r.done {
				if r.err != nil {
					s.terminal = r.err
					s.cancel()
					return nil, r.err
				}
				p.exhausted = true
				s.cancel()
				break
			}
			var key string
			var err error
			e, key, err = normalize(s.kind, r.element)
			if err != nil {
				s.terminal = err
				s.cancel()
				return nil, err
			}
			if _, exists := s.seen[key]; exists {
				continue
			}
			// Charge both seen-key storage and cached logical result storage. This
			// deliberately overaccounts strings that happen to share backing storage.
			if err = s.charge(elementBytes(e) + int64(len(key)+64)); err != nil {
				s.terminal = err
				s.cancel()
				return nil, err
			}
			s.seen[key] = struct{}{}
		}
		n := elementWireBytes(s.kind, e)
		if wireBytes+n > s.owner.config.MaxFrameBytes {
			if len(p.elements) == 0 {
				s.terminal = ErrLimit
				s.cancel()
				return nil, ErrLimit
			}
			s.pending = e
			break
		}
		wireBytes += n
		p.elements = append(p.elements, e)
	}
	slices.SortFunc(p.elements, func(a, b metadatastream.Element) int { return compare(s.kind, a, b) })
	if !p.exhausted {
		if err := s.charge(256); err != nil {
			s.terminal = err
			s.cancel()
			return nil, err
		}
		h := s.owner
		h.mu.Lock()
		if s.expired {
			h.mu.Unlock()
			return nil, ErrCursor
		}
		p.next = token()
		s.tokens = append(s.tokens, p.next)
		h.cursors[p.next] = &cursor{session: s}
		h.mu.Unlock()
	}
	c.page = p
	return p, nil
}
