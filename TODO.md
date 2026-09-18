# TODO — Public container deployment: stateless /v1 proxy with multi-account rotation

## Scope (the product)

Expose `/v1` as a simple stateless OpenAI-compatible endpoint for coding IDEs
(Cursor, ZCode) and personal agents (OpenClaw). Run in a container, publicly
exposed, long-lived and lightweight. Rotate multiple **Cline** accounts and
multiple **OpenCode Zen** accounts round-robin to maximize free-tier limits.
**Combos**: dashboard-defined alias model IDs (e.g. `cline-proxy`,
`opencode-proxy`) that upstream to a chosen concrete model — same platform only.
The admin panel is setup/maintenance only, not part of daily operation.

Release blockers before ANY public exposure: **M1 (env config + /v1 API key)**
and **M2 (admin auth)**. Everything else can land incrementally.

## Current state (audit 2026-09-15)

- Admin panel (`/admin/`, all `/admin/api/*`) has **no authentication at all**;
  CORS `*` everywhere (`internal/app/proxy.go:328`).
- Proxy endpoints check client API keys **only if at least one key exists** —
  empty key list = wide open (`internal/app/proxy.go:107`).
- Go code reads **no env vars** (`PORT` in the Dockerfile is never read; flags only).
- Secrets/config in `data/`:
  - `.cline-accounts.json` — Cline account refresh tokens + client proxy `Keys`
  - `.zen-config.json` — **OpenCode Zen** config: single `Key` (`zen.go:158`),
    baseURL, outbound proxy list, failover/compaction settings
- Cline account rotation already exists: `pickAccount()` (`internal/app/pool.go:154`)
  with `round_robin` (default) / `fill` / `random`, per-account daily token counters.
