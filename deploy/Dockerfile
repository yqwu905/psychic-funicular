# 多阶段构建：一次编译，产出 server / agent 两个镜像目标。
# 用法：
#   docker build -f deploy/Dockerfile --target server -t skipper-server .
#   docker build -f deploy/Dockerfile --target agent  -t skipper-agent .
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# modernc.org/sqlite 为纯 Go，可关闭 CGO 产出静态二进制。
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/ ./cmd/...

FROM gcr.io/distroless/static-debian12 AS server
COPY --from=build /out/skipper-server /usr/local/bin/skipper-server
EXPOSE 7443 8080
ENTRYPOINT ["/usr/local/bin/skipper-server"]

FROM gcr.io/distroless/static-debian12 AS agent
COPY --from=build /out/skipper-agent /usr/local/bin/skipper-agent
ENTRYPOINT ["/usr/local/bin/skipper-agent"]
