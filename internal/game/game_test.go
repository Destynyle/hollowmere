package game

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"hollowmere/internal/logs"
	"hollowmere/internal/proto"
)

type recorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *recorder) Send(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
}

func (r *recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.lines
	r.lines = nil
	return out
}

func (r *recorder) has(prefix string) bool {
	for _, l := range r.take() {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func newTestGame(t *testing.T) *Game {
	t.Helper()
	w, err := LoadWorld("../../data/world")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range w.NPCs {
		n.Aggressive = false // tests walk through enemy rooms; see TestAmbush
	}
	g := New(w, logs.Discard(), 42)
	g.Schedule = func(time.Duration, func()) {} // timers disabled
	return g
}

func join(t *testing.T, g *Game, name string) (*Player, *recorder) {
	t.Helper()
	rec := &recorder{}
	p, err := g.Connect(name, "", "127.0.0.1", rec)
	if err != nil {
		t.Fatalf("connect %s: %v", name, err)
	}
	rec.take()
	return p, rec
}

func do(t *testing.T, g *Game, p *Player, line string) string {
	t.Helper()
	c, err := proto.ParseCommand(line)
	if err != nil {
		t.Fatalf("parse %q: %v", line, err)
	}
	return g.Handle(p, c)
}

func expectOK(t *testing.T, got string) string {
	t.Helper()
	if !strings.HasPrefix(got, "OK") {
		t.Fatalf("expected OK, got %q", got)
	}
	return strings.TrimSpace(strings.TrimPrefix(got, "OK"))
}

func walk(t *testing.T, g *Game, p *Player, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		expectOK(t, do(t, g, p, "MOVE "+d))
	}
}

func TestWorldRequirements(t *testing.T) {
	w, err := LoadWorld("../../data/world")
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Rooms) < 8 {
		t.Errorf("need >= 8 rooms, got %d", len(w.Rooms))
	}
	roles := map[string]bool{}
	for _, n := range w.NPCs {
		roles[n.Role] = true
	}
	if len(roles) < 3 {
		t.Errorf("need >= 3 npc roles, got %v", roles)
	}
	obtainable := 0
	for _, it := range w.Items {
		if it.Obtainable {
			obtainable++
		}
	}
	if len(w.Items) < 4 || obtainable < 2 {
		t.Errorf("items: %d total, %d obtainable", len(w.Items), obtainable)
	}
	if len(w.Quests) < 2 {
		t.Errorf("need >= 2 quests")
	}
	if one := w.OneWayExits(); len(one) != 0 {
		t.Errorf("one-way exits: %v", one)
	}
}

func TestValidationRejectsBadReferences(t *testing.T) {
	bad := `{"start_room":"a","rooms":{"a":{"name":"A","exits":{"north":"nowhere"},"items":["item.x"]}},"items":{},"npcs":{},"quests":{}}`
	_, err := ParseWorld([]byte(bad))
	if err == nil || !strings.Contains(err.Error(), "nowhere") || !strings.Contains(err.Error(), "item.x") {
		t.Fatalf("expected reference errors, got %v", err)
	}
}

func TestNameInUseAndPresence(t *testing.T) {
	g := newTestGame(t)
	_, alice := join(t, g, "alice")
	if _, err := g.Connect("ALICE", "", "x", &recorder{}); err != proto.ErrNameInUse {
		t.Fatalf("expected NAME_IN_USE, got %v", err)
	}
	if _, err := g.Connect("bad name!", "", "x", &recorder{}); err != proto.ErrInvalidName {
		t.Fatalf("expected INVALID_NAME, got %v", err)
	}
	bob, bobRec := join(t, g, "bób")
	lines := alice.take()
	if !contains(lines, "EVT ROOM PRESENCE ENTER bób") || !contains(lines, "EVT STATS players=2") {
		t.Fatalf("alice did not see bob: %v", lines)
	}
	g.Disconnect(bob, "test")
	if !alice.has("EVT ROOM PRESENCE LEAVE bób") {
		t.Fatal("no leave event")
	}
	if len(bobRec.take()) != 0 {
		t.Fatal("disconnected player still receives lines")
	}
	if g.PlayerCount() != 1 {
		t.Fatal("player not removed")
	}
}

