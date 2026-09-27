package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hollowmere/internal/game"
	"hollowmere/internal/logs"
	"hollowmere/internal/proto"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := openTest(t)
	want := &game.StoredPlayer{
		Key:    "abc123",
		Name:   "Ålice",
		Played: 90 * time.Second,
		Data: game.PlayerSave{
			Version:   game.SaveVersion,
			Room:      "loc.cave",
			HP:        42,
			MaxHP:     120,
			Inventory: []string{"item.iron_sword", "item.gold_coin"},
			Quests:    []game.SavedQuest{{ID: "q.fetch_herbs", Status: "completed", Progress: 3}},
		},
	}
	if err := s.SavePlayer(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadPlayer("abc123")
	if err != nil || got == nil {
		t.Fatalf("load: %v %v", got, err)
	}
	if got.Name != want.Name || got.Data.Room != "loc.cave" || got.Data.HP != 42 || got.Data.MaxHP != 120 {
		t.Fatalf("mismatch: %+v", got)
	}
	if len(got.Data.Inventory) != 2 || got.Data.Inventory[0] != "item.iron_sword" {
		t.Fatalf("inventory: %v", got.Data.Inventory)
	}
	if len(got.Data.Quests) != 1 || got.Data.Quests[0].Status != "completed" {
		t.Fatalf("quests: %v", got.Data.Quests)
	}
	if got.Played != 90*time.Second {
		t.Fatalf("played: %v", got.Played)
	}

	// saving again updates the same row
	want.Data.HP = 7
	want.Played = 300 * time.Second
	if err := s.SavePlayer(want); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Count(); n != 1 {
		t.Fatalf("count: %d", n)
	}
	got, _ = s.LoadPlayer("abc123")
	if got.Data.HP != 7 || got.Played != 300*time.Second {
		t.Fatalf("update lost: %+v", got)
	}
}

func TestUnknownKey(t *testing.T) {
	s := openTest(t)
	got, err := s.LoadPlayer("nope")
	if err != nil || got != nil {
		t.Fatalf("expected (nil, nil), got (%v, %v)", got, err)
	}
}

func TestNameOwnerIsCaseInsensitive(t *testing.T) {
	s := openTest(t)
	if owner, err := s.NameOwner("alice"); err != nil || owner != "" {
		t.Fatalf("free name: %q %v", owner, err)
	}
	if err := s.SavePlayer(&game.StoredPlayer{Key: "k1", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	owner, err := s.NameOwner("ALICE")
	if err != nil || owner != "k1" {
		t.Fatalf("owner: %q %v", owner, err)
	}
}

func TestRecentAndBackup(t *testing.T) {
	s := openTest(t)
	for i, name := range []string{"one", "two"} {
		p := &game.StoredPlayer{
			Key: name, Name: name, Played: time.Duration(i+1) * time.Hour,
			Data: game.PlayerSave{Quests: []game.SavedQuest{{ID: "q.a", Status: "completed"}}},
		}
		if err := s.SavePlayer(p); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond) // last_seen has a one-second resolution
	}
	list, err := s.Recent(10)
	if err != nil || len(list) != 2 {
		t.Fatalf("recent: %v %v", list, err)
	}
	if list[0].Name != "two" || list[0].Quests != 1 {
		t.Fatalf("order or quest count wrong: %+v", list)
	}

	dest := filepath.Join(t.TempDir(), "backup", "copy.db")
	if err := s.Backup(dest); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dest); err != nil || st.Size() == 0 {
		t.Fatalf("backup file: %v %v", st, err)
	}
	// the backup opens as a working database holding the same characters
	b, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if n, _ := b.Count(); n != 2 {
		t.Fatalf("backup holds %d characters", n)
	}
}

// --- the store behind a live world -------------------------------------

type recorder struct{ lines []string }

func (r *recorder) Send(line string) { r.lines = append(r.lines, line) }

