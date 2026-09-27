package game

import (
	"fmt"
	"math"
	"strings"

	"hollowmere/internal/proto"
)

// Character progression: experience and levels, trained statistics and
// worn equipment.
//
// Experience comes from defeating enemies (every player who hurt the enemy
// gets it) and from completing quests. Each level asks for
// XPForLevel(level) more points; reaching it gives +10 max HP, +1 attack
// every second level, a full heal and StatPointsPerLevel points to spend
// with TRAIN.

// Stats are the trained statistics of a character.
type Stats struct {
	Strength  int `json:"strength"`  // +1 attack per point
	Agility   int `json:"agility"`   // +1 speed and +2% critical chance per point
	Endurance int `json:"endurance"` // +5 max HP per point
}

// Progression constants.
const (
	MaxLevel           = 20
	StatPointsPerLevel = 2
	HPPerLevel         = 10
	HPPerEndurance     = 5
	CritPerAgility     = 2
	MaxCritPercent     = 50
)

// XPForLevel is the experience needed to go from level n to level n+1:
// 100 × n^1.5.
func XPForLevel(n int) int {
	return int(100 * math.Pow(float64(n), 1.5))
}

// gainXP credits experience and applies level-ups.
func (g *Game) gainXP(p *Player, amount int, reason string) {
	if amount <= 0 || p.Level >= MaxLevel {
		return
	}
	p.XP += amount
	g.log.Info("player_xp", "player", p.Name, "gained", amount, "reason", reason, "xp", p.XP, "level", p.Level)
	g.send(p, fmt.Sprintf("EVT PLAYER XP %d %d/%d", amount, p.XP, XPForLevel(p.Level)))
	for p.Level < MaxLevel && p.XP >= XPForLevel(p.Level) {
		p.XP -= XPForLevel(p.Level)
		p.Level++
		p.MaxHP += HPPerLevel
		p.HP = p.MaxHP
		p.Points += StatPointsPerLevel
		g.log.Info("player_level", "player", p.Name, "level", p.Level, "max_hp", p.MaxHP)
		g.send(p, fmt.Sprintf("EVT PLAYER LEVEL %d", p.Level))
	}
	if p.Level >= MaxLevel {
		p.XP = 0
	}
}

// equipped returns the item worn in a slot, or nil.
func (g *Game) equipped(p *Player, slot string) *Item {
	if id := p.Equipped[slot]; id != "" {
		return g.items[id]
	}
	return nil
}

func (g *Game) playerAttack(p *Player) int {
	atk := g.W.Settings.BaseAttack + p.Stats.Strength + p.Level/2
	for _, slot := range Slots {
		if it := g.equipped(p, slot); it != nil {
			atk += it.Def.Attack
		}
	}
	return atk
}

func (g *Game) playerDefense(p *Player) int {
	def := g.W.Settings.BaseDefense
	for _, slot := range Slots {
		if it := g.equipped(p, slot); it != nil {
			def += it.Def.Defense
		}
	}
	return def
}

func (g *Game) playerSpeed(p *Player) int {
	return g.W.Settings.BaseSpeed + p.Stats.Agility
}

func (g *Game) playerCrit(p *Player) int {
	return minInt(MaxCritPercent, g.W.Settings.CritChancePercent+CritPerAgility*p.Stats.Agility)
}

// canEquip explains why an item cannot be worn, or returns nil.
func (g *Game) canEquip(p *Player, it *Item) *proto.Error {
	if it.Def.Slot == "" {
		return proto.ErrNotEquippable
	}
	if p.Level < it.Def.MinLevel {
		return proto.ErrLevelTooLow
	}
	return nil
}

// equip wears an item, returning the one it replaced ("" if none).
func (g *Game) equip(p *Player, it *Item) string {
	slot := it.Def.Slot
	old := p.Equipped[slot]
	if old == it.ID {
		return ""
	}
	p.Equipped[slot] = it.ID
	g.log.Info("item_equipped", "player", p.Name, "item", it.ID, "slot", slot, "replaced", old)
	return old
}

