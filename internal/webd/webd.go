// Package webd serves the browser client: the web app itself, the
// WebSocket endpoint that carries RFC 42TAP lines, and the operational
// endpoints (/healthz, /metrics).
package webd

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"hollowmere/internal/proto"
	"hollowmere/internal/session"
)

// Config holds the HTTP front-end settings.
type Config struct {
	// Origins accepted for WebSocket upgrades, e.g. "hollowmere.example".
	// Same-origin requests are always allowed.
	Origins []string
	// TrustProxy reads the client IP from X-Forwarded-For / CF-Connecting-IP.
	// Only enable it when the server sits behind a proxy you control.
	TrustProxy bool
	// MetricsToken, when set, is required as ?token= on /metrics.
	MetricsToken string
	IdleTimeout  time.Duration
	WriteTimeout time.Duration
}

// DefaultConfig returns sensible settings.
func DefaultConfig() Config {
	return Config{IdleTimeout: 10 * time.Minute, WriteTimeout: 10 * time.Second}
}

// Server is the HTTP front-end.
type Server struct {
	mgr    *session.Manager
	log    *slog.Logger
	cfg    Config
	assets fs.FS
}

// New creates the front-end. assets holds the web client files.
func New(mgr *session.Manager, log *slog.Logger, cfg Config, assets fs.FS) *Server {
	return &Server{mgr: mgr, log: log, cfg: cfg, assets: assets}
}

// Handler builds the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", s.staticHandler())
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/top", s.handleTop)
	mux.HandleFunc("/top.json", s.handleTopJSON)
	return s.withCommonHeaders(mux)
}

func (s *Server) staticHandler() http.Handler {
	files := http.FileServer(http.FS(s.assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".css") || strings.HasSuffix(r.URL.Path, ".js") {
			w.Header().Set("Cache-Control", "public, max-age=300")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

func (s *Server) withCommonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; connect-src 'self' ws: wss:; img-src 'self' data:; "+
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src https://fonts.gstatic.com; base-uri 'none'; form-action 'none'")
		next.ServeHTTP(w, r)
	})
}

// clientIP resolves the player's address, honouring proxy headers only when
// the deployment says the proxy is trusted.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
			return ip
		}
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			if first, _, ok := strings.Cut(fwd, ","); ok {
				return strings.TrimSpace(first)
			}
			return strings.TrimSpace(fwd)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: s.cfg.Origins,
		// The protocol is line-based text; compression buys little and
		// costs CPU on a small box.
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		s.log.Warn("ws_accept_failed", "ip", ip, "error", err.Error())
		return
	}
	// Allow a little more than a protocol line so an oversized message is
	// answered with ERR 400 instead of killing the connection.
	c.SetReadLimit(4 * proto.MaxLineLength)

	ctx := context.Background()
	conn := &wsConn{c: c, ctx: ctx, ip: ip, cfg: s.cfg}
	if perr := s.mgr.Admit(ip); perr != nil {
		_ = conn.WriteLine(perr.Error())
		_ = c.Close(websocket.StatusTryAgainLater, perr.Message)
		return
	}
	s.mgr.Serve(conn)
}

type healthReply struct {
	Status      string `json:"status"`
	Players     int    `json:"players"`
	Connections int    `json:"connections"`
	UptimeSec   int    `json:"uptime_seconds"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(healthReply{
		Status:      "ok",
		Players:     s.mgr.Game().PlayerCount(),
		Connections: s.mgr.Open(),
		UptimeSec:   int(s.mgr.Metrics().Uptime().Seconds()),
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.metricsAuthorized(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.mgr.Metrics().WriteProm(w, s.mgr.Game().PlayerCount())
}

// metricsAuthorized accepts the token as "Authorization: Bearer <token>"
// (what Prometheus sends, and it stays out of access logs) or as ?token=.
func (s *Server) metricsAuthorized(r *http.Request) bool {
	want := s.cfg.MetricsToken
	if want == "" {
		return true
	}
	got := r.URL.Query().Get("token")
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		got = strings.TrimSpace(bearer)
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// wsConn adapts a WebSocket to session.Conn: one text message per
// protocol line.
type wsConn struct {
	c   *websocket.Conn
	ctx context.Context
	ip  string
	cfg Config
}

func (w *wsConn) ReadLine() (string, error) {
	ctx, cancel := context.WithTimeout(w.ctx, w.cfg.IdleTimeout)
	defer cancel()
	typ, data, err := w.c.Read(ctx)
	if err != nil {
		if ctx.Err() != nil && w.ctx.Err() == nil {
			return "", proto.ErrIdle
		}
		return "", err
	}
	if typ != websocket.MessageText {
		return "", proto.ErrMalformed
	}
	if len(data) > proto.MaxLineLength {
		return "", proto.ErrTooLong
	}
	line := strings.TrimRight(string(data), "\r\n")
	// A browser may batch several commands in one message.
	if strings.ContainsRune(line, '\n') {
		return "", proto.ErrMalformed
	}
	return line, nil
}

func (w *wsConn) WriteLine(line string) error {
	ctx, cancel := context.WithTimeout(w.ctx, w.cfg.WriteTimeout)
	defer cancel()
	return w.c.Write(ctx, websocket.MessageText, []byte(line))
}

func (w *wsConn) Close() error {
	err := w.c.Close(websocket.StatusNormalClosure, "bye")
	if err != nil && !errors.Is(err, net.ErrClosed) {
		_ = w.c.CloseNow()
	}
	return nil
}

func (w *wsConn) RemoteIP() string { return w.ip }
func (w *wsConn) Kind() string     { return "ws" }
