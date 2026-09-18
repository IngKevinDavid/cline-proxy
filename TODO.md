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
- zen chat tool_choice policy (2026-09-18, supersedes the "text-QA gateway"
  note in zen.go history): split by client intent, verified by direct
  upstream A/B on ling — tool_choice=none (no client tools) returns text;
  tool_choice=auto (client tools present) lets models call the client's
  own tools. Keep the split: making auto unconditional regressed ling
  multi-turn to empty text. Responses path (spark) must always send auto
  (gate rejects none) and passes client tools through as flat items.

## Audit of 8a9e2c3 (2026-09-18, fixed in b50191f)
Reviewed the tool-call/IDE changes with three parallel reviewers, then fixed
and re-probed (spark-1.3 + mimo, 21/21 on a fresh container). Points worth
remembering, either as behaviour or as "don't re-propose":
- Client tool definitions intentionally win over the gate stubs of the same
  name (the gate only checks that the *names* exist; the stub's empty schema
  was what made a client `read` call lose `file_path`). Gate names still all
  ship in the request, so the FreeTier check is unaffected.
- A delta whose item_id/output_index belongs to no accumulator is dropped on
  purpose (the later done/output_item event carries the full arguments);
  adopting it into another call is what produced the {"query":..}{"url":..}
  jam. Do not "fix" this by falling back to the last call again.
- SSE comment/heartbeat lines (": keep-alive", mimo sends them before the
  first chunk) are valid SSE; the non-SSE detector must only classify a body
  as JSON when the first non-empty line is not an SSE field line.
- Compaction summary generation now issues stream=true and aggregates it
  (both zen endpoints 403 on stream=false). Remaining limitation: for a
  responses-native model (spark) the summary request still carries the gate
  tools with tool_choice=auto, so the model may answer with a tool call and
  the summary falls back to truncation. Accepted; revisit only if spark
  compaction quality becomes a real complaint.
- Reasoning-heavy models (spark) need real max_tokens headroom: with
  max_tokens≈400 the reasoning consumes the budget and the client gets empty
  content with finish_reason=length. That is upstream model behaviour, not a
  gateway bug — probes should use ≥1500-2000 tokens for tool turns.

## Live catalog as the source of truth (2026-09-18, 9499f99)
The model tables used to be a hybrid: seed ids cross-checked against live
sources. Settled the other way — live is authoritative, seeds are bootstrap.
- zen: the pricing gate (models.opencode.ai `opencode` provider, cost 0/0,
  status != deprecated) *is* the real list. `opencode models` reads the same
  data (probed: CLI 7 = registry free 7), so spawning the CLI per sync adds
  nothing — it is now only the registry-down fallback, paired with the
  `-free` suffix heuristic. Do not reintroduce a seed cross-check: a seed id
  that no longer passes the gate only kept dead models listed.
- cline: /ai/cline/recommended-models is quota-independent — probed 200 on
  three accounts while every pool account was 429'd for inference. Hence
  pickAccountAny() for the sync; with only pickAccount() the whole sync
  skipped during a quota outage and the list froze on the seed snapshot.
- Seeds (both platforms) are pruned like everything else once a live list
  arrives; the existing guard stays (skip the prune when live < half of the
  known table) so a truncated feed cannot wipe the list.
- `defaultModel` is now empty = no preference, and getDefaultModel() picks
  the lexicographically smallest active id. The old random map pick made a
  pruned preference drift to a different model per request. The panel's
  DefaultModel still overrides.
- Consequence to expect: ids that only existed as seeds are gone
  (deepseek/deepseek-v4-flash, stepfun/step-3.7-flash). Requests for them
  return 400 "not available on this gateway (see /v1/models)" unless
  STRICT_MODEL_MATCH=false. A combo pinned to a stale id needs re-pointing.

## cline "requires stream" self-learning (2026-09-18, a4de61a)
The free-model feed publishes only id/name/description/tags, so "does this
model have to be called with stream=true" is inferred from the id shape
(models.go: no ":" in the id -> force upstream streaming). That inference
can only be wrong one way, and the upstream says so explicitly: 500
{"error":"empty response content"}.
- callClineAutoStream (cline_stream.go) wraps callClineAPI on the three
  non-stream ingresses. On exactly that fingerprint it learns the id into
  DATA_DIR/.cline-stream-required.json and retries with stream=true,
  returning streamed=true so the caller aggregates the SSE - the client
  still gets plain JSON with status 200.
