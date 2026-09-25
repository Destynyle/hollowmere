#!/bin/sh
# Daily backup of the character database.
#
# Safe to run while players are connected: SQLite's VACUUM INTO takes a
# consistent snapshot. Old copies are pruned after KEEP_DAYS.
#
# Cron example (every night at 04:30):
#   30 4 * * * /srv/hollowmere/deploy/backup.sh >> /srv/hollowmere/state/backup.log 2>&1
set -eu

DB=${DB:-state/hollowmere.db}
DEST=${DEST:-state/backups}
KEEP_DAYS=${KEEP_DAYS:-14}
BIN=${BIN:-bin/hollowmere}
STAMP=$(date +%Y-%m-%d_%H%M)
OUT="$DEST/hollowmere-$STAMP.db"

if [ -n "${COMPOSE_SERVICE:-}" ]; then
	# Inside Docker: run the same command in the running container.
	docker compose exec -T "$COMPOSE_SERVICE" hollowmere -db "$DB" -backup "$OUT"
else
	"$BIN" -db "$DB" -backup "$OUT"
fi

# gzip keeps a month of history in a few megabytes.
gzip -f "$OUT"
find "$DEST" -name 'hollowmere-*.db.gz' -mtime "+$KEEP_DAYS" -delete

echo "$(date -Is) backup ok: $OUT.gz"
