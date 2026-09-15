# Cline Go Proxy

Cline API 的反向代理服务，支持多账号轮询、OpenAI 和 Anthropic Messages API 双协议、API Key 鉴权，内置中文管理后台。集成 **opencode opencode 免费模型** 统一网关：一个二进制同时服务 Cline 账号池与 zen free 模型，按 model 自动路由。

## 功能

- **双上游统一网关**：按 model 自动路由 — Cline 账号池（`deepseek/deepseek-v4-flash` 等）与 opencode opencode 免费模型（`deepseek-v4-flash-free`、`nemotron-3-ultra-free` 等，匿名 `public` key）
- **双协议兼容**：同时支持 `/v1/chat/completions`（OpenAI）、`/v1/messages`（Anthropic Messages API）、`/v1/responses`（OpenAI Responses API，Cursor 等客户端直连）
- **zen 模型动态同步**：每 10 分钟自动拉取 `https://opencode.ai/zen/v1/models`，新免费模型自动接入；付费模型显式 400 拒绝
- **官方摘要压缩（opencode 机制移植）**：超限时按官方算法 select 尾部预算 → 锚定摘要模板（Objective/Work State/Next Move/Relevant Files）→ 调 zen 模型生成摘要 → 重组 `[摘要+recent]` 继续会话，增量更新摘要；摘要失败自动降级截断
- **多 IP 轮询出口**：zen 上游支持 http/https/socks5 代理池，round_robin/random/fill 策略，绕过单 IP 匿名额度限制
- **token 统计与日志入库**：每请求 JSONL 落盘（`zen-stats.jsonl`），今日/累计聚合、按模型分布，管理后台实时展示
- **多账号轮询**：自动在多个 Cline 账号间切换负载（支持 `round_robin` / `fill` / `random` 策略）
- **中文管理后台**：浏览器访问 `/admin/` 管理账号、API Key、模型配置、请求头、代理设置；`/admin/` 的「opencode 免费模型」页统一管理 zen 上游、代理池、压缩参数、模型与统计（原独立页已合并）
- **API Key 鉴权**：保护代理端点，支持生成/删除多个 API Key
- **System Prompt 覆盖**：项目目录下放 `override.md` 则自动替换系统提示词，不存在则使用客户端自带
- **账号导入**：支持 OAuth 浏览器登录、手动 Token 输入、批量文件导入
- **持久化存储**：账号和 Key 保存在 `.cline-accounts.json`，zen 配置保存在 `.zen-config.json`
- **账号冷却与自动恢复**：命中 429 `INFERENCE_CAP_ERROR` 时自动解析 "Try again in 17h 59m" 并标记冷却，冷却到期自动恢复
- **本地调用统计**：账号列表明确显示「本地今日/累计调用」
- **多平台 CI/CD**：GitHub Actions 自动构建 6 平台二进制

## 快速开始

### 一键启动（推荐）

```bash
./start.sh          # 默认端口 3457，前台运行
./start.sh 8080     # 指定端口
```

脚本自动选择运行方式：本机有 Go 则直接编译运行；没有 Go 时回退到 Docker（构建镜像并前台运行容器，自动挂载 `data/` 与 `override.md`）。Ctrl+C 即停止，日志同时写入 `data/start.log`。端口被占用或参数非法时会直接报错退出，不会关闭其他进程。

启动后本机访问 http://127.0.0.1:3457/admin/；局域网设备访问 http://<本机局域网IP>:3457/admin/。

监听所有网卡会开放管理后台给同网设备，建议仅在可信局域网使用，并在系统防火墙中限制 3457 端口。

### 直接运行

```bash
# 编译并启动（默认监听所有网卡，局域网可访问）
go build -o cline-proxy.exe .
./cline-proxy.exe

# 局域网访问地址：http://<本机局域网IP>:3457/admin/
# 仅允许本机访问时：
./cline-proxy.exe -host 127.0.0.1

# 指定端口
./cline-proxy.exe -port 3457

# 构建 + 启动 + 打开浏览器
go run . -start
```

### Docker 部署

```bash
# 构建并启动
docker compose up -d

# 查看日志
docker compose logs -f

# 停止
docker compose down
```

数据持久化在 `./data/` 目录下，`override.md` 会自动从项目根目录挂载到容器内。

## 使用指南

