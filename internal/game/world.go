package game

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
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
	Zone        string            `json:"-"` // file the room was loaded from
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Exits       map[string]string `json:"exits"`
	Items       []string          `json:"items"`
	Spawns      []Spawn           `json:"spawns"`
	Safe        bool              `json:"safe"`
	MinGroup    int               `json:"min_group,omitempty"` // group members needed to enter
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
	Attack      int    `json:"attack,omitempty"`    // bonus while equipped
	Defense     int    `json:"defense,omitempty"`   // bonus while equipped
	Heal        int    `json:"heal,omitempty"`      // HP restored by USE (item is consumed)
	Slot        string `json:"slot,omitempty"`      // weapon, armor or amulet; guessed from attack/defense
	MinLevel    int    `json:"min_level,omitempty"` // level needed to equip it
}

// Slots are the equipment slots, in display order.
var Slots = []string{"weapon", "armor", "amulet"}

// NPCDef describes an NPC type.
type NPCDef struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Role        string   `json:"role"` // dialogue, quest_giver, merchant, enemy
	Hostile     bool     `json:"hostile"`
	Aggressive  bool     `json:"aggressive"` // attacks players entering its room
	Dialogue    []string `json:"dialogue"`   // lines cycled by TALK
	// DialogueTree replaces Dialogue with a conversation: TALK opens the
	// "start" node and SAY <n> picks one of its options.
	DialogueTree map[string]*DialogueNode `json:"dialogue_tree,omitempty"`
	Stats        NPCStats                 `json:"stats"`
	Loot         []string                 `json:"loot"`
	XP           int                      `json:"xp,omitempty"` // experience for a kill; derived from stats when 0
	// ScalePerPlayer adds this percentage of health per extra player in
	// the room when a fight starts (group bosses).
	ScalePerPlayer int `json:"scale_per_player,omitempty"`
}

// DialogueNode is one line of a conversation and the answers offered.
// A node without options ends the conversation.
type DialogueNode struct {
	Text    string           `json:"text"`
	Options []DialogueOption `json:"options"`
}

// DialogueOption is an answer the player may choose.
type DialogueOption struct {
	Text    string     `json:"text"`
	Next    string     `json:"next"`               // node to show next; "" ends the conversation
	If      *Condition `json:"if,omitempty"`       // hidden unless the condition holds
	Quest   string     `json:"quest,omitempty"`    // accepts or turns in this quest when chosen
	SetFlag string     `json:"set_flag,omitempty"` // remembers the choice on the character
}

// Condition gates a dialogue option or a bonus reward. Every field that is
// set must hold.
type Condition struct {
	Quest  string `json:"quest,omitempty"`
	Status string `json:"status,omitempty"`  // with quest: none, available, active, ready, completed, not_completed
	Flag   string `json:"flag,omitempty"`    // the character has this flag
	NoFlag string `json:"no_flag,omitempty"` // the character does not have this flag
	Item   string `json:"item,omitempty"`    // the character carries this item type
}

// QuestStatuses are the values accepted in Condition.Status.
var QuestStatuses = map[string]bool{
	"none": true, "available": true, "active": true, "ready": true, "completed": true, "not_completed": true,
}

// NPCStats are combat statistics of an NPC.
type NPCStats struct {
	HP      int `json:"hp"`
	Attack  int `json:"attack"`
	Defense int `json:"defense"`
	Speed   int `json:"speed"`
}

// QuestDef describes a quest. A simple quest has one objective (type,
// target, count); a chain lists its objectives in steps instead.
type QuestDef struct {
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Giver       string      `json:"giver"`   // NPC type offering the quest
	TurnIn      string      `json:"turn_in"` // NPC type accepting completion (defaults to giver)
	Type        string      `json:"type"`    // fetch, deliver, kill, visit, talk
	Target      string      `json:"target"`  // item type, NPC type or room, depending on type
	Count       int         `json:"count"`
	Steps       []QuestStep `json:"steps,omitempty"`
	GiveOnStart []string    `json:"give_on_start"` // items handed to the player on acceptance
	Requires    []string    `json:"requires"`      // quests that must be completed first
	Reward      Reward      `json:"reward"`
	simple      bool        // steps were built from type/target/count
}

// Last returns the step handed in to complete the quest.
func (q *QuestDef) Last() QuestStep { return q.Steps[len(q.Steps)-1] }

// QuestStep is one objective of a quest. Every step but the last completes
// on its own as soon as it is met; the last one is handed in to the
// turn-in NPC, which consumes the items of a fetch or deliver step.
type QuestStep struct {
	Description string `json:"description"`
	Type        string `json:"type"`
	Target      string `json:"target"`
	Count       int    `json:"count"`
}

