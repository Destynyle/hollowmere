package game

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"hollowmere/internal/logs"
)

func teleport(g *Game, p *Player, room string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.movePlayer(p, room)
}

func TestTell(t *testing.T) {
	g := newTestGame(t)
	a, _ := join(t, g, "alice")
	b, brec := join(t, g, "bob")
	teleport(g, b, "loc.forest") // private messages cross rooms

	if got := do(t, g, a, "TELL bob meet me at the gate"); got != "OK sent=bob" {
		t.Fatal(got)
	}
	if !brec.has("EVT PRIVATE MESSAGE alice meet me at the gate") {
		t.Fatal("message not delivered")
	}
	if got := do(t, g, a, "TELL carol hi"); got != "ERR 404 PLAYER_NOT_FOUND" {
		t.Fatal(got)
	}
	if got := do(t, g, a, "TELL alice hi"); got != "ERR 400 MALFORMED_COMMAND" {
		t.Fatal(got)
	}
	if got := do(t, g, a, "TELL bob"); got != "ERR 400 MALFORMED_COMMAND" {
		t.Fatal(got)
	}
	// The burst allows a few more, then the bucket runs dry.
	limited := false
	for i := 0; i < TellBurst+1; i++ {
		if do(t, g, a, "TELL bob spam") == "ERR 429 RATE_LIMITED" {
			limited = true
		}
	}
	if !limited {
		t.Fatal("private messages are not rate limited")
	}
}

func TestFriends(t *testing.T) {
	g := newTestGame(t)
	a, arec := join(t, g, "alice")
	b, _ := join(t, g, "bob")

	if got := do(t, g, a, "FRIEND ADD BOB"); got != "OK friend=bob" {
		t.Fatal(got)
	}
	if got := do(t, g, a, "FRIEND ADD ghost"); got != "ERR 404 PLAYER_NOT_FOUND" {
		t.Fatal(got) // unknown and no store to look it up in
	}
	if got := do(t, g, a, "FRIEND ADD alice"); got != "ERR 400 MALFORMED_COMMAND" {
		t.Fatal(got)
	}
	var list []friendInfo
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, a, "FRIEND LIST"))), &list)
	if len(list) != 1 || list[0].Name != "bob" || !list[0].Online {
		t.Fatalf("%+v", list)
	}
	arec.take()
	g.Disconnect(b, "test")
	if !arec.has("EVT FRIEND OFFLINE bob") {
		t.Fatal("no offline notice")
	}
	join(t, g, "bob")
	if !arec.has("EVT FRIEND ONLINE bob") {
		t.Fatal("no online notice")
	}
	if got := do(t, g, a, "FRIEND REMOVE bob"); got != "OK removed=bob" {
		t.Fatal(got)
	}
	if got := do(t, g, a, "FRIEND REMOVE bob"); got != "ERR 404 PLAYER_NOT_FOUND" {
		t.Fatal(got)
	}
}

func TestTop(t *testing.T) {
	w, err := LoadWorld("../../data/world")
	if err != nil {
		t.Fatal(err)
	}
	g := New(w, logs.Discard(), 1)
	g.Schedule = func(time.Duration, func()) {}
	st := &memStore{rows: map[string]*StoredPlayer{}}
	st.rows["k"] = &StoredPlayer{Key: "k", Name: "veteran", Data: PlayerSave{Version: 2, Level: 5, Kills: 40}}
	g.SetStore(st)

	a, perr := g.Connect("alice", "", "x", &recorder{})
	if perr != nil {
		t.Fatal(perr)
	}
	a.Level, a.Kills = 5, 41
	var top []TopEntry
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, a, "TOP"))), &top)
	if len(top) < 2 || top[0].Name != "alice" || !top[0].Online || top[0].Rank != 1 || top[1].Name != "veteran" {
		t.Fatalf("%+v", top)
	}
	// Live values win over the saved row of a connected player.
	a.Level = 1
	if top := g.Top(10); top[0].Name != "veteran" {
		t.Fatalf("%+v", top)
	}
}

