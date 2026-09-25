// Command client is the text (CLI) client for TAP servers.
//
// Friendly commands ("go north", "say hi", "take healing herbs") are
// translated into RFC 42TAP packets; lines written in upper-case protocol
// syntax ("MOVE north") or prefixed with "raw " are sent unchanged.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"hollowmere/internal/proto"
)

var protocolCommands = map[string]bool{
	"CONNECT": true, "LOOK": true, "MOVE": true, "CHAT": true, "TAKE": true, "DROP": true,
	"INVENTORY": true, "TALK": true, "ATTACK": true, "STATUS": true, "QUEST": true,
	"QUESTS": true, "WHO": true, "GROUP": true, "QUIT": true, "DEFEND": true, "FLEE": true,
	"USE": true, "ABANDON": true, "INSPECT": true, "HELP": true,
}

// term serialises all terminal output so that asynchronous events do not
// garble the prompt.
type term struct {
	mu     sync.Mutex
	color  bool
	prompt string
}

const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cRed    = "\033[31m"
	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cBlue   = "\033[34m"
	cPurple = "\033[35m"
	cCyan   = "\033[36m"
	cGrey   = "\033[90m"
)

func (t *term) paint(color, s string) string {
	if !t.color {
		return s
	}
	return color + s + cReset
}

// print writes lines above the prompt and redraws it.
func (t *term) print(lines ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.color {
		fmt.Print("\r\033[K")
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	if t.color {
		fmt.Print(t.prompt)
	}
}

func (t *term) showPrompt() {
	if !t.color {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Print(t.prompt)
}

type client struct {
	conn    net.Conn
	keyID   string // "<server>|<name>" under which the resume key is stored
	t       *term
	mu      sync.Mutex
	pending []string // command names awaiting a reply, in order
	greeted bool
	closed  chan struct{}
	name    string
}

func (c *client) send(line string) error {
	cmd, _ := proto.ParseCommand(line)
	c.mu.Lock()
	c.pending = append(c.pending, cmd.Name)
	c.mu.Unlock()
	_, err := c.conn.Write([]byte(line + "\n"))
	return err
}

func (c *client) popPending() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		return ""
	}
	name := c.pending[0]
	c.pending = c.pending[1:]
	return name
}

func (c *client) readLoop() {
	defer close(c.closed)
	lr := proto.NewLineReader(c.conn, 0)
	for {
		line, err := lr.ReadLine()
		if err != nil {
			c.t.print(c.t.paint(cRed, "*** connection closed by server"))
			return
		}
		c.handle(line)
	}
}

func (c *client) handle(line string) {
	kind, payload := proto.Classify(line)
	switch kind {
	case proto.KindEvent:
		c.t.print(c.renderEvent(payload))
	case proto.KindOK, proto.KindErr:
		if !c.greeted && kind == proto.KindOK && strings.HasPrefix(payload, "hello") {
			c.greeted = true
			c.t.print(c.t.paint(cGrey, "server: "+payload))
			return
		}
		cmd := c.popPending()
		if kind == proto.KindErr {
			c.t.print(c.t.paint(cRed, "✗ "+describeError(payload)))
			return
		}
		c.t.print(c.renderOK(cmd, payload)...)
	default:
		c.t.print(line)
	}
}