// QuestTypes lists the objective types.
var QuestTypes = map[string]bool{"fetch": true, "deliver": true, "kill": true, "visit": true, "talk": true}

// Reward is granted on quest completion.
type Reward struct {
	XP       int      `json:"xp"` // 50 per step when 0
	Items    []string `json:"items"`
	MaxHP    int      `json:"max_hp"`
	HealFull bool     `json:"heal_full"`
	Bonus    []Bonus  `json:"bonus,omitempty"`
}

// Bonus is an extra reward granted only when its condition holds at
// completion time.
type Bonus struct {
	If      Condition `json:"if"`
	Items   []string  `json:"items"`
	MaxHP   int       `json:"max_hp"`
	Message string    `json:"message"`
}

// LoadWorld reads and validates a world. path is either a single JSON file
// or a directory whose *.json files (one zone each) are merged.
func LoadWorld(dir string) (*WorldData, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		raw, err := os.ReadFile(dir)
		if err != nil {
			return nil, err
		}
		return ParseWorld(raw)
	}
	return LoadWorldFS(os.DirFS(dir), ".")
}

// LoadWorldFS merges the *.json zone files of dir inside fsys (an
// embedded copy of data/world, for instance).
func LoadWorldFS(fsys fs.FS, dir string) (*WorldData, error) {
	files, err := fs.Glob(fsys, path.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("world: no *.json file in %s", dir)
	}
	zones := make(map[string][]byte, len(files))
	for _, f := range files {
		raw, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		zones[path.Base(f)] = raw
	}
	return ParseZones(zones)
}

// ParseWorld decodes and validates a world held in one JSON document.
func ParseWorld(raw []byte) (*WorldData, error) {
	return ParseZones(map[string][]byte{"world.json": raw})
}

// ParseZones decodes zone files (name -> JSON), merges them and validates
// the result. An id may be defined in one zone only; the world header
// (name, start_room, safe_room, settings) may appear in any single zone.
func ParseZones(zones map[string][]byte) (*WorldData, error) {
	w := &WorldData{
		Rooms:  map[string]*RoomDef{},
		Items:  map[string]*ItemDef{},
		NPCs:   map[string]*NPCDef{},
		Quests: map[string]*QuestDef{},
	}
	owner := map[string]string{} // "kind id" or header field -> zone file
	claim := func(key, zone string) error {
		if prev, ok := owner[key]; ok {
			return fmt.Errorf("world: %s defined in both %s and %s", key, prev, zone)
		}
		owner[key] = zone
		return nil
	}
	for _, zone := range sortedKeys(zones) {
		var z struct {
			WorldData
			Settings *Settings `json:"settings"`
		}
		dec := json.NewDecoder(strings.NewReader(string(zones[zone])))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&z); err != nil {
			return nil, fmt.Errorf("world: %s: %w", zone, err)
		}
		header := []struct {
			key, val string
			dst      *string
		}{
			{"name", z.Name, &w.Name},
			{"start_room", z.StartRoom, &w.StartRoom},
			{"safe_room", z.SafeRoom, &w.SafeRoom},
		}
		for _, h := range header {
			if h.val == "" {
				continue
			}
			if err := claim(h.key, zone); err != nil {
				return nil, err
			}
			*h.dst = h.val
		}
		if z.Settings != nil {
			if err := claim("settings", zone); err != nil {
				return nil, err
			}
			w.Settings = *z.Settings
		}
		stem := strings.TrimSuffix(zone, filepath.Ext(zone))
		for id, r := range z.Rooms {
			if err := claim("room "+id, zone); err != nil {
				return nil, err
			}
			r.Zone = stem
			w.Rooms[id] = r
		}
		if err := mergeDefs(w.Items, z.Items, "item", zone, claim); err != nil {
			return nil, err
		}
		if err := mergeDefs(w.NPCs, z.NPCs, "npc", zone, claim); err != nil {
			return nil, err
		}
		if err := mergeDefs(w.Quests, z.Quests, "quest", zone, claim); err != nil {
			return nil, err
		}
	}
	w.applyDefaults()
	if err := w.Validate(); err != nil {
		return nil, err
	}
	return w, nil
}

