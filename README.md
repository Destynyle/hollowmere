# Hollowmere

A shared world of text you can actually play: a multiplayer text adventure
(MUD) speaking RFC 42TAP, with a browser client over WebSocket and the raw
TCP transport kept for terminal players.

This is the production version of the 42 school project *The Answer
Protocol*, rebuilt to run as a real service. The school repository stays as
it was submitted; this one is free of its constraints (Go 1.27, third-party
libraries, persistence, deployment).

## Quick start

Requirements: Go 1.27 (the Makefile picks up `~/sdk/go1.27.1` automatically)
or Docker.

```sh
make build
make run                 # web client on http://127.0.0.1:8080, TCP on 4243
make run-client NAME=me  # the terminal client, on the same server
```

With Docker:

```sh
docker compose up -d --build
docker compose logs -f
```

## How it is put together

| Package | Role |
|---------|------|
| `internal/proto` | RFC 42TAP wire format: line framing, command parsing, error codes |
| `internal/game` | The world and every command; no network code, talks to players through a `Sink` |
| `internal/session` | Transport-independent session: admission, rate limiting, bounded output queue, cleanup |
| `internal/tcpd` | Raw TCP transport (CLI client, netcat) |
| `internal/webd` | Web client, WebSocket endpoint, `/healthz`, `/metrics` |
| `internal/web` | The browser client, embedded in the binary |
| `internal/logs` | Non-blocking JSON logging on top of `log/slog` |

A transport only implements `session.Conn` (read a line, write a line,
close). The TCP and WebSocket front-ends therefore share the same abuse
detection, the same ordering guarantees, and the same disconnect handling.

The browser talks WebSocket: one text message per protocol line, in order.
No local bridge any more — the server serves the client and the socket from
the same origin.

## Configuration

Flags, or the matching environment variables (handy in Docker):

| Flag | Env | Default | Meaning |
|------|-----|---------|---------|
| `-http` | `TAP_HTTP` | `0.0.0.0:8080` | web client, WebSocket, health and metrics |
| `-tcp` | `TAP_TCP` | `0.0.0.0:4243` | raw TCP transport (empty string disables it) |
| `-world` | `TAP_WORLD` | `data/world.json` | world file |
| `-log-file` | `TAP_LOG_FILE` | — | append JSON logs to this file as well |
| `-log-level` | `TAP_LOG_LEVEL` | `info` | `debug` logs every command and reply |
| `-origins` | `TAP_ORIGINS` | same-origin only | hosts allowed to open a WebSocket |
| `-trust-proxy` | `TAP_TRUST_PROXY=1` | off | read the client IP from `CF-Connecting-IP` / `X-Forwarded-For` |
| `-metrics-token` | `TAP_METRICS_TOKEN` | — | require `?token=` on `/metrics` |
| `-db` | `TAP_DB` | `state/hollowmere.db` | character database; `none` disables persistence |
| `-autosave` | — | `60s` | how often connected characters are saved |
| `-backup <path>` | — | — | copy the database and exit (for cron) |
| `-check` | — | — | validate the world file and exit |

## Characters, without accounts

There is no sign-up, no password and no e-mail. On the first visit the
server creates a character and sends its **resume key** — 128 random bits —
to that client only:

```text
C: CONNECT marin
S: OK connected
S: EVT PLAYER KEY jv4c2hq7t3m6k9x1b8n5r0wzye
```

The client stores the key (browser local storage, or
`~/.config/hollowmere/keys.json` for the CLI) and sends it next time:

```text
C: CONNECT marin jv4c2hq7t3m6k9x1b8n5r0wzye
S: OK connected          ← same room, health, inventory and quests
```

- The key is the character's only secret: whoever holds it plays it. The web
  client keeps it masked, with **Copy** to save it elsewhere and **New** to
  forget it and start over.
- A name belongs to the key that created it, so nobody else can take it.
- An unknown key is not an error: it simply starts a new character.
- Characters are saved on disconnect, every 60 s, and on shutdown.
- Clients from other groups send `CONNECT <name>` alone and get a fresh
  character each time; the extra argument is a v2 extension.

**What happens to carried items.** World items (herbs, potions, torches) go
back to the room they came from, so the world stays playable for everyone
else. Quest rewards and loot travel with the character and are recreated on
its return — which also frees the loot slot of the enemy that dropped it.
Nothing is duplicated.

**Backups.** `deploy/backup.sh` runs `hollowmere -backup`, which uses
SQLite's `VACUUM INTO`: a consistent copy while players keep playing. It
compresses the copy and keeps two weeks of history. The database lives in
`state/` (the Docker volume), together with the logs.

## Endpoints

| Path | Purpose |
|------|---------|
| `/` | the browser client |
| `/ws` | one WebSocket per player, carrying protocol lines |
| `/healthz` | JSON: status, players, connections, uptime |
| `/metrics` | Prometheus text: players, connections, commands, errors, abuse counters |

## Operations

- **Logs** are JSON lines on stdout (and `TAP_LOG_FILE`), written by a
  background goroutine that drops records rather than slowing the game.
  `tail -f state/server.log | jq -c 'select(.level != "INFO")'`
- **Limits**: 10 commands/s per connection (burst 40) then a kick, 20
  connections per IP per 10 s, 512 connections, 1024 bytes per line.
- **Shutdown**: `SIGTERM` announces the restart to players (`EVT SERVER …`),
  drains the write queues, then exits. Browser clients reconnect on their
  own with a growing delay.

## Roadmap

1. ~~Socle: engine ported, Docker, tests~~
2. ~~Transport: WebSocket + web client served by the server, TCP kept~~
3. ~~Persistence: SQLite, resume key, periodic save~~
4. Content: bigger world, extended format, dialogue choices, quest chains
5. Progression: XP, levels, stats, worn equipment, skills
6. Social: private messages, friends, leaderboard, trading, group dungeons
7. Production: moderation, backups, Cloudflare tunnel, supervision

## Credits

Built by dsom and dlaktaf, from their 42 project. Protocol: RFC 42TAP.
