# CODEBUDDY.md

This file provides guidance to CodeBuddy Code when working with code in this repository.

## 项目简介

due 是一个基于 Go 语言开发的高性能分布式游戏服务器框架。根目录是主模块 `github.com/dobyte/due/v2`，模块设计借鉴了 kratos，采用组件化 + 集群化的架构。当前分支对应 v2.5.1，go.mod 要求 `go 1.25.0`。

## 常用命令

- 构建主模块：`go build -v ./...`
- 运行全部测试：`go test -v -cover ./...`
- 运行单个测试：`go test -v -run <TestName> ./<包路径>`，例如 `go test -v -run TestNewCode ./codes`
- 清理所有子模块依赖：`./tidy.sh`（脚本内置了全部子模块清单，逐个执行 `go mod tidy`）

### 重要：多模块仓库

本仓库每个子模块（`network/tcp`、`network/ws`、`registry/consul`、`registry/etcd`、`eventbus/redis`、`cache/redis`、`lock/redis`、`config/etcd`、`transport/grpc` 等约 26 个）都有独立的 `go.mod`，import path 为 `github.com/dobyte/due/<路径>/v2`。**修改子模块代码后，需要在对应子模块目录内单独执行 `go build ./...` / `go test ./...`**，仅构建根模块不会编译子模块。

### 测试依赖外部中间件

部分包的测试会直连本地默认地址的中间件，中间件不可用时测试会失败（不会自动跳过）：

- `registry/consul`（127.0.0.1:8500）、`registry/etcd`（localhost:2379）、`registry/nacos`
- `locate/redis`、`eventbus/redis`（127.0.0.1:6379）、`eventbus/nats`（4222）、`eventbus/kafka`
- 纯逻辑包（`codes`、`core/*`、`mode`、`packet`、`network/tcp|ws|kcp` 等）可独立测试

本地起依赖可运行 `docker compose up -d`（`docker/docker-compose.yaml` 提供 redis、etcd、consul、nacos、mongo、nats、redis-cluster）。

## 架构总览

### 容器与组件生命周期

入口是 `container.go` 的 `Container`，业务代码模式为：

```go
container := due.NewContainer()
container.Add(componentA, componentB) // 添加组件
container.Serve()                     // 启动
```

`Serve()` 依次执行：组件 `Init` → `Start` → 等待系统信号（SIGINT/SIGTERM 等）→ 并发 `Close` → 并发 `Destroy` → 清理全局模块（eventbus、lock、cache、task、config、etc、log）。

所有组件实现 `component.Component` 接口（`Name/Init/Start/Close/Destroy`），`component.Base` 提供空实现，新组件内嵌它即可。

### 集群模型（cluster/ 核心）

框架的核心是集群架构，四种角色：

- **Gate 网关服**（`cluster/gate`）：管理客户端连接（tcp/kcp/ws），接收客户端路由消息并分发到用户所在的 Node。内部通过 `internal/transporter/gate` 暴露 RPC 服务供 Node 回调（如推送、触发事件）。
- **Node 节点服**（`cluster/node`）：承载核心业务逻辑，提供 Router（路由注册/分发）、Trigger（连接事件）、Scheduler（定时任务）、Actor 模型。支持有状态/无状态两种形态。
- **Mesh 微服务**（`cluster/mesh`）：无状态微服务，供 grpc/rpcx 场景使用，能力是 Node 的子集。
- **Client**（`cluster/client`）：调试用的客户端组件，可直连 Gate 验证全流程。

每个集群组件对外暴露 `Proxy` 给业务层（如 `proxy.Router().AddRouteHandler(route, stateful, handler)`、`proxy.AddHookListener(cluster.Start, ...)`）。集群常量（Kind/State/Event/Hook/消息参数结构体）统一定义在 `cluster/cluster.go`。

### 集群节点间协作

- **registry**（`registry/`）：服务注册发现。`ServiceInstance` 携带 ID/Name/Kind/State/Routes/Endpoint/Weight 等，Gate/Node/Mesh 启动时注册、关闭时注销，通过 State（shut/work/busy/hang）实现优雅上下线与滚动更新。实现有 consul/etcd/nacos。
- **locate**（`locate/`）：用户定位器，将 UID 映射到所在 Node 实例，Node 之间转发消息时先定位再投递。目前只有 redis 实现。
- **internal/transporter** 与 **internal/link**：节点间通信的实现细节（gate↔node 的 RPC、消息投递链路），包在 `internal/` 下，受 Go internal 机制保护，外部模块无法导入。

### 关键基础模块

- `etc`：启动配置（非业务配置）。默认从 `./etc` 目录读取，路径可由环境变量 `DUE_ETC` 或 `-etc` 参数覆盖；只读、不支持热更新。配置项示例见 `gate/etc/etc.toml`（cluster.gate、locate.redis、registry.consul、network.ws.server、packet、log 等段落）。
- `config`：配置中心，供业务动态配置，支持热更新；实现有 file/etcd/consul/nacos。
- `network`：网络抽象层（`network.Server`/`network.Conn`/`network.Client` 接口），实现有 tcp/ws/kcp，每个都同时提供 server 与 client。
- `packet`：消息打包器，定义了线上 wire protocol（见 README 第 6 节）：`size(4B) + header(1B，含心跳位) + route(默认2B) + seq(默认2B) + message`；route/seq 字节数可通过 `[packet]` 配置调整。业务消息用 `cluster.Message{Route, Data, Seq}` 描述。
- `session`：会话管理，维护连接会话（CID）、用户会话（UID）以及频道订阅/发布，Gate 用它管理在线玩家。
- `eventbus`：事件总线抽象，实现有 redis/nats/kafka，用于跨节点事件通知。
- `transport`：Node 对外提供的微服务 RPC 通信方案，实现有 grpc/rpcx。
- `codes`：统一业务错误码（gRPC 风格，OK=0 ~ TooManyRequests=10），业务错误码用 `codes.NewCode(...)` 创建。
- `mode`：运行模式（debug/test/release），优先级：配置文件 < 环境变量 < 运行参数 < `mode.SetMode()`。
- `core/`：底层基础库（buffer、pool、endpoint、limiter、stat、chains、tls、value 等），不依赖业务。
- `utils/x*`：通用工具包（xcall 协程组、xconv 转换、xtime 时间、xuuid、xos 等），各子模块可复用。

## 开发约定

- 代码注释与提交说明为中文，README 为中文。
- 时间获取统一用 `utils/xtime.Now()`（受 etc 的 timezone 配置控制），不要直接用 `time.Now()`。
- 业务错误统一走 `codes` 包的错误码，`ctx.Response` / 接口返回用 `codes.OK` 等。
- 依赖注入风格：组件通过 `WithXxx` 选项函数（options.go）注入依赖（如 `gate.WithServer`、`gate.WithRegistry`），并遵循 `defaultOptions()` + 选项覆盖的模式。
- 添加新子模块时，需同时更新 `tidy.sh` 中的模块清单。
