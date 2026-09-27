package game

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"hollowmere/data"
	"hollowmere/internal/logs"
)

// ---------------------------------------------------------------------------
// World format: zones, validation, warnings

func TestWorldZones(t *testing.T) {
	w, err := LoadWorld("../../data/world")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(w.Rooms); n < 40 || n > 60 {
		t.Errorf("want 40-60 rooms, got %d", n)
	}
	zones := map[string]int{}
	for _, r := range w.Rooms {
		zones[r.Zone]++
	}
	if len(zones) < 5 || zones["village"] == 0 {
		t.Errorf("rooms not spread over zones: %v", zones)
	}
	if w.Name != "Hollowmere" || w.Settings.CritChancePercent != 10 {
		t.Errorf("header not merged: %q %+v", w.Name, w.Settings)
	}
	// The shipped world must stay free of warnings: make check-world
	// prints them, this test fails on them.
	if warns := w.Warnings(); len(warns) != 0 {
		t.Errorf("world warnings:\n  %s", strings.Join(warns, "\n  "))
	}
}

func TestEmbeddedWorldMatchesDisk(t *testing.T) {
	disk, err := LoadWorld("../../data/world")
	if err != nil {
		t.Fatal(err)
	}
	emb, err := LoadWorldFS(data.World, "world")
	if err != nil {
		t.Fatal(err)
	}
	if len(emb.Rooms) != len(disk.Rooms) || len(emb.Quests) != len(disk.Quests) || len(emb.NPCs) != len(disk.NPCs) {
		t.Fatalf("embedded world differs: %d/%d rooms", len(emb.Rooms), len(disk.Rooms))
	}
}

func TestZonesRejectDuplicates(t *testing.T) {
	a := `{"start_room":"a","rooms":{"a":{"name":"A"}}}`
	b := `{"rooms":{"a":{"name":"A again"}}}`
	_, err := ParseZones(map[string][]byte{"one.json": []byte(a), "two.json": []byte(b)})
	if err == nil || !strings.Contains(err.Error(), "room a defined in both one.json and two.json") {
		t.Fatalf("duplicate room accepted: %v", err)
	}
	c := `{"start_room":"b","rooms":{"b":{"name":"B"}}}`
	_, err = ParseZones(map[string][]byte{"one.json": []byte(a), "two.json": []byte(c)})
	if err == nil || !strings.Contains(err.Error(), "start_room defined in both") {
		t.Fatalf("second header accepted: %v", err)
	}
	_, err = ParseZones(map[string][]byte{"bad.json": []byte(`{"rooms":{}, "oops":1}`)})
	if err == nil || !strings.Contains(err.Error(), "bad.json") {
		t.Fatalf("error does not name the zone: %v", err)
	}
}

func TestZonesCrossReference(t *testing.T) {
	village := `{"start_room":"a","rooms":{"a":{"name":"A","exits":{"south":"b"}}}}`
	forest := `{"rooms":{"b":{"name":"B","exits":{"north":"a"},"spawns":[{"npc":"npc.x"}]}},
		"npcs":{"npc.x":{"name":"X","role":"dialogue"}}}`
	w, err := ParseZones(map[string][]byte{"village.json": []byte(village), "forest.json": []byte(forest)})
	if err != nil {
		t.Fatal(err)
	}
	if w.Rooms["a"].Zone != "village" || w.Rooms["b"].Zone != "forest" {
		t.Fatalf("zones: %q %q", w.Rooms["a"].Zone, w.Rooms["b"].Zone)
	}
}

