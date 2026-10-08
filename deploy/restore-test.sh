#!/bin/sh
# Prove a backup restores: load it into a throwaway volume, start the server
# on it, and compare session and event counts with the backup file itself.
#
#   usage: restore-test.sh BACKUP.db
#   FIREKEEPER_IMAGE  image to test with (default: .env beside this script,
#                     else firekeeper:local)
#
# Touches nothing but the temporary volume and container it creates, and
# removes both. It never reads or prints transcript text, only counts.
set -eu

file=${1:?usage: restore-test.sh BACKUP.db}
[ -f "$file" ] || { echo "restore-test: no such file" >&2; exit 2; }
here=$(cd "$(dirname "$0")" && pwd)
if [ -z "${FIREKEEPER_IMAGE:-}" ] && [ -f "$here/.env" ]; then
	FIREKEEPER_IMAGE=$(sed -n 's/^FIREKEEPER_IMAGE=//p' "$here/.env" | tail -n 1)
fi
image=${FIREKEEPER_IMAGE:-firekeeper:local}
dir=$(cd "$(dirname "$file")" && pwd)
base=$(basename "$file")
vol=firekeeper-restore-test-$$
ctr=$vol

cleanup() {
	docker rm -f "$ctr" >/dev/null 2>&1 || true
	docker volume rm "$vol" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

docker volume create "$vol" >/dev/null
# Copy in as root, then hand the directory to the image's non-root user.
docker run --rm --user 0 --entrypoint sh -v "$vol:/data" -v "$dir:/b:ro" "$image" \
	-c "cp /b/$base /data/firekeeper.db && chown -R 10001:10001 /data"

want=$(docker run --rm --user 0 --entrypoint sqlite3 -v "$dir:/b:ro" "$image" -readonly "/b/$base" \
	"SELECT (SELECT count(*) FROM sessions) || ' sessions, ' || (SELECT count(*) FROM events) || ' events'")

docker run -d --name "$ctr" -v "$vol:/data" "$image" >/dev/null
i=0
until [ "$(docker inspect -f '{{.State.Health.Status}}' "$ctr")" = healthy ]; do
	i=$((i + 1))
	[ "$i" -le 30 ] || { echo "restore-test: server did not become healthy" >&2; exit 1; }
	sleep 1
done

got=$(docker exec "$ctr" sqlite3 -readonly /data/firekeeper.db \
	"SELECT (SELECT count(*) FROM sessions) || ' sessions, ' || (SELECT count(*) FROM events) || ' events'")
echo "backup:   $want"
echo "restored: $got"
[ "$want" = "$got" ] || { echo "restore-test: MISMATCH" >&2; exit 1; }
echo "restore-test: ok"
