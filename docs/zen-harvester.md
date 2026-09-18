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

1. **启动时**：key 无 sticky 会话 → 收割一个（避免首请求必 403）；
2. **运行时**：某 key 连续 FreeTier 403 ≥ 2 次 → 后台收割新会话替换
   （同 key 10 分钟内最多一次）；
3. **定时**：每小时检查，最久未更新超过 `ZEN_HARVEST_INTERVAL_HOURS`
   （默认 6h）的 key 补一个。

## 部署：CLI 认证

收割机用容器内 CLI 的 `auth.json`（与网关 `DATA_DIR` 分开，互不干扰）：

```
$ZEN_HARVEST_HOME/.local/share/opencode/auth.json   （默认 HOME=/app/.opencode-home）
```

**认证由收割机自给自足**：`harvestSession` 每次收割前把当前 key 以单 key
形态 `{"opencode":{"type":"api","key":"sk-..."}}` 临时写入该文件，跑完
`opencode run` 后恢复原值（串行化 + defer 保证并发安全与失败恢复）。因此
多 key 部署无需手动准备认证——ZEN_KEYS 里配好的 zen key 即可直接收割。

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
| `ZEN_HARVEST_INTERVAL_HOURS` | `6` | 定时补收割间隔（最小 1h） |

## 降级语义

- CLI 二进制不存在（arm64 等 postinstall 不支持的架构）→ 自动降级为纯网关
  模式（sticky 会话 + 403 收割路径不可用，请求按原有 403/轮换语义返回），
  静默降级，不影响正常代理。
- 收割失败 → 保留旧会话，请求按原有 403/轮换语义返回，不阻塞。
- `ZEN_HARVEST=0` → 行为与收割机不存在完全一致。

## 架构说明

- 仅 amd64 在 CI 中验证（`npm i -g opencode-ai` 官方安装方式）；
  arm64 由官方 postinstall 自行处理，失败即降级。
- CLI 二进制约 +30MB 镜像体积（node:22-alpine 构建阶段，不进最终层；
  最终镜像只多一个静态二进制 + libstdc++）。
