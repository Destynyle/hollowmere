#!/bin/sh
# Copy the backups off this machine: a backup on the disk that dies is no
# backup. Run it after backup.sh.
#
#   OFFSITE=user@nas:/backups/hollowmere deploy/offsite.sh     # over ssh
#   OFFSITE=/media/usb/hollowmere deploy/offsite.sh            # a mounted disk
#
# rsync copies only new files; old ones are kept on the other side (prune
# there, or add --delete if the destination is dedicated to this).
set -eu

SRC=${DEST:-state/backups}
OFFSITE=${OFFSITE:?set OFFSITE to user@host:/path or to a mounted directory}

case "$OFFSITE" in
	*:*) ;;
	*) [ -d "$OFFSITE" ] || { echo "$OFFSITE is not mounted" >&2; exit 1; } ;;
esac

rsync -a --ignore-existing "$SRC"/ "$OFFSITE"/
echo "$(date -Is) offsite copy ok: $OFFSITE"
