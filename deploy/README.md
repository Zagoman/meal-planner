# Deploying the meal planner to the homelab

The app runs as one more stack on the home server, using the same conventions
as the other stacks (homelab repo `Zagoman/stacks`, cloned at `/opt/stacks`):

| What | Where |
|---|---|
| Image | `ghcr.io/zagoman/meal-planner:<git sha>` and `:latest` (private, linux/amd64, distroless, ~16 MB) |
| Stack | `/opt/stacks/mealplanner/` = `compose.yml`, `backup.sh`, `restore.sh` (copies of [`stack/`](stack/)) + `.env` rendered by Ansible |
| Containers | `mealplanner` (app, on `proxy` + default net, no published ports), `mealplanner-db` (postgres:17), `mealplanner-backup` |
| Database files | `/srv/appdata/mealplanner/db` (bind mount, uid 999) |
| Nightly dumps | `/srv/appdata/mealplanner/backups/mealplanner-YYYYMMDD-HHMMSS.dump`, 03:00, kept 14 days |
| URL | `https://meals.liluhome.dk` through the shared Caddy (Cloudflare DNS-01 TLS) |
| Secrets | Ansible vault: `vault_mealplanner_*` → `stack_env.mealplanner` → `.env` |

```
Mac: make deploy
  ├─ docker buildx build --platform linux/amd64  → :<sha>, :latest
  ├─ docker push ghcr.io/zagoman/meal-planner
  └─ ssh lucs@192.168.0.31 (deploy/scripts/remote-deploy.sh)
       cd /opt/stacks/mealplanner
       docker compose pull && docker compose up -d
       check the running image's revision label == <sha>
       poll `docker exec mealplanner /server healthcheck` (120 s)
       curl https://meals.liluhome.dk/healthz via Caddy on the server
       any failure → print compose ps + app logs, exit 1
```

`/server healthcheck` is a small subcommand in `cmd/server/healthcheck.go`: it
GETs `/healthz` on the local listener and exits 0/1. Distroless has no shell
or curl, so the compose healthcheck uses it too.

## First-time setup

Do these once, in order. Steps marked **you** need your accounts or secrets.

### 1. GHCR tokens (you)

GitHub → Settings → Developer settings → Personal access tokens → **Tokens (classic)**:

- **Mac (push):** scopes `write:packages` (includes `read:packages`). Name it `meal-planner push`.
- **Server (pull only):** a second token with only `read:packages`. Name it `homeserver ghcr pull`.

On the Mac:

```bash
docker login ghcr.io -u Zagoman
```

Paste the push token as the password.

On the server, log in **twice**: as `lucs` (used by `make deploy` over SSH)
and as root (used by the Ansible deploy role, which runs with `become`, and by
`homelab-stacks.service` on boot if the image ever has to be pulled again):

```bash
ssh lucs@192.168.0.31
docker login ghcr.io -u Zagoman
sudo docker login ghcr.io -u Zagoman
```

Paste the pull token both times.

### 2. Push the first image

The Ansible deploy pulls the image, so it has to exist first:

```bash
make push
```

The first push creates the package `meal-planner` under your account; it is
private by default. In its package settings, under "Manage Actions access" /
"Repository access", you may link it to `Zagoman/meal-planner` (optional).

### 3. Vault values (you)

In `~/dev/homelab-server/ansible`:

```bash
ansible-vault edit group_vars/all/secrets.yml
```

Add:

```yaml
vault_mealplanner_db_password: "<openssl rand -hex 24>"
vault_mealplanner_session_secret: "<openssl rand -hex 32>"
vault_mealplanner_anthropic_api_key: "sk-ant-..."   # empty = no plan generation
vault_mealplanner_admin_email: "you@example.com"    # the server admin
```

Use hex for the DB password: it goes into `DATABASE_URL` unescaped.

### 4. Apply the homelab changes

[`homelab/mealplanner.patch`](homelab/mealplanner.patch) is made against
`Zagoman/stacks` at `af004eb`. It adds:

- `mealplanner/compose.yml`, `backup.sh`, `restore.sh`
- `caddy/Caddyfile`: a `meals.liluhome.dk { reverse_proxy mealplanner:8080 }` block
- `ansible/group_vars/all/main.yml`: AdGuard rewrite for `meals.liluhome.dk`, the
  two `appdata_dirs` (owner 999), the `mealplanner` stack entry, its `stack_env`
  and empty `vault_mealplanner_*` defaults
- `ansible/examples/secrets.example.yml` and `ansible/README.md`: the new vault vars

```bash
cd ~/dev/homelab-server
git apply --check ~/dev/meal-planner/deploy/homelab/mealplanner.patch
git apply ~/dev/meal-planner/deploy/homelab/mealplanner.patch
git add -A && git commit -m "Add the meal planner stack (meals.liluhome.dk)" && git push
```

On the server:

```bash
cd /opt/stacks && git pull
```

### 5. Run the Ansible deploy

```bash
cd ~/dev/homelab-server/ansible
ansible-playbook playbooks/site.yml --check
ansible-playbook playbooks/site.yml
```

That creates `/srv/appdata/mealplanner/{db,backups}` owned by 999, renders
`/opt/stacks/mealplanner/.env`, brings the stack up and re-renders AdGuard
with the new rewrite. (`--tags` aren't defined in this playbook, so it is the
full run.)

Caddy bind-mounts `Caddyfile` as a single file. `git pull` replaces the file,
so the running container keeps seeing the old one, and a `caddy reload` is not
enough. Restart it:

```bash
ssh lucs@192.168.0.31 docker restart caddy
```

### 6. Check, then create the admin

```bash
make deploy-only
```

