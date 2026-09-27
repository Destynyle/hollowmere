package store

import (
	"strings"
	"testing"

	"hollowmere/internal/game"
	"hollowmere/internal/proto"
)

// conn is a recorder that also notices when the game closes the session.
type conn struct {
	recorder
	closed string
}

func (c *conn) Close(reason string) { c.closed = reason }

type who struct {
	p   *game.Player
	c   *conn
	key string
}

func connect(t *testing.T, g *game.Game, name, key, ip string) who {
	t.Helper()
	c := &conn{}
	p, perr := g.Connect(name, key, ip, c)
	if perr != nil {
		t.Fatalf("connect %s: %v", name, perr)
	}
	return who{p, c, p.Key}
}

func TestModeration(t *testing.T) {
	s := openTest(t)
	g := newWorld(t, s)

	// Characters must exist before the console can grant them a role.
	for _, n := range []string{"alice", "bob"} {
		w := connect(t, g, n, "", "10.0.0.1")
		g.Disconnect(w.p, "test")
	}
	aliceKey, _ := s.NameOwner("alice")
	bobKey, _ := s.NameOwner("bob")
	if err := s.SetRole("alice", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRole("bob", "moderator"); err != nil {
		t.Fatal(err)
	}
	alice := connect(t, g, "alice", aliceKey, "10.0.0.1")
	bob := connect(t, g, "bob", bobKey, "10.0.0.2")
	carol := connect(t, g, "carol", "", "10.0.0.9")
	dave := connect(t, g, "dave", "", "10.0.0.9") // same address as carol
	if alice.p.Role != "admin" || bob.p.Role != "moderator" || carol.p.Role != "" {
		t.Fatalf("roles: %q %q %q", alice.p.Role, bob.p.Role, carol.p.Role)
	}

	// Players cannot moderate; moderators cannot touch their superiors.
	if got := do(t, g, carol.p, "ANNOUNCE hello"); got != "ERR 403 FORBIDDEN" {
		t.Fatal(got)
	}
	if got := do(t, g, bob.p, "KICK alice"); got != "ERR 403 FORBIDDEN" {
		t.Fatal(got)
	}
	if got := do(t, g, bob.p, "BAN carol"); got != "ERR 403 FORBIDDEN" {
		t.Fatal(got)
	}

	if got := do(t, g, bob.p, "ANNOUNCE maintenance at 22h"); got != "OK" || !carol.c.has("EVT SERVER ANNOUNCE maintenance at 22h") {
		t.Fatal(got)
	}

	// Mute blocks chat and private messages, then unmute restores them.
	if got := do(t, g, bob.p, "MUTE carol 10m flooding"); !strings.HasPrefix(got, "OK muted=carol") {
		t.Fatal(got)
	}
	if got := do(t, g, carol.p, "CHAT GLOBAL hi"); got != "ERR 420 MUTED" {
		t.Fatal(got)
	}
	if got := do(t, g, carol.p, "TELL bob hi"); got != "ERR 420 MUTED" {
		t.Fatal(got)
	}
	if got := do(t, g, bob.p, "MUTE carol forever"); got != "ERR 400 MALFORMED_COMMAND" {
		t.Fatal(got)
	}
	expectLifted(t, do(t, g, bob.p, "UNMUTE carol"))
	if got := do(t, g, carol.p, "CHAT GLOBAL hi"); got != "OK" {
		t.Fatal(got)
	}

	// Kick closes the session after telling why.
	if got := do(t, g, bob.p, "KICK dave be nice"); got != "OK kicked=dave" || dave.c.closed != "kick" || !dave.c.has("EVT SERVER KICK be nice") {
		t.Fatalf("%s closed=%q", got, dave.c.closed)
	}
	g.Disconnect(dave.p, "kick")
	dave = connect(t, g, "dave", dave.key, "10.0.0.9")

	// Ban: key and address; everyone on that address leaves.
	carolKey := carol.key
	if got := do(t, g, alice.p, "BAN carol 2h cheating"); !strings.HasPrefix(got, "OK banned=carol") || !strings.HasSuffix(got, "ip=true") {
		t.Fatal(got)
	}
	if carol.c.closed != "ban" || dave.c.closed != "ban" {
		t.Fatalf("closed: carol=%q dave=%q", carol.c.closed, dave.c.closed)
	}
	g.Disconnect(carol.p, "ban")
	g.Disconnect(dave.p, "ban")
	if _, perr := g.Connect("carol", carolKey, "10.9.9.9", &conn{}); perr != proto.ErrBanned {
		t.Fatalf("banned key came back: %v", perr)
	}
	if _, perr := g.Connect("newbie", "", "10.0.0.9", &conn{}); perr != proto.ErrBanned {
		t.Fatalf("banned address came back: %v", perr)
	}
	if _, perr := g.Connect("stranger", "", "10.1.1.1", &conn{}); perr != nil {
		t.Fatalf("an innocent was refused: %v", perr)
	}

	// The ban survives a restart.
	g2 := newWorld(t, s)
	if _, perr := g2.Connect("carol", carolKey, "10.9.9.9", &conn{}); perr != proto.ErrBanned {
		t.Fatalf("ban lost on restart: %v", perr)
	}

	// An offline character can be banned too, using its last address.
	eve := connect(t, g, "eve", "", "10.0.0.50")
	g.Disconnect(eve.p, "test")
	if got := do(t, g, alice.p, "BAN eve"); got != "OK banned=eve until=permanent ip=true" {
		t.Fatal(got)
	}
	if _, perr := g.Connect("sock", "", "10.0.0.50", &conn{}); perr != proto.ErrBanned {
		t.Fatal("offline ban missed the address")
	}

	expectLifted(t, do(t, g, alice.p, "UNBAN carol"))
	if _, perr := g.Connect("carol", carolKey, "10.0.0.9", &conn{}); perr != nil {
		t.Fatalf("unban did not lift the ban: %v", perr)
	}

	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM modlog WHERE actor IN ('alice', 'bob')`).Scan(&n); err != nil || n < 6 {
		t.Fatalf("audit log has %d lines (%v)", n, err)
	}
	list, err := s.Admins()
	if err != nil || len(list) != 2 {
		t.Fatalf("%+v %v", list, err)
	}
}

func expectLifted(t *testing.T, got string) {
	t.Helper()
	if got != "OK lifted=1" {
		t.Fatal(got)
	}
}
