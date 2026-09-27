package game

import (
	"fmt"
	"strings"
	"time"

	"hollowmere/internal/proto"
)

// Combat is turn-based: each ATTACK, DEFEND, FLEE or USE made while fighting
// is one round. Initiative is rolled each ATTACK round (speed + d6, ties go to
// the player); the enemy answers every action that does not kill it.

// rollDamage: attack + d(attack/2) - defense, at least 1; a critical hit
// (critPct percent of the time) doubles the result.
func (g *Game) rollDamage(attack, defense, critPct int) (int, bool) {
	dmg := attack + g.rng.Intn(attack/2+1) - defense
	if dmg < 1 {
		dmg = 1
	}
	crit := g.rng.Intn(100) < critPct
	if crit {
		dmg *= 2
	}
	return dmg, crit
}

func healthLabel(hp, max int) string {
	switch {
	case hp*4 >= max*3:
		return "healthy"
	case hp*100 >= max*35:
		return "wounded"
	default:
		return "critical"
	}
}

func (g *Game) combatEvent(room, except, text string) {
	g.broadcastRoom(room, except, "EVT ROOM COMBAT "+text)
}

func (g *Game) cmdAttack(p *Player, c proto.Command) (string, *proto.Error) {
	if err := requireArgs(c); err != nil {
		return "", err
	}
	id := resolve(c.Args, g.npcCands(p.Room))
	if id == "" {
		return "", proto.ErrNPCNotFound
	}
	n := g.npcs[id]
	if !n.Def.Hostile {
		return "", proto.ErrNPCNotHostile
	}
	if p.Target != "" && p.Target != id {
		old := p.Target
		p.Target = ""
		g.resetNPCIfIdle(old)
	}
	p.Target = id
	p.Defending = false
	g.scaleNPC(n)
	return jsonLine(g.attackRound(p, n, 1)), nil
}

// attackRound plays one ATTACK round against n; mult multiplies the
// player's damage (power strike).
func (g *Game) attackRound(p *Player, n *NPC, mult int) map[string]interface{} {
	id := n.ID
	res := map[string]interface{}{"target": id, "target_max_hp": n.MaxHP, "damage": 0, "critical": false, "counter_damage": 0}
	var log []string
	playerInit := g.playerSpeed(p) + g.rng.Intn(6)
	npcInit := n.Def.Stats.Speed + g.rng.Intn(6)
	npcFirst := npcInit > playerInit
	res["initiative"] = "player"
	if npcFirst {
		res["initiative"] = "npc"
		r := g.npcStrike(p, n)
		log = append(log, r.text)
		res["counter_damage"] = r.damage
	}
	if p.Target == id { // still alive and fighting
		dmg, crit := g.rollDamage(g.playerAttack(p), n.Def.Stats.Defense, g.playerCrit(p))
		dmg *= mult
		n.HP = maxInt(0, n.HP-dmg)
		n.Damagers[strings.ToLower(p.Name)] = true
		res["damage"], res["critical"] = dmg, crit
		text := fmt.Sprintf("%s hits %s for %d damage", p.Name, n.Def.Name, dmg)
		if crit {
			text += " (critical!)"
		}
		log = append(log, text)
		g.combatEvent(p.Room, p.Name, fmt.Sprintf("%s (%s %d/%d)", text, id, n.HP, n.MaxHP))
		g.log.Info("combat_hit", "player", p.Name, "npc", id, "damage", dmg, "critical", crit, "npc_hp", n.HP, "room", p.Room)
		if n.HP == 0 {
			log = append(log, g.defeatNPC(n, p)...)
		} else if !npcFirst {
			r := g.npcStrike(p, n)
			log = append(log, r.text)
			res["counter_damage"] = r.damage
		}
	}
	g.finishRound(p, n, res)
	res["log"] = log
	return res
}

func (g *Game) cmdDefend(p *Player, c proto.Command) (string, *proto.Error) {
	if p.Target == "" {
		return "", proto.ErrNotInCombat
	}
	n := g.npcs[p.Target]
	p.Defending = true
	p.HP = minInt(p.MaxHP, p.HP+2)
	res := map[string]interface{}{"action": "defend"}
	for k, v := range g.npcTurn(p, n) {
		res[k] = v
	}
	return jsonLine(res), nil
}