func TestWarnings(t *testing.T) {
	raw := `{"start_room":"a","rooms":{
		"a":{"name":"A","exits":{"east":"b"},"spawns":[{"npc":"npc.giver"}]},
		"b":{"name":"B"}},
	"items":{"item.gem":{"name":"Gem","obtainable":true}},
	"npcs":{
		"npc.giver":{"name":"Giver","role":"quest_giver","dialogue_tree":{
			"start":{"text":"Hi"},
			"lost":{"text":"Nobody gets here"}}},
		"npc.nobody":{"name":"Nobody"},
		"npc.beast":{"name":"Beast","role":"enemy","hostile":true,"stats":{"hp":5}}},
	"quests":{
		"q.gem":{"title":"Gem","giver":"npc.giver","type":"fetch","target":"item.gem"},
		"q.beast":{"title":"Beast","giver":"npc.giver","type":"kill","target":"npc.beast"},
		"q.loop1":{"title":"L1","giver":"npc.giver","type":"visit","target":"b","requires":["q.loop2"]},
		"q.loop2":{"title":"L2","giver":"npc.giver","type":"visit","target":"b","requires":["q.loop1"]}}}`
	w, err := ParseWorld([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(w.Warnings(), "\n")
	for _, want := range []string{
		"one-way exit a -east-> b",
		"room b has no exit",
		"npc npc.nobody has no role",
		"npc npc.nobody is never placed",
		"npc npc.giver: dialogue node lost can never be reached",
		"item item.gem can never be obtained",
		"quest q.gem step 1 cannot be met: item item.gem",
		"quest q.beast step 1 cannot be met: npc npc.beast is never placed",
		"quest q.loop1 requires itself",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing warning %q in:\n%s", want, got)
		}
	}
}

func TestDialogueAndStepValidation(t *testing.T) {
	raw := `{"start_room":"a","rooms":{"a":{"name":"A","spawns":[{"npc":"npc.x"}]}},
	"npcs":{"npc.x":{"name":"X","role":"dialogue","dialogue_tree":{
		"hello":{"text":"Hi","options":[
			{"text":"Go","next":"nowhere"},
			{"text":"Quest","quest":"q.other"},
			{"text":"Odd","if":{"quest":"q.other","status":"maybe"}}]}}},
		"npc.y":{"name":"Y","role":"quest_giver"}},
	"quests":{"q.other":{"title":"O","giver":"npc.y","type":"kill","target":"npc.y",
		"steps":[{"type":"visit","target":"nowhere"},{"type":"dance"}]}}}`
	_, err := ParseWorld([]byte(raw))
	if err == nil {
		t.Fatal("bad dialogue accepted")
	}
	for _, want := range []string{
		`no "start" node`,
		`unknown next node "nowhere"`,
		`quest "q.other" is neither given nor turned in by this npc`,
		`condition status "maybe"`,
		`set either type/target/count or steps`,
		`step 1: unknown target room "nowhere"`,
		`step 2: unknown type "dance"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing error %q in:\n%v", want, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Dialogue trees and quest chains, played through the protocol

type talkResult struct {
	NPC      string       `json:"npc"`
	Dialogue string       `json:"dialogue"`
	Options  []talkOption `json:"options"`
	Quest    *questReply  `json:"quest"`
}

func talk(t *testing.T, g *Game, p *Player, line string) talkResult {
	t.Helper()
	var r talkResult
	if err := json.Unmarshal([]byte(expectOK(t, do(t, g, p, line))), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDialogueTreeAndQuestChain(t *testing.T) {
	g := newTestGame(t)
	p, rec := join(t, g, "alice")

	if got := do(t, g, p, "SAY 1"); got != "ERR 410 NOT_IN_CONVERSATION" {
		t.Fatal(got)
	}
	walk(t, g, p, "ne")
	r := talk(t, g, p, "TALK wenna")
	if r.NPC != "npc.herbalist" || r.Dialogue == "" || len(r.Options) != 3 || r.Options[0].Text != "You look worried." {
		t.Fatalf("start node: %+v", r)
	}
	if got := do(t, g, p, "SAY 9"); got != "ERR 411 INVALID_CHOICE" {
		t.Fatal(got)
	}
	r = talk(t, g, p, "SAY 1")
	if !strings.Contains(r.Dialogue, "moonpetals") || len(r.Options) != 2 {
		t.Fatalf("worried node: %+v", r)
	}
	r = talk(t, g, p, "SAY 1") // "I'll find him for you."
	if r.Quest == nil || r.Quest.QuestID != "q.moonpetal" || r.Quest.Status != "active" || r.Quest.Steps != 2 || r.Quest.Step != 1 {
		t.Fatalf("quest not accepted: %+v", r.Quest)
	}
	if len(r.Options) != 0 {
		t.Fatalf("leaf node should end the conversation: %+v", r)
	}
	if got := do(t, g, p, "SAY 1"); got != "ERR 410 NOT_IN_CONVERSATION" {
		t.Fatalf("conversation still open: %s", got)
	}

	// Step 1: talk to the druid, which moves the quest on at once.
	walk(t, g, p, "sw", "south", "south", "west", "south", "west")
	rec.take()
	r = talk(t, g, p, "TALK thorn")
	if !rec.has("EVT QUEST STEP q.moonpetal 2/2") {
		t.Fatal("talking to the druid did not complete step 1")
	}
	r = talk(t, g, p, "SAY 1") // ask about the petals
	r = talk(t, g, p, "SAY 1") // promise to take only what is needed
	if !p.Flags["thorn_respect"] {
		t.Fatal("choice not remembered")
	}

	// Step 2: two moonpetals, one here and one in the reeds.
	expectOK(t, do(t, g, p, "TAKE moonpetal"))
	walk(t, g, p, "east", "east", "north")
	expectOK(t, do(t, g, p, "TAKE moonpetal"))
	walk(t, g, p, "north", "north", "west", "ne")

	r = talk(t, g, p, "TALK wenna")
	if len(r.Options) == 0 || r.Options[0].Text != "About the moonpetals..." {
		t.Fatalf("turn-in option missing: %+v", r.Options)
	}
	r = talk(t, g, p, "SAY 1")
	if r.Quest == nil || r.Quest.Status != "completed" {
		t.Fatalf("not completed: %+v", r.Quest)
	}
	// Base reward and the bonus earned by the choice made with Thorn.
	if len(r.Quest.Granted) != 2 || !strings.Contains(r.Quest.Message, "Thorn") || p.MaxHP != 110+HPPerLevel*(p.Level-1) {
		t.Fatalf("bonus not granted: %+v maxhp=%d", r.Quest, p.MaxHP)
	}
	if len(g.heldOfType(p, "item.moonpetal")) != 0 {
		t.Fatal("moonpetals not consumed")
	}
	r = talk(t, g, p, "TALK wenna")
	if r.Options[0].Text != "How is the fever?" {
		t.Fatalf("options did not follow the quest: %+v", r.Options)
	}
}

func TestConversationEndsWhenLeaving(t *testing.T) {
	g := newTestGame(t)
	p, _ := join(t, g, "alice")
	walk(t, g, p, "ne")
	talk(t, g, p, "TALK wenna")
	walk(t, g, p, "sw")
	if got := do(t, g, p, "SAY 1"); got != "ERR 410 NOT_IN_CONVERSATION" {
		t.Fatal(got)
	}
	// Old-style NPCs still answer with a single line and no options.
	r := talk(t, g, p, "TALK crier")
	if r.Dialogue == "" || r.Options != nil {
		t.Fatalf("%+v", r)
	}
	if got := do(t, g, p, "SAY 1"); got != "ERR 410 NOT_IN_CONVERSATION" {
		t.Fatal(got)
	}
}

func TestVisitAndKillSteps(t *testing.T) {
	g := newTestGame(t)
	p, rec := join(t, g, "alice")
	p.MaxHP, p.HP = 5000, 5000
	walk(t, g, p, "south", "south", "west", "west", "south")
	var q questReply
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "QUEST dain"))), &q)
	if q.QuestID != "q.mine_trouble" || q.Step != 1 || q.Steps != 2 {
		t.Fatalf("%+v", q)
	}
	walk(t, g, p, "down")
	rec.take()
	walk(t, g, p, "down")
	if !rec.has("EVT QUEST STEP q.mine_trouble 2/2") {
		t.Fatal("visiting did not complete step 1")
	}
	fightUntilDone(t, g, p, "kobold")
	fightUntilDone(t, g, p, "kobold")
	if st := p.Quests["q.mine_trouble"]; st.Step != 1 || st.Progress != 2 {
		t.Fatalf("kills not counted: %+v", st)
	}
	walk(t, g, p, "up", "up")
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "QUEST dain"))), &q)
	if q.Status != "completed" {
		t.Fatalf("%+v", q)
	}
	// The follow-up quest opens, and is turned in to another NPC.
	_ = json.Unmarshal([]byte(expectOK(t, do(t, g, p, "QUEST dain"))), &q)
	if q.QuestID != "q.golem_heart" || q.TurnIn != "npc.smith" {
		t.Fatalf("%+v", q)
	}
}

// ---------------------------------------------------------------------------
// Saves keep the current step and the remembered choices.

type memStore struct{ rows map[string]*StoredPlayer }

func (m *memStore) LoadPlayer(key string) (*StoredPlayer, error) { return m.rows[key], nil }
func (m *memStore) NameOwner(name string) (string, error) {
	for k, sp := range m.rows {
		if strings.EqualFold(sp.Name, name) {
			return k, nil
		}
	}
	return "", nil
}
func (m *memStore) Top(limit int) ([]TopEntry, error) {
	var out []TopEntry
	for _, sp := range m.rows {
		out = append(out, TopEntry{Name: sp.Name, Level: maxInt(sp.Data.Level, 1), XP: sp.Data.XP, Kills: sp.Data.Kills})
	}
	return out, nil
}
func (m *memStore) SavePlayer(p *StoredPlayer) error {
	cp := *p
	m.rows[p.Key] = &cp
	return nil
}

func TestSaveKeepsStepsAndFlags(t *testing.T) {
	w, err := LoadWorld("../../data/world")
	if err != nil {
		t.Fatal(err)
	}
	g := New(w, logs.Discard(), 1)
	g.Schedule = func(time.Duration, func()) {}
	st := &memStore{rows: map[string]*StoredPlayer{}}
	g.SetStore(st)

	p, perr := g.Connect("alice", "", "x", &recorder{})
	if perr != nil {
		t.Fatal(perr)
	}
	p.Quests["q.moonpetal"] = &QuestState{ID: "q.moonpetal", Status: "active", Step: 1}
	p.QuestList = append(p.QuestList, "q.moonpetal")
	p.Flags["thorn_respect"] = true
	key := p.Key
	g.Disconnect(p, "test")

	back, perr := g.Connect("alice", key, "x", &recorder{})
	if perr != nil {
		t.Fatal(perr)
	}
	if s := back.Quests["q.moonpetal"]; s == nil || s.Step != 1 {
		t.Fatalf("step lost: %+v", s)
	}
	if !back.Flags["thorn_respect"] {
		t.Fatal("flag lost")
	}

	// A save written before steps existed loads at step 1.
	old := `{"version":1,"room":"loc.square","hp":80,"max_hp":100,"quests":[{"id":"q.moonpetal","status":"active","progress":0}]}`
	var save PlayerSave
	if err := json.Unmarshal([]byte(old), &save); err != nil {
		t.Fatal(err)
	}
	st.rows["oldkey"] = &StoredPlayer{Key: "oldkey", Name: "bob", Data: save}
	bob, perr := g.Connect("bob", "oldkey", "x", &recorder{})
	if perr != nil {
		t.Fatal(perr)
	}
	if s := bob.Quests["q.moonpetal"]; s == nil || s.Step != 0 || len(bob.Flags) != 0 {
		t.Fatalf("old save: %+v flags=%v", s, bob.Flags)
	}
}
