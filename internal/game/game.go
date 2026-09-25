// Package game holds the authoritative world state and implements every
// RFC 42TAP command. It knows nothing about sockets: players are attached to
// a Sink that receives protocol lines.
package game

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"hollowmere/internal/proto"
	"log/slog"
)

// Sink receives lines destined to one player. Send must not block.
type Sink interface {
	Send(line string)
}

// Room is the live state of a location.
type Room struct {
	ID      string
	Def     *RoomDef
	Items   []string // item instance ids, in arrival order
	NPCs    []string // npc instance ids
	Players []string // player names, in arrival order
}

// Item is a unique item instance.
type Item struct {
	ID     string
	DefID  string
	Def    *ItemDef
	Room   string // room holding the item ("" if held or in limbo)
	Holder string // player holding the item ("" if not held)
	Origin string // room the item regrows in after being consumed
	Owner  string // npc instance dropping this item as loot
}

// NPC is a live NPC instance.
type NPC struct {
	ID       string
	DefID    string
	Def      *NPCDef
	Home     string
	Room     string // "" while dead
	HP       int
	Alive    bool
	Loot     []string        // item instance ids dropped on defeat
	Damagers map[string]bool // players who hurt the npc since its last reset
}

// QuestState tracks a player's progress on one quest.
type QuestState struct {
	ID       string
	Status   string // active, completed
	Progress int
}

// Player is a connected, authenticated player.
type Player struct {
	Name      string
	IP        string
	Sink      Sink
	Room      string
	HP        int
	MaxHP     int
	Inventory []string
	Group     *Group
	Target    string // npc instance id while in combat
	Defending bool
	Quests    map[string]*QuestState
	QuestList []string
	talkIndex map[string]int
}

// Group is a set of players sharing a chat channel.
type Group struct {
	ID      string
	Leader  string
	Members []string
	Invited map[string]bool
}

// Game is the whole mutable world. All access goes through mu.
type Game struct {
	mu        sync.Mutex
	W         *WorldData
	log       *slog.Logger
	rng       *rand.Rand
	rooms     map[string]*Room
	items     map[string]*Item
	npcs      map[string]*NPC
	players   map[string]*Player // key: lower-cased name
	groups    map[string]*Group
	idCount   map[string]int
	nextGroup int
	// Schedule runs f after d. Tests replace it to control time.
	Schedule func(d time.Duration, f func())
}

