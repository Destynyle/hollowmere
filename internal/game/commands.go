package game

import (
	"fmt"
	"strconv"
	"strings"

	"hollowmere/internal/proto"
)

type handler func(g *Game, p *Player, c proto.Command) (string, *proto.Error)

// dispatch is the command router: one entry per protocol command.
var dispatch map[string]handler

func init() {
	dispatch = map[string]handler{
		"LOOK":      (*Game).cmdLook,
		"MOVE":      (*Game).cmdMove,
		"CHAT":      (*Game).cmdChat,
		"WHO":       (*Game).cmdWho,
		"GROUP":     (*Game).cmdGroup,
		"TAKE":      (*Game).cmdTake,
		"DROP":      (*Game).cmdDrop,
		"INVENTORY": (*Game).cmdInventory,
		"TALK":      (*Game).cmdTalk,
		"SAY":       (*Game).cmdSay,
		"EQUIP":     (*Game).cmdEquip,
		"UNEQUIP":   (*Game).cmdUnequip,
		"TRAIN":     (*Game).cmdTrain,
		"SKILL":     (*Game).cmdSkill,
		"SKILLS":    (*Game).cmdSkills,
		"TELL":      (*Game).cmdTell,
		"FRIEND":    (*Game).cmdFriend,
		"TOP":       (*Game).cmdTop,
		"TRADE":     (*Game).cmdTrade,
		"ANNOUNCE":  (*Game).cmdAnnounce,
		"KICK":      (*Game).cmdKick,
		"MUTE":      (*Game).cmdMute,
		"UNMUTE":    (*Game).cmdUnmute,
		"BAN":       (*Game).cmdBan,
		"UNBAN":     (*Game).cmdUnban,
		"ATTACK":    (*Game).cmdAttack,
		"DEFEND":    (*Game).cmdDefend,
		"FLEE":      (*Game).cmdFlee,
		"USE":       (*Game).cmdUse,
		"STATUS":    (*Game).cmdStatus,
		"QUEST":     (*Game).cmdQuest,
		"QUESTS":    (*Game).cmdQuests,
		"ABANDON":   (*Game).cmdAbandon,
		"INSPECT":   (*Game).cmdInspect,
		"HELP":      (*Game).cmdHelp,
	}
}

// Commands lists every command accepted after authentication.
func Commands() []string {
	names := sortedKeys(dispatch)
	return append(names, "QUIT")
}

// Handle executes one authenticated command. The reply line is sent to the
// player before any event triggered by the command, all under the world
// lock, and is also returned for logging.
func (g *Game) Handle(p *Player, c proto.Command) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.player(p.Name) != p {
		return ""
	}
	h, ok := dispatch[c.Name]
	if !ok {
		reply := proto.ErrUnknownCommand.Error()
		g.send(p, reply)
		return reply
	}
	// The handler queues events in pending; the reply goes out first.
	pendingSink := &deferredSink{}
	real := p.Sink
	p.Sink = pendingSink
	payload, perr := h(g, p, c)
	p.Sink = real
	var reply string
	if perr != nil {
		reply = perr.Error()
	} else if payload == "" {
		reply = "OK"
	} else {
		reply = "OK " + payload
	}
	g.send(p, reply)
	for _, line := range pendingSink.lines {
		g.send(p, line)
	}
	return reply
}

// deferredSink buffers lines addressed to the acting player while a command
// runs, so that its reply is always delivered first.
type deferredSink struct{ lines []string }

func (d *deferredSink) Send(line string) { d.lines = append(d.lines, line) }

func requireArgs(c proto.Command) *proto.Error {
	if c.Args == "" {
		return proto.ErrMalformed
	}
	return nil
}

// ---------------------------------------------------------------------------
// Exploration

type lookItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type lookNPC struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Role    string `json:"role"`
	Hostile bool   `json:"hostile"`
	HP      int    `json:"hp,omitempty"`
	MaxHP   int    `json:"max_hp,omitempty"`
}

type lookRoom struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Exits       map[string]string `json:"exits"`
}

