package webd

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"

	"hollowmere/internal/game"
)

// The leaderboard page: read-only, rendered on the server, no script.
// Names are player input, so everything goes through html/template.

const topSize = 50

var topPage = template.Must(template.New("top").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Hollowmere — Leaderboard</title>
<style>
  :root { --bg: #2b1d12; --paper: #f3e6c8; --ink: #2a1d10; --soft: #7a6647; --accent: #3c8d2f; --gold: #d8a31a; }
  body { margin: 0; background: var(--bg); color: var(--ink); font: 17px/1.5 ui-monospace, "DejaVu Sans Mono", monospace; }
  main { max-width: 760px; margin: 24px auto; padding: 20px 16px; background: var(--paper); border: 4px solid #000; }
  h1 { margin: 0 0 4px; font-size: 26px; letter-spacing: 1px; }
  p { margin: 0 0 16px; color: var(--soft); }
  table { width: 100%; border-collapse: collapse; }
  th, td { padding: 6px 8px; text-align: right; border-bottom: 1px dashed #c9b48a; }
  th:nth-child(2), td:nth-child(2) { text-align: left; }
  th { font-size: 13px; text-transform: uppercase; color: var(--soft); }
  tr.top1 td { color: #8a5b00; font-weight: bold; }
  .online { color: var(--accent); font-size: 12px; margin-left: 6px; }
  a { color: var(--ink); }
  @media (max-width: 520px) { .hide-sm { display: none; } }
</style>
</head>
<body>
<main>
  <h1>Hollowmere — Leaderboard</h1>
  <p>Ranked by level, then experience, quests completed and enemies defeated. <a href="/">Play</a> · <a href="/top.json">JSON</a></p>
  <table>
    <thead><tr><th>#</th><th>Name</th><th>Level</th><th class="hide-sm">XP</th><th>Quests</th><th class="hide-sm">Kills</th></tr></thead>
    <tbody>
    {{range .}}<tr{{if eq .Rank 1}} class="top1"{{end}}><td>{{.Rank}}</td><td>{{.Name}}{{if .Online}}<span class="online">● online</span>{{end}}</td><td>{{.Level}}</td><td class="hide-sm">{{.XP}}</td><td>{{.Quests}}</td><td class="hide-sm">{{.Kills}}</td></tr>
    {{else}}<tr><td colspan="6">Nobody yet. Be the first.</td></tr>
    {{end}}</tbody>
  </table>
</main>
</body>
</html>
`))

func (s *Server) topEntries(r *http.Request) []game.TopEntry {
	n := topSize
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 && v < topSize {
		n = v
	}
	return s.mgr.Game().Top(n)
}

func (s *Server) handleTop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if err := topPage.Execute(w, s.topEntries(r)); err != nil {
		s.log.Warn("top_render_failed", "error", err.Error())
	}
}

func (s *Server) handleTopJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(s.topEntries(r))
}
