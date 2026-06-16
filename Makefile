GOBIN := $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)

.PHONY: all build proto tidy test vet run-server run-agent clean tools

all: build

## tools: 安装代码生成工具链（buf 自带 proto 编译器，免 protoc）
tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	go install github.com/bufbuild/buf/cmd/buf@latest

## proto: 由 .proto 生成 gRPC 代码到 gen/
proto:
	buf generate

## build: 编译三个二进制到 bin/
build:
	go build -o bin/ ./cmd/...

## test: 运行单元测试
test:
	go test ./...

## vet: 静态检查
vet:
	go vet ./...

## tidy: 整理依赖
tidy:
	go mod tidy

## run-server: 用示例配置启动控制平面
run-server: build
	./bin/skipper-server --config deploy/server.yaml

## run-agent: 用示例配置启动节点代理
run-agent: build
	./bin/skipper-agent --config deploy/agent.yaml

## clean: 清理产物与本地库
clean:
	rm -rf bin *.db *.db-shm *.db-wal