func (g *Game) cmdFlee(p *Player, c proto.Command) (string, *proto.Error) {
	if p.Target == "" {
		return "", proto.ErrNotInCombat
	}
	n := g.npcs[p.Target]
	chance := g.W.Settings.FleeChancePercent + (g.playerSpeed(p)-n.Def.Stats.Speed)*5
	chance = maxInt(10, minInt(95, chance))
	var exits []string
	for _, dir := range sortedKeys(g.rooms[p.Room].Def.Exits) {
		if g.mayEnter(p, g.rooms[p.Room].Def.Exits[dir]) == nil {
			exits = append(exits, dir)
		}
	}
	if len(exits) > 0 && g.rng.Intn(100) < chance {
		dir := exits[g.rng.Intn(len(exits))]
		to := g.rooms[p.Room].Def.Exits[dir]
		p.Target = ""
		p.Defending = false
		g.combatEvent(p.Room, p.Name, fmt.Sprintf("%s flees from %s", p.Name, n.Def.Name))
		g.log.Info("combat_flee", "player", p.Name, "npc", n.ID, "success", true, "to", to)
		g.resetNPCIfIdle(n.ID)
		g.movePlayer(p, to)
		return jsonLine(map[string]interface{}{"status": "fled", "direction": dir, "room": to, "hp": p.HP}), nil
	}
	g.log.Info("combat_flee", "player", p.Name, "npc", n.ID, "success", false)
	res := map[string]interface{}{"action": "flee_failed"}
	for k, v := range g.npcTurn(p, n) {
		res[k] = v
	}
	return jsonLine(res), nil
}

// npcTurn lets the enemy act once against p and returns the round summary.
func (g *Game) npcTurn(p *Player, n *NPC) map[string]interface{} {
	r := g.npcStrike(p, n)
	res := map[string]interface{}{"target": n.ID, "target_max_hp": n.MaxHP, "damage": 0, "counter_damage": r.damage, "log": []string{r.text}}
	g.finishRound(p, n, res)
	return res
}

type strike struct {
	damage int
	text   string
}

// npcStrike: the enemy hits p once; a lethal hit respawns the player.
func (g *Game) npcStrike(p *Player, n *NPC) strike {
	r := g.npcHit(p, n)
	if p.HP == 0 {
		g.defeatPlayer(p, n)
		r.text += fmt.Sprintf(" - %s is defeated", p.Name)
	}
	return r
}

// ambush makes the first aggressive enemy of the room attack a player who
// just walked in: the player enters combat and takes a free hit.
func (g *Game) ambush(p *Player) {
	if p.Target != "" {
		return
	}
	for _, id := range g.rooms[p.Room].NPCs {
		n := g.npcs[id]
		if !n.Alive || !n.Def.Aggressive {
			continue
		}
		p.Target = id
		p.Defending = false
		g.scaleNPC(n)
		g.combatEvent(p.Room, p.Name, fmt.Sprintf("%s ambushes %s", n.Def.Name, p.Name))
		g.log.Info("combat_ambush", "player", p.Name, "npc", id, "room", p.Room)
		r := g.npcHit(p, n)
		g.send(p, fmt.Sprintf("EVT PLAYER AMBUSH %s %d %d", id, r.damage, p.HP))
		if p.HP == 0 {
			g.defeatPlayer(p, n)
		}
		return
	}
}

// npcHit applies one enemy hit without handling death.
func (g *Game) npcHit(p *Player, n *NPC) strike {
	dmg, crit := g.rollDamage(n.Def.Stats.Attack, g.playerDefense(p), g.W.Settings.CritChancePercent)
	if p.parrying {
		dmg, crit = 0, false
		p.parrying = false
	} else if p.Defending {
		dmg = maxInt(1, dmg/2)
		p.Defending = false
	}
	p.HP = maxInt(0, p.HP-dmg)
	text := fmt.Sprintf("%s hits %s for %d damage", n.Def.Name, p.Name, dmg)
	if dmg == 0 {
		text = fmt.Sprintf("%s parries %s's blow", p.Name, n.Def.Name)
	}
	if crit {
		text += " (critical!)"
	}
	g.combatEvent(p.Room, p.Name, text)
	g.log.Info("combat_counter", "player", p.Name, "npc", n.ID, "damage", dmg, "critical", crit, "player_hp", p.HP)
	return strike{dmg, text}
}

// finishRound fills the common fields of a combat reply. Every combat
// round goes through it, so skill cooldowns tick here.
func (g *Game) finishRound(p *Player, n *NPC, res map[string]interface{}) {
	g.tickCooldowns(p)
	res["attacker_hp"] = p.HP
	res["target_hp"] = n.HP
	switch {
	case !n.Alive:
		res["status"] = "victory"
	case p.Target == "":
		res["status"] = "defeated"
		res["respawn_room"] = p.Room
	default:
		res["status"] = "combat"
	}
}

// defeatPlayer respawns a player at the safe room with reduced health.
func (g *Game) defeatPlayer(p *Player, n *NPC) {
	p.Target = ""
	p.Defending = false
	g.combatEvent(p.Room, p.Name, fmt.Sprintf("%s was defeated by %s", p.Name, n.Def.Name))
	g.log.Warn("player_defeated", "player", p.Name, "npc", n.ID, "room", p.Room, "respawn", g.W.SafeRoom)
	p.HP = minInt(p.MaxHP, g.W.Settings.RespawnHP)
	g.resetNPCIfIdle(n.ID)
	if p.Room != g.W.SafeRoom {
		g.movePlayer(p, g.W.SafeRoom)
	}
	g.send(p, "EVT PLAYER RESPAWN "+g.W.SafeRoom)
}

