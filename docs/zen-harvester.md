# Zen session harvester（容器内自维持 live 会话）

## 为什么需要它

zen 免费层按服务端会话绑定：只有服务端"见过"的 `sess_` ID 才能通过
FreeTier 检查。本地随机生成的 `sess_` 必 403。网关自身无法凭空造出有效
会话——唯一能 mint 新会话的是官方 opencode CLI（`opencode run` 会在服务端
注册新 session）。

收割机 = 镜像内嵌官方 CLI（二进制 `/app/bin/opencode`），网关按需调用它
跑一条极小请求，从其日志里读出刚 mint 的 `sess_`，存入 sticky 会话表。
容器 24/7 在线，会话自维持，不依赖任何个人电脑。

## 触发时机

1. **启动时**：key 无 live 会话 → 收割一个（避免首请求必 403）。多 key **并行**
   收割（`ZEN_HARVEST_CONCURRENCY`，默认 3），4 个 key 约 1 分钟、11 个 key 约
   2-4 分钟完成；启动日志会打印 `minting N key(s) at startup (concurrency N)`。
2. **运行时**：某 key 连续 FreeTier 403 ≥ 2 次 → 后台收割新会话替换
   （同 key 10 分钟内最多一次）；
3. **定时**：每 10 分钟检查，最久未收割超过 `ZEN_HARVEST_INTERVAL_HOURS`
   （默认 **4h**，见下方"为什么是 4h"）的 key 补一个；
4. **手动**：管理面板「opencode free models → Live session IDs」→
   **Mint missing sessions**（只补未 mint 的）/ **Force mint / refresh all**
   （全部重 mint），后台执行并显示每个 key 的进度与耗时。

**首启为什么慢**：未 mint 的 key 用本地随机 `sess_`，上游必 403，因此请求路径
会优先挑有 live 会话的 key（`pickZenKey` 两轮：先"非冷却 + live"，再"非冷却"）。
首启窗口内一个 live key 都没有时，请求仍会发出并按 403 语义返回——那是收割机
还没 mint 完，不是故障。

## 部署：CLI 认证

收割机用容器内 CLI 的 `auth.json`（与网关 `DATA_DIR` 分开，互不干扰）：

```
$ZEN_HARVEST_HOME/.local/share/opencode/auth.json   （默认 HOME=/app/.opencode-home）
```

**认证由收割机自给自足，且每个 key 用独立 HOME**：收割时把当前 key 以单 key
形态 `{"opencode":{"type":"api","key":"sk-..."}}` 写进
`$ZEN_HARVEST_HOME/keys/<key 哈希前 12 位>/.local/share/opencode/auth.json`，
跑完 `opencode run` 后删除（同 key 重复收割会恢复上一次内容）。因此多 key
部署无需手动准备认证——ZEN_KEYS 里配好的 zen key 即可直接收割。

per-key HOME 有两个作用：**(a) 可并行**（不同 key 无共享 auth.json/日志，
并发上限见 `ZEN_HARVEST_CONCURRENCY`）；**(b) 绝不触碰公共 HOME**——管理员
手工 `opencode auth login` 写进 `$ZEN_HARVEST_HOME` 的真实凭据不会被收割
覆盖（早期实现共用一个 HOME，每次收割覆盖再恢复，恢复失败就永久丢失）。

仅当**还需要 CLI 的其他功能**（人工 `opencode` 登录、非 zen 用法）时才需
手动写入初始认证，二选一：

- **A. 从本机复制**（已有 `opencode` 登录的电脑）：
  ```
  docker cp ~/.local/share/opencode/auth.json <container>:/app/.opencode-home/.local/share/opencode/auth.json
  ```
- **B. 在容器内登录**：`docker exec -it <container> /app/bin/opencode auth login`
 （需 CLI 支持的登录方式，成功后文件自动落到上述路径）。

注意 `docker-compose.yml` 默认只挂载 `./data:/app/data`，`/app/.opencode-home`
不在 volume 里——容器重建后手动写入的认证会丢失（自动收割写入的每次覆盖
恢复，无持久化需求）。如需持久化可给 `ZEN_HARVEST_HOME` 加 volume。

## 收割模型

