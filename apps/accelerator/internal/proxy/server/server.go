package server

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/taeven/nance/accelerator/internal/controlplane/store"
	"github.com/taeven/nance/accelerator/internal/proxy/auth"
	"github.com/taeven/nance/accelerator/internal/proxy/cache"
	"github.com/taeven/nance/accelerator/internal/proxy/cachedcursor"
	"github.com/taeven/nance/accelerator/internal/proxy/cachestats"
	proxyconfig "github.com/taeven/nance/accelerator/internal/proxy/config"
	"github.com/taeven/nance/accelerator/internal/proxy/cursor"
	"github.com/taeven/nance/accelerator/internal/proxy/handler"
	"github.com/taeven/nance/accelerator/internal/proxy/policy"
	"github.com/taeven/nance/accelerator/internal/proxy/pool"
	"github.com/taeven/nance/accelerator/internal/proxy/ratelimit"
	"github.com/taeven/nance/accelerator/internal/proxy/wire"
	"github.com/taeven/nance/accelerator/internal/telemetry"

	"go.mongodb.org/mongo-driver/bson"
)

// Server accepts MongoDB wire protocol connections and proxies commands.
type Server struct {
	cfg           *proxyconfig.Config
	log           *slog.Logger
	auth          *auth.Validator
	pool          *pool.Manager
	cursors       *cursor.Registry
	cachedCursors *cachedcursor.Store
	cache         *cache.Coordinator
	policies      *policy.Engine
	limiter       *ratelimit.Limiter
	handler       *handler.Handler

	ln net.Listener

	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	tenantN map[string]int // open connections per tenant
	connSeq atomic.Uint64
	connID  atomic.Int32
	reqID   atomic.Int32

	wg       sync.WaitGroup
	closed   atomic.Bool
	draining atomic.Bool
}

// Options for constructing the proxy server (Phase 2/3 extras).
type Options struct {
	Cache         *cache.Coordinator
	Policies      *policy.Engine
	CachedCursors *cachedcursor.Store
	CacheStats    *cachestats.Tracker
	Limiter       *ratelimit.Limiter
	// Store is used for connection settings (auto-invalidate on write).
	Store store.Store
}

func New(cfg *proxyconfig.Config, log *slog.Logger, validator *auth.Validator, pools *pool.Manager, cursors *cursor.Registry, opts ...Options) *Server {
	if log == nil {
		log = slog.Default()
	}
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	s := &Server{
		cfg:           cfg,
		log:           log,
		auth:          validator,
		pool:          pools,
		cursors:       cursors,
		cachedCursors: o.CachedCursors,
		cache:         o.Cache,
		policies:      o.Policies,
		limiter:       o.Limiter,
		conns:         make(map[net.Conn]struct{}),
		tenantN:       make(map[string]int),
	}
	s.handler = handler.New(handler.Deps{
		Auth:          validator,
		Pool:          pools,
		Cursors:       cursors,
		CachedCursors: o.CachedCursors,
		CacheStats:    o.CacheStats,
		Cache:         o.Cache,
		Policies:      o.Policies,
		Store:         o.Store,
		Limiter:       o.Limiter,
		Log:           log,
		ConnID:        &s.connID,
		DefaultBatch:  101,
	})
	s.reqID.Store(1)
	return s
}

// ListenAndServe starts accepting connections until ctx is cancelled or Close is called.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.ListenAddr, err)
	}
	s.ln = ln
	s.log.Info("proxy listening", "addr", s.cfg.ListenAddr)

	// Cursor prune loop (backend + emulated cache cursors)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(1 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n := s.cursors.PruneIdle()
				if n > 0 {
					s.log.Debug("pruned idle cursors", "count", n)
				}
				if s.cachedCursors != nil {
					cn := s.cachedCursors.PruneIdle()
					telemetry.ProxyCachedCursorsActive.Set(float64(s.cachedCursors.Count()))
					if cn > 0 {
						s.log.Debug("pruned idle cached cursors", "count", cn)
					}
				}
				if s.limiter != nil {
					s.limiter.Prune(30 * time.Minute)
				}
			}
		}
	}()

	// Accept loop
	errCh := make(chan error, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				if s.closed.Load() || errors.Is(err, net.ErrClosed) {
					errCh <- nil
					return
				}
				errCh <- err
				return
			}
			if s.draining.Load() {
				_ = conn.Close()
				continue
			}
			s.trackConn(conn, true)
			telemetry.ProxyConnectionsActive.Inc()
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer s.trackConn(conn, false)
				defer telemetry.ProxyConnectionsActive.Dec()
				s.serveConn(ctx, conn)
			}()
		}
	}()

	select {
	case <-ctx.Done():
		_ = s.Close()
		s.wg.Wait()
		return nil
	case err := <-errCh:
		s.wg.Wait()
		return err
	}
}

