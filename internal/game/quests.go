package game

import (
	"fmt"
	"strings"

	"hollowmere/internal/proto"
)

// Quest flow:
//   QUEST <npc>  on a giver  -> accepts the next available quest ("active")
//   progress is tracked automatically (items carried, enemies defeated)
//   QUEST <npc>  on the turn-in npc once objectives are met -> "completed",
//                required items are consumed and the reward is granted.

type questReply struct {
	QuestID     string   `json:"quest_id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Reward      string   `json:"reward"`
	Status      string   `json:"status"`
	Progress    string   `json:"progress"`
	TurnIn      string   `json:"turn_in"`
	Granted     []string `json:"granted,omitempty"`
	Message     string   `json:"message,omitempty"`
}

func (g *Game) rewardText(q *QuestDef) string {
	var parts []string
	for _, it := range q.Reward.Items {
		parts = append(parts, it)
	}
	if q.Reward.MaxHP > 0 {
		parts = append(parts, fmt.Sprintf("+%d max_hp", q.Reward.MaxHP))
	}
	if q.Reward.HealFull {
		parts = append(parts, "full_heal")
	}
	return strings.Join(parts, ", ")
}

// progress returns the current objective count for an active quest.
func (g *Game) progress(p *Player, qid string) int {
	q := g.W.Quests[qid]
	st := p.Quests[qid]
	switch q.Type {
	case "fetch", "deliver":
		return minInt(q.Count, len(g.heldOfType(p, q.Target)))
	default:
		return minInt(q.Count, st.Progress)
	}
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
		Reward: g.rewardText(q), TurnIn: q.TurnIn,
	}
	if st == nil {
		r.Status = "available"
		r.Progress = fmt.Sprintf("0/%d", q.Count)
		return r
	}
	r.Status = st.Status
	if st.Status == "completed" {
		r.Progress = fmt.Sprintf("%d/%d", q.Count, q.Count)
	} else {
		r.Progress = fmt.Sprintf("%d/%d", g.progress(p, qid), q.Count)
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
	n := g.npcs[id]

	// 1. Turn in a finished quest.
	for _, qid := range p.QuestList {
		q := g.W.Quests[qid]
		if p.Quests[qid].Status == "active" && q.TurnIn == n.DefID && g.progress(p, qid) >= q.Count {
			return jsonLine(g.completeQuest(p, qid)), nil
		}
	}
	// 2. Report on a quest in progress from this npc.
	for _, qid := range p.QuestList {
		q := g.W.Quests[qid]
		if p.Quests[qid].Status == "active" && (q.Giver == n.DefID || q.TurnIn == n.DefID) {
			r := g.reply(p, qid)
			r.Message = "Come back when the task is done."
			return jsonLine(r), nil
		}
	}
	// 3. Offer and accept the next available quest.
	for _, qid := range sortedKeys(g.W.Quests) {
		q := g.W.Quests[qid]
		if q.Giver != n.DefID || !g.available(p, qid) {
			continue
		}
		p.Quests[qid] = &QuestState{ID: qid, Status: "active"}
		p.QuestList = append(p.QuestList, qid)
		r := g.reply(p, qid)
		for _, itID := range q.GiveOnStart {
			it := g.newItem(itID)
			g.giveItem(it, p)
			r.Granted = append(r.Granted, it.ID)
		}
		r.Progress = fmt.Sprintf("%d/%d", g.progress(p, qid), q.Count)
		g.log.Info("quest_accepted", "player", p.Name, "quest", qid, "npc", id, "granted", r.Granted)
		return jsonLine(r), nil
	}
	return "", proto.ErrNoQuestAvailable
}

func (g *Game) completeQuest(p *Player, qid string) questReply {
	q := g.W.Quests[qid]
	if q.Type == "fetch" || q.Type == "deliver" {
		held := g.heldOfType(p, q.Target)
		for _, itID := range held[:q.Count] {
			g.consumeItem(g.items[itID])
		}
	}
	st := p.Quests[qid]
	st.Status = "completed"
	st.Progress = q.Count
	r := g.reply(p, qid)
	for _, itID := range q.Reward.Items {
		it := g.newItem(itID)
		g.giveItem(it, p)
		r.Granted = append(r.Granted, it.ID)
	}
	p.MaxHP += q.Reward.MaxHP
	if q.Reward.HealFull {
		p.HP = p.MaxHP
	}
	r.Message = "Quest completed!"
	g.log.Info("quest_completed", "player", p.Name, "quest", qid, "granted", r.Granted, "max_hp", p.MaxHP)
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

// advanceFetchQuests notifies progress changes after inventory changes.
func (g *Game) advanceFetchQuests(p *Player) {
	for _, qid := range p.QuestList {
		q := g.W.Quests[qid]
		st := p.Quests[qid]
		if st.Status != "active" || q.Type == "kill" {
			continue
		}
		cur := g.progress(p, qid)
		if cur != st.Progress {
			st.Progress = cur
			g.log.Info("quest_progress", "player", p.Name, "quest", qid, "progress", cur, "goal", q.Count)
			g.send(p, fmt.Sprintf("EVT QUEST PROGRESS %s %d/%d", qid, cur, q.Count))
		}
	}
}

func (g *Game) advanceKillQuests(p *Player, npcType string) {
	for _, qid := range p.QuestList {
		q := g.W.Quests[qid]
		st := p.Quests[qid]
		if st.Status != "active" || q.Type != "kill" || q.Target != npcType || st.Progress >= q.Count {
			continue
		}
		st.Progress++
		g.log.Info("quest_progress", "player", p.Name, "quest", qid, "progress", st.Progress, "goal", q.Count)
		g.send(p, fmt.Sprintf("EVT QUEST PROGRESS %s %d/%d", qid, st.Progress, q.Count))
	}
}

// questHint adds a nudge to NPC dialogue when a quest interaction is possible.
func (g *Game) questHint(p *Player, n *NPC) string {
	for _, qid := range p.QuestList {
		q := g.W.Quests[qid]
		if p.Quests[qid].Status == "active" && q.TurnIn == n.DefID && g.progress(p, qid) >= q.Count {
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
