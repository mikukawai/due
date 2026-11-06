# DUE v2.6.6 受控升级说明

## 范围与使用边界

本分支将原工程从 DUE v2.5.8 合并至官方稳定版 v2.6.6（固定提交 `5d2a7e6ca54277d5ee1ef38dad76d19fd24b4018`），并适配本工程自定义应用。它不是部署操作：原 `main`、原目录、运行数据、现有服务与 GitHub 远端保持不变，不自动启动或重启任何服务。

- 原目录：`/home/miku/docker/due/due`
- 升级分支：`upgrade/due-v2.6.6`
- 升级独立工作区：`/home/miku/docker/due/due-upgrade-v2.6.6`
- 回退引用：`backup/pre-v2.6.6-20261106`，指向原 `db5daddd599226f154cdb3e15ed583d6af901941`
- 完整 Git 备份：`/home/miku/docker/due/backups/pre-v2.6.6-20261106.bundle`

如暂不采用升级，继续使用原目录即可，无需 reset 或覆盖数据。备份包用于在新目录重建仓库，不包含数据库和未跟踪的运行产物；本次没有迁移数据库。

## 已完成的兼容迁移

1. 合并固定的官方 v2.6.6；框架依赖采用该版本原有 go.mod/go.sum。Docker Compose 保留本工程原版本，不升级中间件镜像，不改变 Mongo/Redis 数据卷路径。
2. Gate、Node、Go Client、MCP Client 的独立 go.mod 升至 Go 1.27.0 / DUE v2.6.6，保留本地 replace，并分别整理依赖。
3. Node 的用户推送枚举从已删除的 `session.User` 迁移至 `cluster.User`。
4. Gate/Node RPC 超时改到新版实际读取的 `cluster.gate.linker.callTimeout` / `cluster.node.linker.callTimeout`，值分别保留配置意图的 1s / 3s。旧版本的 `timeout` 键未生效，此次迁移后开始实际生效。
5. MCP 接收新版 `buffer.Buffer`，在合法回调期内复制消息正文；不保留池化 buffer/Context，不释放不归自己所有的接收 buffer。
6. 增加真实本地 WebSocket 接收与正文所有权回归，以及配置、线格式、数据卷契约测试。
7. GitHub Actions 增加四个自定义应用的独立构建/测试 job。工作流尚未推送或在 GitHub 上执行，本次结果来自本地实跑。

## 保留的运行契约

- WebSocket 地址配置仍为 `:3553`，JSON 正文、大端序、`routeBytes=4`、`seqBytes=0`、`bufferBytes=100000`。
- 没有开启新版可选的会话安全握手，不能因这次迁移就要求现有客户端新增加密握手。
- 真实 DS 配置仍为 `100.114.203.26:7001`，外部主机仍为 `100.114.203.26`；没有改为测试替身地址。烟测仅在临时配置副本中覆盖地址。
- Mongo/Redis 仍挂载 `../../data/mongo`、`../../data/redis`，镜像、其他原有中间件编排均保持不变。同级独立 worktree 仍会解析到相同数据目录，**源码分支隔离不等于数据隔离，不能直接 docker compose up 当作隔离测试**。
- 旧配置中的其他历史无效字段没有在本次顺带重构；例如 WS 的旧 `handshakeTimeout` / `heartbeatWithServerTime` 不是新版有效的相应配置入口。当前应用心跳时间戳沿用原本实际生效的默认关闭状态。

## Go 工具链

需要 Go 1.27.0 或更新版本。本次在 `.50` 上放置并校验了官方 Go 1.27.3 的私有工具链：

```text
/home/miku/docker/due/toolchains/go1.27.3/bin/go
```

没有修改系统 Go、全局 PATH 或 sudo 配置。构建时使用该工具链，或自行提供兼容版本。例如：

```bash
export PATH=/home/miku/docker/due/toolchains/go1.27.3/bin:$PATH
export GOTOOLCHAIN=local
cd /home/miku/docker/due/due-upgrade-v2.6.6
# 根模块与四个应用必须分别验证，根 ./... 不覆盖嵌套模块。
go test -mod=readonly -count=1 -timeout=120s ./...
(cd gate && go build -mod=readonly ./...)
(cd node && go build -mod=readonly ./...)
(cd client && go build -mod=readonly ./...)
(cd mcp-client && go test -mod=readonly -race -count=1 -timeout=60s ./...)
```

