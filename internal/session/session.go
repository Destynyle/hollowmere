// Package session holds the transport-independent part of a player
// connection: admission control, rate limiting, the bounded outgoing queue
// and the cleanup that runs when the connection ends.
//
// A transport (raw TCP, WebSocket) only has to provide a Conn: read one
// protocol line, write one protocol line, close. Everything else — framing
// rules, abuse detection, the guarantee that a slow client never blocks a
// broadcast — lives here and is shared.
package session

import (
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"hollowmere/internal/game"
	"hollowmere/internal/proto"
)

// Conn is one client connection, already framed into protocol lines.
type Conn interface {
	// ReadLine returns the next line without its terminator. It returns
	// proto.ErrTooLong for an oversized line (the connection stays usable).
	ReadLine() (string, error)
	WriteLine(line string) error
	Close() error
	// RemoteIP is used for logging and per-IP abuse detection.
	RemoteIP() string
	// Kind labels the transport in logs and metrics ("tcp", "ws").
	Kind() string
}

// Config holds the limits applied to every session.
type Config struct {
	MaxConnections   int
	MaxConnPerWindow int
	ConnWindow       time.Duration
	CommandsPerSec   float64
	CommandBurst     float64
	MaxViolations    int
	OutQueue         int
	IdleTimeout      time.Duration
}

// DefaultConfig returns the production limits.
func DefaultConfig() Config {
	return Config{
		MaxConnections:   512,
		MaxConnPerWindow: 20,
		ConnWindow:       10 * time.Second,
		CommandsPerSec:   10,
		CommandBurst:     40,
		MaxViolations:    30,
		OutQueue:         512,
		IdleTimeout:      30 * time.Minute,
	}
}

// Manager owns the live sessions and the per-IP admission state.
type Manager struct {
	cfg  Config
	game *game.Game
	log  *slog.Logger
	mx   *Metrics

	mu       sync.Mutex
	sessions map[*Session]struct{}
	attempts map[string][]time.Time
	swept    time.Time
}

// NewManager creates a manager around a game.
func NewManager(g *game.Game, log *slog.Logger, cfg Config, mx *Metrics) *Manager {
	if mx == nil {
		mx = NewMetrics()
	}
	return &Manager{
		cfg:      cfg,
		game:     g,
		log:      log,
		mx:       mx,
		sessions: map[*Session]struct{}{},
		attempts: map[string][]time.Time{},
	}
}

// Metrics returns the counters shared with the /metrics endpoint.
func (m *Manager) Metrics() *Metrics { return m.mx }

// Game returns the world served by this manager.
func (m *Manager) Game() *game.Game { return m.game }

