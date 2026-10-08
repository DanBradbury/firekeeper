#!/bin/sh
# Write a consistent, dated copy of the dashboard database on this host.
#
#   BACKUP_DIR  where copies go (default /var/backups/firekeeper)
#   KEEP        how many to keep (default 14)
#   COMPOSE     compose file (default: compose.yml beside this script, else
#               compose.nginx.yml)
#
# Uses SQLite VACUUM INTO inside the container, so it is safe while the
# server runs. Copies stay on this host; moving them elsewhere is up to you.
# A backup holds every transcript: never commit one.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
compose=${COMPOSE:-$here/compose.yml}
[ -f "$compose" ] || compose=$here/compose.nginx.yml
dir=${BACKUP_DIR:-/var/backups/firekeeper}
keep=${KEEP:-14}
case $keep in
'' | *[!0-9]* | 0) echo "backup: KEEP must be a positive number" >&2; exit 2 ;;
esac

umask 077
mkdir -p "$dir"
stamp=$(date -u +%Y%m%dT%H%M%SZ)
out=$dir/firekeeper-$stamp.db
tmp=/tmp/backup-$stamp.db

dc() { docker compose -f "$compose" "$@"; }

dc exec -T firekeeper sqlite3 /data/firekeeper.db "VACUUM INTO '$tmp'"
status=$(dc exec -T firekeeper sqlite3 "$tmp" "PRAGMA integrity_check")
if [ "$status" != ok ]; then
	dc exec -T firekeeper rm -f "$tmp"
	echo "backup: integrity check failed" >&2
	exit 1
fi
dc cp "firekeeper:$tmp" "$out" 2>/dev/null
dc exec -T firekeeper rm -f "$tmp"
chmod 600 "$out"
echo "backup: wrote $out"

# Keep the newest $keep copies (names sort by time).
find "$dir" -maxdepth 1 -name 'firekeeper-*.db' | sort -r | tail -n +"$((keep + 1))" |
	while IFS= read -r old; do
		rm -f -- "$old"
		echo "backup: pruned $old"
	done