- Only the exact fingerprint is learned. Other 5xx pass through untouched:
  a transient overload must never be persisted as a model property (unit
  test guards this). One-way learning is deliberate - over-forcing is
  transparent, under-forcing is the only visible failure.
- To un-learn a model, delete it from that JSON file (or the file) and
  restart; there is no reverse learning and none is planned.
- do not replace the colon heuristic with this: learning costs one failed
  non-stream request per model, the heuristic costs nothing when right.
  They are complementary - heuristic first, learning as the safety net.
- clineCallFn is a package-level seam so the retry path is testable against
  a fake upstream; production value is always callClineAPI.

## arm64 image: QEMU at build time only, no native runner needed (2026-09-18, verified)
Question was whether supporting arm64 means wrapping the Dockerfile in QEMU
instead of a native arm64 build. Measured answer: both are true already and
nothing needs to change.
- opencode-ai@1.18.31 (still npm `latest`) DOES publish arm64 binaries,
  including opencode-linux-arm64-musl (os linux / cpu arm64 / libc musl) -
  so the "CLI is amd64-only" premise is wrong for the pinned version.
- Verified locally with `docker buildx build --platform linux/arm64`: the
  whole build succeeds (exit 0), including the emulated `npm i -g
  opencode-ai` + `opencode --version` (262s under QEMU). The resulting image
  boots, /health returns ok, the banner auto-detects the default model, and
  /app/bin/opencode inside it is a native aarch64 ELF that prints 1.18.31.
- Cost model: QEMU is used ONLY at build time on GitHub's amd64 runners, and
  only for the npm stage (the Go binary is cross-compiled natively via
  GOOS/GOARCH from BuildKit args). At runtime on an arm64 host everything is
  native - no emulation, no performance penalty.
- No CI change needed: the existing setup-qemu + platforms
  linux/amd64,linux/arm64 already produces a working arm64 image. Image size
  is ~540MB on both arches (dominated by the ~185MB opencode binary).
- Caveat if a future opencode-ai release drops arm64: the npm stage would
  fail the arm64 leg and CI would go red (not silently degrade). If that
  happens, gate the CLI install on $TARGETARCH and copy from a directory so
  an empty dir still satisfies COPY - the gateway itself is arch-independent
  and harvestEnabled() already degrades gracefully when the binary is absent.

### Correction (same day, after the real push): the local arm64 build was misleading
The paragraph above said "no CI change needed" on the strength of a local
`buildx --platform linux/arm64` build that passed. The actual CI run failed
on the arm64 leg with exit code 132 — QEMU: uncaught target signal 4
(Illegal instruction) inside `[linux/arm64 opencode-cli 2/2]`. Root cause
is in opencode-ai's own postinstall: it ends with `verifyBinary()`, which
spawns `opencode --version` and treats any non-zero exit as "the package
manager installed the wrong binary", then exits 1 — so `npm i` fails, not
just the check. Docker Desktop's QEMU runs that Bun binary; GitHub's does
not. **Lesson: a foreign-arch build that passes locally under Docker
Desktop's QEMU proves nothing about GitHub's QEMU — for arm64 the only
trustworthy local check is the artifact (ELF arch + native-arch run), and
the real gate is CI.**
Fix (commit `b95e749`, CI green, GHCR verified): the CLI stage branches on
TARGETARCH. Same arch keeps the official `npm i -g opencode-ai` (its
verification works there). Cross arch fetches the same published npm
tarball (opencode-linux-arm64-musl) with busybox wget, extracts the binary,
and validates the architecture from the ELF header (e_machine 0xB7) instead
of executing it — nothing foreign runs at build time, and a wrong-arch
artifact fails the build. Version is pinned once via `OPENCODE_VERSION`
ARG for both branches. Unpinned arches (arm/v7) build without a CLI and
degrade to gateway-only, as harvestEnabled() already documented.
Verified end to end: both CI jobs green, manifest publishes amd64 + arm64,
and the pulled `ghcr.io/foxy1402/cline-proxy:latest` arm64 image boots with
/health ok and its embedded CLI printing 1.18.31.

## Whole-codebase audit (2026-09-18) — findings, fixes, and settled trade-offs

Four parallel audit agents covered the tree (~14.7k lines) partitioned by
subsystem: zen path, proxy + cline_stream, admin/panel/pool, responses +
compact + kit. Every P0/P1 was re-verified against the code before patching;
each fix below was then re-verified in a container against the real upstreams
(21/21 behaviour regression + 8/8 new patch cases).