### 1. 添加 Cline 账号

在管理后台 **账号管理** → **导入账号**，选择以下任一方式：

- **OAuth 浏览器登录**：点击按钮弹出 WorkOS 登录窗口，完成后自动填入
- **手动输入 Token**：输入已有账号的 Access Token
- **批量文件导入**：上传包含账号数据的 JSON 文件

账号列表操作列说明：

- **⚡ 测试**：对账号发起一次真实探测请求。成功则将账号置为活跃（清除冷却/过期状态，相当于升级版重置）；失败则按上游返回的等待时长标记冷却并显示预计恢复时间。
- **↻ 重置**：仅重置该账号的「今日调用」，不影响累计调用、状态和 Token。
- **✕ 删除**：从账号池移除该账号。

### 2. 配置客户端

应用（如 Claude Code、Cline）配置为使用此代理：

**OpenAI 格式（/v1/chat/completions）：**
```
Base URL: http://<本机局域网IP>:3457/v1
API Key:  <在管理后台生成的 Key>
Model:    deepseek/deepseek-v4-flash
```

**Anthropic 格式（/v1/messages）：**
```
Base URL: http://<本机局域网IP>:3457/v1
API Key:  <在管理后台生成的 Key>
Model:    deepseek/deepseek-v4-flash
```

### 3. API Key 管理

在后台 **设置** → **API Keys** 中生成和管理。如果未配置任何 Key，代理允许无鉴权访问。

### 4. System Prompt 覆盖

在项目目录下创建 `override.md`，内容将替换所有客户端请求的系统提示词。删除该文件则使用客户端自带的提示词。

### 5. 请求头配置

后台 **设置** → **请求头** 可编辑转发给上游的自定义请求头（如 `x-client-type: cline-cli`）。

## 可用模型（实测）

### 消耗账户额度

| 模型 ID | 状态 | 说明 |
|---------|:----:|------|
| `deepseek/deepseek-v4-pro` | ✅ 可用 · 消耗额度 | DeepSeek V4 Pro |
| `openai/gpt-4.1-nano` | ✅ 可用 · 消耗额度 | GPT-4.1 Nano |
| `qwen/qwen3-235b-a22b` | ✅ 可用 · 消耗额度 | Qwen3 235B |
| `meta-llama/llama-4-maverick` | ✅ 可用 · 消耗额度 | Llama 4 Maverick |
| `google/gemini-2.5-flash` | ⚠️ 响应为空 · 消耗额度 | API 返回 200 但内容为空 |
| `google/gemini-2.5-pro` | ⚠️ 响应为空 · 消耗额度 | API 返回 200 但内容为空 |

### 官方免费模型

| 模型 ID | 状态 | 说明 |
|---------|:----:|------|
| `deepseek/deepseek-v4-flash` | ✅ 可用 · 不消耗额度 | DeepSeek V4 Flash |
| `poolside/laguna-s-2.1:free` | ✅ 可用 · 不消耗额度 | Poolside Laguna S 2.1 |
| `stepfun/step-3.7-flash` | ✅ 可用 · 不消耗额度 | StepFun 3.7 Flash |

### 需要订阅

| 模型 ID | 状态 | 说明 |
|---------|:----:|------|
| `cline-pass/glm-5.2` | ❌ 403 · 需要订阅 | 需要 `cline-pass` 订阅 |
| `cline-pass/deepseek-v4-flash` | ❌ 403 · 需要订阅 | 需要 `cline-pass` 订阅 |
| `cline-pass/qwen3.7-max` | ❌ 403 · 需要订阅 | 需要 `cline-pass` 订阅 |

可在后台 **设置** → **默认模型** 中修改默认模型。

## CI/CD

Release 版本号以 `v` 开头，从 `v0.0.1` 开始按语义版本递增：首次发布为 `v0.0.1`，后续推送自动发布 `v0.0.2`、`v0.0.3` 等版本。

### 6. opencode 免费模型（统一网关）

无需配置，`cline-proxy.exe` 启动即启用 zen 上游（匿名 key `public`）：

**OpenAI 格式（/v1/chat/completions）：**
```
Base URL: http://<本机局域网IP>:3457/v1
API Key:  <在管理后台生成的 Key>
Model:    deepseek-v4-flash-free   # 或别名 deepseek-v4-flash
```