func describeError(payload string) string {
	hints := map[string]string{
		"NAME_IN_USE":           "that name is already taken",
		"NO_EXIT":               "you cannot go that way",
		"NOT_IN_GROUP":          "you are not in a group",
		"ALREADY_IN_GROUP":      "already in a group",
		"ITEM_NOT_FOUND":        "no such item here",
		"ITEM_NOT_IN_INVENTORY": "you are not carrying that",
		"NPC_NOT_FOUND":         "nobody by that name here",
		"NPC_NOT_HOSTILE":       "you cannot attack them",
		"NO_QUEST_AVAILABLE":    "they have no quest for you",
		"UNKNOWN_COMMAND":       "unknown command (type 'help')",
		"MALFORMED_COMMAND":     "malformed command (type 'help')",
		"IN_COMBAT":             "you are in combat: attack, defend or flee",
		"NOT_IN_COMBAT":         "you are not fighting anyone",
		"ITEM_NOT_OBTAINABLE":   "that cannot be picked up",
		"ITEM_NOT_USABLE":       "you cannot use that",
		"NOT_INVITED":           "you have not been invited to that group",
		"PLAYER_NOT_FOUND":      "no such player",
		"RATE_LIMITED":          "slow down!",
		"NOT_AUTHENTICATED":     "connect first",
	}
	fields := strings.Fields(payload)
	if len(fields) >= 2 {
		if h, ok := hints[fields[1]]; ok {
			return payload + " — " + h
		}
	}
	return payload
}

func (c *client) renderEvent(p string) string {
	f := strings.SplitN(p, " ", 4)
	get := func(i int) string {
		if i < len(f) {
			return f[i]
		}
		return ""
	}
	switch {
	case get(1) == "CHAT":
		rest := strings.SplitN(p, " ", 4)
		msg := ""
		if len(rest) == 4 {
			msg = rest[3]
		}
		colors := map[string]string{"GLOBAL": cYellow, "ROOM": cCyan, "GROUP": cGreen}
		return c.t.paint(colors[get(0)], fmt.Sprintf("[%s] %s: %s", strings.ToLower(get(0)), get(2), msg))
	case get(0) == "ROOM" && get(1) == "PRESENCE":
		verb := "arrives"
		if get(2) == "LEAVE" {
			verb = "leaves"
		}
		return c.t.paint(cBlue, fmt.Sprintf("» %s %s.", get(3), verb))
	case get(0) == "ROOM" && get(1) == "COMBAT":
		return c.t.paint(cPurple, "⚔ "+strings.Join(f[2:], " "))
	case get(0) == "GROUP" && get(1) == "INVITE":
		return c.t.paint(cGreen, fmt.Sprintf("✉ %s invites you to their group. Type: group join %s", get(2), get(2)))
	case get(0) == "GROUP":
		return c.t.paint(cGreen, fmt.Sprintf("[group] %s %s", get(2), strings.ToLower(get(1))+"s"))
	case get(0) == "STATS":
		return c.t.paint(cGrey, "· online: "+strings.TrimPrefix(get(1), "players="))
	case get(0) == "QUEST":
		return c.t.paint(cYellow, "★ quest "+strings.ToLower(get(1))+": "+strings.Join(f[2:], " "))
	case get(0) == "PLAYER" && get(1) == "AMBUSH":
		a := strings.Fields(p)
		if len(a) >= 5 {
			return c.t.paint(cRed, fmt.Sprintf("⚔ %s ambushes you for %s damage (HP %s)! attack, defend or flee", a[2], a[3], a[4]))
		}
	case get(0) == "PLAYER" && get(1) == "KEY":
		if err := saveKey(c.keyID, get(2)); err != nil {
			return c.t.paint(cRed, "could not save your resume key: "+err.Error())
		}
		return c.t.paint(cGrey, "· character saved; this terminal will bring it back automatically")
	case get(0) == "PLAYER" && get(1) == "RESPAWN":
		return c.t.paint(cRed, "☠ You were defeated and wake up in "+get(2)+". (type look)")
	}
	return c.t.paint(cGrey, "event: "+p)
}

func pretty(payload string) string {
	var v interface{}
	if json.Unmarshal([]byte(payload), &v) != nil {
		return payload
	}
	b, err := json.MarshalIndent(v, "  ", "  ")
	if err != nil {
		return payload
	}
	return "  " + string(b)
}