func contains(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func TestReplyBeforeEvents(t *testing.T) {
	g := newTestGame(t)
	p, rec := join(t, g, "alice")
	do(t, g, p, "CHAT GLOBAL hello world")
	lines := rec.take()
	if len(lines) != 2 || lines[0] != "OK" || lines[1] != "EVT GLOBAL CHAT alice hello world" {
		t.Fatalf("unexpected order: %v", lines)
	}
	if got := do(t, g, p, "CHAT GROUP hi"); got != "ERR 401 NOT_IN_GROUP" {
		t.Fatal(got)
	}
	if got := do(t, g, p, "CHAT NOWHERE hi"); !strings.HasPrefix(got, "ERR 400") {
		t.Fatal(got)
	}
}

func TestMovement(t *testing.T) {
	g := newTestGame(t)
	p, _ := join(t, g, "alice")
	_, bob := join(t, g, "bob")
	if got := do(t, g, p, "MOVE north"); got != "OK room=loc.bakery" {
		t.Fatal(got)
	}
	if !bob.has("EVT ROOM PRESENCE LEAVE alice") {
		t.Fatal("bob missed leave")
	}
	if got := do(t, g, p, "MOVE north"); got != "ERR 301 NO_EXIT" {
		t.Fatal(got)
	}
	if got := do(t, g, p, "move S"); got != "OK room=loc.square" {
		t.Fatal(got)
	}
	// full loop: square -> bakery -> tavern -> market -> square
	walk(t, g, p, "north", "east", "south", "west")
	if p.Room != "loc.square" {
		t.Fatalf("loop ended in %s", p.Room)
	}
}

func TestItemsAreUnique(t *testing.T) {
	g := newTestGame(t)
	a, _ := join(t, g, "alice")
	b, _ := join(t, g, "bob")
	walk(t, g, a, "east")
	walk(t, g, b, "east")
	if got := do(t, g, a, "TAKE leather vest"); got != "OK taken=item.leather_vest" {
		t.Fatal(got)
	}
	if got := do(t, g, b, "TAKE item.leather_vest"); got != "ERR 404 ITEM_NOT_FOUND" {
		t.Fatalf("duplicated item: %s", got)
	}
	var look lookReply
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, b, "LOOK"))), &look)
	if len(look.Items) != 0 {
		t.Fatalf("item still in room: %v", look.Items)
	}
	if got := do(t, g, b, "DROP leather vest"); got != "ERR 404 ITEM_NOT_IN_INVENTORY" {
		t.Fatal(got)
	}
	if got := do(t, g, a, "DROP Leather Vest"); got != "OK dropped=item.leather_vest" {
		t.Fatal(got)
	}
	if got := do(t, g, b, "TAKE vest"); got != "OK taken=item.leather_vest" {
		t.Fatal(got)
	}
	if got := do(t, g, b, "INVENTORY"); got != `OK ["item.leather_vest"]` {
		t.Fatal(got)
	}
	walk(t, g, a, "east")
	if got := do(t, g, a, "TAKE anvil"); got != "ERR 405 ITEM_NOT_OBTAINABLE" {
		t.Fatal(got)
	}
}

func TestDisconnectDropsItems(t *testing.T) {
	g := newTestGame(t)
	a, _ := join(t, g, "alice")
	b, _ := join(t, g, "bob")
	expectOK(t, do(t, g, a, "TAKE torch"))
	g.Disconnect(a, "test")
	if got := do(t, g, b, "TAKE torch"); got != "OK taken=item.torch" {
		t.Fatal(got)
	}
}

func TestTalk(t *testing.T) {
	g := newTestGame(t)
	p, _ := join(t, g, "alice")
	var r map[string]string
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "TALK Town Crier"))), &r)
	if r["npc"] != "npc.crier" || r["dialogue"] == "" {
		t.Fatal(r)
	}
	if got := do(t, g, p, "TALK baker"); got != "ERR 404 NPC_NOT_FOUND" {
		t.Fatal(got)
	}
	if got := do(t, g, p, "ATTACK crier"); got != "ERR 405 NPC_NOT_HOSTILE" {
		t.Fatal(got)
	}
}

func TestGroups(t *testing.T) {
	g := newTestGame(t)
	a, ar := join(t, g, "alice")
	b, br := join(t, g, "bob")
	if got := do(t, g, a, "GROUP INVITE bob"); got != "ERR 401 NOT_IN_GROUP" {
		t.Fatal(got)
	}
	if got := do(t, g, a, "GROUP CREATE"); got != "OK group=group.1" {
		t.Fatal(got)
	}
	if got := do(t, g, a, "GROUP CREATE"); got != "ERR 402 ALREADY_IN_GROUP" {
		t.Fatal(got)
	}
	if got := do(t, g, b, "GROUP JOIN alice"); got != "ERR 407 NOT_INVITED" {
		t.Fatal(got)
	}
	expectOK(t, do(t, g, a, "GROUP INVITE bob"))
	if !br.has("EVT GROUP INVITE alice") {
		t.Fatal("no invite event")
	}
	if got := do(t, g, b, "GROUP JOIN alice"); got != "OK group=group.1" {
		t.Fatal(got)
	}
	if !ar.has("EVT GROUP JOIN bob") {
		t.Fatal("no join event")
	}
	expectOK(t, do(t, g, b, "CHAT GROUP secret"))
	if !ar.has("EVT GROUP CHAT bob secret") {
		t.Fatal("no group chat")
	}
	expectOK(t, do(t, g, a, "GROUP LEAVE"))
	if !br.has("EVT GROUP LEAVE alice") || b.Group.Leader != "bob" {
		t.Fatal("leadership not transferred")
	}
}

