package game

import (
	"fmt"
	"strings"
	"time"

	"hollowmere/internal/proto"
)

// Moderation. Roles belong to resume keys and are granted from the
// console (hollowmere -role), never from inside the game:
//
//	moderator: ANNOUNCE, KICK, MUTE, UNMUTE
//	admin:     all of the above, plus BAN and UNBAN
//
// A ban covers the character's key and its IP address. The IP part never
// lasts more than MaxIPBan, since addresses change hands and are shared
// (a school, a family). Every action is logged and kept in the audit log.

// MaxIPBan caps how long an address stays banned.
const MaxIPBan = 7 * 24 * time.Hour

// Sanction is a ban or a mute. A zero Until means permanent.
type Sanction struct {
	ID      int64
	Kind    string // ban, mute
	Key     string
	IP      string
	Name    string
	Reason  string
	By      string
	Created time.Time
	Until   time.Time
	IPUntil time.Time
}

func (s *Sanction) keyActive(now time.Time) bool {
	return s.Key != "" && (s.Until.IsZero() || now.Before(s.Until))
}

func (s *Sanction) ipActive(now time.Time) bool {
	return s.IP != "" && now.Before(s.IPUntil)
}

// Moderation is the storage behind moderation; the SQLite store implements
// it. Without it nobody holds a role and the commands are refused.
type Moderation interface {
	Role(key string) (string, error)
	LastIP(key string) (string, error)
	AddSanction(s *Sanction) error
	LiftSanctions(kind, key string) (int, error)
	ActiveSanctions(now time.Time) ([]Sanction, error)
	Log(actor, action, target, detail string) error
}

// ReloadSanctions refreshes the sanctions kept in memory; the autosave
// calls it so that changes made from the console are picked up.
func (g *Game) ReloadSanctions() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reloadSanctions()
}

func (g *Game) reloadSanctions() {
	if g.mod == nil {
		return
	}
	list, err := g.mod.ActiveSanctions(time.Now())
	if err != nil {
		g.log.Error("sanctions_load_failed", "error", err.Error())
		return
	}
	g.sanctions = list
}

// banned reports the ban keeping a key or an address out, if any.
func (g *Game) banned(key, ip string) *Sanction {
	now := time.Now()
	for i := range g.sanctions {
		s := &g.sanctions[i]
		if s.Kind != "ban" {
			continue
		}
		if (key != "" && s.Key == key && s.keyActive(now)) || (ip != "" && s.IP == ip && s.ipActive(now)) {
			return s
		}
	}
	return nil
}

// muted reports whether a player may not talk right now.
func (g *Game) muted(p *Player) bool {
	now := time.Now()
	for i := range g.sanctions {
		s := &g.sanctions[i]
		if s.Kind == "mute" && p.Key != "" && s.Key == p.Key && s.keyActive(now) {
			return true
		}
	}
	return false
}

var roleRank = map[string]int{"": 0, "moderator": 1, "admin": 2}

func (g *Game) requireRole(p *Player, role string) *proto.Error {
	if roleRank[p.Role] < roleRank[role] {
		g.log.Warn("moderation_denied", "player", p.Name, "role", p.Role, "needed", role)
		return proto.ErrForbidden
	}
	return nil
}

// audit writes a moderation action to the log and to the audit table.
func (g *Game) audit(actor *Player, action, target, detail string) {
	g.log.Warn("moderation", "actor", actor.Name, "action", action, "target", target, "detail", detail)
	if g.mod != nil {
		if err := g.mod.Log(actor.Name, action, target, detail); err != nil {
			g.log.Error("modlog_failed", "error", err.Error())
		}
	}
}

// target is a character a moderator acts on: online, or known to the store.
type target struct {
	name   string
	key    string
	player *Player // nil when offline
}

func (g *Game) findTarget(actor *Player, name string) (*target, *proto.Error) {
	if p := g.player(name); p != nil {
		if roleRank[p.Role] >= roleRank[actor.Role] {
			return nil, proto.ErrForbidden // no acting on peers or superiors
		}
		return &target{name: p.Name, key: p.Key, player: p}, nil
	}
	if g.store == nil {
		return nil, proto.ErrPlayerNotFound
	}
	key, err := g.store.NameOwner(name)
	if err != nil || key == "" {
		return nil, proto.ErrPlayerNotFound
	}
	if role, _ := g.mod.Role(key); roleRank[role] >= roleRank[actor.Role] {
		return nil, proto.ErrForbidden
	}
	return &target{name: name, key: key}, nil
}

// closeSession ends a player's connection after a last message.
func (g *Game) closeSession(p *Player, event, reason string) {
	g.send(p, fmt.Sprintf("EVT SERVER %s %s", event, reason))
	if c, ok := p.Sink.(interface{ Close(reason string) }); ok {
		c.Close(strings.ToLower(event))
	}
}

// parseDuration reads "10m", "2h", "3d" or "perm"; ok is false when the
// word is not a duration at all.
func parseDuration(word string) (d time.Duration, ok bool) {
	w := strings.ToLower(word)
	if w == "perm" || w == "permanent" {
		return 0, true
	}
	if strings.HasSuffix(w, "d") {
		var n int
		if _, err := fmt.Sscanf(w, "%dd", &n); err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour, true
		}
		return 0, false
	}
	d, err := time.ParseDuration(w)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

func restFrom(c proto.Command, n int) string {
	if len(c.Word) <= n {
		return ""
	}
	return strings.Join(c.Word[n:], " ")
}

// ---------------------------------------------------------------------------
// Commands

