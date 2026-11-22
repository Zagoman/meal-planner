#!/usr/bin/env bash
# Runs ON THE SERVER, fed over ssh by `make deploy` (deploy/scripts/deploy.sh):
#   ssh host 'bash -s' -- <stack_dir> <expected_sha> <domain> <compose_sha256> < remote-deploy.sh
set -euo pipefail

stack_dir=$1 want_sha=$2 domain=$3 local_compose_sum=$4
container=mealplanner
timeout=${HEALTH_TIMEOUT:-120}

fail() {
	echo "!! DEPLOY FAILED: $*" >&2
	docker compose ps >&2 || true
	docker compose logs --tail 80 app >&2 || true
	exit 1
}

cd "$stack_dir" 2>/dev/null || { echo "!! $stack_dir missing: apply deploy/homelab and run the Ansible deploy first" >&2; exit 1; }
[[ -f compose.yml && -f .env ]] || { echo "!! $stack_dir has no compose.yml/.env: run the Ansible deploy first" >&2; exit 1; }

server_sum=$(sha256sum compose.yml | cut -d' ' -f1)
if [[ "$server_sum" != "$local_compose_sum" ]]; then
	echo "!! warning: $stack_dir/compose.yml differs from deploy/stack/compose.yml in meal-planner." >&2
	echo "!!          Sync it into the homelab repo (make homelab-sync), commit, git pull on the server." >&2
fi

echo "==> docker compose pull"
docker compose pull --quiet || fail "pull failed (is the server logged in to ghcr.io? see deploy/README.md)"
echo "==> docker compose up -d"
docker compose up -d --remove-orphans || fail "compose up failed"

got_sha=$(docker inspect -f '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$container" 2>/dev/null || true)
if [[ -n "$want_sha" && "$got_sha" != "$want_sha" ]]; then
	fail "running image revision is '$got_sha', expected '$want_sha' (stale :latest? MEALPLANNER_TAG pinned in .env?)"
fi

echo "==> waiting up to ${timeout}s for /healthz (revision $got_sha)"
deadline=$((SECONDS + timeout))
until docker exec "$container" /server healthcheck 2>/dev/null; do
	state=$(docker inspect -f '{{.State.Status}}' "$container" 2>/dev/null || echo missing)
	case $state in
	running | restarting | created) ;; # a crash loop shows up as the timeout below
	*) fail "container is $state" ;;
	esac
	((SECONDS < deadline)) || fail "/healthz not ok after ${timeout}s"
	sleep 2
done
echo "==> app healthy inside the container"

if [[ "${SKIP_PROXY_CHECK:-0}" != 1 ]]; then
	echo "==> checking https://$domain/healthz through Caddy"
	body=$(curl -fsS --max-time 10 --resolve "$domain:443:127.0.0.1" "https://$domain/healthz") ||
		fail "app is healthy but https://$domain/healthz through Caddy failed (Caddy block applied? caddy restarted?)"
	[[ "$body" == ok ]] || fail "https://$domain/healthz returned '$body'"
fi
echo "==> deployed $got_sha"
