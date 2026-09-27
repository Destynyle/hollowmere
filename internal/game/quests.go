package game

import (
	"fmt"
	"strings"

	"hollowmere/internal/proto"
)

// Quest flow:
//   QUEST <npc>  on a giver  -> accepts the next available quest ("active")
//   progress is tracked automatically (items carried, enemies defeated,
//   rooms visited, people talked to); every step but the last completes on
//   its own and the next one begins ("EVT QUEST STEP")
//   QUEST <npc>  on the turn-in npc once the last step is met -> "completed",
//                required items are consumed and the reward is granted.
// A dialogue option carrying "quest" does the same as QUEST <npc>.

type questReply struct {
	QuestID     string   `json:"quest_id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Reward      string   `json:"reward"`
	Status      string   `json:"status"`
	Progress    string   `json:"progress"`
	TurnIn      string   `json:"turn_in"`
	Step        int      `json:"step"`
	Steps       int      `json:"steps"`
	Objective   string   `json:"objective"`
	XP          int      `json:"xp,omitempty"` // experience gained on completion
	Granted     []string `json:"granted,omitempty"`
	Message     string   `json:"message,omitempty"`
}

func (g *Game) rewardText(q *QuestDef) string {
	parts := []string{fmt.Sprintf("%d xp", q.Reward.XP)}
	for _, it := range q.Reward.Items {
		parts = append(parts, it)
	}
	if q.Reward.MaxHP > 0 {
		parts = append(parts, fmt.Sprintf("+%d max_hp", q.Reward.MaxHP))
	}
	if q.Reward.HealFull {
		parts = append(parts, "full_heal")
	}
	if len(q.Reward.Bonus) > 0 {
		parts = append(parts, "maybe more")
	}
	return strings.Join(parts, ", ")
}

// progress returns the objective count of the current step of an active
// quest.
func (g *Game) progress(p *Player, qid string) int {
	q := g.W.Quests[qid]
	st := p.Quests[qid]
	step := q.Steps[st.Step]
	switch step.Type {
	case "fetch", "deliver":
		return minInt(step.Count, len(g.heldOfType(p, step.Target)))
	case "visit":
		if p.Room == step.Target {
			return step.Count
		}
	}
	return minInt(step.Count, st.Progress)
}

// ready reports whether an active quest can be handed in.
func (g *Game) ready(p *Player, qid string) bool {
	q := g.W.Quests[qid]
	st := p.Quests[qid]
	return st != nil && st.Status == "active" && st.Step == len(q.Steps)-1 && g.progress(p, qid) >= q.Last().Count
}

func (g *Game) heldOfType(p *Player, defID string) []string {
	var out []string
	for _, id := range p.Inventory {
		if g.items[id].DefID == defID {
			out = append(out, id)
		}
	}
	return out
}

func (g *Game) reply(p *Player, qid string) questReply {
	q := g.W.Quests[qid]
	st := p.Quests[qid]
	r := questReply{
		QuestID: qid, Title: q.Title, Description: q.Description,
		Reward: g.rewardText(q), TurnIn: q.TurnIn, Steps: len(q.Steps),
	}
	switch {
	case st == nil:
		r.Status = "available"
		r.Step = 1
		r.Progress = fmt.Sprintf("0/%d", q.Steps[0].Count)
		r.Objective = q.Steps[0].Description
	case st.Status == "completed":
		r.Status = st.Status
		r.Step = len(q.Steps)
		r.Progress = fmt.Sprintf("%d/%d", q.Last().Count, q.Last().Count)
		r.Objective = q.Last().Description
	default:
		step := q.Steps[st.Step]
		r.Status = st.Status
		r.Step = st.Step + 1
		r.Progress = fmt.Sprintf("%d/%d", g.progress(p, qid), step.Count)
		r.Objective = step.Description
	}
	return r
}

func (g *Game) available(p *Player, qid string) bool {
	if p.Quests[qid] != nil {
		return false
	}
	for _, req := range g.W.Quests[qid].Requires {
		if st := p.Quests[req]; st == nil || st.Status != "completed" {
			return false
		}
	}
	return true
}

func (g *Game) cmdQuest(p *Player, c proto.Command) (string, *proto.Error) {
	if err := requireArgs(c); err != nil {
		return "", err
	}
	id := resolve(c.Args, g.npcCands(p.Room))
	if id == "" {
		return "", proto.ErrNPCNotFound
	}
	r, perr := g.questWith(p, g.npcs[id], "")
	if perr != nil {
		return "", perr
	}
	return jsonLine(r), nil
}

// questWith turns in, reports on or accepts a quest with an NPC, in that
// order of priority. only restricts the choice to one quest ("" for any).
func (g *Game) questWith(p *Player, n *NPC, only string) (questReply, *proto.Error) {
	match := func(qid string) bool { return only == "" || qid == only }

	// 1. Turn in a finished quest.
	for _, qid := range p.QuestList {
		if match(qid) && g.W.Quests[qid].TurnIn == n.DefID && g.ready(p, qid) {
			return g.completeQuest(p, qid), nil
		}
	}
	// 2. Report on a quest in progress from this npc.
	for _, qid := range p.QuestList {
		q := g.W.Quests[qid]
		if match(qid) && p.Quests[qid].Status == "active" && (q.Giver == n.DefID || q.TurnIn == n.DefID) {
			r := g.reply(p, qid)
			r.Message = "Come back when the task is done."
			return r, nil
		}
	}
	// 3. Offer and accept the next available quest.
	for _, qid := range sortedKeys(g.W.Quests) {
		q := g.W.Quests[qid]
		if !match(qid) || q.Giver != n.DefID || !g.available(p, qid) {
			continue
		}
		p.Quests[qid] = &QuestState{ID: qid, Status: "active"}
		p.QuestList = append(p.QuestList, qid)
		var granted []string
		for _, itID := range q.GiveOnStart {
			it := g.newItem(itID)
			g.giveItem(it, p)
			g.autoEquip(p, it)
			granted = append(granted, it.ID)
		}
		g.log.Info("quest_accepted", "player", p.Name, "quest", qid, "npc", n.ID, "granted", granted)
		g.refreshQuests(p) // a step may already be met
		r := g.reply(p, qid)
		r.Granted = granted
		return r, nil
	}
	return questReply{}, proto.ErrNoQuestAvailable
}

func (g *Game) completeQuest(p *Player, qid string) questReply {
	q := g.W.Quests[qid]
	if last := q.Last(); last.Type == "fetch" || last.Type == "deliver" {
		held := g.heldOfType(p, last.Target)
		for _, itID := range held[:last.Count] {
			g.consumeItem(g.items[itID])
		}
	}
	// Bonuses are judged before the quest counts as completed, so a
	// condition such as "not_completed" refers to the state beforehand.
	var bonuses []Bonus
	for _, b := range q.Reward.Bonus {
		if g.holds(p, &b.If) {
			bonuses = append(bonuses, b)
		}
	}
	st := p.Quests[qid]
	st.Status = "completed"
	st.Step = len(q.Steps) - 1
	st.Progress = q.Last().Count
	r := g.reply(p, qid)
	r.Message = "Quest completed!"
	// Experience first: a level gained here may unlock the reward's gear.
	r.XP = q.Reward.XP
	g.gainXP(p, q.Reward.XP, "quest "+qid)
	grant := func(items []string, maxHP int) {
		for _, itID := range items {
			it := g.newItem(itID)
			g.giveItem(it, p)
			g.autoEquip(p, it)
			r.Granted = append(r.Granted, it.ID)
		}
		p.MaxHP += maxHP
	}
	grant(q.Reward.Items, q.Reward.MaxHP)
	for _, b := range bonuses {
		grant(b.Items, b.MaxHP)
		if b.Message != "" {
			r.Message += " " + b.Message
		}
	}
	if q.Reward.HealFull {
		p.HP = p.MaxHP
	}
	g.log.Info("quest_completed", "player", p.Name, "quest", qid, "granted", r.Granted,
		"bonuses", len(bonuses), "max_hp", p.MaxHP)
	g.send(p, fmt.Sprintf("EVT QUEST COMPLETE %s", qid))
	return r
}

func (g *Game) cmdQuests(p *Player, c proto.Command) (string, *proto.Error) {
	out := make([]questReply, 0, len(p.QuestList))
	for _, qid := range p.QuestList {
		out = append(out, g.reply(p, qid))
	}
	return jsonLine(out), nil
}

func (g *Game) cmdAbandon(p *Player, c proto.Command) (string, *proto.Error) {
	if len(c.Word) != 1 {
		return "", proto.ErrMalformed
	}
	qid := c.Word[0]
	st := p.Quests[qid]
	if st == nil || st.Status != "active" {
		return "", proto.ErrQuestNotFound
	}
	delete(p.Quests, qid)
	p.QuestList = removeString(p.QuestList, qid)
	g.log.Info("quest_abandoned", "player", p.Name, "quest", qid)
	return "abandoned=" + qid, nil
}

// refreshQuests reports progress changes and moves quests to their next
// step once the current one is met. It is called after anything that may
// change an objective: inventory, movement, fights, conversations.
func (g *Game) refreshQuests(p *Player) {
	for _, qid := range p.QuestList {
		q := g.W.Quests[qid]
		st := p.Quests[qid]
		for st.Status == "active" {
			step := q.Steps[st.Step]
			cur := g.progress(p, qid)
			if cur != st.Progress {
				st.Progress = cur
				g.log.Info("quest_progress", "player", p.Name, "quest", qid, "step", st.Step+1, "progress", cur, "goal", step.Count)
				g.send(p, fmt.Sprintf("EVT QUEST PROGRESS %s %d/%d", qid, cur, step.Count))
			}
			if cur < step.Count || st.Step == len(q.Steps)-1 {
				break
			}
			st.Step++
			st.Progress = 0
			g.log.Info("quest_step", "player", p.Name, "quest", qid, "step", st.Step+1, "steps", len(q.Steps))
			g.send(p, fmt.Sprintf("EVT QUEST STEP %s %d/%d", qid, st.Step+1, len(q.Steps)))
		}
	}
}

// countObjective credits one kill or conversation to matching steps.
func (g *Game) countObjective(p *Player, kind, target string) {
	for _, qid := range p.QuestList {
		st := p.Quests[qid]
		if st.Status != "active" {
			continue
		}
		step := g.W.Quests[qid].Steps[st.Step]
		if step.Type == kind && step.Target == target && st.Progress < step.Count {
			st.Progress++
			g.log.Info("quest_progress", "player", p.Name, "quest", qid, "step", st.Step+1, "progress", st.Progress, "goal", step.Count)
			g.send(p, fmt.Sprintf("EVT QUEST PROGRESS %s %d/%d", qid, st.Progress, step.Count))
		}
	}
	g.refreshQuests(p)
}

// holds evaluates a condition for a player.
func (g *Game) holds(p *Player, c *Condition) bool {
	if c == nil {
		return true
	}
	if c.Quest != "" && !g.questIs(p, c.Quest, c.Status) {
		return false
	}
	if c.Flag != "" && !p.Flags[c.Flag] {
		return false
	}
	if c.NoFlag != "" && p.Flags[c.NoFlag] {
		return false
	}
	if c.Item != "" && len(g.heldOfType(p, c.Item)) == 0 {
		return false
	}
	return true
}

func (g *Game) questIs(p *Player, qid, status string) bool {
	st := p.Quests[qid]
	switch status {
	case "none":
		return st == nil
	case "available":
		return g.available(p, qid)
	case "active":
		return st != nil && st.Status == "active"
	case "ready":
		return g.ready(p, qid)
	case "completed":
		return st != nil && st.Status == "completed"
	case "not_completed":
		return st == nil || st.Status != "completed"
	}
	return false
}

// questHint adds a nudge to NPC dialogue when a quest interaction is possible.
func (g *Game) questHint(p *Player, n *NPC) string {
	for _, qid := range p.QuestList {
		q := g.W.Quests[qid]
		if q.TurnIn == n.DefID && g.ready(p, qid) {
			return "(You can turn in \"" + q.Title + "\": QUEST " + shortID(n.DefID) + ")"
		}
	}
	for _, qid := range sortedKeys(g.W.Quests) {
		if g.W.Quests[qid].Giver == n.DefID && g.available(p, qid) {
			return "(This character has a task for you: QUEST " + shortID(n.DefID) + ")"
		}
	}
	return ""
}

func shortID(id string) string {
	if i := strings.IndexByte(id, '.'); i >= 0 {
		return id[i+1:]
	}
	return id
}