### P0 — features that were dead or destructive in production
- **cline "requires stream" self-learning never fired.** `callClineAPI` returns
  a nil response for every non-200 (body read and closed), but
  `callClineAutoStream` only sniffed the body when it got a 500 *response* —
  a shape the real callee cannot produce. The unit test passed because the
  fake upstream returned `(500 response, nil error)`. Fixed by adding a typed
  `clineAPIError{Status, Body}` and classifying by status, and by rewriting
  the test fake to mirror the real contract. A new end-to-end test drives the
  **real** `callClineAPI` against a local upstream (injectable `clineAPIBase`).
- **"Refresh all tokens" expired every static-key account.** The admin loop
  called `refreshAccountToken` for all accounts; `doRefreshAccountToken` marks
  any `APIToken` account `expired` ("static key cannot refresh"). The startup
  prewarm already guarded this, the admin path did not. Now skips them and
  reports the count.
- **A transient zen 500 permanently misrouted a model.** `isWrongEndpoint`
  substring-matched the whole error text against a keyword list containing
  `"no such"` and `"endpoint"` — and Go's DNS failure text is "dial tcp:
  lookup …: no such host", so one network blip was learned and persisted as
  "this model needs /responses", which the catalog sync never rewrites.
  Classification is now status-code based (`zenHTTPError`), and 502/503/504
  are excluded.
- **Learning now requires the retry to succeed.** Probing live showed
  muse-spark returning a bare 500 on its *correct* responses endpoint twice in
  five minutes, so a bare 500 is not proof of a wrong endpoint.
  `handleZenResponsesNative` returns whether the request was actually served,
  and the flip is persisted only then.

### P1 — silently wrong behaviour
- SSE-level `error` events were swallowed by `collectStreamResponse` (empty
  200 instead of an error).
- `emitChatAsSSE` sliced content at byte 2048, splitting multi-byte runes into
  U+FFFD; it now retreats to a rune boundary. Verified live with 372 CJK
  characters over the SSE path (0 replacement chars).
- `chatStreamToResponses` sent `response.completed` with empty output when the
  upstream answered 200 with a non-SSE JSON body; it now emits
  `response.failed`, matching `collectStreamResponse`.
- The zen Anthropic non-stream path skipped `truncateAtStopSequences`, so
  `stop_sequences` silently did not apply there.
- `ZenModel` fields were mutated in place while request paths read the same
  pointer outside the lock. Writers now use copy-on-write and the invariant is
  documented on the type.
- `harvestOnForbidden` did a split check-and-set (N concurrent 403s each
  spawned a CLI harvest) and did not skip the `"public"` sentinel, so it would
  overwrite the admin's real `auth.json` credential with `key:"public"`.
- `Retries` had no cap: `delay *= 2` overflowed to a negative duration and
  retries became a no-backoff hot loop. Now capped at 30s via
  `zenRetryDelay`.
- `buildTransport` set no `TLSHandshakeTimeout`/`ResponseHeaderTimeout`, so a
  hung upstream could hold all 8 zen semaphore slots forever. Set to 15s/5min
  — **5 minutes, not 90s**, because this transport is shared with cline, whose
  non-stream requests legitimately take minutes to first byte. Deliberately a
  guard against "never responds", not a latency budget.
- `ADMIN_PASSWORD_FILE` / `API_KEY_FILE` failed **open**: if the file became
  unreadable at runtime the value became "" (meaning "not configured"), which
  disabled panel auth and made `/v1` fall through to "no key required". Both
  now return a per-process random un-matchable sentinel, logging once.
- `collectStreamResponse` never closed the response body (its sibling
  `responsesSSEToChat` always did).
- `fallbackTruncate` `continue`d past non-fitting messages, keeping older
  history and dropping the newest turn; it now breaks, keeping a contiguous
  tail.
- `compactThreshold` had no floor, so a small-window model with a large
  declared output re-summarised on every single turn.
- `/v1/responses` ignored the admin "zen disabled" switch; the `429` panel
  toast double-escaped (showing `&quot;error&quot;:` literally), zero times
  rendered as year 1, and proxy cooldowns used a clock-only formatter for
  cooldowns that can reach 24h.
- `getNested` accepted negative indices (latent panic).

### Settled trade-offs (do not re-propose)
- **Endpoint learning is confirmation-based, not fingerprint-based.** An extra
  retry per request for the affected model, in exchange for never
  permanently misrouting a healthy model. Rejected alternatives: trusting a
  bare 500 (disproved live), and sniffing `provider.npm` from the catalog
  (23 of 29 free models have no npm field, no pattern).
- **`ResponseHeaderTimeout` stays at 5 minutes.** A tighter value would break
  cline's legitimate long non-stream generations; the goal is only to break
  the "accepts the connection and never answers" case.