- OpenCode Zen has a **single** key — no multi-account rotation (`internal/app/zen.go`).
- `override.md` (replaces the client's system prompt, `internal/app/proxy.go:349`)
  is applied whenever the file exists; docker-compose mounts it by default.
- `requests.jsonl` logs per-request **metadata only** — time, client IP, method,
  path, model name, route tag, status, duration (`internal/app/logs.go:18`); no
  message content. Mounted on every route incl. `/admin` (`proxy.go:298`);
  in-memory ring of 500 + 10MB file cap that wipes (not rotates); always on,
  no toggle.
- Dockerfile: root user, no HEALTHCHECK, no `.dockerignore`.

## M1 — Env-var configuration layer + /v1 API key (blocker)

- [ ] Env config loader (new `internal/app/config.go`): `PORT`, `DATA_DIR`,
      `API_KEY`, `ADMIN_PASSWORD`, `POOL_STRATEGY`, `LOG_REQUESTS`,
      `LOG_FILE_MAX_MB`, `APPLY_SYSTEM_PROMPT_OVERRIDE`, `ZEN_KEYS`,
      `REQUIRE_ADMIN_AUTH`. Flags keep precedence (document the rule).
- [ ] `API_KEY` env → the only valid credential for all `/v1/*` endpoints
      (accept `Authorization: Bearer` and `x-api-key`). When set, it overrides
      the admin-generated key list entirely — stateless, no DB lookup.
      Compare with `crypto/subtle`.
- [ ] Fail closed: publicly listening (non-loopback host) without `API_KEY`
      → refuse to start (matches your "only my env key can call upstream" rule).

## M2 — Admin auth via env var (blocker)

- [ ] `POST /admin/api/login` → HMAC-signed session cookie (`HttpOnly`,
      `SameSite=Strict`, `Secure` behind TLS), ~7d expiry; also accept
      `Authorization: Bearer <session>` for curl/scripting.
- [ ] Wrap ALL admin surfaces: `adminStaticHandler`, every `/admin/api/*`,
      `/admin/zen/` panel (`internal/app/admin.go:81` — easy to miss).
- [ ] `ADMIN_PASSWORD` env (or `ADMIN_PASSWORD_FILE` for docker secrets);
      `crypto/subtle` compare; login rate limit per IP (5/min).
- [ ] Fail closed: non-loopback host + no `ADMIN_PASSWORD` + `REQUIRE_ADMIN_AUTH=true`
      → refuse to start. Logout endpoint clearing the cookie.

## M3 — Multi-account round-robin (core scope)

Cline pool (mostly done, wire it up):
- [ ] **Round-robin is the load-balancing default** — already true in code
      (`defaultProxyConfig` admin.go:807 sets it, and `pickAccount()`'s switch
      falls through to round_robin, pool.go:187). Keep it that way.
- [ ] `POOL_STRATEGY` env → `cfg.Strategy`, **default `round_robin`**; env value
      wins over any persisted config so a redeployed container always comes back
      to round-robin (your multiple accounts spread every call to maximize
      per-account limits).
- [ ] Verify round-robin distributes by call (not by token count) and that
      cooldown/expired accounts are skipped cleanly (`pool.go:179`).
- [ ] Startup seeding for disposable containers: `CLINE_ACCOUNTS_SEED_FILE`
      (mounted JSON array of `{refreshToken, email}`) auto-imported once at boot
      if the pool is empty — makes `docker compose up` on a fresh host fully
      reproducible without OAuth clicks.

OpenCode Zen (new feature, mirrors the Cline pool):
- [ ] Multi-key support: `zenConfigData.Key string` → `Keys []string` with
      per-key usage/cooldown state (`internal/app/zen.go:156`).
- [ ] **Round-robin is the default (and only) strategy for the zen key pool**,
      same as the cline pool: keys rotate per call so your multiple zen accounts
      share load evenly and per-account limits are maximized. 429/quota errors
      additionally trigger cooldown + instant retry on the next key.
- [ ] Round-robin across zen keys on each upstream call (`zen.go:403`, `zen.go:510`);
      on 429/quota errors, mark key cooling and immediately retry on next key.
- [ ] `ZEN_KEYS` env (comma-separated) seeds the key list at boot when config is empty.
- [ ] Admin UI: show zen key list with per-key usage; keep single-key input
      working as a 1-element list (backward compat with existing `.zen-config.json`).

## M3.5 — Proxy routing & abort hardening (done 2026-09-15)

Per-request proxy rotation for all upstreams + client-abort safety:

- [x] **Per-request rotation**: each upstream attempt explicitly picks an exit
      (`pickUpstreamProxy`, round_robin default) via a per-proxy pinned client
      cache (uTLS Chrome fingerprint + h2). Old behavior rotated per *dial*,
      which with HTTP/2 connection reuse meant far less rotation than expected.
- [x] **Proxies apply to cline upstream too** (before: zen only). Enabled by
      `CLINE_USE_PROXIES=true` env (provider-wide) or per-combo `useProxies`
      toggle (dashboard checkbox). zen upstream uses the pool whenever
      `proxies` is configured in its config (unchanged), now per-request.
- [x] **Dead-proxy handling**: failed exit gets a 5-min cooldown and the retry
      automatically uses the next one; proxy failures never mark accounts/keys.
- [x] **Client aborts (IDE cancel/abort) can't poison state**: client context is
      propagated into both upstreams — a cancel terminates the upstream call (no
      wasted quota), aborts bypass retry loops, and never count as rate-limit or
      network failures on keys/accounts/proxies. Background summary generation
      uses its own context (unaffected by client aborts).
- [x] Cooldown poisoning audit: only genuine upstream 429/limit signals cool
      keys/accounts/proxies; stream write errors to a dead client were verified
      to touch no cooldown state.

## M4 — Combo model aliases (dashboard)

Virtual model IDs the IDE calls; each combo maps to one concrete upstream model
on **its own platform only** (a cline combo selects from cline `/models`, a zen
combo from zen's list — cross-platform selection must be impossible).

- [ ] Data model: `combo = {id, platform: "cline"|"zen", target, createdAt}`,
      persisted under `data/`. The alias `id` is **fully user-chosen at creation**
      — you type any name you want (`cline-glm-5.3`, `opencode-proxy`,
      `cline-deepseek`, ...); the only restriction is it must not collide with a
      real upstream model ID (rejected at save with a clear error).
- [ ] CRUD API: `GET /admin/api/combos` (list), `POST /admin/api/combos`
      (create), `DELETE /admin/api/combos/{id}` (delete). Renaming = delete +
      recreate; no hidden magic IDs, all combos are user-defined.
- [ ] Routing hook at the top of `routeModel()` (`internal/app/zen.go:108`):
      combo ID → route to its platform and rewrite the upstream model to `target`.
- [ ] Enforce same-platform at the API layer, not just the UI: on save, a `cline`
      combo's `target` must exist in the cline model cache, a `zen` combo's in
      `resolveZenFreeModel` (free models only).
- [ ] Fallback: if `target` is inactive/expired at call time, fall through to that
      platform's existing default-model logic instead of erroring the request.
- [ ] Include combo IDs in `/v1/models` output so Cursor/ZCode/OpenClaw can pick
      them directly.
- [ ] Dashboard UI (Models section): create/delete combos — free-text alias field
      (your own name), platform picker, then a model dropdown locked to that
      platform's model list. Show target model + platform per combo in the list.
- [ ] Seed defaults after first deploy: `cline-proxy` → `deepseek/deepseek-v4-pro`
      (cline), `opencode-proxy` → chosen zen free model.

## M5 — Import / export all credentials

- [ ] Unified backup: `{version, exportedAt, accounts[], keys[], zenConfig{keys, baseURL, proxies, strategies}, combos[]}`
      — `GET /admin/api/backup/export`, `POST /admin/api/backup/import`
      (`mode: merge|replace`, per-item validation results).
- [ ] Admin UI buttons in Settings (file download / file picker + confirm on replace).
- [ ] Keep legacy `/admin/api/accounts/export` + `/batch-import` working.

## M6 — Lightweight long-run defaults

- [ ] `LOG_REQUESTS` env, **default true (enabled)** — the log is metadata-only
      (no message content, no keys) and disk-bounded (next item), so keeping it
      on costs almost nothing. Set `LOG_REQUESTS=false` to disable entirely:
      no `requests.jsonl` writes, admin log tab shows empty, and the
      request-body probe is skipped too (`logs.go:144` buffers the whole body
      just to extract `model` for the log — wasted work on the hot path when off).
- [ ] Log cap: **keep the existing wipe-on-cap behavior** — when `requests.jsonl`
      passes the cap the file is emptied (`logs.go:58`). Accepted trade-off:
      losing old metadata logs is fine, the admin panel still shows the
      in-memory last 500 entries, and this avoids complicating the logging code
      with a rolling-rewrite scheme. Only change: make the cap size
      configurable via `LOG_FILE_MAX_MB` (default 10). The in-memory 500-entry
      ring stays as is.
- [ ] When logging is off, also skip the request-body probe (`logs.go:144`
      buffers the entire body just to extract `model` for the log) — avoids
      per-request buffering work on the hot path.
- [ ] System-prompt override off by default: `APPLY_SYSTEM_PROMPT_OVERRIDE` env
      (default false) gates `applyOverride` (`proxy.go:349`); Cursor/ZCode/OpenClaw
      keep their own prompts. Remove the `override.md` mount from the default
      docker-compose (opt back in by uncommenting).
- [ ] Rotation of any other growing files (stats jsonl) — cap or disable by default.

## M7 — Container polish

- [ ] Read `PORT` env in `main.go` (fix the Dockerfile mismatch); `DATA_DIR` respected.
- [ ] Dockerfile: non-root `USER`, `HEALTHCHECK` on `/health`, keep CGO_ENABLED=0.
- [ ] `.dockerignore`: `.git`, `data/`, `*.log`, `capture*`, `dist/`, `override.md`.
- [ ] docker-compose: env-driven (`API_KEY`, `ADMIN_PASSWORD`, `ZEN_KEYS`, ...),
      healthcheck, no override.md mount by default.
- [ ] Optional compose profile: Caddy sidecar for automatic TLS in front of /v1 + admin.
- [ ] README "Public deployment" section: required env vars, TLS, backup/restore,
      upgrade flow (volume holds the only state).

## M8 — Later / nice to have

- [ ] Change admin password from the UI (hash in data/, overrides env).
- [ ] Per-key/per-account usage dashboard improvements; zen key cooldown display.
- [ ] Multi-arch images (arm64) via GitHub Actions.

## M9 — Hardening round 1 (2026-09-15, done)

Full-codebase audit (3 parallel review passes: proxy/zen core, protocol
conversion, admin/auth/deploy) then patch of all confirmed findings.
Commit `5b60cc3`. Highlights:

- [x] Request body cap (MAX_BODY_MB=32) + 413 before handlers; envBool
      fail-closed; admin keys from crypto/rand; wildcard CORS removed from
      /admin/api/*; esc() quote-safe + data-attr event sinks; oauthSessions
      and loginFails lifecycle; config update copy-on-write.
- [x] callClineAPI rebuilds request per attempt (retry loops reused the
      consumed body); account refresh single-flight + only 400/401/403
      expires an account; token-expiry parse failure falls back to 55min
      TTL; cooldowns capped 24h; mid-stream errors surface as 500.
- [x] Anthropic/Responses translators: real usage passthrough, parallel
      tool calls, deterministic tool order, orphaned tool-message guards
      in compaction, temperature=0 / stop_sequences no longer dropped.
- [x] Graceful shutdown (SIGTERM, 10s drain); Dockerfile go.sum; CI vet
      gate; cline upstream UA + crypto-random session IDs; auth JSON
      client 60s timeout; .env gitignored; raw SSE dump (STREAM_LOG)
      default off.

Known deferred (low risk, revisit if needed):
- zen-stats.jsonl still grows unbounded (aggregate-rewrite idea).
- Dead credentials subsystem in internal/cline/auth.go (GetToken /
  LoadCredentials) — delete or mutex-guard before ever reusing.
- main.go "already running" detection is Windows-only; releases auto-tag
  every push to main.

## Settled trade-offs (do not re-propose)
- /v1/responses with a native-responses zen model (muse-spark) goes through
  the chat-completions upstream and 500s — by design. The Upstream-aware
  path for spark is /v1/chat/completions (routes to native /v1/responses
  upstream automatically). README's "spark via /v1/responses" claim refers
  to that routing. Fixing /v1/responses client-dialect for spark would
  require a responses-dialect emitter for the native upstream; deferred.
- ZEN_DEBUG_BODY was a temporary probe hook, removed before commit.