// defeatNPC removes a dead enemy, drops its loot, credits quests and
// schedules its respawn.
func (g *Game) defeatNPC(n *NPC, killer *Player) []string {
	room := n.Room
	n.Alive = false
	r := g.rooms[room]
	r.NPCs = removeString(r.NPCs, n.ID)
	n.Room = ""
	log := []string{fmt.Sprintf("%s is defeated", n.Def.Name)}
	g.combatEvent(room, killer.Name, fmt.Sprintf("%s defeated %s", killer.Name, n.Def.Name))
	g.broadcastRoom(room, "", "EVT ROOM NPC DEFEAT "+n.ID)
	g.log.Info("npc_defeated", "npc", n.ID, "killer", killer.Name, "room", room)

	for _, p := range g.players {
		if p.Target == n.ID {
			p.Target = ""
			p.Defending = false
		}
	}
	for _, lid := range n.Loot {
		it := g.items[lid]
		if it.Room == "" && it.Holder == "" {
			g.placeItem(it, room)
			log = append(log, fmt.Sprintf("%s drops %s", n.Def.Name, it.Def.Name))
			g.broadcastRoom(room, "", "EVT ROOM ITEM SPAWN "+lid)
			g.log.Info("loot_dropped", "npc", n.ID, "item", lid, "room", room)
		}
	}
	for key := range n.Damagers {
		if p := g.players[key]; p != nil {
			p.Kills++
			g.gainXP(p, n.Def.XP, "kill "+n.DefID)
			g.countObjective(p, "kill", n.DefID)
		}
	}
	n.Damagers = map[string]bool{}

	id := n.ID
	g.Schedule(time.Duration(g.W.Settings.NPCRespawnSeconds)*time.Second, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		cur := g.npcs[id]
		if cur.Alive {
			return
		}
		g.spawnNPC(cur)
		g.log.Info("npc_respawned", "npc", id, "room", cur.Home)
		g.broadcastRoom(cur.Home, "", "EVT ROOM NPC SPAWN "+id)
	})
	return log
}

// resetNPCIfIdle heals an enemy nobody is fighting anymore.
func (g *Game) resetNPCIfIdle(id string) {
	n := g.npcs[id]
	if n == nil || !n.Alive {
		return
	}
	for _, p := range g.players {
		if p.Target == id {
			return
		}
	}
	n.MaxHP = n.Def.Stats.HP
	n.HP = n.MaxHP
	n.Damagers = map[string]bool{}
}

// scaleNPC sets the health of a fresh enemy from the number of players in
// its room when a fight starts: +ScalePerPlayer percent per extra player.
// Players joining a fight already under way do not change it.
func (g *Game) scaleNPC(n *NPC) {
	if n.Def.ScalePerPlayer <= 0 || n.HP != n.MaxHP || len(n.Damagers) > 0 {
		return
	}
	k := len(g.rooms[n.Room].Players)
	n.MaxHP = n.Def.Stats.HP * (100 + n.Def.ScalePerPlayer*maxInt(0, k-1)) / 100
	n.HP = n.MaxHP
	g.log.Info("npc_scaled", "npc", n.ID, "players", k, "max_hp", n.MaxHP)
}

type statusReply struct {
	HP        int     `json:"hp"`
	MaxHP     int     `json:"max_hp"`
	Status    string  `json:"status"`
	Condition string  `json:"condition"`
	InCombat  bool    `json:"in_combat"`
	Target    *string `json:"target"`
	TargetHP  *int    `json:"target_hp"`
	Defending bool    `json:"defending"`
	Attack    int     `json:"attack"`
	Defense   int     `json:"defense"`
	Room      string  `json:"room"`
	Played    string  `json:"played"`
	// Progression (extension)
	Level      int                `json:"level"`
	XP         int                `json:"xp"`
	XPNext     int                `json:"xp_next"`
	Stats      Stats              `json:"stats"`
	Points     int                `json:"points"`
	Speed      int                `json:"speed"`
	CritChance int                `json:"critical_chance"`
	Equipment  map[string]*string `json:"equipment"`
}

func (g *Game) cmdStatus(p *Player, c proto.Command) (string, *proto.Error) {
	cond := healthLabel(p.HP, p.MaxHP)
	rep := statusReply{
		HP: p.HP, MaxHP: p.MaxHP, Status: cond, Condition: cond,
		Defending: p.Defending, Attack: g.playerAttack(p), Defense: g.playerDefense(p), Room: p.Room,
		Played: PlayedString(p.played + time.Since(p.joined)),
		Level:  p.Level, XP: p.XP, XPNext: XPForLevel(p.Level), Stats: p.Stats, Points: p.Points,
		Speed: g.playerSpeed(p), CritChance: g.playerCrit(p), Equipment: map[string]*string{},
	}
	for _, slot := range Slots {
		var v *string
		if id := p.Equipped[slot]; id != "" {
			v = &id
		}
		rep.Equipment[slot] = v
	}
	if p.Target != "" {
		n := g.npcs[p.Target]
		t, hp := n.ID, n.HP
		rep.Status, rep.InCombat, rep.Target, rep.TargetHP = "combat", true, &t, &hp
	}
	return jsonLine(rep), nil
}