type lookDetails struct {
	Items []lookItem `json:"items"`
	NPCs  []lookNPC  `json:"npcs"`
	Safe  bool       `json:"safe"`
}

type lookReply struct {
	Room    lookRoom    `json:"room"`
	Players []string    `json:"players"`
	Items   []string    `json:"items"`
	NPCs    []string    `json:"npcs"`
	Details lookDetails `json:"details"`
}

func (g *Game) cmdLook(p *Player, c proto.Command) (string, *proto.Error) {
	r := g.rooms[p.Room]
	exits := map[string]string{}
	for k, v := range r.Def.Exits {
		exits[k] = v
	}
	rep := lookReply{
		Room:    lookRoom{ID: r.ID, Name: r.Def.Name, Description: r.Def.Description, Exits: exits},
		Players: append([]string{}, r.Players...),
		Items:   append([]string{}, r.Items...),
		NPCs:    append([]string{}, r.NPCs...),
		Details: lookDetails{Items: []lookItem{}, NPCs: []lookNPC{}, Safe: r.Def.Safe},
	}
	for _, id := range r.Items {
		rep.Details.Items = append(rep.Details.Items, lookItem{ID: id, Name: g.items[id].Def.Name})
	}
	for _, id := range r.NPCs {
		n := g.npcs[id]
		ln := lookNPC{ID: id, Name: n.Def.Name, Role: n.Def.Role, Hostile: n.Def.Hostile}
		if n.Def.Hostile {
			ln.HP, ln.MaxHP = n.HP, n.MaxHP
		}
		rep.Details.NPCs = append(rep.Details.NPCs, ln)
	}
	return jsonLine(rep), nil
}

var dirAliases = map[string]string{
	"n": "north", "s": "south", "e": "east", "w": "west", "u": "up", "d": "down",
	"ne": "northeast", "nw": "northwest", "se": "southeast", "sw": "southwest",
}

func (g *Game) cmdMove(p *Player, c proto.Command) (string, *proto.Error) {
	if len(c.Word) != 1 {
		return "", proto.ErrNoExit
	}
	dir := strings.ToLower(c.Word[0])
	if full, ok := dirAliases[dir]; ok {
		dir = full
	}
	to, ok := g.rooms[p.Room].Def.Exits[dir]
	if !ok {
		return "", proto.ErrNoExit
	}
	if p.Target != "" {
		return "", proto.ErrInCombat
	}
	if err := g.mayEnter(p, to); err != nil {
		return "", err
	}
	g.movePlayer(p, to)
	return "room=" + to, nil
}

// mayEnter checks the group requirement of a room: at least MinGroup
// members of the player's group, counting those already inside and those
// entering with them (standing in the same room).
func (g *Game) mayEnter(p *Player, to string) *proto.Error {
	need := g.rooms[to].Def.MinGroup
	if need <= 1 {
		return nil
	}
	if p.Group == nil {
		return proto.ErrGroupRequired
	}
	n := 0
	for _, m := range p.Group.Members {
		if mp := g.player(m); mp != nil && (mp.Room == p.Room || mp.Room == to) {
			n++
		}
	}
	if n < need {
		return proto.ErrGroupRequired
	}
	return nil
}

func (g *Game) cmdInspect(p *Player, c proto.Command) (string, *proto.Error) {
	if err := requireArgs(c); err != nil {
		return "", err
	}
	ids := append(append([]string{}, p.Inventory...), g.rooms[p.Room].Items...)
	if id := resolve(c.Args, g.itemCands(ids)); id != "" {
		it := g.items[id]
		return jsonLine(map[string]interface{}{
			"id": id, "type": "item", "name": it.Def.Name, "description": it.Def.Description,
			"obtainable": it.Def.Obtainable, "attack": it.Def.Attack, "defense": it.Def.Defense,
			"heal": it.Def.Heal, "held": it.Holder == p.Name,
			"slot": it.Def.Slot, "min_level": it.Def.MinLevel, "equipped": p.Equipped[it.Def.Slot] == id && id != "",
		}), nil
	}
	if id := resolve(c.Args, g.npcCands(p.Room)); id != "" {
		n := g.npcs[id]
		out := map[string]interface{}{
			"id": id, "type": "npc", "name": n.Def.Name, "description": n.Def.Description,
			"role": n.Def.Role, "hostile": n.Def.Hostile,
		}
		if n.Def.Hostile {
			out["hp"], out["max_hp"] = n.HP, n.MaxHP
		}
		return jsonLine(out), nil
	}
	return "", proto.ErrItemNotFound
}

