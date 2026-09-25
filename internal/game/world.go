package game

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// WorldData is the static world description loaded from JSON.
type WorldData struct {
	Name      string               `json:"name"`
	StartRoom string               `json:"start_room"`
	SafeRoom  string               `json:"safe_room"`
	Rooms     map[string]*RoomDef  `json:"rooms"`
	Items     map[string]*ItemDef  `json:"items"`
	NPCs      map[string]*NPCDef   `json:"npcs"`
	Quests    map[string]*QuestDef `json:"quests"`
	Settings  Settings             `json:"settings"`
}

// Settings are tunable game constants.
type Settings struct {
	PlayerMaxHP       int `json:"player_max_hp"`
	RespawnHP         int `json:"respawn_hp"`
	BaseAttack        int `json:"base_attack"`
	BaseDefense       int `json:"base_defense"`
	BaseSpeed         int `json:"base_speed"`
	NPCRespawnSeconds int `json:"npc_respawn_seconds"`
	ItemRegrowSeconds int `json:"item_regrow_seconds"`
	CritChancePercent int `json:"crit_chance_percent"`
	FleeChancePercent int `json:"flee_chance_percent"`
}

// RoomDef describes a location.
type RoomDef struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Exits       map[string]string `json:"exits"`
	Items       []string          `json:"items"`
	Spawns      []Spawn           `json:"spawns"`
	Safe        bool              `json:"safe"`
}

// Spawn places count NPCs of a given type in a room.
type Spawn struct {
	NPC   string `json:"npc"`
	Count int    `json:"count"`
}

// ItemDef describes an item type.
type ItemDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Obtainable  bool   `json:"obtainable"`
	Attack      int    `json:"attack,omitempty"`  // bonus while carried (best weapon counts)
	Defense     int    `json:"defense,omitempty"` // bonus while carried (best armor counts)
	Heal        int    `json:"heal,omitempty"`    // HP restored by USE (item is consumed)
}

// NPCDef describes an NPC type.
type NPCDef struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Role        string   `json:"role"` // dialogue, quest_giver, merchant, enemy
	Hostile     bool     `json:"hostile"`
	Aggressive  bool     `json:"aggressive"` // attacks players entering its room
	Dialogue    []string `json:"dialogue"`
	Stats       NPCStats `json:"stats"`
	Loot        []string `json:"loot"`
}

// NPCStats are combat statistics of an NPC.
type NPCStats struct {
	HP      int `json:"hp"`
	Attack  int `json:"attack"`
	Defense int `json:"defense"`
	Speed   int `json:"speed"`
}

// QuestDef describes a quest.
type QuestDef struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Giver       string   `json:"giver"`   // NPC type offering the quest
	TurnIn      string   `json:"turn_in"` // NPC type accepting completion (defaults to giver)
	Type        string   `json:"type"`    // fetch, kill, deliver
	Target      string   `json:"target"`  // item type (fetch/deliver) or NPC type (kill)
	Count       int      `json:"count"`
	GiveOnStart []string `json:"give_on_start"` // items handed to the player on acceptance
	Requires    []string `json:"requires"`      // quests that must be completed first
	Reward      Reward   `json:"reward"`
}

// Reward is granted on quest completion.
type Reward struct {
	Items    []string `json:"items"`
	MaxHP    int      `json:"max_hp"`
	HealFull bool     `json:"heal_full"`
}

// LoadWorld reads and validates a world file.
func LoadWorld(path string) (*WorldData, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseWorld(raw)
}

// ParseWorld decodes and validates world JSON.
func ParseWorld(raw []byte) (*WorldData, error) {
	var w WorldData
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("world: %w", err)
	}
	w.applyDefaults()
	if err := w.Validate(); err != nil {
		return nil, err
	}
	return &w, nil
}

func (w *WorldData) applyDefaults() {
	s := &w.Settings
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&s.PlayerMaxHP, 100)
	def(&s.RespawnHP, 50)
	def(&s.BaseAttack, 6)
	def(&s.BaseDefense, 1)
	def(&s.BaseSpeed, 5)
	def(&s.NPCRespawnSeconds, 60)
	def(&s.ItemRegrowSeconds, 30)
	if s.CritChancePercent < 0 {
		s.CritChancePercent = 0
	}
	def(&s.FleeChancePercent, 60)
	if w.SafeRoom == "" {
		w.SafeRoom = w.StartRoom
	}
	for _, q := range w.Quests {
		if q.TurnIn == "" {
			q.TurnIn = q.Giver
		}
		if q.Count <= 0 {
			q.Count = 1
		}
	}
	for _, r := range w.Rooms {
		for i := range r.Spawns {
			if r.Spawns[i].Count <= 0 {
				r.Spawns[i].Count = 1
			}
		}
	}
}