`harvestSession` 首选 `opencode/big-pickle`（zen 免费层默认模型别名），失败
则依次尝试至多 2 个动态获取的价格 0 模型（`opencode models` 在线列表 ∩
公共目录 cost 0/0 且非 deprecated，列表缓存 1h）——big-pickle 未来下架后
自动落到其余免费模型，收割不中断。模型只用于在服务端 mint 会话，回答
内容不关心。

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `ZEN_HARVEST` | 开启 | `0` 关闭收割机（纯网关模式） |
| `ZEN_HARVEST_BIN` | `/app/bin/opencode` | CLI 二进制路径 |
| `ZEN_HARVEST_HOME` | `/app/.opencode-home` | 容器内 CLI 的 HOME |
| `ZEN_HARVEST_INTERVAL_HOURS` | `4` | 定时补收割间隔（最小 1h）。**必须小于 zen 的 5h 额度窗口**，否则每轮都有一段时间全池会话已过期 |
| `ZEN_HARVEST_CONCURRENCY` | `3` | 并行收割上限（1..8）。CLI 是 Bun 进程，调高会吃内存 |
| `ZEN_HARVEST_KEY_TIMEOUT_SECONDS` | `150` | 单个 key 的收割总预算（最小 30s）。失败路径最多 3 模型 × 3 次尝试，不封顶会占住 worker 9 分钟 |

## 降级语义

- CLI 二进制不存在（未 pin 平台包的架构，如 arm/v7）→ 自动降级为纯网关
  模式（sticky 会话 + 403 收割路径不可用，请求按原有 403/轮换语义返回），
  静默降级，不影响正常代理。
- 收割失败 → 保留旧会话，请求按原有 403/轮换语义返回，不阻塞。
- `ZEN_HARVEST=0` → 行为与收割机不存在完全一致（面板按钮会提示
  harvester unavailable，不会静默失败）。
- mint 失败**不会覆盖**该 key 既有的 live 会话（旧会话继续可用）。

## 架构说明

- amd64 与 arm64 都带 CLI，但安装路径不同（见 Dockerfile 注释）：
  amd64 在 amd64 runner 上走官方 `npm i -g opencode-ai`（同架构，postinstall
  的 `opencode --version` 验证可执行）；arm64 是跨架构构建，postinstall 那段
  验证会在 QEMU 下 SIGILL 并让整条构建失败，因此改为取同一个官方 npm tarball
  解包，用 ELF 头校验架构、不执行二进制。两种产物相同，真机运行时均原生执行。
- CLI 二进制约 +185MB（node:22-alpine 构建阶段，不进最终层；
  最终镜像只多一个静态二进制 + libstdc++）。

## 额度窗口与会话寿命（为什么是 4h）

zen 免费层按**出口 IP** 记账（实测约 200 请求 / 5 小时 / IP）。两点推论要说清楚：

- **重新 mint 会话不会重置额度。** 额度记在 IP 上，会话只是"来自 OpenCode"的
  凭证。想提高吞吐只能加出口（多个 socks5 IP 各自一份额度），不能靠换会话。
- **反过来，mint 本身要花额度。** 收割 run 直连公网（不走代理池），出口就是
  容器自己的 IP，与请求共用同一个 200/5h 桶（若该 IP 同时对外提供 socks5
  出口，则和池子里的请求抢同一个桶）。所以 mint 频率不能调太高。

会话寿命的上界疑似就是同一个 5h 窗口（服务端只认得"窗口内见过"的会话）。因此
刷新间隔必须**短于**窗口，默认取 4h：

- 6h（旧默认）> 5h 窗口 → 每轮都会出现"全池会话已过期"的时段，请求开始 403，
  只能靠 `harvestOnForbidden` 逐个补救（每 key 需 2 次连续 403，且同 key 10 分钟
  冷却），那段时间失败率和首字节延迟都会抬起来。
- 4h 在过期前换新，死窗口不出现。11 个 key × 每 4h 一轮 ≈ 66 次 mint/天，
  相对该 IP 约 960/天 的桶是零头。
- 检查 tick 是 **10 分钟**（不是 1 小时）：间隔到与真正执行之间差一个 tick，
  4h 目标配 1h ticker 实际会落在 4h-5h，正好顶到窗口边缘。
- 1h 之类则要 264 次 mint/天，开始真的抢额度，得不偿失。

**想确认会话到底能活多久**：403 日志现在带上被拒会话的年龄
（`[minted 5h12m ago]` / `[placeholder (never minted, always 403)]`）。
占位会话 403 是预期内的；出现 `minted ... ago` 的 403 就说明会话确实到期，
那一刻的年龄就是实测寿命——据此再决定要不要把间隔调得更紧。

## 运维

- 面板「opencode free models」页顶部显示 `N/M live`，表格列出每个 key 的
  会话状态（live / stale / not minted）与最近收割时间。
- 排障只看一处：`/admin/api/opencode/sessions`（`harvestEnabled`、`liveCount`、
  每个 key 的 `live/minted/harvested`、最近一次 mint 任务的逐 key 结果）。
- 大量 key 时把 `ZEN_HARVEST_CONCURRENCY` 调到 5-8 可显著缩短首启；只受
  容器内存限制。
