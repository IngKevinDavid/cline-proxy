# Feature: OpenCode Free Models Adaptive Routing & Auto-Verification

## Objective
Implement automatic endpoint verification, learning, and routing for OpenCode free models in `cline-proxy-foxy`: probe `inference/openai/v1/chat/completions` first with canonical free-tier fingerprinting; fall back to `inference/openai/v1/responses` if unavailable; memorize verified routes in `data/.zen-endpoints.json`; and automatically probe newly added free models and prune removed models during background catalog synchronization.

## Problem & Context
OpenCode periodically enables and disables free models (`mimo-v2.6-flash-free`, `big-pickle`, `muse-spark-1.3-contributor-free`, `nemotron-3.5-lightning-free`, `ling-3.0-flash-fin-free`).
1. Free models do not all share the same upstream endpoint: standard models serve on `/inference/openai/v1/chat/completions`, whereas others (such as `muse-spark`) return 503 on `chat/completions` and must be routed to `/inference/openai/v1/responses`.
2. OpenCode's free tier rejects calls without the 4 core CLI tools (`bash`, `glob`, `grep`, `read`) with `403 FreeTierError`.
3. Upstream only accepts streaming requests (`stream: true`). Non-streaming calls must be converted and aggregated transparently by the proxy.
4. When new models appear or disappear in `models.opencode.ai`, the proxy automatically discovers, probes, and updates routing tables without manual intervention.

## Scope & Constraints
- Support OpenCode Console credentials (`opencode db` / SQLite `account` & `account_state` tables).
- Maintain backward compatibility with legacy Zen static keys (`oc_sk_...`).
- Persist learned routes in `data/.zen-endpoints.json` across server restarts.
- Auto-probe new free models during catalog sync (startup + 10-minute ticker) and prune removed models.
- Zero external CGo dependencies; strictly Windows-friendly.

## Checklist & Tasks
- [x] `task-1`: Console OAuth credentials extractor and cache manager (`internal/app/console_auth.go`).
- [x] `task-2`: OpenCode free-tier fingerprint injector (4 gate tools, canonical session ID) & stream aggregator for non-streaming clients (`internal/app/zen_fingerprint.go`).
- [x] `task-3`: Dual-endpoint probe engine (test `chat/completions` first, fallback to `responses`) & persistent learning in `data/.zen-endpoints.json` (`internal/app/zen_endpoint.go` & `internal/app/zen_probe.go`).
- [x] `task-4`: Catalog sync hook for dynamic discovery of newly added/removed free models and runtime request dispatcher (`internal/app/zen.go`).
- [x] `task-5`: Build with `install.ps1`, restart daemon, run end-to-end empirical verification of free models through proxy, and record observations in Engram.
- [x] `task-6`: Deep audit and alignment with Dashboard `GET /admin` & multi-account key control (`resolveZenKeyIdentity`, `pickZenKey` round-robin with multi-account format `token#org_id`, live status detection, per-key dashboard "Test" probe with canonical session headers, and automatic 401 failover).

## Verification Evidence
1. Unit Tests: `go test -count=1 ./...` passed 100% cleanly across all packages in 8.525s (including new tests `TestResolveZenKeyIdentity`, `TestZenKeyTestConsoleTokenHeaders`, and `TestZenKeyMultiAccountPoolRotation`).
2. Binary Compilation: `cline-proxy.exe` compiled cleanly (9.42 MB) via `install.ps1 -ForceRebuild`.
3. Auto-Probe & Memorization: `data/.zen-endpoints.json` automatically generated and populated:
   - `big-pickle`: `"chat"`
   - `ling-3.0-flash-fin-free`: `"chat"`
   - `mimo-v2.6-flash-free`: `"chat"`
   - `nemotron-3-ultra-free`: `"chat"`
   - `nemotron-3.5-lightning-free`: `"chat"`
   - `space-bunny-free`: `"chat"`
   - `muse-spark-1.2-contributor-free`: `"responses"`
   - `muse-spark-1.3-contributor-free`: `"responses"`
4. Live Proxy Dispatch (`http://127.0.0.1:3457/v1/chat/completions`):
   - Streaming (`stream: true`): 200 OK on `big-pickle`, `ling-3.0-flash-fin-free`, `nemotron-3.5-lightning-free`, `muse-spark-1.3-contributor-free`, `space-bunny-free`.
   - Non-Streaming (`stream: false`): 200 OK with valid synthesized JSON responses on `big-pickle`, `ling-3.0-flash-fin-free`, `muse-spark-1.3-contributor-free`.
5. Dashboard `GET /admin` & Multi-Account Key Pool Verification:
   - `GET /admin/api/zen/config` renders detected `consoleAuth` (`available: true`, `orgID`, `tokenMask`).
   - `POST /admin/api/opencode/config/update` accepts pool of Console OAuth accounts (`st_...#org_...`) and Zen keys (`oc_sk_...`).
   - Dashboard table renders each account with `keyMask`, `sessionLive: true`, `sessionMinted: true`, `session: "console-oauth"`, and working "Test" button.
   - `POST /admin/api/zen/keys/test` accurately probes Console keys and returns `status: "active"`, `reason: "ok"`, latency, and uncools the key.
   - Automatic 401 failover: invalid/expired keys in pool are cooled for 1 hour while requests transparently rotate to healthy accounts.
