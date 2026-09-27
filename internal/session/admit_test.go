package session

import (
	"testing"

	"hollowmere/internal/game"
	"hollowmere/internal/logs"
	"hollowmere/internal/proto"
)

type fakeConn struct{ ip string }

func (f *fakeConn) ReadLine() (string, error) { select {} }
func (f *fakeConn) WriteLine(string) error    { return nil }
func (f *fakeConn) Close() error              { return nil }
func (f *fakeConn) RemoteIP() string          { return f.ip }
func (f *fakeConn) Kind() string              { return "test" }

func TestConnectionsPerIP(t *testing.T) {
	w, err := game.LoadWorld("../../data/world")
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.MaxConnPerIP = 2
	m := NewManager(game.New(w, logs.Discard(), 1), logs.Discard(), cfg, nil)
	for i := 0; i < 2; i++ {
		if perr := m.Admit("1.2.3.4"); perr != nil {
			t.Fatal(perr)
		}
		m.add(&Session{mgr: m, conn: &fakeConn{ip: "1.2.3.4"}})
	}
	if perr := m.Admit("1.2.3.4"); perr != proto.ErrConnectionFailed {
		t.Fatalf("third connection from one address admitted: %v", perr)
	}
	if perr := m.Admit("5.6.7.8"); perr != nil {
		t.Fatalf("another address refused: %v", perr)
	}
}