// Admit applies the connection limits. A non-nil error must be sent to the
// client before closing the connection.
func (m *Manager) Admit(ip string) *proto.Error {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweep(now)
	recent := m.attempts[ip][:0]
	for _, t := range m.attempts[ip] {
		if now.Sub(t) < m.cfg.ConnWindow {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	m.attempts[ip] = recent
	if len(recent) > m.cfg.MaxConnPerWindow {
		m.mx.AbuseRapidConnections.Add(1)
		m.log.Warn("abuse_rapid_connections", "ip", ip, "attempts", len(recent),
			"window_s", m.cfg.ConnWindow.Seconds())
		return proto.ErrConnectionFailed
	}
	if len(m.sessions) >= m.cfg.MaxConnections {
		m.mx.ConnectionsRejected.Add(1)
		m.log.Warn("connection_rejected_full", "ip", ip, "open", len(m.sessions))
		return proto.ErrServerFull
	}
	return nil
}

// sweep forgets IPs quiet for a whole window; called with m.mu held.
func (m *Manager) sweep(now time.Time) {
	if now.Sub(m.swept) < m.cfg.ConnWindow {
		return
	}
	m.swept = now
	for ip, times := range m.attempts {
		if len(times) == 0 || now.Sub(times[len(times)-1]) >= m.cfg.ConnWindow {
			delete(m.attempts, ip)
		}
	}
}

// CloseAll disconnects every player (used on shutdown).
func (m *Manager) CloseAll(reason string) {
	m.mu.Lock()
	list := make([]*Session, 0, len(m.sessions))
	for s := range m.sessions {
		list = append(list, s)
	}
	m.mu.Unlock()
	for _, s := range list {
		s.Close(reason)
	}
}

// Open returns the number of live sessions.
func (m *Manager) Open() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

func (m *Manager) add(s *Session) {
	m.mu.Lock()
	m.sessions[s] = struct{}{}
	m.mu.Unlock()
	m.mx.ConnectionsOpen.Add(1)
	m.mx.ConnectionsTotal.Add(1)
}

func (m *Manager) remove(s *Session) {
	m.mu.Lock()
	delete(m.sessions, s)
	m.mu.Unlock()
	m.mx.ConnectionsOpen.Add(-1)
}

// Session is one connected client.
type Session struct {
	mgr  *Manager
	conn Conn
	log  *slog.Logger

	out       chan string
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup

	mu     sync.Mutex
	reason string
	name   string

	player     *game.Player
	tokens     float64
	last       time.Time
	violations int
}

// Serve runs one session to completion: it greets the client, reads its
// commands and cleans up. It blocks until the connection ends.
func (m *Manager) Serve(conn Conn) {
	s := &Session{
		mgr:    m,
		conn:   conn,
		log:    m.log.With("ip", conn.RemoteIP(), "transport", conn.Kind()),
		out:    make(chan string, m.cfg.OutQueue),
		done:   make(chan struct{}),
		tokens: m.cfg.CommandBurst,
		last:   time.Now(),
	}
	m.add(s)
	defer m.remove(s)

	s.wg.Add(1)
	go s.writeLoop()
	s.log.Info("connection_opened")
	s.Send(proto.Greeting)
	s.readLoop()
	s.cleanup()
	s.wg.Wait()
}

// Send queues a line. It never blocks: a client whose queue is full is too
// slow to follow the world and gets disconnected, while the broadcast that
// triggered this call carries on for everyone else.
func (s *Session) Send(line string) {
	select {
	case <-s.done:
		return
	default:
	}
	select {
	case s.out <- line:
	default:
		s.mgr.mx.SlowClients.Add(1)
		s.log.Warn("send_queue_full", "player", s.PlayerName())
		go s.Close("send_queue_full")
	}
}

// Close asks the session to stop; the writer flushes what is queued and
// closes the connection, which also unblocks the reader.
func (s *Session) Close(reason string) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.reason = reason
		s.mu.Unlock()
		close(s.done)
	})
}

func (s *Session) closeReason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reason
}

// PlayerName is the authenticated name, or "" before CONNECT.
func (s *Session) PlayerName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.name
}

func (s *Session) writeLoop() {
	defer s.wg.Done()
	defer s.conn.Close()
	for {
		select {
		case line := <-s.out:
			if !s.write(line) {
				return
			}
		case <-s.done:
			for {
				select {
				case line := <-s.out:
					if !s.write(line) {
						return
					}
				default:
					return
				}
			}
		}
	}
}

func (s *Session) write(line string) bool {
	if err := s.conn.WriteLine(line); err != nil {
		if s.closeReason() == "" {
			s.log.Warn("send_failed", "player", s.PlayerName(), "error", err.Error(),
				"code", proto.ErrSendFailed.Code)
		}
		s.Close("send_failed")
		return false
	}
	s.mgr.mx.LinesSent.Add(1)
	return true
}