func tradeView_(t *testing.T, got string) tradeView {
	t.Helper()
	var v tradeView
	if err := json.Unmarshal([]byte(expectOK(t, got)), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTrade(t *testing.T) {
	g := newTestGame(t)
	a, arec := join(t, g, "alice")
	b, brec := join(t, g, "bob")
	expectOK(t, do(t, g, a, "TAKE torch")) // alice wears it
	g.mu.Lock()
	potion := g.newItem("item.healing_potion")
	g.giveItem(potion, b)
	g.mu.Unlock()

	if got := do(t, g, a, "TRADE OFFER torch"); got != "ERR 417 NOT_TRADING" {
		t.Fatal(got)
	}
	if got := do(t, g, a, "TRADE bob"); got != "OK requested=bob" || !brec.has("EVT TRADE REQUEST alice") {
		t.Fatal(got)
	}
	v := tradeView_(t, do(t, g, b, "TRADE alice"))
	if v.With != "alice" || !arec.has("EVT TRADE OPEN bob") {
		t.Fatalf("%+v", v)
	}
	_, crec := join(t, g, "carol")
	c := g.player("carol")
	if got := do(t, g, c, "TRADE alice"); got != "ERR 418 TRADE_BUSY" {
		t.Fatal(got)
	}
	crec.take()

	tradeView_(t, do(t, g, a, "TRADE OFFER torch"))
	tradeView_(t, do(t, g, b, "TRADE OFFER potion"))
	v = tradeView_(t, do(t, g, a, "TRADE ACCEPT"))
	if !v.Accepted || v.TheyAccepted {
		t.Fatalf("%+v", v)
	}
	// Bob changes his offer: alice's acceptance is withdrawn.
	tradeView_(t, do(t, g, b, "TRADE REMOVE potion"))
	v = tradeView_(t, do(t, g, a, "TRADE INFO"))
	if v.Accepted || len(v.Theirs) != 0 {
		t.Fatalf("acceptance survived a change: %+v", v)
	}
	tradeView_(t, do(t, g, b, "TRADE OFFER potion"))
	tradeView_(t, do(t, g, a, "TRADE ACCEPT"))
	arec.take()
	tradeView_(t, do(t, g, b, "TRADE ACCEPT"))
	if !arec.has("EVT TRADE DONE") {
		t.Fatal("no done event")
	}
	if !containsString(a.Inventory, potion.ID) || !containsString(b.Inventory, "item.torch") ||
		containsString(a.Inventory, "item.torch") || containsString(b.Inventory, potion.ID) {
		t.Fatalf("not swapped: a=%v b=%v", a.Inventory, b.Inventory)
	}
	if a.Equipped["weapon"] != "" || b.Equipped["weapon"] != "item.torch" {
		t.Fatalf("equipment: a=%v b=%v", a.Equipped, b.Equipped)
	}
	if a.trade != nil || b.trade != nil {
		t.Fatal("trade still open")
	}

	// An item used after accepting is caught before the swap.
	do(t, g, a, "TRADE bob")
	do(t, g, b, "TRADE alice")
	tradeView_(t, do(t, g, a, "TRADE OFFER potion"))
	tradeView_(t, do(t, g, b, "TRADE OFFER torch"))
	tradeView_(t, do(t, g, a, "TRADE ACCEPT"))
	a.HP = 10
	expectOK(t, do(t, g, a, "USE potion"))
	v = tradeView_(t, do(t, g, b, "TRADE ACCEPT"))
	if v.Accepted || len(v.Theirs) != 0 || b.trade == nil {
		t.Fatalf("trade went through with a used item: %+v", v)
	}
	// Walking away cancels.
	brec.take()
	walk(t, g, a, "north")
	if !brec.has("EVT TRADE CANCEL alice") || b.trade != nil {
		t.Fatal("moving did not cancel the trade")
	}
	if got := do(t, g, b, "TRADE alice"); got != "ERR 404 PLAYER_NOT_FOUND" {
		t.Fatal(got) // not in the same room any more
	}
}

func TestGroupDungeon(t *testing.T) {
	g := newTestGame(t)
	a, _ := join(t, g, "alice")
	b, _ := join(t, g, "bob")
	teleport(g, a, "loc.dwarf_forge")
	g.npcs["npc.golem"].Alive = false // out of the way
	if got := do(t, g, a, "MOVE down"); got != "ERR 419 GROUP_REQUIRED" {
		t.Fatal(got)
	}
	expectOK(t, do(t, g, a, "GROUP CREATE"))
	expectOK(t, do(t, g, a, "GROUP INVITE bob"))
	expectOK(t, do(t, g, b, "GROUP JOIN alice"))
	if got := do(t, g, a, "MOVE down"); got != "ERR 419 GROUP_REQUIRED" {
		t.Fatal("bob is not here yet: " + got)
	}
	teleport(g, b, "loc.dwarf_forge")
	walk(t, g, a, "down")
	walk(t, g, b, "down") // alice is already inside: still counts
	walk(t, g, a, "north", "north")
	walk(t, g, b, "north", "north")

	wyrm := g.npcs["npc.wyrm"]
	a.MaxHP, a.HP = 100000, 100000
	expectOK(t, do(t, g, a, "ATTACK wyrm"))
	if want := 260 * 180 / 100; wyrm.MaxHP != want {
		t.Fatalf("wyrm max hp %d, want %d", wyrm.MaxHP, want)
	}
	var look struct {
		Details lookDetails `json:"details"`
	}
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, b, "LOOK"))), &look)
	if look.Details.NPCs[0].MaxHP != wyrm.MaxHP {
		t.Fatalf("look shows %d", look.Details.NPCs[0].MaxHP)
	}
	a.Stats.Strength = 60 // finish the fight within fightUntilDone's rounds
	if res := fightUntilDone(t, g, a, "wyrm"); res["status"] != "victory" || wyrm.Alive {
		t.Fatal(res)
	}
}

func TestFleeAvoidsGroupRooms(t *testing.T) {
	g := newTestGame(t)
	p, _ := join(t, g, "alice")
	p.MaxHP = 100000
	for i := 0; i < 40; i++ {
		teleport(g, p, "loc.dwarf_forge")
		p.HP = p.MaxHP
		expectOK(t, do(t, g, p, "ATTACK golem"))
		for p.Target != "" {
			got := do(t, g, p, "FLEE")
			if strings.Contains(got, "loc.deep_gate") {
				t.Fatal("fled into a group-only room")
			}
		}
	}
}
