package game

import (
	"strings"

	"hollowmere/internal/proto"
)

// Trading between two players standing in the same room.
//
//	A: TRADE bob          -> bob gets EVT TRADE REQUEST A
//	B: TRADE alice        -> both get EVT TRADE OPEN <other>
//	TRADE OFFER <item> / TRADE REMOVE <item>
//	TRADE ACCEPT          -> the swap happens once both sides accepted
//	TRADE CANCEL | TRADE INFO
//
// Any change to either offer withdraws both acceptances, so nobody can
// swap an item out after the other side agreed. The swap itself runs in a
// single command, under the world lock, after checking that every offered
// item is still held by its owner: an item is never lost nor duplicated.

// MaxTradeItems caps one side of a trade.
const MaxTradeItems = 8

// Trade is an open exchange between two players.
type Trade struct {
	players  [2]*Player
	offers   [2][]string // item instance ids
	accepted [2]bool
}

func (t *Trade) side(p *Player) int {
	if t.players[0] == p {
		return 0
	}
	return 1
}

type tradeView struct {
	With         string   `json:"with"`
	Mine         []string `json:"mine"`
	Theirs       []string `json:"theirs"`
	Accepted     bool     `json:"accepted"`
	TheyAccepted bool     `json:"they_accepted"`
}

func (t *Trade) view(p *Player) tradeView {
	me := t.side(p)
	return tradeView{
		With:         t.players[1-me].Name,
		Mine:         append([]string{}, t.offers[me]...),
		Theirs:       append([]string{}, t.offers[1-me]...),
		Accepted:     t.accepted[me],
		TheyAccepted: t.accepted[1-me],
	}
}

var tradeSubcommands = map[string]bool{"OFFER": true, "REMOVE": true, "ACCEPT": true, "CANCEL": true, "INFO": true, "WITH": true}

func (g *Game) cmdTrade(p *Player, c proto.Command) (string, *proto.Error) {
	if len(c.Word) == 0 {
		return "", proto.ErrMalformed
	}
	sub := strings.ToUpper(c.Word[0])
	if !tradeSubcommands[sub] {
		return g.tradeRequest(p, c.Args)
	}
	arg := strings.TrimSpace(strings.TrimPrefix(c.Args, c.Word[0]))
	if sub == "WITH" {
		return g.tradeRequest(p, arg)
	}
	t := p.trade
	if t == nil {
		return "", proto.ErrNotTrading
	}
	me := t.side(p)
	switch sub {
	case "INFO":
		g.pruneTrade(t)
		return jsonLine(t.view(p)), nil
	case "CANCEL":
		g.cancelTrade(t, p.Name)
		return "", nil
	case "OFFER", "REMOVE":
		if arg == "" {
			return "", proto.ErrMalformed
		}
		var id string
		if sub == "OFFER" {
			id = resolve(arg, g.itemCands(p.Inventory))
		} else {
			id = resolve(arg, g.itemCands(t.offers[me]))
		}
		if id == "" {
			return "", proto.ErrItemNotInInventory
		}
		if sub == "OFFER" {
			if containsString(t.offers[me], id) {
				return jsonLine(t.view(p)), nil
			}
			if len(t.offers[me]) >= MaxTradeItems {
				return "", proto.ErrMalformed
			}
			t.offers[me] = append(t.offers[me], id)
		} else {
			t.offers[me] = removeString(t.offers[me], id)
		}
		g.pruneTrade(t)
		t.accepted = [2]bool{}
		g.tradeUpdate(t)
		return jsonLine(t.view(p)), nil
	case "ACCEPT":
		if g.pruneTrade(t) {
			// Something changed under the offer: show it again first.
			t.accepted = [2]bool{}
			g.tradeUpdate(t)
			return jsonLine(t.view(p)), nil
		}
		t.accepted[me] = true
		if !t.accepted[1-me] {
			g.tradeUpdate(t)
			return jsonLine(t.view(p)), nil
		}
		return jsonLine(g.executeTrade(t, p)), nil
	}
	return "", proto.ErrMalformed
}

// tradeRequest asks another player to trade, or answers their request.
func (g *Game) tradeRequest(p *Player, name string) (string, *proto.Error) {
	name = strings.TrimSpace(name)
	o := g.player(name)
	if o == nil || o == p || o.Room != p.Room {
		return "", proto.ErrPlayerNotFound
	}
	if p.trade != nil || o.trade != nil {
		return "", proto.ErrTradeBusy
	}
	if strings.EqualFold(o.tradeAsk, p.Name) {
		// o asked first: open the trade.
		t := &Trade{players: [2]*Player{o, p}}
		o.trade, p.trade = t, t
		o.tradeAsk, p.tradeAsk = "", ""
		g.send(o, "EVT TRADE OPEN "+p.Name)
		g.send(p, "EVT TRADE OPEN "+o.Name)
		g.log.Info("trade_opened", "player", p.Name, "with", o.Name)
		return jsonLine(t.view(p)), nil
	}
	p.tradeAsk = o.Name
	g.send(o, "EVT TRADE REQUEST "+p.Name)
	g.log.Info("trade_requested", "player", p.Name, "with", o.Name)
	return "requested=" + o.Name, nil
}

// pruneTrade drops offered items their owner no longer holds (used,
// dropped). It reports whether anything was removed.
func (g *Game) pruneTrade(t *Trade) bool {
	changed := false
	for i, p := range t.players {
		kept := t.offers[i][:0:0]
		for _, id := range t.offers[i] {
			if it := g.items[id]; it != nil && it.Holder == p.Name {
				kept = append(kept, id)
			} else {
				changed = true
			}
		}
		t.offers[i] = kept
	}
	return changed
}

func (g *Game) tradeUpdate(t *Trade) {
	for _, p := range t.players {
		g.send(p, "EVT TRADE UPDATE "+jsonLine(t.view(p)))
	}
}

// executeTrade swaps both offers in one step. Called with g.mu held, after
// pruneTrade confirmed that every offered item is still in place.
func (g *Game) executeTrade(t *Trade, actor *Player) tradeView {
	a, b := t.players[0], t.players[1]
	moves := [2][]*Item{}
	for i := range t.players {
		for _, id := range t.offers[i] {
			moves[i] = append(moves[i], g.items[id])
		}
	}
	for _, it := range moves[0] {
		g.giveItem(it, b)
	}
	for _, it := range moves[1] {
		g.giveItem(it, a)
	}
	for i, p := range t.players {
		for _, it := range moves[1-i] {
			g.autoEquip(p, it)
		}
	}
	res := t.view(actor)
	for _, p := range t.players {
		p.trade = nil
		g.send(p, "EVT TRADE DONE "+jsonLine(t.view(p)))
		g.refreshQuests(p)
	}
	g.log.Info("trade_done", "a", a.Name, "b", b.Name, "a_gave", t.offers[0], "b_gave", t.offers[1])
	return res
}

// cancelTrade closes a trade without moving anything.
func (g *Game) cancelTrade(t *Trade, by string) {
	for _, p := range t.players {
		p.trade = nil
		g.send(p, "EVT TRADE CANCEL "+by)
	}
	g.log.Info("trade_cancelled", "a", t.players[0].Name, "b", t.players[1].Name, "by", by)
}

// leaveTrades closes whatever trade involves p, and forgets its requests.
// Called when p moves or disconnects.
func (g *Game) leaveTrades(p *Player) {
	if p.trade != nil {
		g.cancelTrade(p.trade, p.Name)
	}
	p.tradeAsk = ""
	for _, o := range g.players {
		if strings.EqualFold(o.tradeAsk, p.Name) {
			o.tradeAsk = ""
		}
	}
}