// autoEquip wears a newly received item when its slot is empty, so a new
// sword is not left in the bag by mistake.
func (g *Game) autoEquip(p *Player, it *Item) {
	slot := it.Def.Slot
	if slot == "" || p.Equipped[slot] != "" || g.canEquip(p, it) != nil {
		return
	}
	g.equip(p, it)
	g.send(p, fmt.Sprintf("EVT PLAYER EQUIP %s %s", slot, it.ID))
}

// equipBest fills empty slots with the strongest wearable items carried.
func (g *Game) equipBest(p *Player) {
	for _, slot := range Slots {
		if p.Equipped[slot] != "" {
			continue
		}
		var best *Item
		for _, id := range p.Inventory {
			it := g.items[id]
			if it.Def.Slot != slot || g.canEquip(p, it) != nil {
				continue
			}
			if best == nil || it.Def.Attack+it.Def.Defense > best.Def.Attack+best.Def.Defense {
				best = it
			}
		}
		if best != nil {
			p.Equipped[slot] = best.ID
		}
	}
}

// ---------------------------------------------------------------------------
// Commands

func (g *Game) cmdEquip(p *Player, c proto.Command) (string, *proto.Error) {
	if err := requireArgs(c); err != nil {
		return "", err
	}
	id := resolve(c.Args, g.itemCands(p.Inventory))
	if id == "" {
		return "", proto.ErrItemNotInInventory
	}
	it := g.items[id]
	if err := g.canEquip(p, it); err != nil {
		return "", err
	}
	old := g.equip(p, it)
	return jsonLine(map[string]interface{}{
		"slot": it.Def.Slot, "item": id, "replaced": old,
		"attack": g.playerAttack(p), "defense": g.playerDefense(p),
	}), nil
}

func (g *Game) cmdUnequip(p *Player, c proto.Command) (string, *proto.Error) {
	if len(c.Word) != 1 {
		return "", proto.ErrMalformed
	}
	slot := strings.ToLower(c.Word[0])
	if !containsString(Slots, slot) {
		// Accept the item itself too: "UNEQUIP iron sword".
		if id := resolve(c.Args, g.itemCands(p.Inventory)); id != "" {
			slot = g.items[id].Def.Slot
		}
	}
	id := p.Equipped[slot]
	if id == "" {
		return "", proto.ErrSlotEmpty
	}
	delete(p.Equipped, slot)
	g.log.Info("item_unequipped", "player", p.Name, "item", id, "slot", slot)
	return jsonLine(map[string]interface{}{
		"slot": slot, "item": id, "attack": g.playerAttack(p), "defense": g.playerDefense(p),
	}), nil
}

var statAliases = map[string]string{
	"strength": "strength", "str": "strength",
	"agility": "agility", "agi": "agility",
	"endurance": "endurance", "end": "endurance",
}

func (g *Game) cmdTrain(p *Player, c proto.Command) (string, *proto.Error) {
	if len(c.Word) != 1 {
		return "", proto.ErrMalformed
	}
	stat, ok := statAliases[strings.ToLower(c.Word[0])]
	if !ok {
		return "", proto.ErrMalformed
	}
	if p.Points <= 0 {
		return "", proto.ErrNoStatPoints
	}
	p.Points--
	var value int
	switch stat {
	case "strength":
		p.Stats.Strength++
		value = p.Stats.Strength
	case "agility":
		p.Stats.Agility++
		value = p.Stats.Agility
	case "endurance":
		p.Stats.Endurance++
		value = p.Stats.Endurance
		p.MaxHP += HPPerEndurance
		p.HP += HPPerEndurance
	}
	g.log.Info("player_train", "player", p.Name, "stat", stat, "value", value, "points_left", p.Points)
	return jsonLine(map[string]interface{}{
		"stat": stat, "value": value, "points": p.Points, "stats": p.Stats,
		"max_hp": p.MaxHP, "attack": g.playerAttack(p), "speed": g.playerSpeed(p), "critical_chance": g.playerCrit(p),
	}), nil
}