func (c *client) renderOK(cmd, payload string) []string {
	ok := func(s string) []string { return []string{c.t.paint(cGreen, "✓ "+s)} }
	switch cmd {
	case "CONNECT":
		return ok("connected. Type 'help' for commands.")
	case "LOOK":
		return c.renderLook(payload)
	case "MOVE":
		return ok("you move to " + strings.TrimPrefix(payload, "room="))
	case "TAKE":
		return ok("taken: " + strings.TrimPrefix(payload, "taken="))
	case "DROP":
		return ok("dropped: " + strings.TrimPrefix(payload, "dropped="))
	case "CHAT", "GROUP":
		if payload == "" {
			return nil
		}
		return ok(payload)
	case "INVENTORY":
		var items []string
		if json.Unmarshal([]byte(payload), &items) == nil {
			if len(items) == 0 {
				return ok("your bag is empty")
			}
			return ok("you carry: " + strings.Join(items, ", "))
		}
	case "TALK":
		var t struct{ NPC, Name, Dialogue string }
		if json.Unmarshal([]byte(payload), &t) == nil && t.Dialogue != "" {
			who := t.Name
			if who == "" {
				who = t.NPC
			}
			return []string{c.t.paint(cCyan, fmt.Sprintf("%s says: \"%s\"", who, t.Dialogue))}
		}
		return []string{c.t.paint(cCyan, "» "+payload)}
	case "WHO":
		var w struct {
			Room    []string `json:"room"`
			Server  int      `json:"server"`
			Players []string `json:"players"`
		}
		if json.Unmarshal([]byte(payload), &w) == nil {
			out := fmt.Sprintf("here: %s | online: %d", strings.Join(w.Room, ", "), w.Server)
			if len(w.Players) > 0 {
				out += " (" + strings.Join(w.Players, ", ") + ")"
			}
			return ok(out)
		}
	case "ATTACK", "DEFEND", "FLEE", "USE":
		return c.renderCombat(payload)
	case "STATUS":
		var s struct {
			HP       int    `json:"hp"`
			MaxHP    int    `json:"max_hp"`
			Status   string `json:"status"`
			Target   string `json:"target"`
			TargetHP *int   `json:"target_hp"`
			Attack   int    `json:"attack"`
			Defense  int    `json:"defense"`
			InCombat bool   `json:"in_combat"`
		}
		if json.Unmarshal([]byte(payload), &s) == nil {
			line := fmt.Sprintf("HP %d/%d (%s)", s.HP, s.MaxHP, s.Status)
			if s.Attack > 0 {
				line += fmt.Sprintf(" atk %d def %d", s.Attack, s.Defense)
			}
			if s.InCombat && s.TargetHP != nil {
				line += fmt.Sprintf(" — fighting %s (hp %d)", s.Target, *s.TargetHP)
			}
			return ok(line)
		}
	case "QUEST":
		var q questInfo
		if json.Unmarshal([]byte(payload), &q) == nil && q.QuestID != "" {
			return c.renderQuest(q)
		}
	case "QUESTS":
		var qs []questInfo
		if json.Unmarshal([]byte(payload), &qs) == nil {
			if len(qs) == 0 {
				return ok("no quests yet — talk to the villagers")
			}
			out := []string{c.t.paint(cYellow, "Quest journal:")}
			for _, q := range qs {
				out = append(out, fmt.Sprintf("  [%s] %s (%s) %s", q.Status, q.title(), q.QuestID, q.Progress))
			}
			return out
		}
	case "INSPECT":
		var d map[string]interface{}
		if json.Unmarshal([]byte(payload), &d) == nil && d["name"] != nil {
			line := fmt.Sprintf("%v (%v): %v", d["name"], d["id"], d["description"])
			for _, k := range []string{"attack", "defense", "heal", "hp"} {
				if v, isNum := d[k].(float64); isNum && v > 0 {
					line += fmt.Sprintf(" [%s %d]", k, int(v))
				}
			}
			return []string{c.t.paint(cCyan, line)}
		}
	case "QUIT":
		return ok("bye")
	}
	if payload == "" {
		return ok("ok")
	}
	return []string{c.t.paint(cGreen, "✓"), pretty(payload)}
}

