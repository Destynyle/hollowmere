package game

import (
	"fmt"
	"strings"
	"time"

	"hollowmere/internal/proto"
)

// Skills are special actions unlocked by level. Their cooldown is counted
// in combat rounds; out of combat, each round of cooldown also runs out
// after SkillRoundOutOfCombat, so a heal used on the road comes back.

// SkillDef describes one skill.
type SkillDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Level       int    `json:"level"`    // level at which it unlocks
	Cooldown    int    `json:"cooldown"` // rounds before it can be used again
	CombatOnly  bool   `json:"combat_only"`
}

// Skills lists every skill, in unlock order.
var Skills = []SkillDef{
	{Name: "strike", Description: "Power strike: an attack round that deals double damage.", Level: 1, Cooldown: 3, CombatOnly: true},
	{Name: "parry", Description: "Parry the enemy's next blow completely, then riposte for half your attack.", Level: 2, Cooldown: 4, CombatOnly: true},
	{Name: "heal", Description: "Second wind: recover 20 HP plus 5 per level.", Level: 3, Cooldown: 5},
}

// SkillRoundOutOfCombat is how long one round of cooldown lasts when the
// player is not fighting.
const SkillRoundOutOfCombat = 6 * time.Second

type cooldown struct {
	rounds int
	since  time.Time
}

func skillByName(name string) *SkillDef {
	for i := range Skills {
		if Skills[i].Name == name {
			return &Skills[i]
		}
	}
	return nil
}

// remaining returns the rounds before a skill is ready again.
func (p *Player) remaining(skill string) int {
	cd := p.cooldowns[skill]
	if cd == nil {
		return 0
	}
	if p.Target == "" && time.Since(cd.since) >= time.Duration(cd.rounds)*SkillRoundOutOfCombat {
		delete(p.cooldowns, skill)
		return 0
	}
	return cd.rounds
}

// tickCooldowns counts one combat round down on every skill.
func (g *Game) tickCooldowns(p *Player) {
	for name, cd := range p.cooldowns {
		cd.rounds--
		cd.since = time.Now()
		if cd.rounds <= 0 {
			delete(p.cooldowns, name)
		}
	}
}

type skillInfo struct {
	SkillDef
	Unlocked bool `json:"unlocked"`
	Ready    bool `json:"ready"`
	Wait     int  `json:"wait"` // rounds left
}

func (g *Game) cmdSkills(p *Player, c proto.Command) (string, *proto.Error) {
	out := make([]skillInfo, 0, len(Skills))
	for _, s := range Skills {
		wait := p.remaining(s.Name)
		unlocked := p.Level >= s.Level
		out = append(out, skillInfo{SkillDef: s, Unlocked: unlocked, Ready: unlocked && wait == 0, Wait: wait})
	}
	return jsonLine(out), nil
}

func (g *Game) cmdSkill(p *Player, c proto.Command) (string, *proto.Error) {
	if len(c.Word) != 1 {
		return "", proto.ErrMalformed
	}
	s := skillByName(strings.ToLower(c.Word[0]))
	if s == nil {
		return "", proto.ErrSkillNotFound
	}
	if p.Level < s.Level {
		return "", proto.ErrLevelTooLow
	}
	if s.CombatOnly && p.Target == "" {
		return "", proto.ErrNotInCombat
	}
	if p.remaining(s.Name) > 0 {
		return "", proto.ErrSkillNotReady
	}
	g.log.Info("skill_used", "player", p.Name, "skill", s.Name, "target", p.Target)

	var res map[string]interface{}
	switch s.Name {
	case "strike":
		res = g.attackRound(p, g.npcs[p.Target], 2)
	case "parry":
		res = g.parryRound(p, g.npcs[p.Target])
	case "heal":
		amount := 20 + 5*p.Level
		before := p.HP
		p.HP = minInt(p.MaxHP, p.HP+amount)
		res = map[string]interface{}{"healed": p.HP - before, "hp": p.HP, "max_hp": p.MaxHP}
		if p.Target != "" {
			for k, v := range g.npcTurn(p, g.npcs[p.Target]) {
				res[k] = v
			}
		}
	}
	// Set after the round, so the round that used it does not count.
	p.cooldowns[s.Name] = &cooldown{rounds: s.Cooldown, since: time.Now()}
	res["skill"] = s.Name
	res["cooldown"] = s.Cooldown
	return jsonLine(res), nil
}

// parryRound: the enemy strikes and is parried, then the player ripostes.
func (g *Game) parryRound(p *Player, n *NPC) map[string]interface{} {
	p.parrying = true
	r := g.npcStrike(p, n)
	p.parrying = false
	log := []string{r.text}
	res := map[string]interface{}{"target": n.ID, "target_max_hp": n.MaxHP, "counter_damage": r.damage, "damage": 0}
	if p.Target == n.ID {
		dmg := maxInt(1, g.playerAttack(p)/2-n.Def.Stats.Defense/2)
		n.HP = maxInt(0, n.HP-dmg)
		n.Damagers[strings.ToLower(p.Name)] = true
		res["damage"] = dmg
		text := fmt.Sprintf("%s ripostes against %s for %d damage", p.Name, n.Def.Name, dmg)
		log = append(log, text)
		g.combatEvent(p.Room, p.Name, fmt.Sprintf("%s (%s %d/%d)", text, n.ID, n.HP, n.MaxHP))
		if n.HP == 0 {
			log = append(log, g.defeatNPC(n, p)...)
		}
	}
	g.finishRound(p, n, res)
	res["log"] = log
	return res
}
