package game

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
	"time"

	"hollowmere/internal/proto"
)

// Persistence, without accounts.
//
// A character is identified by a "resume key": a random secret the client
// stores (browser local storage, or a file for the CLI). There is no
// password and no e-mail; the key alone brings a character back.
//
// Carried items follow a simple rule on logout, so nothing is duplicated
// and nothing is lost for the other players:
//
//   - items that grow in the world (herbs, potions, torches) go back to the
//     room they came from, exactly like dropping them;
//   - everything else (quest rewards, loot) is saved as an item type and
//     recreated when the player comes back, which also frees the loot slot
//     of the enemy that dropped it.

// SaveVersion is bumped when the shape of PlayerSave changes. Optional
// fields (quest steps, flags) read as zero in older saves and need no bump.
//
//	1: room, health, inventory, quests
//	2: level, experience, statistics, equipment
const SaveVersion = 2

// PlayerSave is the character state stored between sessions.
type PlayerSave struct {
	Version   int          `json:"version"`
	Room      string       `json:"room"`
	HP        int          `json:"hp"`
	MaxHP     int          `json:"max_hp"`
	Inventory []string     `json:"inventory"` // item type ids
	Quests    []SavedQuest `json:"quests"`
	Flags     []string     `json:"flags,omitempty"`
	// Version 2
	Level    int               `json:"level,omitempty"`
	XP       int               `json:"xp,omitempty"`
	Stats    Stats             `json:"stats"`
	Points   int               `json:"points,omitempty"`
	Equipped map[string]string `json:"equipped,omitempty"` // slot -> item type id
	// Optional since step 6
	Friends []string `json:"friends,omitempty"`
	Kills   int      `json:"kills,omitempty"`
}

// SavedQuest is one quest line of a save.
type SavedQuest struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Step     int    `json:"step,omitempty"`
	Progress int    `json:"progress"`
}

// StoredPlayer is one row of the character store.
type StoredPlayer struct {
	Key    string
	Name   string
	IP     string // last address, kept for bans; not part of the save
	Data   PlayerSave
	Played time.Duration
}

// Store keeps characters between sessions. A nil Store means the world
// resets when the server restarts.
type Store interface {
	// LoadPlayer returns nil when the key is unknown.
	LoadPlayer(key string) (*StoredPlayer, error)
	// NameOwner returns the key owning a name, or "" when it is free.
	NameOwner(name string) (string, error)
	SavePlayer(p *StoredPlayer) error
	// Top returns the best saved characters (see TopEntry).
	Top(limit int) ([]TopEntry, error)
}

// SetStore enables persistence. It must be called before players connect.
// A store that also implements Moderation enables moderation.
func (g *Game) SetStore(s Store) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.store = s
	if m, ok := s.(Moderation); ok {
		g.mod = m
		g.reloadSanctions()
	}
}

// NewKey returns a fresh resume key: 26 characters of base32, 128 bits of
// entropy, safe to put in a URL or read out loud.
func NewKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand only fails when the system is broken.
		panic("cannot read random bytes: " + err.Error())
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

// capture builds the save of a connected player. Called with g.mu held.
func (g *Game) capture(p *Player) *StoredPlayer {
	save := PlayerSave{
		Version: SaveVersion,
		Room:    p.Room,
		HP:      p.HP,
		MaxHP:   p.MaxHP,
		Quests:  make([]SavedQuest, 0, len(p.QuestList)),
		Level:   p.Level,
		XP:      p.XP,
		Stats:   p.Stats,
		Points:  p.Points,
		Friends: append([]string(nil), p.Friends...),
		Kills:   p.Kills,
	}
	for slot, id := range p.Equipped {
		// World items go home on logout, so only kept items stay worn.
		if it := g.items[id]; it != nil && it.Origin == "" {
			if save.Equipped == nil {
				save.Equipped = map[string]string{}
			}
			save.Equipped[slot] = it.DefID
		}
	}
	for _, id := range p.Inventory {
		if it := g.items[id]; it != nil && it.Origin == "" {
			save.Inventory = append(save.Inventory, it.DefID)
		}
	}
	for _, qid := range p.QuestList {
		st := p.Quests[qid]
		save.Quests = append(save.Quests, SavedQuest{ID: qid, Status: st.Status, Step: st.Step, Progress: st.Progress})
	}
	save.Flags = sortedKeys(p.Flags)
	return &StoredPlayer{Key: p.Key, Name: p.Name, IP: p.IP, Data: save, Played: p.played + time.Since(p.joined)}
}

