# ============================================================
# DockOrae-Agent 运行镜像
# 宿主机控制平面:privileged + pid:host + nsenter 操作真实宿主
# 需要挂载:docker.sock(容器内 docker 操作)、/run/dockorae(socket+token 共享)
# ============================================================
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=unknown
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
RUN apk add --no-cache git ca-certificates
WORKDIR /app
RUN go env -w GOPROXY=https://goproxy.cn,direct
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X github.com/DockOrae/DockOrae-Agent/cmd.Version=${VERSION} -X github.com/DockOrae/DockOrae-Agent/cmd.Commit=${COMMIT} -X github.com/DockOrae/DockOrae-Agent/cmd.BuildTime=${BUILD_TIME} -X github.com/DockOrae/DockOrae-Agent/internal/binary.Version=${VERSION}" \
    -o dockorae-agent .

# 运行镜像:alpine + util-linux(nsenter)+ docker CLI(宿主 compose 操作经 nsenter)
FROM alpine:3.20
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=unknown
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
LABEL org.opencontainers.image.title="DockOrae-Agent"
LABEL org.opencontainers.image.source="https://github.com/DockOrae/DockOrae-Agent"
LABEL org.opencontainers.image.version=${VERSION}
LABEL org.opencontainers.image.revision=${COMMIT}
LABEL org.opencontainers.image.created=${BUILD_TIME}
# nsenter(util-linux)+ docker CLI + 常用工具
RUN apk add --no-cache ca-certificates tini util-linux docker-cli curl iproute2 lsblk bash \
    && mkdir -p /run/dockorae /var/lib/dockorae-agent /var/log/dockorae

COPY --from=build /app/dockorae-agent /usr/local/bin/dockorae-agent

VOLUME ["/var/lib/dockorae-agent"]
ENTRYPOINT ["/sbin/tini", "--"]
CMD ["dockorae-agent"]