type questInfo struct {
	QuestID     string   `json:"quest_id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Reward      string   `json:"reward"`
	Status      string   `json:"status"`
	Progress    string   `json:"progress"`
	Granted     []string `json:"granted"`
	Message     string   `json:"message"`
}

func (q questInfo) title() string {
	if q.Title != "" {
		return q.Title
	}
	return q.QuestID
}

func (c *client) renderQuest(q questInfo) []string {
	out := []string{c.t.paint(cYellow, fmt.Sprintf("★ %s [%s] %s", q.title(), q.Status, q.Progress))}
	if q.Description != "" {
		out = append(out, "  "+q.Description)
	}
	if q.Reward != "" {
		out = append(out, "  Reward: "+q.Reward)
	}
	if len(q.Granted) > 0 {
		out = append(out, c.t.paint(cGreen, "  Received: "+strings.Join(q.Granted, ", ")))
	}
	if q.Message != "" {
		out = append(out, "  "+q.Message)
	}
	return out
}

func (c *client) renderLook(payload string) []string {
	var l struct {
		Room struct {
			ID, Name, Description string
			Exits                 map[string]string
		}
		Players []string
		Items   []string
		NPCs    []string
		Details struct {
			Items []struct{ ID, Name string }
			NPCs  []struct {
				ID, Name string
				Hostile  bool
				HP       int
				MaxHP    int `json:"max_hp"`
			}
		}
	}
	if err := json.Unmarshal([]byte(payload), &l); err != nil {
		return []string{pretty(payload)}
	}
	names := map[string]string{}
	for _, it := range l.Details.Items {
		names[it.ID] = it.Name
	}
	hostile := map[string]string{}
	for _, n := range l.Details.NPCs {
		names[n.ID] = n.Name
		if n.Hostile {
			hostile[n.ID] = fmt.Sprintf(" [hostile %d/%d]", n.HP, n.MaxHP)
		}
	}
	label := func(id string) string {
		if n, ok := names[id]; ok {
			return fmt.Sprintf("%s (%s)", n, id)
		}
		return id
	}
	out := []string{
		c.t.paint(cBold, "== "+l.Room.Name+" ==") + c.t.paint(cGrey, " "+l.Room.ID),
		"  " + l.Room.Description,
	}
	dirs := make([]string, 0, len(l.Room.Exits))
	for d := range l.Room.Exits {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	out = append(out, c.t.paint(cBlue, "  Exits: "+strings.Join(dirs, ", ")))
	if len(l.Items) > 0 {
		var s []string
		for _, id := range l.Items {
			s = append(s, label(id))
		}
		out = append(out, c.t.paint(cYellow, "  Items: "+strings.Join(s, ", ")))
	}
	if len(l.NPCs) > 0 {
		var s []string
		for _, id := range l.NPCs {
			s = append(s, label(id)+hostile[id])
		}
		out = append(out, c.t.paint(cPurple, "  NPCs: "+strings.Join(s, ", ")))
	}
	out = append(out, c.t.paint(cCyan, "  Players: "+strings.Join(l.Players, ", ")))
	return out
}

func (c *client) renderCombat(payload string) []string {
	var r map[string]interface{}
	if json.Unmarshal([]byte(payload), &r) != nil {
		return []string{payload}
	}
	var out []string
	if logs, ok := r["log"].([]interface{}); ok {
		for _, l := range logs {
			out = append(out, c.t.paint(cPurple, "⚔ "+fmt.Sprint(l)))
		}
	}
	num := func(k string) string {
		if v, ok := r[k].(float64); ok {
			return fmt.Sprintf("%d", int(v))
		}
		return "?"
	}
	if used, ok := r["used"]; ok {
		out = append(out, c.t.paint(cGreen, fmt.Sprintf("✓ used %v, healed %s (HP %s/%s)", used, num("healed"), num("hp"), num("max_hp"))))
	}
	switch r["status"] {
	case "fled":
		out = append(out, c.t.paint(cGreen, fmt.Sprintf("✓ you fled %v to %v", r["direction"], r["room"])))
	case "victory":
		out = append(out, c.t.paint(cGreen, fmt.Sprintf("✓ victory! HP %s", num("attacker_hp"))))
	case "defeated":
		out = append(out, c.t.paint(cRed, fmt.Sprintf("☠ defeated — respawned in %v with %s HP", r["respawn_room"], num("attacker_hp"))))
	case "combat":
		out = append(out, fmt.Sprintf("  you: %s HP | %v: %s/%s HP", num("attacker_hp"), r["target"], num("target_hp"), num("target_max_hp")))
	}
	if len(out) == 0 {
		out = append(out, pretty(payload))
	}
	return out
}

// translate turns a friendly command into a protocol line.
func translate(input string) (string, error) {
	input = strings.TrimSpace(input)
	word, rest, _ := strings.Cut(input, " ")
	rest = strings.TrimSpace(rest)
	if protocolCommands[word] {
		return input, nil // already RFC syntax
	}
	need := func(cmd string) (string, error) {
		if rest == "" {
			return "", fmt.Errorf("usage: %s <argument>", strings.ToLower(word))
		}
		return cmd + " " + rest, nil
	}
	dirs := map[string]string{
		"n": "north", "s": "south", "e": "east", "w": "west", "u": "up", "d": "down",
		"north": "north", "south": "south", "east": "east", "west": "west", "up": "up", "down": "down",
	}
	lw := strings.ToLower(word)
	if d, ok := dirs[lw]; ok && rest == "" {
		return "MOVE " + d, nil
	}
	switch lw {
	case "raw":
		if rest == "" {
			return "", fmt.Errorf("usage: raw <protocol line>")
		}
		return rest, nil
	case "look", "l":
		return "LOOK", nil
	case "go", "move", "walk":
		if d, ok := dirs[strings.ToLower(rest)]; ok {
			return "MOVE " + d, nil
		}
		return need("MOVE")
	case "say":
		return need("CHAT ROOM")
	case "shout", "yell", "g":
		return need("CHAT GLOBAL")
	case "gsay", "gs", "party":
		return need("CHAT GROUP")
	case "chat":
		return need("CHAT")
	case "take", "get", "pick":
		return need("TAKE")
	case "drop":
		return need("DROP")
	case "inventory", "inv", "i":
		return "INVENTORY", nil
	case "talk", "speak":
		return need("TALK")
	case "attack", "kill", "k", "hit":
		return need("ATTACK")
	case "defend", "block":
		return "DEFEND", nil
	case "flee", "run":
		return "FLEE", nil
	case "use", "drink", "eat":
		return need("USE")
	case "status", "st", "hp":
		return "STATUS", nil
	case "quest":
		return need("QUEST")
	case "quests", "journal", "j":
		return "QUESTS", nil
	case "abandon":
		return need("ABANDON")
	case "who":
		return "WHO", nil
	case "inspect", "examine", "x":
		return need("INSPECT")
	case "group":
		sub, arg, _ := strings.Cut(rest, " ")
		sub = strings.ToUpper(sub)
		switch sub {
		case "CREATE", "LEAVE", "INFO":
			return "GROUP " + sub, nil
		case "INVITE", "JOIN":
			if strings.TrimSpace(arg) == "" {
				return "", fmt.Errorf("usage: group %s <player>", strings.ToLower(sub))
			}
			return "GROUP " + sub + " " + strings.TrimSpace(arg), nil
		}
		return "", fmt.Errorf("usage: group create|invite <p>|join <leader>|leave|info")
	case "quit", "exit", "q":
		return "QUIT", nil
	}
	return "", fmt.Errorf("unknown command %q (type 'help')", word)
}

const helpText = `Commands (protocol syntax such as "MOVE north" also works):
  look | l                     describe the room
  go <dir> | n s e w u d       move
  say <msg>                    chat to the room
  shout <msg>                  chat to everyone
  gsay <msg>                   chat to your group
  take <item> / drop <item>    pick up / drop (id or name, multi-word ok)
  inv | i                      inventory
  use <item>                   drink/eat a healing item
  inspect <thing>              details about an item or NPC
  talk <npc>                   talk to an NPC
  attack <npc> | defend | flee combat (turn-based)
  status                       your health
  quest <npc>                  accept / check / turn in a quest
  quests | abandon <quest_id>  your quest journal
  who                          players here and online
  group create | invite <p> | join <leader> | leave | info
  raw <line>                   send a raw protocol line
  quit                         leave the game`

// Resume keys are stored per server and per character name, so the same
// terminal can play several characters.
func keyFilePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "hollowmere", "keys.json")
}

func loadKeys() map[string]string {
	keys := map[string]string{}
	path := keyFilePath()
	if path == "" {
		return keys
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return keys
	}
	_ = json.Unmarshal(data, &keys)
	return keys
}

func saveKey(id, key string) error {
	path := keyFilePath()
	if path == "" {
		return nil
	}
	keys := loadKeys()
	if keys[id] == key {
		return nil
	}
	keys[id] = key
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	// The key is the only secret of a character: keep the file private.
	return os.WriteFile(path, data, 0o600)
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func main() {
	addr := flag.String("addr", "127.0.0.1:4243", "server address")
	name := flag.String("name", "", "player name (asked if empty)")
	noColor := flag.Bool("no-color", false, "disable colors")
	key := flag.String("key", "", "resume key (default: the one saved for this server and name)")
	newChar := flag.Bool("new", false, "ignore the saved key and start a new character")
	flag.Parse()

	t := &term{color: !*noColor && isTerminal(os.Stdout), prompt: "> "}
	conn, err := net.DialTimeout("tcp", *addr, 5*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", proto.ErrConnectionFailed.Error(), err)
		os.Exit(1)
	}
	c := &client{conn: conn, t: t, closed: make(chan struct{})}
	go c.readLoop()

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 4096), proto.MaxLineLength*4)
	if *name == "" {
		t.mu.Lock()
		fmt.Print("Your name: ")
		t.mu.Unlock()
		if !in.Scan() {
			return
		}
		*name = strings.TrimSpace(in.Text())
	}
	c.name = *name
	c.keyID = *addr + "|" + strings.ToLower(*name)
	if *key == "" && !*newChar {
		*key = loadKeys()[c.keyID]
	}
	connectLine := "CONNECT " + *name
	if *key != "" {
		connectLine += " " + *key
	}
	if err := c.send(connectLine); err != nil {
		fmt.Fprintln(os.Stderr, proto.ErrSendFailed.Error())
		os.Exit(1)
	}
	_ = c.send("LOOK")

	lines := make(chan string)
	go func() {
		defer close(lines)
		for in.Scan() {
			lines <- in.Text()
		}
	}()
	t.showPrompt()
	for {
		select {
		case <-c.closed:
			fmt.Println()
			return
		case input, ok := <-lines:
			if !ok {
				_ = c.send("QUIT")
				select {
				case <-c.closed:
				case <-time.After(2 * time.Second):
				}
				return
			}
			input = strings.TrimSpace(input)
			if input == "" {
				t.showPrompt()
				continue
			}
			if strings.EqualFold(input, "help") || input == "?" {
				t.print(helpText)
				continue
			}
			line, err := translate(input)
			if err != nil {
				t.print(t.paint(cRed, err.Error()))
				continue
			}
			if err := c.send(line); err != nil {
				t.print(t.paint(cRed, proto.ErrSendFailed.Error()))
				return
			}
		}
	}
}