// restore applies a save to a freshly created player. Called with g.mu held.
func (g *Game) restore(p *Player, sp *StoredPlayer) {
	s := sp.Data
	if _, ok := g.rooms[s.Room]; ok {
		p.Room = s.Room
	}
	if s.MaxHP > 0 {
		p.MaxHP = s.MaxHP
	}
	if s.HP > 0 {
		p.HP = minInt(s.HP, p.MaxHP)
	}
	for _, defID := range s.Inventory {
		if _, ok := g.W.Items[defID]; !ok {
			continue // the item was removed from the world file
		}
		g.giveItem(g.newItem(defID), p)
	}
	for _, q := range s.Quests {
		if _, ok := g.W.Quests[q.ID]; !ok {
			continue
		}
		// The world file may have lost steps since the save.
		step := minInt(maxInt(q.Step, 0), len(g.W.Quests[q.ID].Steps)-1)
		p.Quests[q.ID] = &QuestState{ID: q.ID, Status: q.Status, Step: step, Progress: q.Progress}
		p.QuestList = append(p.QuestList, q.ID)
	}
	for _, f := range s.Flags {
		p.Flags[f] = true
	}
	p.Friends = append([]string(nil), s.Friends...)
	p.Kills = s.Kills
	if s.Version < 2 {
		g.migrateV1(p)
	} else {
		p.Level = minInt(maxInt(s.Level, 1), MaxLevel)
		p.XP, p.Stats, p.Points = s.XP, s.Stats, s.Points
		for _, slot := range Slots {
			defID := s.Equipped[slot]
			for _, id := range p.Inventory {
				if it := g.items[id]; defID != "" && it.DefID == defID && it.Def.Slot == slot {
					p.Equipped[slot] = id
					break
				}
			}
		}
	}
	p.played = sp.Played
}

// migrateV1 upgrades a character saved before levels existed: it gets the
// experience of the quests it already completed, and wears the best items
// it carries, which is what counted in combat back then.
func (g *Game) migrateV1(p *Player) {
	xp := 0
	for _, qid := range p.QuestList {
		if p.Quests[qid].Status == "completed" {
			xp += g.W.Quests[qid].Reward.XP
		}
	}
	for p.Level < MaxLevel && xp >= XPForLevel(p.Level) {
		xp -= XPForLevel(p.Level)
		p.Level++
		p.MaxHP += HPPerLevel
		p.Points += StatPointsPerLevel
	}
	p.XP = xp
	g.equipBest(p)
}

// releaseItems empties the inventory on logout: world items go home, the
// rest is saved as types and its instances are freed.
func (g *Game) releaseItems(p *Player) (returned []string) {
	for _, id := range append([]string(nil), p.Inventory...) {
		it := g.items[id]
		if it == nil {
			continue
		}
		switch {
		case it.Origin != "":
			g.placeItem(it, it.Origin)
			returned = append(returned, it.ID)
		case it.Owner != "":
			g.detachItem(it) // the enemy can drop it again
		default:
			g.detachItem(it)
			delete(g.items, it.ID)
		}
	}
	return returned
}

// persist writes a player to the store, logging failures.
func (g *Game) persist(p *Player) {
	if g.store == nil || p.Key == "" {
		return
	}
	if err := g.store.SavePlayer(g.capture(p)); err != nil {
		g.log.Error("save_failed", "player", p.Name, "error", err.Error())
		return
	}
	g.log.Debug("player_saved", "player", p.Name, "room", p.Room)
}

// SaveAll writes every connected player; called periodically and before
// shutdown so a crash costs at most one interval.
func (g *Game) SaveAll() (n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.store == nil {
		return 0
	}
	for _, p := range g.players {
		g.persist(p)
		n++
	}
	return n
}

// AutoSave saves every connected player on each tick until stop is closed.
func (g *Game) AutoSave(every time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if n := g.SaveAll(); n > 0 {
				g.log.Debug("autosave", "players", n)
			}
			g.ReloadSanctions() // picks up console changes
		case <-stop:
			return
		}
	}
}

// loadForConnect resolves the resume key and the name against the store.
// Called with g.mu held. A nil StoredPlayer means a brand new character.
func (g *Game) loadForConnect(name, key string) (*StoredPlayer, *proto.Error) {
	if g.store == nil {
		return nil, nil
	}
	var sp *StoredPlayer
	if key != "" {
		var err error
		sp, err = g.store.LoadPlayer(key)
		if err != nil {
			g.log.Error("load_failed", "error", err.Error())
			return nil, proto.ErrStorage
		}
	}
	owner, err := g.store.NameOwner(name)
	if err != nil {
		g.log.Error("name_lookup_failed", "error", err.Error())
		return nil, proto.ErrStorage
	}
	if owner != "" && (sp == nil || owner != sp.Key) {
		return nil, proto.ErrNameInUse
	}
	return sp, nil
}

// PlayedString renders a play time for the STATUS reply.
func PlayedString(d time.Duration) string {
	d = d.Round(time.Minute)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%02dm", h, m)
}