func (g *Game) cmdHelp(p *Player, c proto.Command) (string, *proto.Error) {
	return jsonLine(map[string]interface{}{"commands": Commands()}), nil
}

// ---------------------------------------------------------------------------
// Communication

func (g *Game) cmdChat(p *Player, c proto.Command) (string, *proto.Error) {
	if g.muted(p) {
		return "", proto.ErrMuted
	}
	scope, msg, _ := strings.Cut(c.Args, " ")
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "", proto.ErrMalformed
	}
	scope = strings.ToUpper(scope)
	line := fmt.Sprintf("EVT %s CHAT %s %s", scope, p.Name, msg)
	switch scope {
	case "GLOBAL":
		g.broadcastAll(line)
	case "ROOM":
		g.broadcastRoom(p.Room, "", line)
	case "GROUP":
		if p.Group == nil {
			return "", proto.ErrNotInGroup
		}
		g.broadcastGroup(p.Group, line)
	default:
		return "", proto.ErrMalformed
	}
	g.log.Info("chat", "player", p.Name, "scope", scope, "message", msg)
	return "", nil
}

type whoReply struct {
	Room    []string `json:"room"`
	Server  int      `json:"server"`
	Players []string `json:"players"`
}

func (g *Game) cmdWho(p *Player, c proto.Command) (string, *proto.Error) {
	return jsonLine(whoReply{
		Room:    append([]string{}, g.rooms[p.Room].Players...),
		Server:  len(g.players),
		Players: g.playerNames(),
	}), nil
}

// ---------------------------------------------------------------------------
// Groups

func (g *Game) cmdGroup(p *Player, c proto.Command) (string, *proto.Error) {
	if len(c.Word) == 0 {
		return "", proto.ErrMalformed
	}
	sub := strings.ToUpper(c.Word[0])
	switch {
	case sub == "CREATE" && len(c.Word) == 1:
		if p.Group != nil {
			return "", proto.ErrAlreadyInGroup
		}
		g.nextGroup++
		gr := &Group{ID: fmt.Sprintf("group.%d", g.nextGroup), Leader: p.Name, Members: []string{p.Name}, Invited: map[string]bool{}}
		g.groups[gr.ID] = gr
		p.Group = gr
		g.log.Info("group_created", "player", p.Name, "group", gr.ID)
		return "group=" + gr.ID, nil

	case sub == "INVITE" && len(c.Word) == 2:
		if p.Group == nil {
			return "", proto.ErrNotInGroup
		}
		t := g.player(c.Word[1])
		if t == nil {
			return "", proto.ErrPlayerNotFound
		}
		if t.Group != nil {
			return "", proto.ErrAlreadyInGroup
		}
		p.Group.Invited[strings.ToLower(t.Name)] = true
		g.send(t, "EVT GROUP INVITE "+p.Group.Leader)
		g.log.Info("group_invite", "player", p.Name, "target", t.Name, "group", p.Group.ID)
		return "", nil

	case sub == "JOIN" && len(c.Word) == 2:
		if p.Group != nil {
			return "", proto.ErrAlreadyInGroup
		}
		l := g.player(c.Word[1])
		if l == nil {
			return "", proto.ErrPlayerNotFound
		}
		gr := l.Group
		if gr == nil {
			return "", proto.ErrNotInGroup
		}
		key := strings.ToLower(p.Name)
		if !gr.Invited[key] {
			return "", proto.ErrNotInvited
		}
		delete(gr.Invited, key)
		gr.Members = append(gr.Members, p.Name)
		p.Group = gr
		g.broadcastGroup(gr, "EVT GROUP JOIN "+p.Name)
		g.log.Info("group_join", "player", p.Name, "group", gr.ID)
		return "group=" + gr.ID, nil

	case sub == "LEAVE" && len(c.Word) == 1:
		if p.Group == nil {
			return "", proto.ErrNotInGroup
		}
		gr := p.Group
		g.removeFromGroup(p)
		g.send(p, "EVT GROUP LEAVE "+p.Name)
		g.broadcastGroup(gr, "EVT GROUP LEAVE "+p.Name)
		return "", nil

	case sub == "INFO" && len(c.Word) == 1:
		if p.Group == nil {
			return "", proto.ErrNotInGroup
		}
		gr := p.Group
		return jsonLine(map[string]interface{}{"group": gr.ID, "leader": gr.Leader, "members": gr.Members}), nil
	}
	return "", proto.ErrMalformed
}

