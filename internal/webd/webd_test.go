package webd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"hollowmere/internal/game"
	"hollowmere/internal/logs"
	"hollowmere/internal/session"
	"hollowmere/internal/web"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	w, err := game.LoadWorld("../../data/world.json")
	if err != nil {
		t.Fatal(err)
	}
	log := logs.Discard()
	mgr := session.NewManager(game.New(w, log, 7), log, session.DefaultConfig(), session.NewMetrics())
	srv := httptest.NewServer(New(mgr, log, DefaultConfig(), web.Assets).Handler())
	t.Cleanup(srv.Close)
	return srv
}

type wsClient struct {
	t   *testing.T
	c   *websocket.Conn
	ctx context.Context
}

func dialWS(t *testing.T, srv *httptest.Server) *wsClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return &wsClient{t: t, c: c, ctx: ctx}
}

func (w *wsClient) send(line string) {
	w.t.Helper()
	if err := w.c.Write(w.ctx, websocket.MessageText, []byte(line)); err != nil {
		w.t.Fatal(err)
	}
}

func (w *wsClient) read() string {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(w.ctx, 3*time.Second)
	defer cancel()
	typ, data, err := w.c.Read(ctx)
	if err != nil {
		w.t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageText {
		w.t.Fatalf("unexpected message type %v", typ)
	}
	return string(data)
}

func (w *wsClient) expect(want string) {
	w.t.Helper()
	for {
		if got := w.read(); got == want {
			return
		}
	}
}

func TestWebSocketSession(t *testing.T) {
	srv := newTestServer(t)
	a := dialWS(t, srv)
	if got := a.read(); got != "OK hello proto=1" {
		t.Fatalf("greeting: %q", got)
	}
	a.send("LOOK")
	if got := a.read(); got != "ERR 403 NOT_AUTHENTICATED" {
		t.Fatal(got)
	}
	a.send("CONNECT alice")
	if got := a.read(); got != "OK connected" {
		t.Fatal(got)
	}
	a.expect("EVT STATS players=1")

	// a second player: the two sessions see each other
	b := dialWS(t, srv)
	b.expect("OK hello proto=1")
	b.send("CONNECT bob")
	b.expect("OK connected")
	a.expect("EVT ROOM PRESENCE ENTER bob")

	b.send("CHAT GLOBAL héllo 👋")
	b.expect("OK")
	a.expect("EVT GLOBAL CHAT bob héllo 👋")

	a.send("LOOK")
	line := a.read()
	if !strings.HasPrefix(line, `OK {"room":{"id":"loc.square"`) {
		t.Fatalf("look: %q", line)
	}

	// an oversized message is refused without killing the session
	a.send("CHAT GLOBAL " + strings.Repeat("x", 2000))
	a.expect("ERR 400 LINE_TOO_LONG")
	a.send("WHO")
	if got := a.read(); !strings.Contains(got, `"server":2`) {
		t.Fatal(got)
	}

	// a closed socket removes the player
	_ = b.c.Close(websocket.StatusNormalClosure, "bye")
	a.expect("EVT STATS players=1")
}

func TestQuitClosesCleanly(t *testing.T) {
	srv := newTestServer(t)
	c := dialWS(t, srv)
	c.expect("OK hello proto=1")
	c.send("CONNECT zoe")
	c.expect("OK connected")
	c.send("QUIT")
	c.expect("OK bye")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, _, err := c.c.Read(ctx); err == nil {
		t.Fatal("expected the server to close the socket after QUIT")
	}
}

func TestOperationalEndpoints(t *testing.T) {
	srv := newTestServer(t)
	res, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var h healthReply
	if err := json.NewDecoder(res.Body).Decode(&h); err != nil || h.Status != "ok" {
		t.Fatalf("health: %+v %v", h, err)
	}

	res2, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	body, _ := io.ReadAll(res2.Body)
	for _, want := range []string{"tap_players_online", "tap_commands_total", "tap_abuse_command_flood_total"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("metrics missing %s", want)
		}
	}

	res3, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res3.Body.Close()
	page, _ := io.ReadAll(res3.Body)
	if !strings.Contains(string(page), "HOLLOWMERE") {
		t.Fatal("the web client is not served at /")
	}
	if got := res3.Header.Get("Content-Security-Policy"); got == "" {
		t.Fatal("missing CSP header")
	}
}

func TestMetricsToken(t *testing.T) {
	w, err := game.LoadWorld("../../data/world.json")
	if err != nil {
		t.Fatal(err)
	}
	log := logs.Discard()
	mgr := session.NewManager(game.New(w, log, 1), log, session.DefaultConfig(), session.NewMetrics())
	cfg := DefaultConfig()
	cfg.MetricsToken = "s3cret"
	srv := httptest.NewServer(New(mgr, log, cfg, web.Assets).Handler())
	defer srv.Close()

	res, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 without a token, got %d", res.StatusCode)
	}
	res2, err := http.Get(srv.URL + "/metrics?token=s3cret")
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with the token, got %d", res2.StatusCode)
	}
}