首次使用会下载声明的依赖。不要为了使用旧工具链而把 go.mod 的 Go 版本数字改低。

## 已实跑的验证

- 33 个 Go module 构建通过。HTTP 插件首次因依赖下载与冷构建超过 300 秒而超时；单独重跑后退出 0，保留了首次失败与重跑日志，没有将首次超时算作成功。
- 根模块完整 `go test ./...` 退出 0，统计 5999 个通过项（包含子用例）；不是声称运行了所有嵌套模块的全部中间件集成测试。
- 四个自定义应用测试命令均退出 0；MCP 两个真实 WebSocket 回归额外通过 race 检查。
- MCP RED 阶段确实收到空正文；修复后 GREEN 阶段恢复原始 JSON 字节。另在合法回调期覆盖实际接收 buffer，确认 inbox 正文不被修改。
- 烟测工具另外通过 5 项短回归：4 项覆盖 initialize 失败、忽略 SIGTERM、MCP 清理异常后继续清理其他资源、Python 3.10 下原异常不被清理报错覆盖；另 1 项验证 MCP 文本内嵌 JSON 的递归脱敏且不修改真实 RPC 数据。报告目录 0700，summary/transcript 在写入前设为 0600。5 项在 NAS 与 `.50` 的 Python 3.10.12 均实际通过，已加入 CI；最初三份测试转录也已脱敏并校验。
- 临时 Consul 1.20.6、兼容 Redis 协议的临时 Valkey 9.1.2、真实 Gate/Node/MCP 编译产物完成 12 项链路检查：登录、greet、续期、快速匹配、取消、观战控制响应、邀请推送、无效邀请码、重连/登出，以及结果轮询/结算/战绩。临时进程和容器已经清理。
- **DS 使用 localhost 控制协议替身**；这里只验证真实 DUE 消息链路及结算编排，不代表真实 DS 开战、游戏规则、实际观战或战斗客户端兼容已验收。
- 四应用 `go vet ./...` 通过。根模块 `go vet ./...` 仍报告官方 v2.6.6 已存在的 `core/buffer.Reader.ReadRune` 非标准签名警告；该文件与上游一致，未为消警告改动公共 API。不能表述为全仓 vet 全绿。
- 四应用另有 Linux/amd64、`CGO_ENABLED=0` 的静态编译产物；已核实 ELF 不依赖动态加载器，并在 `.50` 实际运行 MCP 的 stdio 初始化和 19 项工具列表检查，没有建立游戏连接。不能据此替代真实 DS 战斗验收。
- 全合并 whitespace 检查保留上游 `due.go` ASCII banner 一处行尾空白告警；相对固定官方版本的自定义增量检查退出 0，未为纯样式告警修改官方框架文件。

## 没有被升级自动修复的既存业务问题

本次兼容升级没有扩展账号、存档或匹配业务。以下问题仍需单独修复，不能据构建/兼容回归通过认定适合直接公开上线：

- JWT 密钥/TTL 仍在入口写死，token 类型和续期主体校验不足。
- 邀请房加入者的 **2009 直接响应仍返回房主身份/token**；2003 双方推送身份正确。烟测明确记录该既存缺陷，没有将错误的直接响应当作正确结果。
- `player_id=0` 等必需零值可能因 omitempty 缺失。
- 并发快速匹配交接、配对期间取消/失败补偿、结果可靠交付与内存持久化问题仍存在。
- 业务还不是可直接多 Node 扩容的共享匹配/结算服务。

## 重跑隔离链路

`scripts/upgrade_smoke.py` 是测试工具，不会编译或部署真实服务。需要先准备本地 Gate、Node、MCP 编译产物，以及**专用可丢弃**的 Consul/Redis 兼容实例，再显式传入：

```text
python3 scripts/upgrade_smoke.py \
  --bin-dir <absolute-build-directory> \
  --consul-addr 127.0.0.1:<test-consul-port> \
  --redis-addr 127.0.0.1:<test-redis-port> \
  --work-dir <new-empty-test-directory> \
  --disposable-middleware
```

测试会启动它自己的 Gate/Node/MCP 子进程与 DS 替身，并在退出时清理这些子进程；外部提供的测试中间件由调用方负责回收。不能指向原有中间件或生产 DS。输出 summary.json 中的 `known_baseline_issues` 与 `ds_mode` 必须与通过项一起阅读。
