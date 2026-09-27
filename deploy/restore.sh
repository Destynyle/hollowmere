#!/bin/sh
# Restore the character database from a backup made by backup.sh.
#
#   deploy/restore.sh state/backups/hollowmere-2026-09-27_0430.db.gz
#   COMPOSE_SERVICE=hollowmere deploy/restore.sh state/backups/...   (Docker)
#
# The server must be stopped: players connected during a restore would
# overwrite it at their next save. The current database is kept aside as
# hollowmere.db.before-restore-<date>, never deleted.
set -eu

BACKUP=${1:?usage: restore.sh <backup.db.gz|backup.db>}
DB=${DB:-state/hollowmere.db}
BIN=${BIN:-bin/hollowmere}

[ -f "$BACKUP" ] || { echo "no such file: $BACKUP" >&2; exit 1; }

if [ -n "${COMPOSE_SERVICE:-}" ]; then
	if docker compose ps --status running --services 2>/dev/null | grep -qx "$COMPOSE_SERVICE"; then
		echo "stop the server first: docker compose stop $COMPOSE_SERVICE" >&2
		exit 1
	fi
elif pgrep -x hollowmere >/dev/null 2>&1; then
	echo "stop the server first (systemctl stop hollowmere)" >&2
	exit 1
fi

TMP="$DB.restoring"
trap 'rm -f "$TMP"' EXIT # whatever fails below, no half-written copy stays
case "$BACKUP" in
	*.gz) gzip -dc "$BACKUP" > "$TMP" ;;
	*) cp "$BACKUP" "$TMP" ;;
esac

# Check the copy before touching anything: a truncated backup must not
# replace a working database.
if [ -n "${COMPOSE_SERVICE:-}" ]; then
	# The state directory is the container's /data volume.
	verify() { docker compose run --rm --no-deps "$COMPOSE_SERVICE" -db "/data/$(basename "$TMP")" -verify-db; }
else
	verify() { "$BIN" -db "$TMP" -verify-db; }
fi
if ! verify; then
	echo "the backup does not open as a Hollowmere database, nothing changed" >&2
	exit 1
fi

if [ -f "$DB" ]; then
	KEEP="$DB.before-restore-$(date +%Y-%m-%d_%H%M%S)"
	mv "$DB" "$KEEP"
	echo "previous database kept as $KEEP"
fi
rm -f "$DB-wal" "$DB-shm"
mv "$TMP" "$DB"
echo "$(date -Is) restored $DB from $BACKUP; start the server again"
