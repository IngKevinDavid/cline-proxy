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
# 官方安装方式：npm i -g opencode-ai（postinstall 按平台/musl/AVX2 自动选二进制）。
# amd64 确认可用；arm64 由官方 postinstall 自行处理（不支持则该架构无收割机，
# 网关照常运行，见 harvestEnabled 降级）。
FROM node:22-alpine AS opencode-cli
RUN npm i -g opencode-ai@1.18.31 --no-audit --no-fund \
 && opencode --version \
 && cp /usr/local/bin/opencode /tmp/opencode-bin \
 && chmod +x /tmp/opencode-bin

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata libstdc++ libgcc \
    && addgroup -S app && adduser -S app -G app

WORKDIR /app
COPY --from=builder /build/cline-proxy .
COPY --from=opencode-cli /tmp/opencode-bin /app/bin/opencode

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