// Close stops accepting and closes all client connections.
func (s *Server) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	s.draining.Store(true)
	var err error
	if s.ln != nil {
		err = s.ln.Close()
	}
	// Brief drain window for in-flight connections before hard close
	drain := s.cfg.DrainTimeout
	if drain <= 0 {
		drain = 5 * time.Second
	}
	deadline := time.Now().Add(drain)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := len(s.conns)
		s.mu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.mu.Lock()
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	return err
}

func (s *Server) trackConn(c net.Conn, add bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if add {
		s.conns[c] = struct{}{}
	} else {
		delete(s.conns, c)
	}
}

func (s *Server) serveConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	connKey := fmt.Sprintf("%s-%d", remote, s.connSeq.Add(1))
	s.log.Debug("client connected", "remote", remote, "conn", connKey)

	cs := &handler.ConnState{
		Key:         connKey,
		RemoteAddr:  remote,
		AllowUnauth: s.cfg.AllowUnauthenticated,
	}

	defer func() {
		s.cursors.CleanupConn(connKey)
		if s.cachedCursors != nil {
			s.cachedCursors.CleanupConn(connKey)
		}
		if cs.Tenant != nil {
			s.releaseTenantConn(cs.Tenant.TenantID)
		}
		s.log.Debug("client disconnected", "remote", remote, "conn", connKey)
	}()

	for {
		if err := conn.SetReadDeadline(time.Now().Add(10 * time.Minute)); err != nil {
			return
		}

		hdr, err := wire.ReadHeader(conn)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				s.log.Debug("read header", "error", err, "remote", remote)
			}
			return
		}

		switch hdr.OpCode {
		case wire.OpMsg:
			msg, err := wire.ReadMsg(conn, hdr)
			if err != nil {
				s.log.Warn("read OP_MSG", "error", err, "remote", remote)
				return
			}
			if err := s.handleMsg(ctx, conn, cs, msg); err != nil {
				s.log.Debug("handle msg", "error", err, "remote", remote)
				return
			}
		case wire.OpQuery:
			// Legacy OP_QUERY (some tools/drivers still send for isMaster)
			if err := s.handleLegacyQuery(ctx, conn, cs, hdr); err != nil {
				return
			}
		default:
			s.log.Warn("unsupported opcode", "opcode", hdr.OpCode, "remote", remote)
			// Drain body to keep stream aligned
			if bl, err := hdr.BodyLength(); err == nil && bl > 0 {
				_, _ = io.CopyN(io.Discard, conn, int64(bl))
			}
			_ = s.writeErrorMsg(conn, hdr.RequestID, 2, "BadValue", fmt.Sprintf("unsupported opcode %d; use modern driver with OP_MSG", hdr.OpCode))
		}
	}
}

func (s *Server) handleMsg(ctx context.Context, conn net.Conn, cs *handler.ConnState, msg *wire.Msg) error {
	// Per-tenant connection limit check on first successful auth is handled below
	replyDoc, err := s.handler.Handle(ctx, cs, msg)
	if err != nil {
		replyDoc = handlerErrorDoc(err)
	}

	// After auth, enforce per-tenant connection limits
	if cs.Authed && cs.Tenant != nil {
		if !csHasTenantCounted(cs) {
			if !s.tryAcquireTenantConn(cs.Tenant.TenantID) {
				replyDoc = auth.TooManyConnectionsDoc()
				// Force disconnect after reply
				defer conn.Close()
			} else {
				markTenantCounted(cs)
			}
		}
	}

	body, err := encodeReply(replyDoc)
	if err != nil {
		body, _ = wire.EncodeBody(map[string]any{"ok": 0, "errmsg": err.Error(), "code": 1})
	}

	respID := s.reqID.Add(1)
	if err := conn.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return err
	}
	return wire.WriteMsg(conn, respID, msg.Header.RequestID, body)
}