**Anthropic 格式（/v1/messages）：**
```
Model:    deepseek-v4-flash-free
```

**Responses API（/v1/responses，Cursor 等）：**
```
Model:    deepseek-v4-flash-free
```

可用 opencode 免费模型（`GET /v1/models` 实时列出）：

| 模型 ID | 上下文 | 说明 |
|---------|:----:|------|
| `deepseek-v4-flash-free` | 200K | 别名 `deepseek-v4-flash` |
| `nemotron-3-ultra-free` | 1M | 免费里最大上下文 |
| `north-mini-code-free` | 256K | |
| `mimo-v2.5-free` / `ling-3.0-flash-free` / `laguna-s-2.1-free` / `longcat-2.0-free` / `big-pickle` | 200K | |

超限时自动触发官方摘要压缩（`/admin/` → opencode 免费模型 页可调参数）；付费 zen 模型（如 `glm-5.1`）返回 400 拒绝。

## 7. 项目结构

```
├── main.go               入口与 CLI 参数处理
├── proxy.go              HTTP 服务、API 路由与协议转换
├── models.go             官方免费模型同步（Cline）
├── zen.go                opencode 免费模型上游、三态路由、配置持久化
├── compact.go            opencode 官方摘要压缩机制移植
├── proxy_pool.go         zen 上游多 IP 轮询出口（HTTP/SOCKS5 代理池）
├── stats.go              token 统计与 JSONL 日志入库
├── responses.go          /v1/responses（OpenAI Responses API）转换
├── admin.go              管理后台 REST API
├── admin_zen.go          zen 管理页面与 API
├── admin_html.go         管理后台页面
├── auth.go               WorkOS OAuth 登录与 Token 刷新
├── pool.go               账号池管理与持久化
├── types.go              数据结构定义
├── capture.go            OAuth 信息捕获工具
├── http.go               HTTP 客户端与工具函数
├── Dockerfile            Docker 构建配置
├── docker-compose.yml    Docker Compose 配置
├── go.mod                Go 模块定义
├── .cline-accounts.json  账号池数据
├── .zen-config.json      zen 上游配置（代理池、压缩参数）
├── override.md           可选的系统提示词覆盖文件
└── README.md             项目说明
```

---

## 公网容器部署（Docker）

面向公网长期运行：`/v1` 是唯一对外服务面（给 Cursor / ZCode / OpenClaw 等客户端），管理面板只用于配置和维护。

### 1. 必填环境变量

监听非回环地址时以下两项缺一不可，否则进程拒绝启动（fail closed）：

| 变量 | 说明 |
|---|---|
| `API_KEY` | `/v1/*` 的唯一有效 key（`Authorization: Bearer` 或 `x-api-key`）。设置后管理面板生成的动态 key 全部失效 |
| `ADMIN_PASSWORD` | 管理面板登录密码；也可用 `ADMIN_PASSWORD_FILE` 指向 secrets 文件 |

在 `.env` 文件中提供（compose 会读取）：

```
API_KEY=你的固定key
ADMIN_PASSWORD=你的管理密码
ZEN_KEYS=key1,key2,key3        # 可选：opencode zen 多 key，逗号分隔
POOL_STRATEGY=round_robin      # 默认值，可不填
LOG_REQUESTS=true              # 默认值，可不填
```

启动：

```bash
docker compose up -d --build
curl http://127.0.0.1:3457/health
```