func mergeDefs[V any](dst, src map[string]V, kind, zone string, claim func(key, zone string) error) error {
	for id, v := range src {
		if err := claim(kind+" "+id, zone); err != nil {
			return err
		}
		dst[id] = v
	}
	return nil
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
	for _, it := range w.Items {
		if it.Slot == "" {
			switch {
			case it.Attack > 0:
				it.Slot = "weapon"
			case it.Defense > 0:
				it.Slot = "armor"
			}
		}
		if it.Slot != "" && it.MinLevel < 1 {
			it.MinLevel = 1
		}
	}
	for _, n := range w.NPCs {
		if n.Hostile && n.XP <= 0 {
			n.XP = n.Stats.HP/2 + 2*n.Stats.Attack + 2*n.Stats.Defense
		}
	}
	for _, q := range w.Quests {
		if q.TurnIn == "" {
			q.TurnIn = q.Giver
		}
		if len(q.Steps) == 0 {
			// A simple quest is a chain of one step.
			q.Steps = []QuestStep{{Description: q.Description, Type: q.Type, Target: q.Target, Count: q.Count}}
			q.simple = true
		}
		for i := range q.Steps {
			if q.Steps[i].Count <= 0 {
				q.Steps[i].Count = 1
			}
		}
		if q.Reward.XP <= 0 {
			q.Reward.XP = 50 * len(q.Steps)
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
		if r.MinGroup < 0 || r.MinGroup > 8 {
			add("room %s: min_group must be between 0 and 8", id)
		}
		if r.MinGroup > 1 && id == w.StartRoom {
			add("room %s: the start room cannot require a group", id)
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
		if it.Slot != "" && !containsString(Slots, it.Slot) {
			add("item %s: unknown slot %q", id, it.Slot)
		}
		if it.Slot != "" && !it.Obtainable {
			add("item %s: equipment must be obtainable", id)
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
		if n.ScalePerPlayer < 0 {
			add("npc %s: scale_per_player cannot be negative", id)
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
		if !q.simple && (q.Type != "" || q.Target != "" || q.Count != 0) {
			add("quest %s: set either type/target/count or steps, not both", id)
		}
		for i, st := range q.Steps {
			where := fmt.Sprintf("quest %s", id)
			if !q.simple {
				where = fmt.Sprintf("quest %s step %d", id, i+1)
			}
			switch st.Type {
			case "fetch", "deliver":
				if _, ok := w.Items[st.Target]; !ok {
					add("%s: unknown target item %q", where, st.Target)
				}
			case "kill":
				if n, ok := w.NPCs[st.Target]; !ok || !n.Hostile {
					add("%s: target %q is not a hostile npc", where, st.Target)
				}
			case "talk":
				if _, ok := w.NPCs[st.Target]; !ok {
					add("%s: unknown target npc %q", where, st.Target)
				}
			case "visit":
				if _, ok := w.Rooms[st.Target]; !ok {
					add("%s: unknown target room %q", where, st.Target)
				}
			default:
				add("%s: unknown type %q", where, st.Type)
			}
		}
		items := append(append([]string{}, q.GiveOnStart...), q.Reward.Items...)
		for i, b := range q.Reward.Bonus {
			items = append(items, b.Items...)
			w.checkCondition(&b.If, fmt.Sprintf("quest %s bonus %d", id, i+1), add)
		}
		for _, it := range items {
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
	for id, n := range w.NPCs {
		if n.DialogueTree == nil {
			continue
		}
		if _, ok := n.DialogueTree["start"]; !ok {
			add("npc %s: dialogue_tree has no \"start\" node", id)
		}
		for nid, node := range n.DialogueTree {
			where := fmt.Sprintf("npc %s node %s", id, nid)
			if node == nil || node.Text == "" {
				add("%s: missing text", where)
				continue
			}
			for i, o := range node.Options {
				ow := fmt.Sprintf("%s option %d", where, i+1)
				if o.Text == "" {
					add("%s: missing text", ow)
				}
				if _, ok := n.DialogueTree[o.Next]; o.Next != "" && !ok {
					add("%s: unknown next node %q", ow, o.Next)
				}
				if o.If != nil {
					w.checkCondition(o.If, ow, add)
				}
				if o.Quest != "" {
					q, ok := w.Quests[o.Quest]
					if !ok {
						add("%s: unknown quest %q", ow, o.Quest)
					} else if q.Giver != id && q.TurnIn != id {
						add("%s: quest %q is neither given nor turned in by this npc", ow, o.Quest)
					}
				}
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

func (w *WorldData) checkCondition(c *Condition, where string, add func(string, ...interface{})) {
	if *c == (Condition{}) {
		add("%s: empty condition", where)
	}
	if c.Quest != "" {
		if _, ok := w.Quests[c.Quest]; !ok {
			add("%s: condition on unknown quest %q", where, c.Quest)
		}
		if !QuestStatuses[c.Status] {
			add("%s: condition status %q must be one of none, available, active, ready, completed, not_completed", where, c.Status)
		}
	} else if c.Status != "" {
		add("%s: condition status without a quest", where)
	}
	if c.Item != "" {
		if _, ok := w.Items[c.Item]; !ok {
			add("%s: condition on unknown item %q", where, c.Item)
		}
	}
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

// Warnings lists design problems that do not prevent the server from
// running but probably are mistakes: one-way exits, dead ends, NPCs without
// a role or never placed, items nobody can ever get, quests nobody can
// finish, and dialogue nodes nobody can reach.
func (w *WorldData) Warnings() []string {
	var out []string
	add := func(format string, a ...interface{}) { out = append(out, fmt.Sprintf(format, a...)) }

	for _, e := range w.OneWayExits() {
		add("one-way exit %s", e)
	}
	spawned := map[string]bool{}
	for id, r := range w.Rooms {
		if len(r.Exits) == 0 && len(w.Rooms) > 1 {
			add("room %s has no exit", id)
		}
		for _, sp := range r.Spawns {
			spawned[sp.NPC] = true
		}
	}

	// Where each item type can come from.
	available := map[string]bool{}
	for _, r := range w.Rooms {
		for _, it := range r.Items {
			if w.Items[it].Obtainable {
				available[it] = true
			}
		}
	}
	for id, n := range w.NPCs {
		if spawned[id] {
			for _, it := range n.Loot {
				available[it] = true
			}
		}
	}
	for _, q := range w.Quests {
		for _, it := range q.GiveOnStart {
			available[it] = true
		}
		for _, it := range q.Reward.Items {
			available[it] = true
		}
		for _, b := range q.Reward.Bonus {
			for _, it := range b.Items {
				available[it] = true
			}
		}
	}

	for id, n := range w.NPCs {
		if n.Role == "" {
			add("npc %s has no role", id)
		}
		if !spawned[id] {
			add("npc %s is never placed in a room", id)
		}
		for _, nid := range unreachableNodes(n.DialogueTree) {
			add("npc %s: dialogue node %s can never be reached", id, nid)
		}
	}
	for id, it := range w.Items {
		placed := false
		for _, r := range w.Rooms {
			if containsString(r.Items, id) {
				placed = true
				break
			}
		}
		if it.Obtainable && !available[id] {
			add("item %s can never be obtained", id)
		} else if !it.Obtainable && !placed {
			add("item %s is not obtainable and placed nowhere", id)
		}
	}
	for id, q := range w.Quests {
		if !spawned[q.Giver] {
			add("quest %s cannot start: giver %s is never placed", id, q.Giver)
		}
		if !spawned[q.TurnIn] {
			add("quest %s cannot finish: turn_in %s is never placed", id, q.TurnIn)
		}
		for i, st := range q.Steps {
			switch st.Type {
			case "fetch", "deliver":
				if !available[st.Target] {
					add("quest %s step %d cannot be met: item %s can never be obtained", id, i+1, st.Target)
				}
			case "kill", "talk":
				if !spawned[st.Target] {
					add("quest %s step %d cannot be met: npc %s is never placed", id, i+1, st.Target)
				}
			}
		}
		if requiresItself(w.Quests, id) {
			add("quest %s requires itself through its prerequisites", id)
		}
	}
	sort.Strings(out)
	return out
}

// unreachableNodes returns the nodes of a dialogue tree that no path from
// "start" leads to.
func unreachableNodes(tree map[string]*DialogueNode) []string {
	if tree["start"] == nil {
		return nil
	}
	seen := map[string]bool{"start": true}
	queue := []string{"start"}
	for len(queue) > 0 {
		cur := tree[queue[0]]
		queue = queue[1:]
		for _, o := range cur.Options {
			if next := tree[o.Next]; next != nil && !seen[o.Next] {
				seen[o.Next] = true
				queue = append(queue, o.Next)
			}
		}
	}
	var out []string
	for id := range tree {
		if !seen[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// requiresItself reports whether a quest appears among its own
// prerequisites, directly or not.
func requiresItself(quests map[string]*QuestDef, id string) bool {
	seen := map[string]bool{}
	stack := append([]string{}, quests[id].Requires...)
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == id {
			return true
		}
		if q, ok := quests[cur]; ok && !seen[cur] {
			seen[cur] = true
			stack = append(stack, q.Requires...)
		}
	}
	return false
}
