#!/usr/bin/env bash
# Local end-to-end check of the production stack, laid out exactly like the
# server (stack folder with compose.yml + .env, APPDATA_DIR bind mounts):
#   1. stack up from a locally built image, /healthz ok (in-container and over
#      the `proxy` network, as Caddy sees it)
#   2. backup.sh writes a dump
#   3. restore.sh brings the database back to that dump
#
# Usage: deploy/scripts/smoke.sh <image:tag> [workdir]
# Needs: the image built (`make image`), no local containers named mealplanner*.
set -euo pipefail

image=${1:?usage: smoke.sh <image:tag> [workdir]}
work=${2:-$(mktemp -d "${TMPDIR:-/tmp}/mealplanner-smoke.XXXXXX")}
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
stack="$work/stack"

mkdir -p "$stack" "$work/appdata/mealplanner/db" "$work/appdata/mealplanner/backups"
chmod 777 "$work/appdata/mealplanner/backups" # server: owned by 999 via Ansible
cp "$here/../stack/compose.yml" "$here/../stack/backup.sh" "$here/../stack/restore.sh" "$stack/"
pw=smoke$RANDOM$RANDOM
cat >"$stack/.env" <<ENV
APPDATA_DIR=$work/appdata
MEALPLANNER_TAG=${image##*:}
POSTGRES_PASSWORD=$pw
DATABASE_URL=postgres://mealplanner:$pw@db:5432/mealplanner?sslmode=disable
SESSION_SECRET=smoke-session-secret-0123456789abcdef
BASE_URL=http://localhost:8080
ADMIN_EMAIL=admin@example.com
TZ=Europe/Copenhagen
ENV

cd "$stack"
dc() { docker compose "$@"; }
psql_() { dc exec -T db psql -U mealplanner -d mealplanner -tAc "$1"; }
cleanup() { echo "==> down"; dc down --timeout 5 >/dev/null 2>&1 || true; echo "workdir left at $work"; }
trap cleanup EXIT

docker network inspect proxy >/dev/null 2>&1 || docker network create proxy >/dev/null

echo "==> up ($image)"
dc up -d
echo "==> waiting for /healthz"
for i in $(seq 1 60); do
	docker exec mealplanner /server healthcheck 2>/dev/null && break
	[[ $i == 60 ]] && { dc logs --tail 50 app; echo "FAIL: not healthy"; exit 1; }
	sleep 2
done
body=$(docker run --rm --network proxy curlimages/curl:8.11.1 -fsS http://mealplanner:8080/healthz)
echo "GET http://mealplanner:8080/healthz over proxy network -> $body"
[[ $body == ok ]] || { echo "FAIL: healthz body '$body'"; exit 1; }
echo "revision label: $(docker inspect -f '{{index .Config.Labels "org.opencontainers.image.revision"}}' mealplanner)"

echo "==> backup round-trip"
psql_ "INSERT INTO households (name) VALUES ('smoke-before')" >/dev/null
dc exec -T backup sh /backup.sh now
dumpfile=$(ls -t "$work"/appdata/mealplanner/backups/mealplanner-*.dump | head -1)
psql_ "INSERT INTO households (name) VALUES ('smoke-after')" >/dev/null
echo "households before restore: $(psql_ "SELECT string_agg(name, ',' ORDER BY name) FROM households")"
sleep 1 # restore.sh's safety dump gets a different timestamp
RESTORE_YES=1 ./restore.sh "$dumpfile"
after=$(psql_ "SELECT string_agg(name, ',' ORDER BY name) FROM households")
echo "households after restore:  $after"
[[ $after == smoke-before ]] || { echo "FAIL: restore did not roll back"; exit 1; }
for i in $(seq 1 30); do
	docker exec mealplanner /server healthcheck 2>/dev/null && break
	[[ $i == 30 ]] && { echo "FAIL: app unhealthy after restore"; exit 1; }
	sleep 2
done
echo "app healthy after restore; dumps:"
ls -l "$work/appdata/mealplanner/backups"
echo "SMOKE OK"
