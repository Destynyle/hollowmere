package game

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"hollowmere/internal/logs"
)

func TestXPCurve(t *testing.T) {
	for n, want := range map[int]int{1: 100, 2: 282, 4: 800, 9: 2700} {
		if got := XPForLevel(n); got != want {
			t.Errorf("XPForLevel(%d) = %d, want %d", n, got, want)
		}
	}
}

func TestKillGivesXPAndLevel(t *testing.T) {
	g := newTestGame(t)
	p, rec := join(t, g, "alice")
	p.MaxHP, p.HP = 1000, 500
	p.XP = XPForLevel(1) - 1
	walk(t, g, p, "west", "south")
	rec.take()
	if res := fightUntilDone(t, g, p, "skeleton"); res["status"] != "victory" {
		t.Fatal(res)
	}
	xp := g.W.NPCs["npc.skeleton"].XP
	if !rec.has("EVT PLAYER XP " + itoa(xp) + " ") {
		t.Fatal("no xp event")
	}
	if p.Level != 2 || p.XP != xp-1 || p.MaxHP != 1010 || p.HP != p.MaxHP || p.Points != StatPointsPerLevel {
		t.Fatalf("level up: level=%d xp=%d max=%d hp=%d points=%d", p.Level, p.XP, p.MaxHP, p.HP, p.Points)
	}
	var st statusReply
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "STATUS"))), &st)
	if st.Level != 2 || st.XPNext != XPForLevel(2) || st.Points != 2 || st.Attack != g.W.Settings.BaseAttack+1 {
		t.Fatalf("status: %+v", st)
	}
}

func TestLevelUpEvent(t *testing.T) {
	g := newTestGame(t)
	p, rec := join(t, g, "alice")
	g.mu.Lock()
	g.gainXP(p, XPForLevel(1)+XPForLevel(2), "test")
	g.mu.Unlock()
	lines := strings.Join(rec.take(), "\n")
	if !strings.Contains(lines, "EVT PLAYER LEVEL 2") || !strings.Contains(lines, "EVT PLAYER LEVEL 3") || p.Level != 3 || p.XP != 0 {
		t.Fatalf("level=%d xp=%d lines:\n%s", p.Level, p.XP, lines)
	}
}

func TestTrain(t *testing.T) {
	g := newTestGame(t)
	p, _ := join(t, g, "alice")
	if got := do(t, g, p, "TRAIN strength"); got != "ERR 415 NO_STAT_POINTS" {
		t.Fatal(got)
	}
	p.Points = 3
	if got := do(t, g, p, "TRAIN charisma"); got != "ERR 400 MALFORMED_COMMAND" {
		t.Fatal(got)
	}
	expectOK(t, do(t, g, p, "TRAIN str"))
	expectOK(t, do(t, g, p, "TRAIN endurance"))
	expectOK(t, do(t, g, p, "TRAIN AGI"))
	if p.Stats != (Stats{1, 1, 1}) || p.Points != 0 {
		t.Fatalf("%+v points=%d", p.Stats, p.Points)
	}
	if g.playerAttack(p) != g.W.Settings.BaseAttack+1 || p.MaxHP != 105 || g.playerSpeed(p) != g.W.Settings.BaseSpeed+1 ||
		g.playerCrit(p) != g.W.Settings.CritChancePercent+CritPerAgility {
		t.Fatalf("stats not applied: atk=%d max=%d", g.playerAttack(p), p.MaxHP)
	}
}

func TestEquipment(t *testing.T) {
	g := newTestGame(t)
	p, rec := join(t, g, "alice")
	base := g.W.Settings.BaseAttack

	expectOK(t, do(t, g, p, "TAKE torch"))
	if !rec.has("EVT PLAYER EQUIP weapon item.torch") || g.playerAttack(p) != base+2 {
		t.Fatal("torch not worn on pickup")
	}
	walk(t, g, p, "south", "south", "east")
	expectOK(t, do(t, g, p, "TAKE dagger"))
	if p.Equipped["weapon"] != "item.torch" {
		t.Fatal("a full slot must not be replaced automatically")
	}
	var r map[string]interface{}
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "EQUIP rusty dagger"))), &r)
	if r["slot"] != "weapon" || r["replaced"] != "item.torch" || g.playerAttack(p) != base+4 {
		t.Fatal(r)
	}
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "EQUIP dagger"))), &r)
	if r["replaced"] != "" {
		t.Fatalf("re-equipping replaced itself: %v", r)
	}
	expectOK(t, do(t, g, p, "UNEQUIP weapon"))
	if g.playerAttack(p) != base {
		t.Fatal("unequip ignored")
	}
	if got := do(t, g, p, "UNEQUIP weapon"); got != "ERR 404 SLOT_EMPTY" {
		t.Fatal(got)
	}
	expectOK(t, do(t, g, p, "EQUIP dagger"))
	expectOK(t, do(t, g, p, "DROP dagger"))
	if p.Equipped["weapon"] != "" {
		t.Fatal("dropped item still worn")
	}
	expectOK(t, do(t, g, p, "TAKE herbs"))
	if got := do(t, g, p, "EQUIP herbs"); got != "ERR 413 NOT_EQUIPPABLE" {
		t.Fatal(got)
	}
	g.mu.Lock()
	sword := g.newItem("item.iron_sword")
	g.giveItem(sword, p)
	g.mu.Unlock()
	if got := do(t, g, p, "EQUIP iron sword"); got != "ERR 412 LEVEL_TOO_LOW" {
		t.Fatal(got)
	}
	p.Level = 2
	expectOK(t, do(t, g, p, "EQUIP iron sword"))

	// armor and amulet add up
	g.mu.Lock()
	vest, amulet := g.newItem("item.leather_vest"), g.newItem("item.blessed_amulet")
	g.giveItem(vest, p)
	g.giveItem(amulet, p)
	g.mu.Unlock()
	expectOK(t, do(t, g, p, "EQUIP vest"))
	expectOK(t, do(t, g, p, "EQUIP amulet"))
	if g.playerDefense(p) != g.W.Settings.BaseDefense+3+4 {
		t.Fatalf("defense %d", g.playerDefense(p))
	}
}

