#!/bin/sh
# Minimal supervision without Prometheus: check /healthz and complain when
# it fails twice in a row. Run it from cron every few minutes.
#
#   URL=https://hollowmere.example.org/healthz NOTIFY_URL=https://ntfy.sh/my-topic deploy/healthcheck.sh
#
# NOTIFY_URL receives a POST with the message (ntfy.sh, a Discord or Slack
# webhook proxy, ...). Without it the message only goes to stderr, which
# cron mails to the machine owner.
set -u

URL=${URL:-http://127.0.0.1:8080/healthz}
STATE=${STATE:-state/healthcheck.failures}

if curl -fsS --max-time 5 "$URL" | grep -q '"status":"ok"'; then
	if [ -s "$STATE" ] && [ "$(cat "$STATE")" -ge 2 ]; then
		MSG="Hollowmere is back up ($URL)"
		[ -n "${NOTIFY_URL:-}" ] && curl -fsS --max-time 10 -d "$MSG" "$NOTIFY_URL" >/dev/null
		echo "$(date -Is) $MSG" >&2
	fi
	echo 0 > "$STATE"
	exit 0
fi

N=$(( $(cat "$STATE" 2>/dev/null || echo 0) + 1 ))
echo "$N" > "$STATE"
if [ "$N" -eq 2 ]; then
	MSG="Hollowmere is DOWN: $URL failed twice in a row"
	[ -n "${NOTIFY_URL:-}" ] && curl -fsS --max-time 10 -d "$MSG" "$NOTIFY_URL" >/dev/null
	echo "$(date -Is) $MSG" >&2
fi
exit 1
