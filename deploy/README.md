# Running Hollowmere for real

The target is a machine at home (a PC or a Raspberry Pi), reachable from
the internet through a Cloudflare tunnel, with no port opened on the router
except, optionally, the raw TCP port for the terminal client.

| File | Purpose |
|------|---------|
| `../compose.yaml` | the game, plus the `tunnel` and `monitoring` profiles |
| `../.env.example` | settings and secrets, copied to `.env` |
| `hollowmere.service` | systemd unit, for running without Docker |
| `backup.sh` / `restore.sh` / `offsite.sh` | nightly backup, restore, off-machine copy |
| `healthcheck.sh` | watchdog for cron, with an optional phone notification |
| `crontab.example` | the two cron lines |
| `prometheus/`, `grafana/` | metrics, alert rules and a ready dashboard |

## 1. Start the server

```sh
cp .env.example .env            # then edit it
mkdir -p state
openssl rand -hex 24 > state/metrics_token && chmod 600 state/metrics_token
docker compose up -d --build
curl -s localhost:8080/healthz
```

`restart: unless-stopped` brings the game back after a crash or a reboot
(enable the Docker service: `sudo systemctl enable --now docker`). Without
Docker, follow the comments at the top of `hollowmere.service`.

## 2. Name the moderators

Roles belong to characters and are granted from the console, never from
inside the game. Play once with the character, then:

```sh
docker compose exec hollowmere hollowmere -db /data/hollowmere.db -grant marin:admin
docker compose exec hollowmere hollowmere -db /data/hollowmere.db -admins
# without Docker: bin/hollowmere -db state/hollowmere.db -grant marin:admin
```

The role applies at the character's next login. In game:

| Role | Commands |
|------|----------|
| moderator | `ANNOUNCE <msg>`, `KICK <p> [why]`, `MUTE <p> <10m\|2h\|3d> [why]`, `UNMUTE <p>` |
| admin | the above, plus `BAN <p> [2h\|3d\|perm] [why]`, `UNBAN <p>` |

A ban covers the character's resume key **and** its IP address (the last
one it played from, if it is offline); everyone connected from that address
is disconnected. The IP part lasts at most 7 days, because addresses change
hands and are shared: a whole school may sit behind one. Every action goes
to the log (`"msg":"moderation"`) and to the `modlog` table. From the
console: `-sanctions` lists what is in force, `-unban <name>` lifts a ban.

## 3. Cloudflare tunnel

A tunnel lets Cloudflare reach the server from the inside: nothing to open
on the router, and the home IP stays hidden. You need a domain on
Cloudflare.

1. Cloudflare dashboard → Zero Trust → Networks → Tunnels → *Create a
   tunnel* (type Cloudflared), name it `hollowmere`.
2. Copy the token it shows into `.env` as `CLOUDFLARE_TUNNEL_TOKEN`.
3. *Public hostname*: `hollowmere.<your-domain>` → service
   `http://hollowmere:8080`.
4. In `.env`: `TAP_ORIGINS=hollowmere.<your-domain>`, `TAP_TRUST_PROXY=1`,
   `HTTP_BIND=127.0.0.1`.
5. `docker compose --profile tunnel up -d`

WebSockets need no extra setting. `HTTP_BIND=127.0.0.1` matters: with
`TAP_TRUST_PROXY=1` the server believes the `CF-Connecting-IP` header, so
nobody but the tunnel may reach port 8080.

The tunnel carries HTTP only. For the terminal client, forward TCP 4243 on
the router to this machine (`TCP_BIND=0.0.0.0`), or keep it for the local
network (`TCP_BIND=127.0.0.1` and play on the machine itself).

Without Docker, the same thing from the command line:

```sh
cloudflared tunnel login
cloudflared tunnel create hollowmere
cloudflared tunnel route dns hollowmere hollowmere.<your-domain>
cloudflared tunnel run --url http://localhost:8080 hollowmere
```

## 4. Backups

```sh
crontab -e     # paste the lines of crontab.example, adapted
```

- `backup.sh` takes a consistent copy while the game runs (SQLite
  `VACUUM INTO`), gzips it and keeps 14 days.
- `offsite.sh` copies the backups to another machine over ssh or to a
  mounted USB disk. A backup on the disk that dies is no backup.
- `restore.sh <file>` refuses to run while the server is up, checks the
  backup's integrity first (a damaged file changes nothing), and keeps the
  current database aside as `*.before-restore-<date>`.

With Docker, prefix the scripts with `COMPOSE_SERVICE=hollowmere` and use
the paths inside the volume (`DB=/data/hollowmere.db DEST=/data/backups`).

**Test a restore before you need one**: stop the server, restore last
night's file, start it again, and log in with your resume key.

## 5. Supervision

The simple way: `healthcheck.sh` from cron checks `/healthz` every five
minutes and, after two failures in a row, posts to `NOTIFY_URL` (for
instance an [ntfy](https://ntfy.sh) topic you subscribe to on your phone),
and again when the server comes back.

The complete way: `docker compose --profile monitoring up -d` starts
Prometheus (30 days of history) and Grafana with the *Hollowmere*
dashboard: players, connections, commands, errors, abuse, traffic. Both
listen on 127.0.0.1 only; from another computer:
`ssh -L 3000:localhost:3000 you@server`, then http://localhost:3000.
Prometheus authenticates with the token in `state/metrics_token`. Alert
rules (server down, restarts, flooding, errors) show in both UIs; for
notifications keep `healthcheck.sh` as well.

## 6. Before opening to the public

- [ ] `.env` filled in, `.env` and `state/` out of git (see `.gitignore`).
- [ ] `HTTP_BIND=127.0.0.1` with the tunnel; the site loads over https.
- [ ] One admin granted, one moderator if someone helps you.
- [ ] Backups in cron, the off-machine copy works, **one restore tested**.
- [ ] `healthcheck.sh` notifies your phone (stop the server to see it).
- [ ] Limits read in `internal/session` `DefaultConfig`: 10 commands/s per
      connection (burst 40), 20 connections per IP per 10 s, 16 at once per
      IP, 512 in total, 1024 bytes per line, 30 min idle timeout.
- [ ] Logs hold IP addresses (needed for bans) but never resume keys: the
      key in `CONNECT` is replaced by `[key]` at every log level.
