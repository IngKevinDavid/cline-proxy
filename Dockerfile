FROM golang:1.26-alpine AS builder

# BuildKit supplies these on multi-arch builds: cross-compile the Go binary
# natively on the builder arch instead of running the whole compile under QEMU.
ARG TARGETOS
ARG TARGETARCH

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o cline-proxy .

# ---- opencode CLI（zen 会话收割机用，见 internal/app/zen_harvest.go）----
# 官方安装方式：npm i -g opencode-ai —— postinstall 按平台/musl/AVX2 自动选二进制，
# 但它在结尾会执行 `opencode --version` 验证，验证不过就判定安装失败。
# 跨架构构建时这条验证必然失败：GitHub runner 的 QEMU 跑 Bun 二进制直接
# SIGILL（实测 exit 132 "Illegal instruction"），arm64 整条构建因此挂掉。
# （本机 Docker Desktop 的 QEMU 恰好能跑，所以本地先通过、CI 才暴露——是仿真
# 环境差异，不是二进制或代码问题。）
# 因此分两条路，产物完全相同：
#   * 同架构（amd64 镜像在 amd64 runner 上）：照旧走 npm，版本校验有效；
#   * 跨架构（arm64）：取同一个官方发布的 npm tarball，只解包不执行，并用
#     ELF 头 e_machine 代替跑不起来的 `--version` 做架构校验（校验失败即构建
#     失败，不会静默塞进错架构的二进制）。
# 注：存在未 pin 的平台（如 arm/v7）时该架构不带 CLI，网关自身照常运行，
# harvestEnabled() 会自动降级（日志可见）。
FROM node:22-alpine AS opencode-cli
ARG TARGETARCH
ARG BUILDARCH
ARG OPENCODE_VERSION=1.18.31
RUN set -eux; \
    mkdir -p /tmp/oc-bin; \
    if [ "$TARGETARCH" = "$BUILDARCH" ]; then \
      npm i -g "opencode-ai@${OPENCODE_VERSION}" --no-audit --no-fund; \
      opencode --version; \
      cp /usr/local/bin/opencode /tmp/oc-bin/opencode; \
    else \
      case "$TARGETARCH" in \
        arm64) pkg="opencode-linux-arm64-musl"; elf="b700" ;; \
        *) pkg=""; elf="" ;; \
      esac; \
      if [ -n "$pkg" ]; then \
        wget -qO /tmp/oc.tgz "https://registry.npmjs.org/${pkg}/-/${pkg}-${OPENCODE_VERSION}.tgz"; \
        tar -xzf /tmp/oc.tgz -C /tmp; \
        cp /tmp/package/bin/opencode /tmp/oc-bin/opencode; \
        test "$(od -An -tx1 -j18 -N2 /tmp/oc-bin/opencode | tr -d ' \n')" = "$elf"; \
        rm -rf /tmp/package /tmp/oc.tgz; \
      else \
        echo "no pinned opencode CLI artifact for $TARGETARCH - harvester disabled on this arch"; \
      fi; \
    fi; \
    if [ -f /tmp/oc-bin/opencode ]; then chmod +x /tmp/oc-bin/opencode; fi; \
    ls -la /tmp/oc-bin/

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata libstdc++ libgcc \
    && addgroup -S app && adduser -S app -G app

WORKDIR /app
COPY --from=builder /build/cline-proxy .
# 目录拷贝：无 CLI 的架构上空目录，COPY 依旧成功（缺文件才失败）
COPY --from=opencode-cli /tmp/oc-bin/ /app/bin/

RUN mkdir -p /app/data /app/.opencode-home && chown -R app:app /app

USER app

EXPOSE 3457

VOLUME ["/app/data"]

# 容器内数据目录固定为 /app/data（可用 DATA_DIR 覆盖）
ENV DATA_DIR=/app/data
ENV PORT=3457

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:${PORT}/health || exit 1

ENTRYPOINT ["/app/cline-proxy"]
CMD []