// handleLegacyQuery supports minimal OP_QUERY for isMaster/hello from very old clients.
func (s *Server) handleLegacyQuery(ctx context.Context, conn net.Conn, cs *handler.ConnState, hdr wire.Header) error {
	bodyLen, err := hdr.BodyLength()
	if err != nil {
		return err
	}
	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(conn, body); err != nil {
		return err
	}
	// OP_QUERY layout: flags(4) + fullCollectionName cstring + numberToSkip(4) + numberToReturn(4) + query doc + optional returnFields
	if len(body) < 4 {
		return io.ErrUnexpectedEOF
	}
	off := 4
	// skip cstring
	end := off
	for end < len(body) && body[end] != 0 {
		end++
	}
	if end >= len(body) {
		return io.ErrUnexpectedEOF
	}
	collName := string(body[off:end])
	off = end + 1
	if off+8 > len(body) {
		return io.ErrUnexpectedEOF
	}
	off += 8 // skip + return
	if off+4 > len(body) {
		return io.ErrUnexpectedEOF
	}
	qlen := int(binary.LittleEndian.Uint32(body[off : off+4]))
	if off+qlen > len(body) {
		return io.ErrUnexpectedEOF
	}
	queryRaw := bson.Raw(body[off : off+qlen])

	// Build synthetic OP_MSG for handler
	cmdName, _ := wire.CommandName(queryRaw)
	if cmdName == "" {
		// Often { isMaster: 1 } or { hello: 1 }
		if _, err := queryRaw.LookupErr("isMaster"); err == nil {
			cmdName = "isMaster"
		} else if _, err := queryRaw.LookupErr("ismaster"); err == nil {
			cmdName = "ismaster"
		} else if _, err := queryRaw.LookupErr("hello"); err == nil {
			cmdName = "hello"
		}
	}

	_ = collName
	_ = ctx

	var reply any
	switch cmdName {
	case "hello", "isMaster", "ismaster", "":
		reply = s.handler.HandleHelloOnly(cs, "isMaster")
	default:
		// Try as passthrough via fake msg — limited support
		reply = map[string]any{"ok": 0, "errmsg": "legacy OP_QUERY only supported for isMaster/hello", "code": 2}
	}

	// Write OP_REPLY
	return writeOpReply(conn, hdr.RequestID, reply)
}

// HandleHelloOnly is a small extension point — implemented via type assertion in handler.
// We add a method on handler via this adapter.
func (s *Server) writeErrorMsg(conn net.Conn, requestID int32, code int32, codeName, msg string) error {
	body, _ := wire.EncodeBody(bson.D{
		{Key: "ok", Value: float64(0)},
		{Key: "errmsg", Value: msg},
		{Key: "code", Value: code},
		{Key: "codeName", Value: codeName},
	})
	respID := s.reqID.Add(1)
	return wire.WriteMsg(conn, respID, requestID, body)
}

func (s *Server) tryAcquireTenantConn(tenantID string) bool {
	if s.cfg.MaxConnsPerTenant <= 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tenantN[tenantID] >= s.cfg.MaxConnsPerTenant {
		return false
	}
	s.tenantN[tenantID]++
	return true
}

func (s *Server) releaseTenantConn(tenantID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tenantN[tenantID] > 0 {
		s.tenantN[tenantID]--
	}
}

// tenantCounted flag stored in ConnState via a simple map on server — use a field on ConnState.
// We add TenantCounted bool to ConnState in handler — patch via helpers.

type connExtras struct {
	tenantCounted bool
}

var connExtraMu sync.Mutex
var connExtraMap = map[*handler.ConnState]*connExtras{}

func csHasTenantCounted(cs *handler.ConnState) bool {
	connExtraMu.Lock()
	defer connExtraMu.Unlock()
	ex, ok := connExtraMap[cs]
	return ok && ex.tenantCounted
}

func markTenantCounted(cs *handler.ConnState) {
	connExtraMu.Lock()
	defer connExtraMu.Unlock()
	ex, ok := connExtraMap[cs]
	if !ok {
		ex = &connExtras{}
		connExtraMap[cs] = ex
	}
	ex.tenantCounted = true
}

func encodeReply(doc any) (bson.Raw, error) {
	switch v := doc.(type) {
	case bson.Raw:
		return v, nil
	case bson.D:
		return wire.EncodeBody(v)
	case bson.M:
		return wire.EncodeBody(v)
	default:
		return wire.EncodeBody(doc)
	}
}

func handlerErrorDoc(err error) any {
	return bson.D{
		{Key: "ok", Value: float64(0)},
		{Key: "errmsg", Value: err.Error()},
		{Key: "code", Value: int32(1)},
		{Key: "codeName", Value: "InternalError"},
	}
}

func writeOpReply(conn io.Writer, responseTo int32, doc any) error {
	// OP_REPLY: header + responseFlags(4) + cursorID(8) + startingFrom(4) + numberReturned(4) + documents
	raw, err := encodeReply(doc)
	if err != nil {
		return err
	}
	const opReply = 1
	bodyLen := 4 + 8 + 4 + 4 + len(raw)
	total := wire.MsgHeaderSize + bodyLen
	h := wire.Header{
		MessageLength: int32(total),
		RequestID:     1,
		ResponseTo:    responseTo,
		OpCode:        opReply,
	}
	if err := wire.WriteHeader(conn, h); err != nil {
		return err
	}
	var meta [20]byte
	// responseFlags = 8 (AwaitCapable) optional; use 0
	// cursorID = 0
	// startingFrom = 0
	binary.LittleEndian.PutUint32(meta[16:20], 1) // numberReturned = 1
	if _, err := conn.Write(meta[:]); err != nil {
		return err
	}
	_, err = conn.Write(raw)
	return err
}
