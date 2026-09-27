//go:build js && wasm

// Command demo is Hollowmere compiled to WebAssembly: the whole server runs
// in the visitor's browser, so the game can be played from a static page
// (GitHub Pages) with no server at all.
//
// The web client is the regular one; demo.js gives it a WebSocket
// look-alike that talks to this program instead of the network. Each
// visitor has a private world; characters are kept in localStorage.
package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall/js"
	"time"

	"hollowmere/data"
	"hollowmere/internal/game"
	"hollowmere/internal/logs"
	"hollowmere/internal/session"
)

func main() {
	log := logs.New(logs.NewAsyncWriter(os.Stdout), slog.LevelWarn)
	world, err := game.LoadWorldFS(data.World, "world")
	if err != nil {
		js.Global().Get("console").Call("error", err.Error())
		return
	}
	g := game.New(world, log, time.Now().UnixNano())
	g.SetStore(localStore{})
	stop := make(chan struct{})
	go g.AutoSave(30*time.Second, stop)
	mgr := session.NewManager(g, log, session.DefaultConfig(), nil)

	// hollowmereDemoOpen(onLine, onClose) -> {send(line), close()}
	js.Global().Set("hollowmereDemoOpen", js.FuncOf(func(this js.Value, args []js.Value) any {
		c := &jsConn{in: make(chan string, 64), done: make(chan struct{}), onLine: args[0], onClose: args[1]}
		go mgr.Serve(c)
		return js.ValueOf(map[string]any{
			"send": js.FuncOf(func(this js.Value, a []js.Value) any {
				c.push(a[0].String())
				return nil
			}),
			"close": js.FuncOf(func(this js.Value, a []js.Value) any {
				c.Close()
				return nil
			}),
		})
	}))
	// Save everyone when the tab closes.
	js.Global().Call("addEventListener", "pagehide", js.FuncOf(func(this js.Value, args []js.Value) any {
		g.SaveAll()
		return nil
	}))
	if ready := js.Global().Get("hollowmereDemoReady"); ready.Type() == js.TypeFunction {
		ready.Invoke()
	}
	select {}
}

// jsConn is a session.Conn between the Go server and the page.
type jsConn struct {
	in      chan string
	done    chan struct{}
	once    sync.Once
	onLine  js.Value
	onClose js.Value
}

func (c *jsConn) push(line string) {
	select {
	case c.in <- line:
	case <-c.done:
	}
}

func (c *jsConn) ReadLine() (string, error) {
	select {
	case line := <-c.in:
		return line, nil
	case <-c.done:
		return "", io.EOF
	}
}

func (c *jsConn) WriteLine(line string) error {
	select {
	case <-c.done:
		return io.ErrClosedPipe
	default:
	}
	c.onLine.Invoke(line)
	return nil
}

func (c *jsConn) Close() error {
	c.once.Do(func() {
		close(c.done)
		c.onClose.Invoke()
	})
	return nil
}

func (c *jsConn) RemoteIP() string { return "browser" }
func (c *jsConn) Kind() string     { return "demo" }

// localStore keeps characters in the browser's localStorage, so a visitor
// finds their character again after a reload.
type localStore struct{}

const savePrefix = "hollowmere.demo.save."

func storage() js.Value { return js.Global().Get("localStorage") }

func (localStore) LoadPlayer(key string) (*game.StoredPlayer, error) {
	v := storage().Call("getItem", savePrefix+key)
	if v.IsNull() {
		return nil, nil
	}
	var sp game.StoredPlayer
	if err := json.Unmarshal([]byte(v.String()), &sp); err != nil {
		return nil, nil // a damaged save starts over
	}
	return &sp, nil
}

func (localStore) NameOwner(name string) (string, error) {
	for _, sp := range allSaves() {
		if strings.EqualFold(sp.Name, name) {
			return sp.Key, nil
		}
	}
	return "", nil
}

func (localStore) SavePlayer(p *game.StoredPlayer) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	storage().Call("setItem", savePrefix+p.Key, string(raw))
	return nil
}

func (localStore) Top(limit int) ([]game.TopEntry, error) {
	var out []game.TopEntry
	for _, sp := range allSaves() {
		out = append(out, game.TopEntry{Name: sp.Name, Level: max(sp.Data.Level, 1), XP: sp.Data.XP, Kills: sp.Data.Kills})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Level > out[j].Level })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func allSaves() []game.StoredPlayer {
	s := storage()
	var out []game.StoredPlayer
	for i := 0; i < s.Get("length").Int(); i++ {
		k := s.Call("key", i).String()
		if !strings.HasPrefix(k, savePrefix) {
			continue
		}
		var sp game.StoredPlayer
		if json.Unmarshal([]byte(s.Call("getItem", k).String()), &sp) == nil {
			out = append(out, sp)
		}
	}
	return out
}