func (g *Game) removeFromGroup(p *Player) {
	gr := p.Group
	p.Group = nil
	gr.Members = removeString(gr.Members, p.Name)
	if len(gr.Members) == 0 {
		delete(g.groups, gr.ID)
	} else if gr.Leader == p.Name {
		gr.Leader = gr.Members[0]
	}
	g.log.Info("group_leave", "player", p.Name, "group", gr.ID, "new_leader", gr.Leader, "remaining", len(gr.Members))
}

// ---------------------------------------------------------------------------
// Items

func (g *Game) cmdTake(p *Player, c proto.Command) (string, *proto.Error) {
	if err := requireArgs(c); err != nil {
		return "", err
	}
	id := resolve(c.Args, g.itemCands(g.rooms[p.Room].Items))
	if id == "" {
		return "", proto.ErrItemNotFound
	}
	it := g.items[id]
	if !it.Def.Obtainable {
		return "", proto.ErrItemNotObtain
	}
	g.giveItem(it, p)
	g.autoEquip(p, it)
	g.log.Info("item_taken", "player", p.Name, "item", id, "room", p.Room)
	g.broadcastRoom(p.Room, p.Name, "EVT ROOM ITEM TAKE "+p.Name+" "+id)
	g.refreshQuests(p)
	return "taken=" + id, nil
}

func (g *Game) cmdDrop(p *Player, c proto.Command) (string, *proto.Error) {
	if err := requireArgs(c); err != nil {
		return "", err
	}
	id := resolve(c.Args, g.itemCands(p.Inventory))
	if id == "" {
		return "", proto.ErrItemNotInInventory
	}
	g.placeItem(g.items[id], p.Room)
	g.log.Info("item_dropped", "player", p.Name, "item", id, "room", p.Room)
	g.broadcastRoom(p.Room, p.Name, "EVT ROOM ITEM DROP "+p.Name+" "+id)
	g.refreshQuests(p)
	return "dropped=" + id, nil
}

func (g *Game) cmdInventory(p *Player, c proto.Command) (string, *proto.Error) {
	return jsonLine(append([]string{}, p.Inventory...)), nil
}

func (g *Game) cmdUse(p *Player, c proto.Command) (string, *proto.Error) {
	if err := requireArgs(c); err != nil {
		return "", err
	}
	id := resolve(c.Args, g.itemCands(p.Inventory))
	if id == "" {
		return "", proto.ErrItemNotInInventory
	}
	it := g.items[id]
	if it.Def.Heal <= 0 {
		return "", proto.ErrItemNotUsable
	}
	before := p.HP
	p.HP = minInt(p.MaxHP, p.HP+it.Def.Heal)
	g.consumeItem(it)
	g.log.Info("item_used", "player", p.Name, "item", id, "healed", p.HP-before, "hp", p.HP)
	out := map[string]interface{}{"used": id, "healed": p.HP - before, "hp": p.HP, "max_hp": p.MaxHP}
	if p.Target != "" {
		// Using an item costs the player's turn: the enemy strikes back.
		res := g.npcTurn(p, g.npcs[p.Target])
		for k, v := range res {
			out[k] = v
		}
	}
	return jsonLine(out), nil
}

// ---------------------------------------------------------------------------
// NPCs

type talkOption struct {
	N    int    `json:"n"`
	Text string `json:"text"`
}