- **Secret files seal instead of falling back.** An explicit `ADMIN_PASSWORD`
  is still ignored while `ADMIN_PASSWORD_FILE` is set but unreadable — this is
  intentional: a broken secret mount must not silently downgrade auth.
- **The cline naming convention stays** (`":" ⇒ no forced stream) and learning
  remains one-directional: a false positive only costs one server-side
  aggregation, a false negative is what the learning path exists to repair.
- **`zenSessions` / harvest counters are pruned on key removal**; the learn
  cooldown map is not (bounded by the model count, which the catalog sync owns).

## Zen first-boot session minting: parallel + per-key HOME (2026-09-18)

Reported from the live instance: with 11 zen keys, the first calls after a
fresh deploy were extremely slow, and the logs showed a chain of
`session rejected (403)` across keys #5..#11 with harvests interleaved.
Root cause was two-independent things:

- **The request path had no notion of session liveness.** `pickZenKey` only
  skipped rate-limit cooldowns, so it happily handed out keys whose session
  was a locally random `sess_` placeholder — which is a *guaranteed* 403
  (verified 2026-09-17: the server only accepts sessions it has seen). One
  client request therefore walked key after key, each costing a full upstream
  round trip (~15-20s in the reported logs), before failing.
- **Minting was strictly serial.** One global mutex for the whole harvest,
  a shared CLI `HOME` (so parallelism was impossible: every key rewrote the
  same `auth.json`), a 5s stagger between keys, and a failure path that could
  burn 3 models × 3 attempts × 60s = 9 minutes *per key*.

### Changes
- **Per-key CLI HOME** (`ZEN_HARVEST_HOME/keys/<sha256(key)[:6]>`): auth.json
  and CLI logs are isolated, so keys mint in parallel and the shared HOME is
  never touched. The directory name is a hash — the key itself must not end
  up in a path (logs, `ps`, `ls`).
- **Bounded parallel minting** (`mintZenSessions`, `ZEN_HARVEST_CONCURRENCY`,
  default 3) used by all three triggers: startup, the hourly stale-key pass,
  and the new manual button.
- **Per-key budget** (`ZEN_HARVEST_KEY_TIMEOUT_SECONDS`, default 150s) so the
  9-minute failure path can't hold a worker.
- **`pickZenKey` prefers keys with a live session** (two passes: not-cooling +
  live, then not-cooling) instead of handing out certain-403 keys.
- **Panel**: new "Live session IDs" section on the opencode tab — `N/M live`
  summary, a per-key table (live / stale / not minted + last mint time), and
  two buttons: *Mint missing sessions* and *Force mint / refresh all*.
  `POST /admin/api/{opencode,zen}/sessions/mint` runs in the background and
  returns immediately; `GET .../sessions` reports per-key state plus
  per-key progress of the running job (the panel polls it every 2s).
- A key is only marked live by an actual CLI mint, and a **failed mint never
  overwrites** an existing live session, so a mis-clicked force refresh can't
  cost a working session.

### Measured (container, real upstream, 4 real zen keys, fresh volume)
- Startup mint: all 4 keys live **~65-78s** after boot (3 in parallel within
  ~35s; the slow key needs the 60s run timeout once). Previously serial.
- `Force mint / refresh all`: 4/4 OK, per-key 7-13s except one 60s.
- Warm restart with an existing session file: no mint work at all, first
  request served immediately (no 403 chain).
- 20/21 IDE regression probe; the single failure is the known-flaky
  `parallel_calls_no_jam` on muse-spark (200 with 0 tool calls), which passes
  on re-run with both tools called — upstream model variance, not the gateway.

### Settled trade-offs (do not re-propose)
- **`ZEN_HARVEST_CONCURRENCY` defaults to 3, not "as many as keys".** The CLI
  is a Bun runtime; the limit exists to protect container memory, not to cap
  throughput. Raise it via env when the box has headroom.
- **Background job + polling, not a synchronous endpoint.** A force refresh of
  11 keys is ~1 minute when healthy and up to ~10 in a pathological case;
  holding an admin HTTP request open that long invites reverse-proxy timeouts.
- **Live-session preference is a two-pass rotation, not a filter.** When no key
  has a live session (the first seconds of a fresh boot) requests must still
  go out — the 403 is then the only signal available, and the harvester is
  already minting.
- **Skip-if-live is the default; force is explicit.** Re-minting a working
  session wastes upstream quota for no benefit, so the non-force button is the
  normal action and only the force path re-mints live keys.