This checks the stack, the health and the Caddy route without building.
Then open `https://meals.liluhome.dk` and sign up with the
`vault_mealplanner_admin_email` address; that member is the server admin
(`ADMIN_EMAIL`). The sign-up/invite flow itself belongs to package A; see its
notes if it differs.

## Routine deploy

```bash
make deploy
```

One command, from a clean, committed tree. It builds, pushes `:<sha>` and
`:latest`, pulls and restarts on the server, then fails loudly unless the new
revision is running and healthy both in the container and through Caddy.
`make image` refuses a dirty tree, so the SHA tag always matches the code
(`ALLOW_DIRTY=1` overrides it for local experiments).

Variables, with defaults taken from the homelab repo:

| Variable | Default |
|---|---|
| `DEPLOY_HOST` | `192.168.0.31` (`inventory.yml`; the Tailscale name works too) |
| `DEPLOY_USER` | `lucs` |
| `STACK_DIR` | `/opt/stacks/mealplanner` |
| `DOMAIN` | `meals.liluhome.dk` |
| `IMAGE` | `ghcr.io/zagoman/meal-planner` |
| `PLATFORM` | `linux/amd64` |
| `HEALTH_TIMEOUT` (env) | `120` seconds |
| `SKIP_PROXY_CHECK=1` (env) | skip the through-Caddy check |

Other targets:

- `make rollback SHA=<12-char sha>`: points `:latest` at an older pushed image
  (registry-side, `buildx imagetools create`) and deploys it.
- `make homelab-sync`: after changing anything in `deploy/stack/`, copies it into
  `~/dev/homelab-server/mealplanner/`. Commit there and `git pull` on the server.
  `make deploy` warns when the server's `compose.yml` differs from this repo's.
- `make stack-config`: `docker compose config` validation.
- `make smoke`: local end-to-end test (below).

Ansible stays the source of truth for `.env`: re-running the playbook rewrites
it, and the boot unit (`homelab-stacks.service`) runs `docker compose up -d
--no-build` with whatever `:latest` image is on the host. To pin a version
instead of `latest`, add `MEALPLANNER_TAG: "<sha>"` to `stack_env.mealplanner`.

## Backups

`mealplanner-backup` runs `pg_dump --format=custom` at 03:00 local time, 30
minutes before the restic timer (03:30), which backs up all of `/srv/appdata`.
Dumps older than 14 days are deleted. On start it dumps once if there is no
dump from the last 24 h. Restic also copies the live
`/srv/appdata/mealplanner/db` directory, but a copy of a running Postgres is
not guaranteed consistent. **Restore from the dumps.**

Manual dump:

```bash
cd /opt/stacks/mealplanner && docker compose exec backup sh /backup.sh now
```

Logs:

```bash
docker logs mealplanner-backup
```

### Restore

On the server:

```bash
cd /opt/stacks/mealplanner
ls -lt /srv/appdata/mealplanner/backups | head
./restore.sh /srv/appdata/mealplanner/backups/mealplanner-YYYYMMDD-HHMMSS.dump
```

`restore.sh` asks for `yes`, takes a safety dump of the current database, stops
the app, drops and recreates the database, runs `pg_restore --no-owner
--exit-on-error`, and starts the app again (migrations run on start). The same
steps by hand:

```bash
docker compose exec -T backup sh /backup.sh now
docker compose stop app
docker compose exec -T db dropdb -U mealplanner --force --if-exists mealplanner
docker compose exec -T db createdb -U mealplanner -O mealplanner mealplanner
docker compose exec -T db pg_restore -U mealplanner -d mealplanner --no-owner --exit-on-error < FILE.dump
docker compose start app
```

To restore from restic (dump file gone from disk): `restic restore latest
--target /tmp/r --include /srv/appdata/mealplanner/backups`, then run
`restore.sh` on the file.

`make smoke` tests this path for real (see below).

## Access: LAN + Tailscale vs public

**Current setup (recommended): not public.** Like every other `*.liluhome.dk`
site, `meals.liluhome.dk` resolves to `192.168.0.31` through the AdGuard
rewrite, and Caddy gets a real certificate via Cloudflare DNS-01 without any
inbound port. HTTPS matters here: the service worker and the offline tick queue
only work on a secure origin. Away from home, use Tailscale, either by:

- advertising the LAN from the server (`tailscale up --advertise-routes=192.168.0.0/24 ...`,
  approve in the admin console) and adding a split-DNS nameserver in the tailnet
  DNS settings: `liluhome.dk` → `192.168.0.31` (AdGuard), or
- pointing the tailnet's split DNS at AdGuard on the server's Tailscale IP and
  adding a rewrite to that IP.

Both are tailnet/Ansible changes outside this repo; nothing here assumes either.

**Public via Caddy** (only if someone without Tailscale needs it): add a
Cloudflare DNS record for `meals.liluhome.dk` and forward 443 on the router.
That exposes **every** vhost on the shared Caddy, so first limit the others to
private ranges (a `@private remote_ip 192.168.0.0/16 100.64.0.0/10` matcher)
or use a Cloudflare Tunnel for just this hostname. The app has its own login
and per-household isolation, but the Anthropic key behind it costs money per
plan, so keep it private unless needed.

## Local verification

```bash
make image ALLOW_DIRTY=1
make smoke
```

`make smoke` (`deploy/scripts/smoke.sh`) lays the stack out like the server
(a stack folder with `compose.yml` and `.env`, bind-mounted appdata), starts
it from the local image, checks `/healthz` in the container and over the
`proxy` network as Caddy sees it, then does a dump, adds a row, runs
`restore.sh` and checks the row is gone. It removes the containers when done
and leaves the temp directory.