// Directions accepted in exits.
var Directions = map[string]bool{
	"north": true, "south": true, "east": true, "west": true, "up": true, "down": true,
	"northeast": true, "northwest": true, "southeast": true, "southwest": true,
}

// Validate checks that every reference in the world is correct and that the
// map is fully reachable from the start room.
func (w *WorldData) Validate() error {
	var errs []string
	add := func(format string, a ...interface{}) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if len(w.Rooms) == 0 {
		add("no rooms defined")
	}
	if _, ok := w.Rooms[w.StartRoom]; !ok {
		add("start_room %q does not exist", w.StartRoom)
	}
	if _, ok := w.Rooms[w.SafeRoom]; !ok {
		add("safe_room %q does not exist", w.SafeRoom)
	}
	for id, r := range w.Rooms {
		if r.Name == "" {
			add("room %s: missing name", id)
		}
		for dir, to := range r.Exits {
			if !Directions[dir] {
				add("room %s: unknown direction %q", id, dir)
			}
			if _, ok := w.Rooms[to]; !ok {
				add("room %s: exit %s leads to unknown room %q", id, dir, to)
			}
		}
		for _, it := range r.Items {
			if _, ok := w.Items[it]; !ok {
				add("room %s: unknown item %q", id, it)
			}
		}
		for _, sp := range r.Spawns {
			n, ok := w.NPCs[sp.NPC]
			if !ok {
				add("room %s: unknown npc %q", id, sp.NPC)
			} else if n.Aggressive && (r.Safe || id == w.SafeRoom) {
				add("room %s: aggressive npc %q in a safe room", id, sp.NPC)
			}
		}
	}
	for id, it := range w.Items {
		if it.Name == "" {
			add("item %s: missing name", id)
		}
	}
	for id, n := range w.NPCs {
		if n.Name == "" {
			add("npc %s: missing name", id)
		}
		if n.Aggressive && !n.Hostile {
			add("npc %s: aggressive npc must be hostile", id)
		}
		if n.Hostile && n.Stats.HP <= 0 {
			add("npc %s: hostile npc needs stats.hp > 0", id)
		}
		for _, it := range n.Loot {
			if _, ok := w.Items[it]; !ok {
				add("npc %s: unknown loot item %q", id, it)
			}
		}
	}
	for id, q := range w.Quests {
		if _, ok := w.NPCs[q.Giver]; !ok {
			add("quest %s: unknown giver %q", id, q.Giver)
		}
		if _, ok := w.NPCs[q.TurnIn]; !ok {
			add("quest %s: unknown turn_in npc %q", id, q.TurnIn)
		}
		switch q.Type {
		case "fetch", "deliver":
			if _, ok := w.Items[q.Target]; !ok {
				add("quest %s: unknown target item %q", id, q.Target)
			}
		case "kill":
			if n, ok := w.NPCs[q.Target]; !ok || !n.Hostile {
				add("quest %s: target %q is not a hostile npc", id, q.Target)
			}
		default:
			add("quest %s: unknown type %q", id, q.Type)
		}
		for _, it := range append(append([]string{}, q.GiveOnStart...), q.Reward.Items...) {
			if _, ok := w.Items[it]; !ok {
				add("quest %s: unknown item %q", id, it)
			}
		}
		for _, r := range q.Requires {
			if _, ok := w.Quests[r]; !ok {
				add("quest %s: unknown prerequisite %q", id, r)
			}
		}
	}
	if _, ok := w.Rooms[w.StartRoom]; ok {
		seen := map[string]bool{w.StartRoom: true}
		queue := []string{w.StartRoom}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, to := range w.Rooms[cur].Exits {
				if _, ok := w.Rooms[to]; ok && !seen[to] {
					seen[to] = true
					queue = append(queue, to)
				}
			}
		}
		for id := range w.Rooms {
			if !seen[id] {
				add("room %s is unreachable from %s", id, w.StartRoom)
			}
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("invalid world:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// OneWayExits lists exits with no way back (reported as warnings).
func (w *WorldData) OneWayExits() []string {
	var out []string
	for id, r := range w.Rooms {
		for dir, to := range r.Exits {
			back := false
			for _, t := range w.Rooms[to].Exits {
				if t == id {
					back = true
					break
				}
			}
			if !back {
				out = append(out, fmt.Sprintf("%s -%s-> %s", id, dir, to))
			}
		}
	}
	sort.Strings(out)
	return out
}