// talkReply extends the RFC reply (npc, name, dialogue) with the options of
// a dialogue tree. No options means the conversation is over.
type talkReply struct {
	NPC      string       `json:"npc"`
	Name     string       `json:"name"`
	Dialogue string       `json:"dialogue"`
	Options  []talkOption `json:"options,omitempty"`
	Quest    *questReply  `json:"quest,omitempty"`
}

func (g *Game) cmdTalk(p *Player, c proto.Command) (string, *proto.Error) {
	if err := requireArgs(c); err != nil {
		return "", err
	}
	id := resolve(c.Args, g.npcCands(p.Room))
	if id == "" {
		return "", proto.ErrNPCNotFound
	}
	n := g.npcs[id]
	g.log.Info("npc_talk", "player", p.Name, "npc", id, "room", p.Room)
	g.countObjective(p, "talk", n.DefID)
	if n.Def.DialogueTree != nil {
		p.talking = n.ID
		return jsonLine(g.showNode(p, n, "start")), nil
	}
	line := "..."
	if len(n.Def.Dialogue) > 0 {
		i := p.talkIndex[n.DefID]
		line = n.Def.Dialogue[i%len(n.Def.Dialogue)]
		p.talkIndex[n.DefID] = i + 1
	}
	if hint := g.questHint(p, n); hint != "" {
		line += " " + hint
	}
	return jsonLine(talkReply{NPC: id, Name: n.Def.Name, Dialogue: line}), nil
}

// cmdSay picks option n (1-based, among the options shown) of the open
// conversation.
func (g *Game) cmdSay(p *Player, c proto.Command) (string, *proto.Error) {
	if len(c.Word) != 1 {
		return "", proto.ErrMalformed
	}
	n := g.npcs[p.talking]
	if n == nil || !n.Alive || n.Room != p.Room {
		p.talking, p.talkNode = "", ""
		return "", proto.ErrNotTalking
	}
	opts := g.visibleOptions(p, n.Def.DialogueTree[p.talkNode])
	k, err := strconv.Atoi(c.Word[0])
	if err != nil || k < 1 || k > len(opts) {
		return "", proto.ErrInvalidChoice
	}
	o := opts[k-1]
	g.log.Info("npc_say", "player", p.Name, "npc", n.ID, "node", p.talkNode, "choice", k, "next", o.Next)
	if o.SetFlag != "" {
		p.Flags[o.SetFlag] = true
	}
	var qr *questReply
	if o.Quest != "" {
		if r, perr := g.questWith(p, n, o.Quest); perr == nil {
			qr = &r
		}
	}
	var rep talkReply
	if o.Next == "" {
		p.talking, p.talkNode = "", ""
		rep = talkReply{NPC: n.ID, Name: n.Def.Name}
	} else {
		rep = g.showNode(p, n, o.Next)
	}
	rep.Quest = qr
	return jsonLine(rep), nil
}

// showNode moves the conversation to a node and renders it. A node with no
// option left to choose ends the conversation.
func (g *Game) showNode(p *Player, n *NPC, node string) talkReply {
	def := n.Def.DialogueTree[node]
	rep := talkReply{NPC: n.ID, Name: n.Def.Name, Dialogue: def.Text}
	for i, o := range g.visibleOptions(p, def) {
		rep.Options = append(rep.Options, talkOption{N: i + 1, Text: o.Text})
	}
	if len(rep.Options) == 0 {
		p.talking, p.talkNode = "", ""
	} else {
		p.talkNode = node
	}
	return rep
}

// visibleOptions filters the options a player may choose: the condition
// must hold, and an option carrying a quest only shows while that quest
// can still be accepted or is under way.
func (g *Game) visibleOptions(p *Player, node *DialogueNode) []DialogueOption {
	if node == nil {
		return nil
	}
	var out []DialogueOption
	for _, o := range node.Options {
		if !g.holds(p, o.If) {
			continue
		}
		if o.Quest != "" && !g.questIs(p, o.Quest, "available") && !g.questIs(p, o.Quest, "active") {
			continue
		}
		out = append(out, o)
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
