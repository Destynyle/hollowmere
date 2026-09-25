// Package tcpd serves the raw TCP transport described by RFC 42TAP, so the
// CLI client and any netcat still work against the production server.
package tcpd

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"hollowmere/internal/proto"
	"hollowmere/internal/session"
)

// Server accepts TCP connections and hands them to a session.Manager.
type Server struct {
	mgr *session.Manager
	log *slog.Logger
	cfg Config
}

// Config holds the TCP-specific timeouts.
type Config struct {
	IdleTimeout  time.Duration
	WriteTimeout time.Duration
}

// DefaultConfig returns sensible timeouts.
func DefaultConfig() Config {
	return Config{IdleTimeout: 30 * time.Minute, WriteTimeout: 5 * time.Second}
}

// New creates a TCP front-end.
func New(mgr *session.Manager, log *slog.Logger, cfg Config) *Server {
	return &Server{mgr: mgr, log: log, cfg: cfg}
}

// Serve accepts connections until ctx is cancelled or the listener fails.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	s.log.Info("tcp_listening", "addr", ln.Addr().String())
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return err
		}
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	conn := &tcpConn{
		conn: c,
		lr:   proto.NewLineReader(c, proto.MaxLineLength),
		ip:   hostOf(c.RemoteAddr()),
		cfg:  s.cfg,
	}
	if perr := s.mgr.Admit(conn.ip); perr != nil {
		_ = c.SetWriteDeadline(time.Now().Add(time.Second))
		_, _ = c.Write([]byte(perr.Error() + "\n"))
		_ = c.Close()
		return
	}
	s.mgr.Serve(conn)
}

func hostOf(addr net.Addr) string {
	h, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return h
}

// tcpConn adapts net.Conn to session.Conn.
type tcpConn struct {
	conn net.Conn
	lr   *proto.LineReader
	ip   string
	cfg  Config
}

func (t *tcpConn) ReadLine() (string, error) {
	if t.cfg.IdleTimeout > 0 {
		_ = t.conn.SetReadDeadline(time.Now().Add(t.cfg.IdleTimeout))
	}
	line, err := t.lr.ReadLine()
	var ne net.Error
	if err != nil && errors.As(err, &ne) && ne.Timeout() {
		return "", proto.ErrIdle
	}
	return line, err
}

func (t *tcpConn) WriteLine(line string) error {
	_ = t.conn.SetWriteDeadline(time.Now().Add(t.cfg.WriteTimeout))
	_, err := t.conn.Write([]byte(line + "\n"))
	return err
}

func (t *tcpConn) Close() error     { return t.conn.Close() }
func (t *tcpConn) RemoteIP() string { return t.ip }
func (t *tcpConn) Kind() string     { return "tcp" }