func fightUntilDone(t *testing.T, g *Game, p *Player, target string) map[string]interface{} {
	t.Helper()
	for i := 0; i < 100; i++ {
		var res map[string]interface{}
		if err := json.Unmarshal([]byte(expectOK(t, do(t, g, p, "ATTACK "+target))), &res); err != nil {
			t.Fatal(err)
		}
		if res["status"] != "combat" {
			return res
		}
	}
	t.Fatal("combat never ended")
	return nil
}

func TestCombatVictoryAndLoot(t *testing.T) {
	g := newTestGame(t)
	p, _ := join(t, g, "alice")
	walk(t, g, p, "west", "south") // graveyard
	p.MaxHP, p.HP = 1000, 1000     // make the test deterministic
	if got := do(t, g, p, "MOVE north"); got != "OK room=loc.chapel" {
		t.Fatal(got)
	}
	walk(t, g, p, "south")
	expectOK(t, do(t, g, p, "ATTACK skeleton"))
	if got := do(t, g, p, "MOVE north"); got != "ERR 303 IN_COMBAT" {
		t.Fatal(got)
	}
	var st statusReply
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "STATUS"))), &st)
	if st.Status != "combat" || !st.InCombat {
		t.Fatalf("status: %+v", st)
	}
	res := fightUntilDone(t, g, p, "skeleton")
	if res["status"] != "victory" {
		t.Fatal(res)
	}
	if got := do(t, g, p, "TAKE bone"); got != "OK taken=item.bone" {
		t.Fatal(got)
	}
	if got := do(t, g, p, "ATTACK skeleton"); got != "ERR 404 NPC_NOT_FOUND" {
		t.Fatal(got)
	}
	if got := do(t, g, p, "DEFEND"); got != "ERR 408 NOT_IN_COMBAT" {
		t.Fatal(got)
	}
}

func TestDeathRespawn(t *testing.T) {
	g := newTestGame(t)
	p, rec := join(t, g, "alice")
	walk(t, g, p, "south", "south", "south") // goblin cave
	p.HP = 1
	res := fightUntilDone(t, g, p, "goblin")
	if res["status"] != "defeated" {
		t.Fatal(res)
	}
	if p.Room != g.W.SafeRoom || p.HP != g.W.Settings.RespawnHP || p.Target != "" {
		t.Fatalf("bad respawn: room=%s hp=%d target=%s", p.Room, p.HP, p.Target)
	}
	if !rec.has("EVT PLAYER RESPAWN " + g.W.SafeRoom) {
		t.Fatal("no respawn event")
	}
	if n := g.npcs["npc.goblin"]; n.HP != n.Def.Stats.HP {
		t.Fatal("idle enemy not healed")
	}
}

func TestFetchQuest(t *testing.T) {
	g := newTestGame(t)
	p, rec := join(t, g, "alice")
	walk(t, g, p, "north")
	var q questReply
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "QUEST baker"))), &q)
	if q.QuestID != "q.fetch_herbs" || q.Status != "active" {
		t.Fatal(q)
	}
	walk(t, g, p, "south", "south", "south", "east")
	expectOK(t, do(t, g, p, "TAKE Healing Herbs"))
	expectOK(t, do(t, g, p, "TAKE herbs"))
	if !rec.has("EVT QUEST PROGRESS q.fetch_herbs 2/3") {
		t.Fatal("no progress event")
	}
	walk(t, g, p, "west")
	expectOK(t, do(t, g, p, "TAKE herbs"))
	walk(t, g, p, "north", "north", "north")
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "QUEST baker"))), &q)
	if q.Status != "completed" || len(q.Granted) != 1 || p.MaxHP != 110 {
		t.Fatalf("%+v maxhp=%d", q, p.MaxHP)
	}
	if got := do(t, g, p, "INVENTORY"); got != `OK ["item.honey_bread"]` {
		t.Fatal(got)
	}
	if got := do(t, g, p, "QUEST baker"); got != "ERR 406 NO_QUEST_AVAILABLE" {
		t.Fatal(got)
	}
	var list []questReply
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "QUESTS"))), &list)
	if len(list) != 1 || list[0].Status != "completed" {
		t.Fatal(list)
	}
	var used map[string]interface{}
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "USE honey bread"))), &used)
	if used["used"] != "item.honey_bread" {
		t.Fatal(used)
	}
}

