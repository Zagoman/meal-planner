# Homelab changes (not applied)

`mealplanner.patch` is a `git apply` patch for `Zagoman/stacks`
(`~/dev/homelab-server`), made against `af004eb`. How to apply it:
[../README.md](../README.md#4-apply-the-homelab-changes). What it changes:

**`mealplanner/`** (new stack): copies of `deploy/stack/{compose.yml,backup.sh,restore.sh}`.
After later changes there, run `make homelab-sync`.

**`caddy/Caddyfile`**

```caddy
# Meal planner (SSE streams: flush immediately)
meals.liluhome.dk {
    reverse_proxy mealplanner:8080 {
        flush_interval -1
    }
}
```

**`ansible/group_vars/all/main.yml`**

```yaml
adguard_rewrites:
  - domain: "meals.liluhome.dk"
    answer: "{{ server_lan_ip }}"

appdata_dirs:
  - path: "{{ appdata_dir }}/mealplanner/db"
    owner: "999"            # postgres container uid
    group: "999"
  - path: "{{ appdata_dir }}/mealplanner/backups"
    owner: "999"            # nightly pg_dump runs as the postgres uid
    group: "999"

stacks:
  - name: mealplanner

stack_env:
  mealplanner:
    TZ: "{{ timezone }}"
    BASE_URL: "https://meals.liluhome.dk"
    ADMIN_EMAIL: "{{ vault_mealplanner_admin_email }}"
    SESSION_SECRET: "{{ vault_mealplanner_session_secret }}"
    ANTHROPIC_API_KEY: "{{ vault_mealplanner_anthropic_api_key }}"
    CLAUDE_MODEL: "claude-sonnet-5-5"
    POSTGRES_PASSWORD: "{{ vault_mealplanner_db_password }}"
    DATABASE_URL: "postgres://mealplanner:{{ vault_mealplanner_db_password }}@db:5432/mealplanner?sslmode=disable"

vault_mealplanner_db_password: ""
vault_mealplanner_session_secret: ""
vault_mealplanner_anthropic_api_key: ""
vault_mealplanner_admin_email: ""
```

**`ansible/examples/secrets.example.yml`** and **`ansible/README.md`**: the four
`vault_mealplanner_*` variables, with how to generate each one.
