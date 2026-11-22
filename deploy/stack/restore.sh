#!/usr/bin/env bash
# Restore the mealplanner database from a pg_dump file made by backup.sh.
# Run on the server from the stack folder:
#   ./restore.sh /srv/appdata/mealplanner/backups/mealplanner-YYYYMMDD-HHMMSS.dump
# Takes a fresh safety dump first, then REPLACES the current database.
# Set RESTORE_YES=1 to skip the prompt.
set -euo pipefail

dump=${1:?usage: restore.sh <file.dump>}
[[ -r "$dump" ]] || { echo "restore: cannot read $dump" >&2; exit 1; }
cd "$(dirname "${BASH_SOURCE[0]}")"

if [[ "${RESTORE_YES:-}" != 1 ]]; then
	read -r -p "Replace the mealplanner database with $(basename "$dump")? Type yes: " ans
	[[ "$ans" == yes ]] || { echo "aborted"; exit 1; }
fi

echo "==> safety dump of the current database"
docker compose exec -T backup sh /backup.sh now

echo "==> stopping app"
docker compose stop app

echo "==> recreating database"
docker compose exec -T db dropdb -U mealplanner --force --if-exists mealplanner
docker compose exec -T db createdb -U mealplanner -O mealplanner mealplanner

echo "==> restoring $dump"
docker compose exec -T db pg_restore -U mealplanner -d mealplanner --no-owner --exit-on-error <"$dump"

echo "==> starting app"
docker compose start app
echo "restored; check: docker exec mealplanner /server healthcheck && echo ok"