### 2. 全部环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `PORT` | `3457` | 监听端口（显式 `-port` flag 优先于环境变量） |
| `DATA_DIR` | 容器内 `/app/data` | 全部状态文件的目录 |
| `API_KEY` | 空 | `/v1` 固定 key；为空时沿用管理面板动态 key（本地模式） |
| `ADMIN_PASSWORD` / `ADMIN_PASSWORD_FILE` | 空 | 管理面板密码；设置后 `/admin/*` 全部需要登录 |
| `REQUIRE_ADMIN_AUTH` | `true` | `false` 豁免公网无密码运行检查（如反代已做认证） |
| `POOL_STRATEGY` | `round_robin` | cline 账号池策略（`round_robin`/`fill`/`random`），环境变量覆盖面板配置 |
| `LOG_REQUESTS` | `true` | 请求日志开关（仅元数据：IP/路径/模型/状态/耗时，不含对话内容）；`false` 完全关闭 |
| `LOG_FILE_MAX_MB` | `10` | `requests.jsonl` 大小上限，超出清空 |
| `APPLY_SYSTEM_PROMPT_OVERRIDE` | `false` | `true` 才启用 `override.md` 系统提示词替换（编码 IDE / Agent 默认保留自己的提示词） |
| `ZEN_KEYS` | 空 | opencode zen 多 key（逗号分隔）；面板已有 key 配置时不覆盖 |
| `CLINE_ACCOUNTS_SEED_FILE` | 空 | 账号种子 JSON，池为空时启动自动导入。两种条目：OAuth `[{"refreshToken":"...","email":"..."}]` 或静态 API key `[{"apiToken":"sk_...","email":"..."}]` |
| `CLINE_USE_PROXIES` | `false` | `true` 时 cline 上游走出口代理池（zen 上游配置 `proxies` 后默认走池） |
| `MAX_BODY_MB` | `32` | 单请求体上限 MB，超出返回 413 |
| `STREAM_LOG` | `false` | `true` 才把 Anthropic 流式路径的原始 SSE 落盘（完整对话内容，调试用） |
| `CLIENT_IP_HEADER` | 空 | 反代部署时信任的客户端 IP 头（如 `X-Real-IP`）；默认只取 RemoteAddr，不信任转发头 |
| `API_KEY_FILE` | 空 | API key 文件路径（docker secrets），优先于 `API_KEY` |

### 3. 账号与多 key 轮转（round-robin）

- **cline 账号池**：多个账号按 round-robin（默认）轮流承接请求，单账号限流/超额自动冷却并跳过，冷却到期自动恢复，最大化总免费额度。
- **opencode zen 多 key**：管理面板「opencode 免费模型」页可填多个 key（每行一个），或用 `ZEN_KEYS` 环境变量注入；请求按 round-robin 轮转，某 key 触发 429/限流时立即冷却并切换下一个 key 重试。

### 3.5 出口代理池（应对按 IP 限流）

在 zen 配置的 `proxies` 列表（管理面板「opencode 免费模型 → 上游配置」）填入 http(s)/socks5 代理（每行一个，支持 `socks5://user:pass@host:port`）：

- 上游请求按 round-robin **逐请求轮转**出口（每次上游尝试显式选一个出口，冷却中的出口自动跳过）。
- 某出口触发限流（429 等）时按 `Retry-After`（默认 10 分钟）冷却该出口，请求自动换下一个出口重试。
- zen 上游配置了 `proxies` 即默认启用；cline 上游通过 `CLINE_USE_PROXIES=true` 全局启用，或在「Combos」创建别名时勾选「走代理池」按别名启用。
- 拨号失败的出口冷却 5 分钟并自动换下一个重试；代理故障不会污染账号/key 的冷却状态。

### 4. Combos（别名模型）

管理面板「Combos」页可创建自定义别名模型（如 `cline-glm-5.3`）：客户端请求该别名，代理自动改写为所选平台的目标模型。严格同平台：cline combo 只能选 cline 模型，zen combo 只能选 zen 免费模型。可选勾选「走代理池」让该别名的上游调用走出口代理池。别名会出现在 `/v1/models` 列表中，Cursor / ZCode / OpenClaw 可直接选用。

### 5. 管理面板认证

设置 `ADMIN_PASSWORD` 后访问 `/admin/` 出现登录页；登录后以 HttpOnly Cookie 保持会话（7 天）。脚本调用可用登录响应中的 token：`Authorization: Bearer <token>`。登录限流：每 IP 每分钟 5 次失败上限。

### 6. TLS 与备份

- **TLS**：compose 内置可选 Caddy profile（自动 HTTPS）。取消 `docker-compose.yml` 中 caddy 段的注释，`.env` 加 `DOMAIN=你的域名`，然后 `docker compose --profile tls up -d`。Caddyfile 示例：

  ```
  你的域名 {
      reverse_proxy cline-proxy:3457
  }
  ```

- **备份/恢复**：全部状态在 `data/` 卷内（`.cline-accounts.json`、`.zen-config.json`、`combos.json`、`requests.jsonl`）。迁移 = 拷贝目录；或用管理面板导出账号 JSON。

---

感谢 [LINUX DO](https://linux.do) 社区