func TestQuestChain(t *testing.T) {
	g := newTestGame(t)
	p, _ := join(t, g, "alice")
	walk(t, g, p, "east", "east")
	if got := do(t, g, p, "QUEST smith"); got != "ERR 406 NO_QUEST_AVAILABLE" {
		t.Fatalf("prerequisite ignored: %s", got)
	}
	walk(t, g, p, "west", "west", "south")
	var q questReply
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "QUEST guard"))), &q)
	if q.QuestID != "q.deliver_letter" || len(q.Granted) != 1 {
		t.Fatal(q)
	}
	walk(t, g, p, "west", "north") // graveyard -> chapel
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "QUEST priest"))), &q)
	if q.Status != "completed" {
		t.Fatal(q)
	}
	walk(t, g, p, "east", "east", "east")
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "QUEST smith"))), &q)
	if q.QuestID != "q.defeat_goblin" || q.Status != "active" {
		t.Fatal(q)
	}
	walk(t, g, p, "west", "west", "south", "south", "south")
	p.MaxHP, p.HP = 5000, 5000
	if res := fightUntilDone(t, g, p, "Goblin Chief"); res["status"] != "victory" {
		t.Fatal(res)
	}
	walk(t, g, p, "north", "north", "north", "east", "east")
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "QUEST smith"))), &q)
	if q.Status != "completed" || q.Granted[0] != "item.iron_sword" {
		t.Fatal(q)
	}
	if p.Equipped["weapon"] != "item.iron_sword" || g.playerAttack(p) != g.W.Settings.BaseAttack+9+p.Level/2 {
		t.Fatalf("sword not worn: %v attack=%d level=%d", p.Equipped, g.playerAttack(p), p.Level)
	}
}

func TestResolve(t *testing.T) {
	c := []named{
		{id: "item.herbs", defID: "item.herbs", name: "Healing Herbs"},
		{id: "item.herbs#2", defID: "item.herbs", name: "Healing Herbs"},
		{id: "item.healing_potion", defID: "item.healing_potion", name: "Healing Potion"},
	}
	cases := map[string]string{
		"item.herbs#2":   "item.herbs#2",
		"ITEM.HERBS":     "item.herbs",
		"herbs":          "item.herbs",
		"healing potion": "item.healing_potion",
		"healing  herbs": "item.herbs",
		"potion":         "item.healing_potion",
		"heal":           "item.herbs",
		"sword":          "",
	}
	for q, want := range cases {
		if got := resolve(q, c); got != want {
			t.Errorf("resolve(%q) = %q, want %q", q, got, want)
		}
	}
}

func TestAmbush(t *testing.T) {
	g := newTestGame(t)
	g.W.NPCs["npc.wolf"].Aggressive = true
	p, rec := join(t, g, "alice")
	walk(t, g, p, "south")
	rec.take()
	if got := do(t, g, p, "MOVE south"); got != "OK room=loc.forest" {
		t.Fatal(got)
	}
	lines := rec.take()
	if len(lines) < 2 || lines[0] != "OK room=loc.forest" || !strings.HasPrefix(lines[1], "EVT PLAYER AMBUSH npc.wolf ") {
		t.Fatalf("no ambush after reply: %v", lines)
	}
	if p.Target != "npc.wolf" || p.HP >= p.MaxHP {
		t.Fatalf("target=%q hp=%d", p.Target, p.HP)
	}
	if got := do(t, g, p, "MOVE east"); got != "ERR 303 IN_COMBAT" {
		t.Fatal(got)
	}

	// a lethal ambush respawns the player in the safe room
	g2 := newTestGame(t)
	g2.W.NPCs["npc.wolf"].Aggressive = true
	q, qrec := join(t, g2, "bob")
	walk(t, g2, q, "south")
	q.HP = 1
	do(t, g2, q, "MOVE south")
	if q.Room != g2.W.SafeRoom || q.Target != "" || !qrec.has("EVT PLAYER RESPAWN "+g2.W.SafeRoom) {
		t.Fatalf("room=%s target=%s", q.Room, q.Target)
	}
}

func TestAggressiveNPCNotInSafeRoom(t *testing.T) {
	bad := `{"start_room":"a","rooms":{"a":{"name":"A","safe":true,"spawns":[{"npc":"npc.x"}]}},"items":{},"npcs":{"npc.x":{"name":"X","hostile":true,"aggressive":true,"stats":{"hp":5}}},"quests":{}}`
	if _, err := ParseWorld([]byte(bad)); err == nil || !strings.Contains(err.Error(), "safe room") {
		t.Fatalf("expected safe room error, got %v", err)
	}
}