// New builds the live world from static data.
func New(w *WorldData, log *slog.Logger, seed int64) *Game {
	g := &Game{
		W:       w,
		log:     log,
		rng:     rand.New(rand.NewSource(seed)),
		rooms:   map[string]*Room{},
		items:   map[string]*Item{},
		npcs:    map[string]*NPC{},
		players: map[string]*Player{},
		groups:  map[string]*Group{},
		idCount: map[string]int{},
		Schedule: func(d time.Duration, f func()) {
			time.AfterFunc(d, f)
		},
	}
	for _, id := range sortedKeys(w.Rooms) {
		def := w.Rooms[id]
		r := &Room{ID: id, Def: def}
		g.rooms[id] = r
		for _, it := range def.Items {
			inst := g.newItem(it)
			inst.Origin = id
			g.placeItem(inst, id)
		}
		for _, sp := range def.Spawns {
			for i := 0; i < sp.Count; i++ {
				n := &NPC{
					ID:       g.newID(sp.NPC),
					DefID:    sp.NPC,
					Def:      w.NPCs[sp.NPC],
					Home:     id,
					Damagers: map[string]bool{},
				}
				for _, l := range n.Def.Loot {
					li := g.newItem(l)
					li.Owner = n.ID
					n.Loot = append(n.Loot, li.ID)
				}
				g.npcs[n.ID] = n
				g.spawnNPC(n)
			}
		}
	}
	return g
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// newID returns a unique instance id: the type id for the first instance,
// then "<type>#2", "<type>#3", ...
func (g *Game) newID(defID string) string {
	g.idCount[defID]++
	if n := g.idCount[defID]; n > 1 {
		return fmt.Sprintf("%s#%d", defID, n)
	}
	return defID
}

func (g *Game) newItem(defID string) *Item {
	it := &Item{ID: g.newID(defID), DefID: defID, Def: g.W.Items[defID]}
	g.items[it.ID] = it
	return it
}

func (g *Game) spawnNPC(n *NPC) {
	n.Alive = true
	n.HP = n.Def.Stats.HP
	n.Room = n.Home
	n.Damagers = map[string]bool{}
	r := g.rooms[n.Home]
	r.NPCs = append(r.NPCs, n.ID)
}

func removeString(list []string, v string) []string {
	for i, s := range list {
		if s == v {
			return append(list[:i:i], list[i+1:]...)
		}
	}
	return list
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// placeItem moves an item instance into a room.
func (g *Game) placeItem(it *Item, room string) {
	g.detachItem(it)
	it.Room = room
	g.rooms[room].Items = append(g.rooms[room].Items, it.ID)
}

// giveItem moves an item instance into a player's inventory.
func (g *Game) giveItem(it *Item, p *Player) {
	g.detachItem(it)
	it.Holder = p.Name
	p.Inventory = append(p.Inventory, it.ID)
}

// detachItem removes an item from wherever it is (limbo afterwards).
func (g *Game) detachItem(it *Item) {
	if it.Room != "" {
		r := g.rooms[it.Room]
		r.Items = removeString(r.Items, it.ID)
		it.Room = ""
	}
	if it.Holder != "" {
		if p := g.players[strings.ToLower(it.Holder)]; p != nil {
			p.Inventory = removeString(p.Inventory, it.ID)
		}
		it.Holder = ""
	}
}

// consumeItem uses up an item. World items regrow in their origin room,
// loot returns to its npc, and everything else is destroyed.
func (g *Game) consumeItem(it *Item) {
	g.detachItem(it)
	switch {
	case it.Origin != "":
		delay := time.Duration(g.W.Settings.ItemRegrowSeconds) * time.Second
		id := it.ID
		g.Schedule(delay, func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			cur := g.items[id]
			if cur == nil || cur.Room != "" || cur.Holder != "" {
				return
			}
			g.placeItem(cur, cur.Origin)
			g.log.Info("item_regrown", "item", id, "room", cur.Origin)
			g.broadcastRoom(cur.Origin, "", "EVT ROOM ITEM SPAWN "+id)
		})
	case it.Owner != "":
		// stays in limbo until its npc is defeated again
	default:
		delete(g.items, it.ID)
	}
}

// ---------------------------------------------------------------------------
// Output helpers. Every call happens with g.mu held, so the order of lines
// is identical to the order of state changes.

func (g *Game) send(p *Player, line string) {
	if p != nil && p.Sink != nil {
		p.Sink.Send(line)
	}
}

func (g *Game) player(name string) *Player {
	return g.players[strings.ToLower(name)]
}

// broadcastRoom sends a line to every player in room except the one named.
func (g *Game) broadcastRoom(room, except, line string) {
	r := g.rooms[room]
	if r == nil {
		return
	}
	for _, name := range r.Players {
		if name != except {
			g.send(g.player(name), line)
		}
	}
}

func (g *Game) broadcastAll(line string) {
	for _, name := range g.playerNames() {
		g.send(g.player(name), line)
	}
}

func (g *Game) broadcastGroup(gr *Group, line string) {
	for _, name := range gr.Members {
		g.send(g.player(name), line)
	}
}

func (g *Game) playerNames() []string {
	names := make([]string, 0, len(g.players))
	for _, p := range g.players {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return names
}

func (g *Game) statsEvent() string {
	return fmt.Sprintf("EVT STATS players=%d", len(g.players))
}

func jsonLine(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// PlayerCount returns the number of connected players.
func (g *Game) PlayerCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.players)
}

// ---------------------------------------------------------------------------
// Session lifecycle.

// Connect authenticates a new player. On success the player is placed in the
// start room and the reply "OK connected" has already been sent to sink.
func (g *Game) Connect(name, ip string, sink Sink) (*Player, *proto.Error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !proto.ValidUsername(name) {
		return nil, proto.ErrInvalidName
	}
	key := strings.ToLower(name)
	if _, taken := g.players[key]; taken {
		return nil, proto.ErrNameInUse
	}
	max := g.W.Settings.PlayerMaxHP
	p := &Player{
		Name:      name,
		IP:        ip,
		Sink:      sink,
		Room:      g.W.StartRoom,
		HP:        max,
		MaxHP:     max,
		Quests:    map[string]*QuestState{},
		talkIndex: map[string]int{},
	}
	g.players[key] = p
	r := g.rooms[p.Room]
	r.Players = append(r.Players, p.Name)
	g.send(p, "OK connected")
	g.log.Info("player_joined", "player", name, "ip", ip, "room", p.Room, "online", len(g.players))
	g.broadcastRoom(p.Room, p.Name, "EVT ROOM PRESENCE ENTER "+p.Name)
	g.broadcastAll(g.statsEvent())
	return p, nil
}

// Disconnect removes every trace of the player, then notifies the others.
func (g *Game) Disconnect(p *Player, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := strings.ToLower(p.Name)
	if g.players[key] != p {
		return
	}
	room := p.Room
	// Carried items fall on the floor so nothing disappears from the world.
	dropped := append([]string(nil), p.Inventory...)
	for _, id := range dropped {
		g.placeItem(g.items[id], room)
	}
	target := p.Target
	p.Target = ""
	var gr *Group
	if p.Group != nil {
		gr = p.Group
		g.removeFromGroup(p)
	}
	for _, other := range g.groups {
		delete(other.Invited, key)
	}
	r := g.rooms[room]
	r.Players = removeString(r.Players, p.Name)
	delete(g.players, key)
	p.Sink = nil

	g.log.Info("player_left", "player", p.Name, "ip", p.IP, "room", room, "reason", reason, "dropped_items", dropped, "online", len(g.players))
	if target != "" {
		g.resetNPCIfIdle(target)
	}
	g.broadcastRoom(room, "", "EVT ROOM PRESENCE LEAVE "+p.Name)
	for _, id := range dropped {
		g.broadcastRoom(room, "", "EVT ROOM ITEM DROP "+p.Name+" "+id)
	}
	if gr != nil {
		g.broadcastGroup(gr, "EVT GROUP LEAVE "+p.Name)
	}
	g.broadcastAll(g.statsEvent())
}

// movePlayer relocates a player and emits presence events.
func (g *Game) movePlayer(p *Player, to string) {
	from := p.Room
	g.rooms[from].Players = removeString(g.rooms[from].Players, p.Name)
	g.broadcastRoom(from, p.Name, "EVT ROOM PRESENCE LEAVE "+p.Name)
	p.Room = to
	g.broadcastRoom(to, p.Name, "EVT ROOM PRESENCE ENTER "+p.Name)
	g.rooms[to].Players = append(g.rooms[to].Players, p.Name)
	g.log.Info("player_moved", "player", p.Name, "from", from, "to", to)
	g.ambush(p)
}

// ---------------------------------------------------------------------------
// Name resolution: canonical id, type id, short id, display name, or a
// unique word/prefix of the display name. Matching is case-insensitive and
// multi-word names are supported.

type named struct {
	id    string
	defID string
	name  string
}

func resolve(query string, cands []named) string {
	q := strings.ToLower(strings.Join(strings.Fields(query), " "))
	if q == "" {
		return ""
	}
	short := func(s string) string {
		if i := strings.IndexByte(s, '.'); i >= 0 {
			return s[i+1:]
		}
		return s
	}
	tiers := []func(c named) bool{
		func(c named) bool { return strings.ToLower(c.id) == q },
		func(c named) bool { return strings.ToLower(c.defID) == q || strings.ToLower(short(c.id)) == q },
		func(c named) bool { return strings.ToLower(short(c.defID)) == q || strings.ToLower(c.name) == q },
		func(c named) bool {
			for _, w := range strings.Fields(strings.ToLower(c.name)) {
				if w == q {
					return true
				}
			}
			return false
		},
		func(c named) bool { return strings.HasPrefix(strings.ToLower(c.name), q) },
	}
	for _, match := range tiers {
		for _, c := range cands {
			if match(c) {
				return c.id
			}
		}
	}
	return ""
}

func (g *Game) itemCands(ids []string) []named {
	out := make([]named, 0, len(ids))
	for _, id := range ids {
		it := g.items[id]
		out = append(out, named{id: it.ID, defID: it.DefID, name: it.Def.Name})
	}
	return out
}

func (g *Game) npcCands(room string) []named {
	var out []named
	for _, id := range g.rooms[room].NPCs {
		n := g.npcs[id]
		out = append(out, named{id: n.ID, defID: n.DefID, name: n.Def.Name})
	}
	return out
}

// Announce broadcasts a server notice to every player, e.g. before a
// restart. Clients that do not know the event type ignore it.
func (g *Game) Announce(text string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.log.Info("server_announce", "text", text)
	g.broadcastAll("EVT SERVER " + text)
}
