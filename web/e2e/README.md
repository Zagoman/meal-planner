# Web UI end-to-end test

```bash
make e2e
```

Installs Playwright (pinned in `package.json`) and Chromium, starts
`go run ./internal/web/e2eserver` (the real router with the in-memory
`webfake` services and seed data; no Postgres, never the Claude API) and runs
one flow on a phone-sized viewport:

log in as two members → list → tick on one phone → the other phone sees it
over SSE → go offline, tick (queued in localStorage, badge shows "1 to sync")
→ back online, the queue replays and the other phone sees it → the service
worker controls the page and has cached the list.

To click around yourself: `make e2e-server`, open http://127.0.0.1:8090 and
log in as `anna@example.test` / `correct horse` (also `ben@`, `carl@`). A plan
prompt containing "fail" simulates a failed generation.