func TestSkills(t *testing.T) {
	g := newTestGame(t)
	p, _ := join(t, g, "alice")
	p.MaxHP, p.HP = 1000, 1000
	if got := do(t, g, p, "SKILL strike"); got != "ERR 408 NOT_IN_COMBAT" {
		t.Fatal(got)
	}
	if got := do(t, g, p, "SKILL parry"); got != "ERR 412 LEVEL_TOO_LOW" {
		t.Fatal(got)
	}
	if got := do(t, g, p, "SKILL fireball"); got != "ERR 404 SKILL_NOT_FOUND" {
		t.Fatal(got)
	}
	walk(t, g, p, "west", "south")
	g.npcs["npc.skeleton"].HP = 10000 // a long fight
	expectOK(t, do(t, g, p, "ATTACK skeleton"))

	var r map[string]interface{}
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "SKILL strike"))), &r)
	if r["skill"] != "strike" || r["status"] != "combat" || r["damage"].(float64) < 2 {
		t.Fatal(r)
	}
	if got := do(t, g, p, "SKILL strike"); got != "ERR 414 SKILL_NOT_READY" {
		t.Fatal(got)
	}
	for i := 0; i < 3; i++ {
		expectOK(t, do(t, g, p, "DEFEND"))
	}
	expectOK(t, do(t, g, p, "SKILL strike")) // ready again after 3 rounds

	p.Level = 3
	before := g.npcs["npc.skeleton"].HP
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "SKILL parry"))), &r)
	if r["counter_damage"].(float64) != 0 || g.npcs["npc.skeleton"].HP >= before {
		t.Fatalf("parry: %v", r)
	}
	p.HP = 10
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "SKILL heal"))), &r)
	if r["healed"].(float64) != float64(20+5*3) {
		t.Fatal(r)
	}
	var list []skillInfo
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "SKILLS"))), &list)
	if len(list) != 3 || list[2].Name != "heal" || list[2].Ready || list[2].Wait != 5 {
		t.Fatalf("%+v", list)
	}
}

func TestSaveV2AndMigration(t *testing.T) {
	w, err := LoadWorld("../../data/world")
	if err != nil {
		t.Fatal(err)
	}
	g := New(w, logs.Discard(), 1)
	g.Schedule = func(time.Duration, func()) {}
	st := &memStore{rows: map[string]*StoredPlayer{}}
	g.SetStore(st)

	// A version 1 save: two quests done, a sword in the bag, no levels.
	old := `{"version":1,"room":"loc.smithy","hp":90,"max_hp":120,"inventory":["item.iron_sword","item.blessed_amulet"],
		"quests":[{"id":"q.deliver_letter","status":"completed","progress":1},{"id":"q.defeat_goblin","status":"completed","progress":1}]}`
	var save PlayerSave
	if err := json.Unmarshal([]byte(old), &save); err != nil {
		t.Fatal(err)
	}
	st.rows["k1"] = &StoredPlayer{Key: "k1", Name: "vet", Data: save}
	p, perr := g.Connect("vet", "k1", "x", &recorder{})
	if perr != nil {
		t.Fatal(perr)
	}
	xp := w.Quests["q.deliver_letter"].Reward.XP + w.Quests["q.defeat_goblin"].Reward.XP
	if p.Level != 2 || p.XP != xp-XPForLevel(1) || p.MaxHP != 130 || p.Points != StatPointsPerLevel {
		t.Fatalf("migration: level=%d xp=%d max=%d points=%d", p.Level, p.XP, p.MaxHP, p.Points)
	}
	if p.Equipped["weapon"] == "" || p.Equipped["amulet"] == "" {
		t.Fatalf("old gear not worn: %v", p.Equipped)
	}

	// Round trip in version 2.
	p.Stats = Stats{Strength: 2}
	p.Points = 0
	g.Disconnect(p, "test")
	if v := st.rows["k1"].Data.Version; v != SaveVersion {
		t.Fatalf("saved as version %d", v)
	}
	back, perr := g.Connect("vet", "k1", "x", &recorder{})
	if perr != nil {
		t.Fatal(perr)
	}
	if back.Level != 2 || back.Stats.Strength != 2 || back.Points != 0 || back.MaxHP != 130 {
		t.Fatalf("v2 lost: %+v level=%d", back.Stats, back.Level)
	}
	if id := back.Equipped["weapon"]; id == "" || g.items[id].DefID != "item.iron_sword" {
		t.Fatalf("equipment lost: %v", back.Equipped)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