func (r *recorder) has(prefix string) bool {
	for _, l := range r.lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func newWorld(t *testing.T, s *Store) *game.Game {
	t.Helper()
	w, err := game.LoadWorld("../../data/world")
	if err != nil {
		t.Fatal(err)
	}
	g := game.New(w, logs.Discard(), 3)
	g.SetStore(s)
	return g
}

func do(t *testing.T, g *game.Game, p *game.Player, line string) string {
	t.Helper()
	c, err := proto.ParseCommand(line)
	if err != nil {
		t.Fatal(err)
	}
	return g.Handle(p, c)
}

func TestCharacterComesBackWithItsKey(t *testing.T) {
	s := openTest(t)
	g := newWorld(t, s)

	rec := &recorder{}
	p, perr := g.Connect("alice", "", "127.0.0.1", rec)
	if perr != nil {
		t.Fatal(perr)
	}
	key := p.Key
	if len(key) < 20 {
		t.Fatalf("weak resume key %q", key)
	}
	// take a quest and an item, move away, then leave
	do(t, g, p, "MOVE south")
	do(t, g, p, "QUEST guard")
	do(t, g, p, "MOVE west")
	if got := do(t, g, p, "TAKE herbs"); !strings.HasPrefix(got, "OK taken=") {
		t.Fatal(got)
	}
	p.HP = 55
	room := p.Room
	g.Disconnect(p, "test")

	// the herbs (a world item) went back to their room for everyone else
	other, _ := g.Connect("bob", "", "127.0.0.1", &recorder{})
	do(t, g, other, "MOVE south")
	do(t, g, other, "MOVE west")
	if got := do(t, g, other, "TAKE herbs"); !strings.HasPrefix(got, "OK taken=") {
		t.Fatalf("world item did not return: %s", got)
	}
	g.Disconnect(other, "test")

	// coming back with the key restores the character
	rec2 := &recorder{}
	back, perr := g.Connect("alice", key, "127.0.0.1", rec2)
	if perr != nil {
		t.Fatal(perr)
	}
	if back.Room != room || back.HP != 55 {
		t.Fatalf("state lost: room=%s hp=%d", back.Room, back.HP)
	}
	if st := back.Quests["q.deliver_letter"]; st == nil || st.Status != "active" {
		t.Fatalf("quest lost: %+v", back.Quests)
	}
	// the quest letter is a given item: it follows the character
	inv := do(t, g, back, "INVENTORY")
	if !strings.Contains(inv, "item.sealed_letter") {
		t.Fatalf("quest item lost: %s", inv)
	}
	if rec2.has("EVT ROOM ITEM DROP") {
		t.Fatal("a saved character should not drop its belongings")
	}
	g.Disconnect(back, "test")

	// the name belongs to that key now
	if _, perr := g.Connect("ALICE", "", "127.0.0.1", &recorder{}); perr != proto.ErrNameInUse {
		t.Fatalf("name not reserved: %v", perr)
	}
	// an unknown key simply gives a new character
	fresh, perr := g.Connect("carol", "not-a-real-key", "127.0.0.1", &recorder{})
	if perr != nil || fresh.Key == "not-a-real-key" {
		t.Fatalf("unknown key: %v %q", perr, fresh.Key)
	}
}

func TestSaveAllAndRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "world.db")

	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	g1 := newWorld(t, s1)
	p, _ := g1.Connect("dana", "", "127.0.0.1", &recorder{})
	key := p.Key
	do(t, g1, p, "MOVE north")
	p.MaxHP = 150
	if n := g1.SaveAll(); n != 1 {
		t.Fatalf("SaveAll saved %d players", n)
	}
	// the process dies without a clean disconnect
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	g2 := newWorld(t, s2)
	back, perr := g2.Connect("dana", key, "127.0.0.1", &recorder{})
	if perr != nil {
		t.Fatal(perr)
	}
	if back.Room != "loc.bakery" || back.MaxHP != 150 {
		t.Fatalf("autosave lost: room=%s maxhp=%d", back.Room, back.MaxHP)
	}
}

func TestSchemaMigrationAndTop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	// A database as schema version 1 left it.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)`,
		`INSERT INTO meta VALUES ('schema_version', '1')`,
		`CREATE TABLE players (key TEXT PRIMARY KEY, name TEXT NOT NULL, name_lower TEXT NOT NULL UNIQUE,
			data TEXT NOT NULL, play_seconds INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, last_seen INTEGER NOT NULL)`,
		`INSERT INTO players VALUES ('k1', 'Old', 'old', '{"version":2,"level":3,"xp":10,"kills":7,
			"quests":[{"id":"a","status":"completed"},{"id":"b","status":"completed"},{"id":"c","status":"active"}]}', 0, 1, 1)`,
		`INSERT INTO players VALUES ('k2', 'Older', 'older', '{"version":1,"quests":[]}', 0, 1, 1)`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	top, err := s.Top(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || top[0].Name != "Old" || top[0].Level != 3 || top[0].Quests != 2 || top[0].Kills != 7 ||
		top[1].Name != "Older" || top[1].Level != 1 {
		t.Fatalf("%+v", top)
	}
	// Saving keeps the columns current.
	if err := s.SavePlayer(&game.StoredPlayer{Key: "k2", Name: "Older", Data: game.PlayerSave{Version: 2, Level: 9}}); err != nil {
		t.Fatal(err)
	}
	top, _ = s.Top(1)
	if top[0].Name != "Older" || top[0].Level != 9 {
		t.Fatalf("%+v", top)
	}
	// Opening again does not migrate twice.
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	s.Close()
}
