# mcp-client

基于 due 框架 `cluster/client` 的 **MCP 服务端**，把游戏客户端能力暴露成 MCP 工具，供 CodeBuddy 等 MCP 客户端驱动多个游戏客户端登录、发送协议，用于验证服务器逻辑。

- 支持**多个客户端同时在线**（每个 `connect_client` 建立一个独立连接）
- 支持登录、发送任意协议（`send_protocol` / `request_protocol`）、接收服务端推送（`wait_message` / `poll_messages`）
- 内置常用协议工具（快速匹配、邀请房、观战、战绩等），无需手拼路由号和请求体
- 日志写入 stderr，stdout 只走 MCP JSON-RPC，不会污染 stdio 协议

## 目录结构

```
mcp-client/
├── main.go                      入口：日志重定向 + 消息打包配置 + MCP stdio 服务
├── etc/etc.toml                 ws 拨号地址、packet 配置等（供参考，关键配置在 main.go 已写死）
└── internal/
    ├── gameclient/              due client 组件封装：多连接管理、消息队列、请求-响应
    ├── jwttoken/                JWT 生成（与 node 服务器 auth 一致的 HS256）
    └── mcpserver/               MCP 工具定义
```

## 构建与运行

```bash
cd mcp-client
go build -o mcp-client .
# 或直接
go run .
```

启动后以 **stdio** 方式提供 MCP 服务，默认连接 `ws://127.0.0.1:3553`（gate），可被任何 MCP 客户端拉起。

> 前置依赖：gate/node 已启动，且 redis（locate）、consul（registry）可用。参见根目录 `docker/docker-compose.yaml`。

## 在 CodeBuddy 中配置

项目根目录（或用户全局）的 `.mcp.json`：

```json
{
  "mcpServers": {
    "due-client": {
      "command": "go",
      "args": ["run", "main.go"],
      "cwd": "/home/miku/docker/due/due/mcp-client"
    }
  }
}
```

## 工具一览

| 工具 | 说明 |
|------|------|
| `server_info` | 查看路由号对照表 |
| `generate_token` | 为指定 uid 生成登录令牌（JWT） |
| `connect_client` | 建立新的游戏客户端连接（支持多开） |
| `list_clients` | 查看所有在线客户端 |
| `disconnect_client` | 断开指定客户端 |
| `login` | 登录（路由 1001） |
| `send_protocol` | 发送协议，不等响应 |
| `request_protocol` | 发送协议并等待响应 |
| `wait_message` | 等待下一条消息（服务端推送，如 2003/2006/2010/2012） |
| `poll_messages` | 取出已收到的所有消息 |
| `greet` / `logout` / `quick_match` / `cancel_match` | 常用协议快捷工具 |
| `create_invite_room` / `join_invite_room` | 邀请房 |
| `spectate_list` / `spectate_join` | 观战 |
| `get_stats` | 查询战绩 |

## 典型验证流程

1. `connect_client` 建立客户端 A、B
2. `generate_token`(uid=10001) → `login`(A)
3. `generate_token`(uid=10002) → `login`(B)
4. `request_protocol` 或 `quick_match` 发匹配请求
5. `wait_message`(route=2003) 接收匹配成功推送

## 已知问题

~~服务器 `node` 的快速匹配存在阻塞 bug（已修复）~~：`quickMatchHandler` 会在匹配队列上阻塞等待对手，而 `cluster/node/router.go` 在单线程 dispatch 循环里同步执行 handler，导致第一个玩家发起匹配后整个节点无法处理任何消息（死锁）。

**修复方式**（`node/main.go` + `node/internal/match/manager.go`）：`quickMatchHandler` 与 `joinInviteRoomHandler` 中的阻塞逻辑（等待配对、调用 DS 建房）改用 `ctx.Task(...)` 放入任务池异步执行，dispatch 循环得以继续处理后续消息，两个玩家的匹配 handler 可并发运行，无缓冲 channel 配对正常生效。同时修复了邀请房创建者 gid 未缓存导致收不到 2003 推送的问题（`match.Manager.CreateInviteRoom` 增加 gid 缓存），以及 `cancel_match` 空操作问题（管理器记录排队玩家匹配等待的取消函数，`CancelMatch` 真正触发取消；被取消玩家收到的 2001 响应返回 code=1 Canceled）。

已实测：双客户端并发快速匹配成功进入同一房间（双方各自收到 2001 响应，先匹配者额外收到 2003 推送）；邀请房创建者收到 2003 推送、加入者收到 2009 响应、无效邀请码返回 code=5；取消匹配后玩家被移出队列（后续匹配者不会再配到该玩家），排队中断线也会自动移出队列。
