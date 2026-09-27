package game

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"hollowmere/internal/limit"
	"hollowmere/internal/proto"
)

// Social features: private messages, friends and the leaderboard.

// Private messages have their own, stricter budget on top of the session
// limits: a burst of 5, then one every 2 seconds.
const (
	TellRate   = 0.5
	TellBurst  = 5
	MaxFriends = 50
)

func (g *Game) cmdTell(p *Player, c proto.Command) (string, *proto.Error) {
	name, msg, _ := strings.Cut(c.Args, " ")
	msg = strings.TrimSpace(msg)
	if name == "" || msg == "" {
		return "", proto.ErrMalformed
	}
	t := g.player(name)
	if t == nil {
		return "", proto.ErrPlayerNotFound
	}
	if t == p {
		return "", proto.ErrMalformed
	}
	if g.muted(p) {
		return "", proto.ErrMuted
	}
	if p.tellBucket == nil {
		p.tellBucket = limit.New(TellRate, TellBurst)
	}
	if !p.tellBucket.Allow(time.Now()) {
		g.log.Warn("tell_rate_limited", "player", p.Name, "target", t.Name)
		return "", proto.ErrRateLimited
	}
	g.send(t, fmt.Sprintf("EVT PRIVATE MESSAGE %s %s", p.Name, msg))
	g.log.Info("tell", "player", p.Name, "target", t.Name, "message", msg)
	return "sent=" + t.Name, nil
}

// ---------------------------------------------------------------------------
// Friends

func (g *Game) isFriend(p *Player, name string) bool {
	for _, f := range p.Friends {
		if strings.EqualFold(f, name) {
			return true
		}
	}
	return false
}

type friendInfo struct {
	Name   string `json:"name"`
	Online bool   `json:"online"`
}

func (g *Game) cmdFriend(p *Player, c proto.Command) (string, *proto.Error) {
	if len(c.Word) == 0 {
		return "", proto.ErrMalformed
	}
	sub := strings.ToUpper(c.Word[0])
	switch {
	case sub == "LIST" && len(c.Word) == 1:
		out := make([]friendInfo, 0, len(p.Friends))
		for _, f := range p.Friends {
			out = append(out, friendInfo{Name: f, Online: g.player(f) != nil})
		}
		return jsonLine(out), nil

	case sub == "ADD" && len(c.Word) == 2:
		name := c.Word[1]
		if strings.EqualFold(name, p.Name) || !proto.ValidUsername(name) {
			return "", proto.ErrMalformed
		}
		if g.isFriend(p, name) {
			return "friend=" + name, nil
		}
		if len(p.Friends) >= MaxFriends {
			return "", proto.ErrTooManyFriends
		}
		if t := g.player(name); t != nil {
			name = t.Name
		} else if !g.characterExists(name) {
			return "", proto.ErrPlayerNotFound
		}
		p.Friends = append(p.Friends, name)
		g.log.Info("friend_added", "player", p.Name, "friend", name)
		return "friend=" + name, nil

	case sub == "REMOVE" && len(c.Word) == 2:
		for i, f := range p.Friends {
			if strings.EqualFold(f, c.Word[1]) {
				p.Friends = append(p.Friends[:i:i], p.Friends[i+1:]...)
				g.log.Info("friend_removed", "player", p.Name, "friend", f)
				return "removed=" + f, nil
			}
		}
		return "", proto.ErrPlayerNotFound
	}
	return "", proto.ErrMalformed
}

// characterExists tells whether a name belongs to a saved character.
func (g *Game) characterExists(name string) bool {
	if g.store == nil {
		return false
	}
	owner, err := g.store.NameOwner(name)
	if err != nil {
		g.log.Error("name_lookup_failed", "error", err.Error())
		return false
	}
	return owner != ""
}

// notifyFriends tells everyone who lists p as a friend that p came or went.
func (g *Game) notifyFriends(p *Player, state string) {
	for _, other := range g.players {
		if other != p && g.isFriend(other, p.Name) {
			g.send(other, "EVT FRIEND "+state+" "+p.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// Leaderboard

// TopEntry is one line of the leaderboard.
type TopEntry struct {
	Rank   int    `json:"rank"`
	Name   string `json:"name"`
	Level  int    `json:"level"`
	XP     int    `json:"xp"`
	Quests int    `json:"quests"`
	Kills  int    `json:"kills"`
	Online bool   `json:"online"`
}

// TopDefault is how many lines TOP returns.
const TopDefault = 10

func (g *Game) cmdTop(p *Player, c proto.Command) (string, *proto.Error) {
	return jsonLine(g.top(TopDefault)), nil
}

// Top returns the leaderboard; it is safe to call from any goroutine.
func (g *Game) Top(n int) []TopEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.top(n)
}

// top merges the saved characters with the live state of connected ones.
// Called with g.mu held.
func (g *Game) top(n int) []TopEntry {
	byName := map[string]*TopEntry{}
	if g.store != nil {
		rows, err := g.store.Top(n + len(g.players))
		if err != nil {
			g.log.Error("top_failed", "error", err.Error())
		}
		for i := range rows {
			byName[strings.ToLower(rows[i].Name)] = &rows[i]
		}
	}
	for key, p := range g.players {
		byName[key] = &TopEntry{Name: p.Name, Level: p.Level, XP: p.XP, Quests: g.completedQuests(p), Kills: p.Kills, Online: true}
	}
	out := make([]TopEntry, 0, len(byName))
	for _, e := range byName {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Level != b.Level:
			return a.Level > b.Level
		case a.XP != b.XP:
			return a.XP > b.XP
		case a.Quests != b.Quests:
			return a.Quests > b.Quests
		case a.Kills != b.Kills:
			return a.Kills > b.Kills
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	if len(out) > n {
		out = out[:n]
	}
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

func (g *Game) completedQuests(p *Player) int {
	n := 0
	for _, st := range p.Quests {
		if st.Status == "completed" {
			n++
		}
	}
	return n
}
