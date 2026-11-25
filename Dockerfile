# agy2api 镜像：前端（Node + pnpm）→ Go 单二进制（无 CGO，嵌入前端）→ 运行镜像（Alpine，非 root）。
#
#   docker build --build-arg VERSION=$(git describe --tags --always --dirty) -t agy2api .
#
# 多架构（发布时 linux/amd64、linux/arm64）：前端和 Go 两段在构建机的架构上跑（$BUILDPLATFORM），Go 交叉编译到目标架构，
# 只有最后的运行镜像按目标架构生成，不用在模拟器里编译。
#
# 数据（SQLite 库和加密密钥）在 /data，挂卷持久化，密钥要和库一起备份。部署说明见 docs/deploy.md。

FROM --platform=$BUILDPLATFORM node:26-alpine AS web
RUN npm install -g pnpm@12.21.0
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
# 产物输出到 ../internal/server/webdist（见 vite.config.ts），下一阶段嵌进二进制。
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY ddl ./ddl
COPY internal ./internal
COPY --from=web /src/internal/server/webdist ./internal/server/webdist
# 构建上下文不带 .git（见 .dockerignore），版本号从参数传入，同 scripts/build.sh 的注入方式。
ARG VERSION=dev
# TARGETOS、TARGETARCH 由 BuildKit 按目标平台自动填，单架构本地构建时就是本机。
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-X main.version=${VERSION}" -o /out/agy2api .

FROM alpine:3.22
# ca-certificates：连上游和代理用 TLS；tzdata：设 TZ 后日志按本地时间；wget（busybox 自带）给健康检查用。
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 agy2api \
    && mkdir /data && chown agy2api:agy2api /data
COPY --from=build /out/agy2api /usr/local/bin/agy2api
USER agy2api
ENV AGY2API_ADDR=:8080 \
    AGY2API_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["agy2api"]