func (s *Session) readLoop() {
	for {
		line, err := s.conn.ReadLine()
		if errors.Is(err, proto.ErrTooLong) {
			s.mgr.mx.AbuseLineTooLong.Add(1)
			s.log.Warn("abuse_line_too_long", "player", s.PlayerName(), "limit", proto.MaxLineLength)
			s.reply(proto.ErrLineTooLong.Error(), "")
			continue
		}
		if err != nil {
			if s.closeReason() == "" {
				s.Close(closeReasonFor(err))
			}
			return
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		s.mgr.mx.LinesReceived.Add(1)
		if !s.handleLine(line) {
			return
		}
	}
}

func closeReasonFor(err error) string {
	if errors.Is(err, proto.ErrIdle) {
		return "idle_timeout"
	}
	return "client_closed"
}

// allow implements the per-connection token bucket.
func (s *Session) allow() bool {
	now := time.Now()
	s.tokens += now.Sub(s.last).Seconds() * s.mgr.cfg.CommandsPerSec
	if s.tokens > s.mgr.cfg.CommandBurst {
		s.tokens = s.mgr.cfg.CommandBurst
	}
	s.last = now
	if s.tokens < 1 {
		return false
	}
	s.tokens--
	return true
}

// handleLine processes one received line; false ends the session.
func (s *Session) handleLine(line string) bool {
	isQuit := strings.EqualFold(strings.TrimSpace(line), "QUIT")
	if !isQuit && !s.allow() {
		s.violations++
		s.mgr.mx.AbuseCommandFlood.Add(1)
		s.log.Warn("abuse_command_flood", "player", s.PlayerName(), "violations", s.violations)
		s.reply(proto.ErrRateLimited.Error(), "")
		if s.violations >= s.mgr.cfg.MaxViolations {
			s.log.Warn("abuse_kick", "player", s.PlayerName())
			s.Close("flooding")
			return false
		}
		return true
	}
	if err := proto.ValidateLine(line); err != nil {
		s.log.Warn("malformed_line", "player", s.PlayerName(), "raw", truncate(line, 256))
		s.reply(err.Error(), "")
		return true
	}
	cmd, err := proto.ParseCommand(line)
	if err != nil {
		s.log.Warn("malformed_line", "player", s.PlayerName(), "raw", truncate(line, 256))
		s.reply(err.Error(), "")
		return true
	}
	s.mgr.mx.Commands.Add(1)
	s.log.Debug("command", "player", s.PlayerName(), "command", cmd.Name, "args", truncate(cmd.Args, 256))

	switch {
	case cmd.Name == "QUIT":
		s.reply("OK bye", cmd.Name)
		s.Close("quit")
		return false

	case cmd.Name == "CONNECT":
		if s.player != nil {
			s.reply(proto.ErrAlreadyConnected.Error(), cmd.Name)
			return true
		}
		// CONNECT <name> [resume key] — the key is a v2 extension; other
		// groups' clients send the name alone and get a new character.
		if len(cmd.Word) < 1 || len(cmd.Word) > 2 {
			s.reply(proto.ErrInvalidName.Error(), cmd.Name)
			return true
		}
		var key string
		if len(cmd.Word) == 2 {
			key = cmd.Word[1]
		}
		p, perr := s.mgr.game.Connect(cmd.Word[0], key, s.conn.RemoteIP(), s)
		if perr != nil {
			s.reply(perr.Error(), cmd.Name)
			return true
		}
		s.player = p
		s.mu.Lock()
		s.name = p.Name
		s.mu.Unlock()
		s.logReply("OK connected", cmd.Name)
		if p.Key != "" {
			// The client stores this to come back as the same character.
			s.Send("EVT PLAYER KEY " + p.Key)
		}
		return true

	case s.player == nil:
		s.reply(proto.ErrNotAuthenticated.Error(), cmd.Name)
		return true
	}

	reply := s.mgr.game.Handle(s.player, cmd)
	s.logReply(reply, cmd.Name)
	return true
}

func (s *Session) reply(line, cmd string) {
	s.Send(line)
	s.logReply(line, cmd)
}

func (s *Session) logReply(line, cmd string) {
	if strings.HasPrefix(line, "ERR ") {
		s.mgr.mx.Errors.Add(1)
		s.log.Warn("response_error", "player", s.PlayerName(), "command", cmd, "response", truncate(line, 256))
		return
	}
	s.log.Debug("response", "player", s.PlayerName(), "command", cmd, "response", truncate(line, 256))
}

// cleanup removes the player from the world before the leave events are
// broadcast, whatever ended the session.
func (s *Session) cleanup() {
	s.Close("client_closed")
	if s.player != nil {
		s.mgr.game.Disconnect(s.player, s.closeReason())
	}
	s.log.Info("connection_closed", "player", s.PlayerName(), "reason", s.closeReason())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