func (g *Game) cmdAnnounce(p *Player, c proto.Command) (string, *proto.Error) {
	if err := g.requireRole(p, "moderator"); err != nil {
		return "", err
	}
	if c.Args == "" {
		return "", proto.ErrMalformed
	}
	g.broadcastAll("EVT SERVER ANNOUNCE " + c.Args)
	g.audit(p, "announce", "", c.Args)
	return "", nil
}

func (g *Game) cmdKick(p *Player, c proto.Command) (string, *proto.Error) {
	if err := g.requireRole(p, "moderator"); err != nil {
		return "", err
	}
	if len(c.Word) < 1 {
		return "", proto.ErrMalformed
	}
	t, perr := g.findTarget(p, c.Word[0])
	if perr != nil {
		return "", perr
	}
	if t.player == nil {
		return "", proto.ErrPlayerNotFound
	}
	reason := restFrom(c, 1)
	g.closeSession(t.player, "KICK", reason)
	g.audit(p, "kick", t.name, reason)
	return "kicked=" + t.name, nil
}

func (g *Game) cmdMute(p *Player, c proto.Command) (string, *proto.Error) {
	if err := g.requireRole(p, "moderator"); err != nil {
		return "", err
	}
	if len(c.Word) < 2 {
		return "", proto.ErrMalformed
	}
	d, ok := parseDuration(c.Word[1])
	if !ok || d == 0 {
		return "", proto.ErrMalformed // mutes always end
	}
	t, perr := g.findTarget(p, c.Word[0])
	if perr != nil {
		return "", perr
	}
	now := time.Now()
	s := Sanction{Kind: "mute", Key: t.key, Name: t.name, Reason: restFrom(c, 2), By: p.Name, Created: now, Until: now.Add(d)}
	if err := g.addSanction(&s); err != nil {
		return "", err
	}
	if t.player != nil {
		g.send(t.player, fmt.Sprintf("EVT SERVER MUTE %s %s", d, s.Reason))
	}
	g.audit(p, "mute", t.name, fmt.Sprintf("%s %s", d, s.Reason))
	return fmt.Sprintf("muted=%s until=%s", t.name, s.Until.UTC().Format(time.RFC3339)), nil
}

func (g *Game) cmdUnmute(p *Player, c proto.Command) (string, *proto.Error) {
	return g.lift(p, c, "moderator", "mute")
}

func (g *Game) cmdUnban(p *Player, c proto.Command) (string, *proto.Error) {
	return g.lift(p, c, "admin", "ban")
}

func (g *Game) lift(p *Player, c proto.Command, role, kind string) (string, *proto.Error) {
	if err := g.requireRole(p, role); err != nil {
		return "", err
	}
	if len(c.Word) != 1 {
		return "", proto.ErrMalformed
	}
	t, perr := g.findTarget(p, c.Word[0])
	if perr != nil {
		return "", perr
	}
	n, err := g.mod.LiftSanctions(kind, t.key)
	if err != nil {
		g.log.Error("lift_failed", "error", err.Error())
		return "", proto.ErrStorage
	}
	g.reloadSanctions()
	g.audit(p, "un"+kind, t.name, fmt.Sprintf("%d lifted", n))
	return fmt.Sprintf("lifted=%d", n), nil
}

func (g *Game) cmdBan(p *Player, c proto.Command) (string, *proto.Error) {
	if err := g.requireRole(p, "admin"); err != nil {
		return "", err
	}
	if len(c.Word) < 1 {
		return "", proto.ErrMalformed
	}
	t, perr := g.findTarget(p, c.Word[0])
	if perr != nil {
		return "", perr
	}
	var d time.Duration // permanent unless a duration follows the name
	reason := restFrom(c, 1)
	if len(c.Word) >= 2 {
		if dd, ok := parseDuration(c.Word[1]); ok {
			d, reason = dd, restFrom(c, 2)
		}
	}
	now := time.Now()
	s := Sanction{Kind: "ban", Key: t.key, Name: t.name, Reason: reason, By: p.Name, Created: now}
	if d > 0 {
		s.Until = now.Add(d)
	}
	if t.player != nil {
		s.IP = t.player.IP
	} else if ip, err := g.mod.LastIP(t.key); err == nil {
		s.IP = ip
	}
	if s.IP != "" {
		ipd := MaxIPBan
		if d > 0 && d < ipd {
			ipd = d
		}
		s.IPUntil = now.Add(ipd)
	}
	if err := g.addSanction(&s); err != nil {
		return "", err
	}
	// Everyone playing from that address leaves, not only the target.
	for _, o := range g.players {
		if o == t.player || (s.IP != "" && o.IP == s.IP && roleRank[o.Role] < roleRank[p.Role]) {
			g.closeSession(o, "BAN", reason)
		}
	}
	until := "permanent"
	if !s.Until.IsZero() {
		until = s.Until.UTC().Format(time.RFC3339)
	}
	g.audit(p, "ban", t.name, fmt.Sprintf("until=%s ip=%t %s", until, s.IP != "", reason))
	return fmt.Sprintf("banned=%s until=%s ip=%t", t.name, until, s.IP != ""), nil
}

func (g *Game) addSanction(s *Sanction) *proto.Error {
	if g.mod == nil {
		return proto.ErrStorage
	}
	if s.Key == "" {
		return proto.ErrPlayerNotFound // no persistence: nothing to attach it to
	}
	if err := g.mod.AddSanction(s); err != nil {
		g.log.Error("sanction_failed", "error", err.Error())
		return proto.ErrStorage
	}
	g.sanctions = append(g.sanctions, *s)
	return nil
}
